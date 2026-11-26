// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// goAdvisorFixture lays out, in a module example.com/zerorpc, the protos of
// zerorpc (an S2S client to upstreamllm.LlmProxy, the service whose
// (spec.service).name is llmproxy, as in cross-test/proto-zerorpc), and
// returns the config of a zerorpc that declares an advisor, with the client
// services and generated clients the generator hands the advisor.
func goAdvisorFixture(t *testing.T) (*tactype.Config, []ServiceInfo, []generatedGrpcClient) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	project := t.TempDir()
	protoDir := filepath.Join(t.TempDir(), "proto")
	specProto, err := os.ReadFile("../../../proto/sys/spec.proto")
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range map[string]string{
		"sys/spec.proto": string(specProto),
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
	outDir := filepath.Join(project, "model")
	if err := os.MkdirAll(filepath.Join(outDir, "s2sm"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := &tactype.Config{
		OutputDir:         outDir,
		ProjectDir:        project,
		AnnotatedProtoDir: protoDir,
		ModuleBasePath:    "example.com/zerorpc/model",
		SelfPackage:       "zerorpc",
		AdvisorDir:        filepath.Join(project, "advisor"),
		Language:          "go",
	}
	clientServices := []ServiceInfo{{ProtoFile: "services/llmproxy.proto", ServiceName: "LlmProxy", PackageName: "upstreamllm", ParentDir: "services"}}
	clients := []generatedGrpcClient{{
		PackageName:     "upstreamllm",
		ServiceName:     "LlmProxy",
		ClientName:      "LlmProxyClient",
		TypesImportPath: "example.com/zerorpc/model/types/services/upstreamllm",
		ParentDir:       "services",
	}}
	return config, clientServices, clients
}

func readGoGenerated(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestGoAdvisorPortIsTypedOnTheModelServicesMessages: ModelPort has one method
// per unary rpc of llmproxy, typed on llmproxy's messages, which the port
// aliases with their oneof wrappers and enum values, and never the raw
// transport client beside them.
func TestGoAdvisorPortIsTypedOnTheModelServicesMessages(t *testing.T) {
	config, svcs, clients := goAdvisorFixture(t)
	if err := generateGoAdvisorPort(config, svcs, clients); err != nil {
		t.Fatalf("generateGoAdvisorPort: %v", err)
	}
	port := readGoGenerated(t, filepath.Join(config.OutputDir, "advisor", "port.go"))
	for _, want := range []string{
		"package advisor\n",
		`wire "example.com/zerorpc/model/types/services/upstreamllm"`,
		"CompleteRequest      = wire.CompleteRequest",
		"ContentBlock_Text      = wire.ContentBlock_Text",
		"ContentBlock_ToolUseId = wire.ContentBlock_ToolUseId",
		"Role_ROLE_USER        = wire.Role_ROLE_USER",
		"Generate(ctx context.Context, traceID string, req *CompleteRequest) (*CompleteResponse, error)",
	} {
		if !strings.Contains(strings.Join(strings.Fields(port), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("advisor/port.go lacks %q:\n%s", want, port)
		}
	}
	for _, stub := range []string{"LlmProxyClient", "LlmProxyServer", "grpc", "GenerateStream"} {
		if strings.Contains(port, stub) {
			t.Errorf("advisor/port.go reaches %s:\n%s", stub, port)
		}
	}
}

// TestGoAdvisorAdapterCarriesTheTraceAndTheServiceToken: the core's adapter
// calls the generated S2S client (which attaches the service's token) with a
// copy of the request carrying the advisor's trace_id (field 200).
func TestGoAdvisorAdapterCarriesTheTraceAndTheServiceToken(t *testing.T) {
	config, svcs, clients := goAdvisorFixture(t)
	if err := generateGoAdvisorPort(config, svcs, clients); err != nil {
		t.Fatalf("generateGoAdvisorPort: %v", err)
	}
	adapter := readGoGenerated(t, filepath.Join(config.OutputDir, "s2sm", "advisor_port.go"))
	for _, want := range []string{
		"package sm\n",
		`"example.com/zerorpc/model/advisor"`,
		"var _ advisor.ModelPort = (*ModelPortAdapter)(nil)",
		"out = proto.Clone(req).(*advisor.CompleteRequest)",
		"out.TraceId = traceID",
		"return a.sm.Grpc().Clients().Services().LlmProxyClient().Generate(ctx, out)",
	} {
		if !strings.Contains(adapter, want) {
			t.Errorf("s2sm/advisor_port.go lacks %q:\n%s", want, adapter)
		}
	}
}

// TestGoAdvisorRefusesAServiceWithoutTheModelClient: no S2S client to llmproxy,
// no model to reach; generation says how to give it one.
func TestGoAdvisorRefusesAServiceWithoutTheModelClient(t *testing.T) {
	config, _, _ := goAdvisorFixture(t)
	err := generateGoAdvisorPort(config, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "add llmproxy to depends") {
		t.Fatalf("generateGoAdvisorPort without the llmproxy client = %v", err)
	}
}

// TestGoAdvisorGate runs the generated llm_rule_test.go in a module laid out as
// a service is (the generated tree under model/, with stand-ins for the types
// package, the ServiceManager and the token chassis; the core; another package
// of the repository; the advisor): green when the advisor imports only the
// port, red, naming the import, for each other part of the service it reaches.
func TestGoAdvisorGate(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test in a module of its own")
	}
	config, svcs, clients := goAdvisorFixture(t)
	if err := generateGoAdvisorPort(config, svcs, clients); err != nil {
		t.Fatalf("generateGoAdvisorPort: %v", err)
	}
	// The adapter needs the real ServiceManager; the gate does not build it.
	if err := os.Remove(filepath.Join(config.OutputDir, "s2sm", "advisor_port.go")); err != nil {
		t.Fatal(err)
	}
	project := config.ProjectDir
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/zerorpc\n\ngo 1.22\n")
	write("model/types/services/upstreamllm/upstreamllm.go", `package upstreamllm

type Role int32

const (
	Role_ROLE_UNSPECIFIED Role = 0
	Role_ROLE_USER        Role = 1
)

type ContentBlock struct{ Block isContentBlock_Block }
type isContentBlock_Block interface{ isContentBlock_Block() }
type ContentBlock_Text struct{ Text string }
type ContentBlock_ToolUseId struct{ ToolUseId string }

func (*ContentBlock_Text) isContentBlock_Block()      {}
func (*ContentBlock_ToolUseId) isContentBlock_Block() {}

type CompleteRequest struct{ Prompt, TraceId string }
type CompleteResponse struct{ Text, TraceId string }
type Chunk struct{ Text string }

// LlmProxyClient stands in for the raw transport stub beside the messages.
type LlmProxyClient interface{}
`)
	write("model/s2sm/sm.go", "package sm\n\ntype ServiceManager struct{}\n")
	write("model/chassis/token/token.go", "package token\n\ntype Token struct{}\n")
	write("core/core.go", "package core\n\nfunc Write() {}\n")
	write("helper/helper.go", "package helper\n\nconst Name = \"helper\"\n")

	green := `package advisor

import (
	"context"

	port "example.com/zerorpc/model/advisor"
)

func Ask(ctx context.Context, m port.ModelPort, traceID, prompt string) (string, error) {
	r, err := m.Generate(ctx, traceID, &port.CompleteRequest{Prompt: prompt})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}
`
	run := func() (string, bool) {
		cmd := exec.Command("go", "test", "-count=1", "./model/advisor")
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		return string(out), err == nil
	}

	write("advisor/advisor.go", green)
	if out, ok := run(); !ok {
		t.Fatalf("green: the gate failed an advisor that imports only the port:\n%s", out)
	}

	for _, tc := range []struct{ name, file, text, want string }{
		{"the ServiceManager", "advisor/sm.go", "package advisor\n\nimport _ \"example.com/zerorpc/model/s2sm\"\n", "imports example.com/zerorpc/model/s2sm"},
		{"the token chassis, from a test", "advisor/token_test.go", "package advisor\n\nimport _ \"example.com/zerorpc/model/chassis/token\"\n", "imports example.com/zerorpc/model/chassis/token"},
		{"the types package's raw stubs", "advisor/types.go", "package advisor\n\nimport _ \"example.com/zerorpc/model/types/services/upstreamllm\"\n", "imports example.com/zerorpc/model/types/services/upstreamllm"},
		{"the core", "advisor/core.go", "package advisor\n\nimport _ \"example.com/zerorpc/core\"\n", "imports example.com/zerorpc/core"},
		{"another package of the repository, from a subpackage", "advisor/tools/tools.go", "package tools\n\nimport _ \"example.com/zerorpc/helper\"\n", "imports example.com/zerorpc/helper"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.file, tc.text)
			defer os.Remove(filepath.Join(project, tc.file))
			out, ok := run()
			if ok {
				t.Fatalf("red: the gate passed an advisor that imports %s:\n%s", tc.name, out)
			}
			if !strings.Contains(out, "tacplus LLM rule") || !strings.Contains(out, tc.want) {
				t.Fatalf("red: the failure is not the gate's %q:\n%s", tc.want, out)
			}
		})
	}

	if out, ok := run(); !ok {
		t.Fatalf("green again: the gate failed the advisor once it was back to the port alone:\n%s", out)
	}
}
