// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// advisorFixture lays out zerorpc, whose S2S client names upstreamllm.LlmProxy
// (the service whose (spec.service).name is llmproxy, as in
// cross-test/proto-zerorpc), and a generated tree with two chassis crates, and
// returns the config of a zerorpc that declares an advisor.
func advisorFixture(t *testing.T) (*tactype.Config, []ServiceInfo) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	project := t.TempDir()
	protoDir := filepath.Join(project, "proto")
	specProto, err := os.ReadFile("../../../proto/sys/spec.proto")
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range map[string]string{
		"sys/spec.proto": string(specProto),
		"svc/zerorpc.proto": `syntax = "proto3";
package zerorpc;
import "sys/spec.proto";
option (spec.service) = {name: "zerorpc"};
option (spec.grpc_s2s_client) = {service: "upstreamllm.LlmProxy"};
message PingState {
  option (spec.service_state) = {name: "PingState", label: ["zerorpc"]};
  string ping = 1;
}
`,
		"services/llmproxy.proto": `syntax = "proto3";
package upstreamllm;
import "sys/spec.proto";
option (spec.service) = {name: "llmproxy"};
enum Role {
  ROLE_UNSPECIFIED = 0;
  ROLE_USER = 1;
}
message ContentBlock {
  oneof block {
    string text = 1;
    string tool_use_id = 2;
  }
}
message CompleteRequest {
  string prompt = 1;
  repeated ContentBlock blocks = 2;
  Role role = 3;
}
message CompleteResponse { string text = 1; }
message Chunk { string text = 1; }
service LlmProxy {
  option (spec.service_s2s) = {};
  rpc Generate(CompleteRequest) returns (CompleteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
  rpc GenerateStream(CompleteRequest) returns (stream Chunk) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`,
	} {
		path := filepath.Join(protoDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outDir := filepath.Join(project, "target", "model")
	for _, crate := range []string{"token", "servicemanager"} {
		dir := filepath.Join(outDir, "chassis", crate)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \""+crate+"\"\nversion = \"1.0.0\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := &tactype.Config{
		OutputDir:         outDir,
		ProjectDir:        project,
		AnnotatedProtoDir: protoDir,
		CrateBasePath:     "zerorpc-model",
		SelfPackage:       "zerorpc",
		AdvisorDir:        filepath.Join(project, "advisor"),
		Language:          "rust",
	}
	if err := service.InferGrpcClientsFromAnnotations(config, protoDir); err != nil {
		t.Fatalf("InferGrpcClientsFromAnnotations: %v", err)
	}
	services, err := detectServicesForWrapper(config, protoDir)
	if err != nil {
		t.Fatalf("detectServicesForWrapper: %v", err)
	}
	return config, service.ResolveClientServices(config, services)
}

func readGenerated(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestRustAdvisorPortIsTypedOnTheModelServicesMessages: the port's trait has one
// method per unary rpc of llmproxy, typed on llmproxy's messages, and wire::
// re-exports those messages and never the raw transport stubs beside them.
func TestRustAdvisorPortIsTypedOnTheModelServicesMessages(t *testing.T) {
	config, clients := advisorFixture(t)
	if err := generateRustAdvisorPort(config, clients); err != nil {
		t.Fatalf("generateRustAdvisorPort: %v", err)
	}
	lib := readGenerated(t, filepath.Join(config.OutputDir, "advisor", "lib.rs"))
	for _, want := range []string{
		"pub use zerorpc_model_types::services::upstreamllm::{",
		"        Chunk,\n        CompleteRequest,\n        CompleteResponse,\n        ContentBlock,\n        Role,\n        content_block,\n    };",
		"pub trait ModelPort: Send + Sync {",
		"async fn generate(&self, trace_id: &str, request: wire::CompleteRequest) -> Result<wire::CompleteResponse, ModelPortError>;",
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("advisor/lib.rs lacks %q:\n%s", want, lib)
		}
	}
	// A stream has no place on the port.
	for _, stub := range []string{"llm_proxy_client", "llm_proxy_server", "tonic", "generate_stream"} {
		if strings.Contains(lib, stub) {
			t.Errorf("advisor/lib.rs reaches %s:\n%s", stub, lib)
		}
	}

	cargo := readGenerated(t, filepath.Join(config.OutputDir, "advisor", "Cargo.toml"))
	deps := cargo[strings.Index(cargo, "[dependencies]"):strings.Index(cargo, "[build-dependencies]")]
	if want := "[dependencies]\nzerorpc-model-types = { path = \"../types\" }\nasync-trait = { workspace = true }\n\n"; deps != want {
		t.Errorf("the port crate depends on more than the types crate and async-trait:\n%s", deps)
	}
}

// TestRustAdvisorAdapterCarriesTheTraceAndTheServiceToken: the core's adapter
// makes the call over the generated S2S client (which attaches the service's
// token) and puts the advisor's trace_id on the request (field 200).
func TestRustAdvisorAdapterCarriesTheTraceAndTheServiceToken(t *testing.T) {
	config, clients := advisorFixture(t)
	if err := generateRustAdvisorPort(config, clients); err != nil {
		t.Fatalf("generateRustAdvisorPort: %v", err)
	}
	adapter := readGenerated(t, filepath.Join(config.OutputDir, "advisor_port.rs"))
	for _, want := range []string{
		"pub use zerorpc_model_advisor::{async_trait, wire, ModelPort, ModelPortError};",
		"use crate::s2sm::ServiceManager;",
		"impl ModelPort for ModelPortAdapter {",
		"let request = wire::CompleteRequest { trace_id: trace_id.to_string(), ..request };",
		".s2s()\n            .upstreamllm()\n            .llm_proxy_client()\n            .generate(request)",
	} {
		if !strings.Contains(adapter, want) {
			t.Errorf("advisor_port.rs lacks %q:\n%s", want, adapter)
		}
	}
}

// TestRustAdvisorGateForbidsEveryGeneratedCrate: the build script refuses the
// model crate, the types crate and every chassis crate by the package name its
// manifest declares, and points at the advisor tacplus.yaml names.
func TestRustAdvisorGateForbidsEveryGeneratedCrate(t *testing.T) {
	config, clients := advisorFixture(t)
	if err := generateRustAdvisorPort(config, clients); err != nil {
		t.Fatalf("generateRustAdvisorPort: %v", err)
	}
	build := readGenerated(t, filepath.Join(config.OutputDir, "advisor", "build.rs"))
	want := "const FORBIDDEN: &[&str] = &[\n    \"servicemanager\",\n    \"token\",\n    \"zerorpc-model\",\n    \"zerorpc-model-types\",\n];"
	if !strings.Contains(build, want) {
		t.Errorf("advisor/build.rs lacks\n%s\nin:\n%s", want, build)
	}
	if !strings.Contains(build, `const ADVISOR_DIR: &str = "../../../advisor";`) {
		t.Errorf("advisor/build.rs does not point at the advisor:\n%s", build)
	}
}

// TestRustAdvisorRefusesAServiceWithoutTheModelClient: an advisor whose
// service has no S2S client to llmproxy has no model to reach, and generation
// says how to give it one.
func TestRustAdvisorRefusesAServiceWithoutTheModelClient(t *testing.T) {
	config, _ := advisorFixture(t)
	err := generateRustAdvisorPort(config, nil)
	if err == nil || !strings.Contains(err.Error(), "add llmproxy to depends") {
		t.Fatalf("generateRustAdvisorPort without the llmproxy client = %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.OutputDir, "advisor")); !os.IsNotExist(err) {
		t.Errorf("a refused advisor left its port crate behind: %v", err)
	}
}

// TestRustNoAdvisorNoPort: a service that declares no advisor gets none of it.
func TestRustNoAdvisorNoPort(t *testing.T) {
	config, clients := advisorFixture(t)
	config.AdvisorDir = ""
	if err := generateRustAdvisorPort(config, clients); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"advisor", "advisor_port.rs"} {
		if _, err := os.Stat(filepath.Join(config.OutputDir, p)); !os.IsNotExist(err) {
			t.Errorf("%s generated without an advisor: %v", p, err)
		}
	}
}
