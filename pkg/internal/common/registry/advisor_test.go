// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestGenerateMarksTheAdvisorsService proves registry.json carries advisor:
// true on the service whose tacplus.yaml declares an LLM advisor (tacplus#909),
// so tooling sees the service has one, and on that service only: never on a
// service it depends on (its model service among them), and nowhere when it
// declares none.
func TestGenerateMarksTheAdvisorsService(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}
	dir := t.TempDir()
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	files := map[string]string{
		"sys/spec.proto": string(specProto),
		"svc/skipper.proto": `syntax = "proto3";
package skipper;
import "sys/spec.proto";
option (spec.service) = {name: "skipper"};
option (spec.grpc_s2s_client) = {service: "llmproxy.LlmProxy"};
message AskRequest { string q = 1; }
message AskResponse { string a = 1; }
service Skipper {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ask(AskRequest) returns (AskResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE};
  }
}
`,
		"services/llmproxy.proto": `syntax = "proto3";
package llmproxy;
import "sys/spec.proto";
option (spec.service) = {name: "llmproxy"};
message CompleteRequest { string prompt = 1; }
message CompleteResponse { string text = 1; }
service LlmProxy {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Complete(CompleteRequest) returns (CompleteResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE};
  }
}
`,
	}
	for rel, text := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	parsed := []model.ProtoFile{
		{Package: "skipper", RelPath: "svc/skipper.proto"},
		{Package: "llmproxy", RelPath: "services/llmproxy.proto"},
	}

	advisorOf := func(cfg Config) map[string]bool {
		t.Helper()
		cfg.OutputDir = filepath.Join(t.TempDir(), "out")
		cfg.InputDir = dir
		cfg.Language = "none"
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Generate(cfg, parsed, dp); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(cfg.OutputDir, "registry.json"))
		if err != nil {
			t.Fatal(err)
		}
		var root spec.PackageNode
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &root); err != nil {
			t.Fatalf("unmarshal registry.json: %v", err)
		}
		got := map[string]bool{}
		var walk func(*spec.PackageNode)
		walk = func(n *spec.PackageNode) {
			for _, s := range n.GetServices() {
				got[s.GetGrpcService()] = s.GetAdvisor()
			}
			for _, c := range n.GetChildren() {
				walk(c)
			}
		}
		walk(&root)
		return got
	}

	got := advisorOf(Config{Advisor: true, SelfPackage: "skipper"})
	if want := map[string]bool{"skipper.Skipper": true, "llmproxy.LlmProxy": false}; !mapsEqual(got, want) {
		t.Errorf("with an advisor: services' advisor = %v, want %v", got, want)
	}
	got = advisorOf(Config{SelfPackage: "skipper"})
	if want := map[string]bool{"skipper.Skipper": false, "llmproxy.LlmProxy": false}; !mapsEqual(got, want) {
		t.Errorf("without an advisor: services' advisor = %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
