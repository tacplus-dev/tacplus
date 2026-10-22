// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPyModuleName(t *testing.T) {
	tests := map[string]string{
		"gitlab-connector": "gitlab_connector",
		"claude-sensor":    "claude_sensor",
		"proxy.policy":     "proxy_policy",
		"9lives":           "_9lives",
		"as":               "as_",
		"":                 "_",
	}

	for input, want := range tests {
		if got := pyModuleName(input); got != want {
			t.Fatalf("pyModuleName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPyPath(t *testing.T) {
	got := pyPath(filepath.Join("vservices", "gitlab-connector"))
	want := filepath.Join("vservices", "gitlab_connector")
	if got != want {
		t.Fatalf("pyPath() = %q, want %q", got, want)
	}
}

func TestPyPackageBase(t *testing.T) {
	got := pyPackageBase("webapp-tacplus")
	want := "webapp_tacplus"
	if got != want {
		t.Fatalf("pyPackageBase() = %q, want %q", got, want)
	}

	got = pyPackagePath("my-org.my-project")
	want = filepath.Join("my_org", "my_project")
	if got != want {
		t.Fatalf("pyPackagePath() = %q, want %q", got, want)
	}
}

func TestPyGeneratedSnakeNameDigitAcronym(t *testing.T) {
	tests := map[string]string{
		"ContractS2S":     "contract_s2s",
		"ContractC2S":     "contract_c2s",
		"MintP2SService":  "mint_p2s_service",
		"IdentityService": "identity_service",
	}
	for input, want := range tests {
		if got := pyGeneratedSnakeName(input); got != want {
			t.Fatalf("pyGeneratedSnakeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRewritePythonProtoImports(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "webapp_tacplus", "types", "services", "oauth")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pkgDir, "oauth_pb2_grpc.py")
	input := `import grpc
from sys import trace_pb2 as sys_dot_trace__pb2
from services import oauth_pb2 as services_dot_oauth__pb2
from contract import contract_pb2 as contract_dot_contract__pb2
from google.protobuf import empty_pb2 as google_dot_protobuf_dot_empty__pb2
`
	if err := os.WriteFile(path, []byte(input), 0644); err != nil {
		t.Fatal(err)
	}

	protoFiles := []string{"sys/trace.proto", "services/oauth.proto", "contract/contract.proto"}
	layouts := map[string]pythonProtoLayout{
		"sys/trace_pb2.py":        {ProtoDir: "sys", PackageName: "trace"},
		"services/oauth_pb2.py":    {ProtoDir: "services", PackageName: "oauth"},
		"contract/contract_pb2.py": {ProtoDir: "contract", PackageName: "contractclient"},
	}
	if err := rewritePythonProtoImports(dir, "webapp_tacplus", protoFiles, layouts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := `import grpc
from webapp_tacplus.types.sys.trace import trace_pb2 as sys_dot_trace__pb2
from webapp_tacplus.types.services.oauth import oauth_pb2 as services_dot_oauth__pb2
from webapp_tacplus.types.contract.contractclient import contract_pb2 as contract_dot_contract__pb2
from google.protobuf import empty_pb2 as google_dot_protobuf_dot_empty__pb2
`
	if got != want {
		t.Fatalf("rewritten imports:\n%s\nwant:\n%s", got, want)
	}
}

func TestPyExceptionImportFrom(t *testing.T) {
	tests := map[string]string{
		filepath.Join("types", "top_level"):             "..sys.errors.exceptions",
		filepath.Join("types", "services", "oauth"):     "...sys.errors.exceptions",
		filepath.Join("types", "sys", "grpcconfig"):    "..errors.exceptions",
		filepath.Join("types", "sys", "errors"):        ".exceptions",
		filepath.Join("types", "nested", "v1", "thing"): "....sys.errors.exceptions",
	}

	for input, want := range tests {
		if got := pyExceptionImportFrom(input); got != want {
			t.Fatalf("pyExceptionImportFrom(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHasFileWithSuffix(t *testing.T) {
	dir := t.TempDir()
	if hasFileWithSuffix(dir, "_pb2_grpc.py") {
		t.Fatal("empty dir unexpectedly matched _pb2_grpc.py")
	}

	nested := filepath.Join(dir, "types", "services", "gateway")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "gateway_pb2_grpc.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if !hasFileWithSuffix(dir, "_pb2_grpc.py") {
		t.Fatal("expected generated gRPC stub to be detected")
	}
}
