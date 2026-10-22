// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// mcpCarrierProtoDir lays out a proto tree holding one service and returns the
// prepared directory (user proto + embedded sys protos), which is the shape
// AppendMcpCarrierRpcsToProtos runs against inside CopyAndPrepareProtoDir.
func mcpCarrierProtoDir(t *testing.T, protoBody string) string {
	t.Helper()
	protoDir := t.TempDir()
	servicesDir := filepath.Join(protoDir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("mkdir services/: %v", err)
	}
	if err := os.WriteFile(filepath.Join(servicesDir, "scotty.proto"), []byte(protoBody), 0644); err != nil {
		t.Fatalf("write scotty.proto: %v", err)
	}
	if err := CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	// The embedded sys protos import generated/*.proto; CopyAndPrepareProtoDir
	// has already produced those by the time the injector runs.
	if err := writeMinimalGeneratedStubs([]string{protoDir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	return protoDir
}

const mcpCarrierToolProto = `syntax = "proto3";

package scotty;

import "sys/spec.proto";

message GetTicketRequest {
  string id = 1;
}

message GetTicketResponse {
  string body = 1;
}

service Scotty {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // GetTicketBrief summarizes one ticket.
  rpc GetTicketBrief(GetTicketRequest) returns (GetTicketResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }

  // GetTicket returns one ticket.
  rpc GetTicket(GetTicketRequest) returns (GetTicketResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER};
  }
}
`

// The paired control: the same service with no MCP rpc at all.
const mcpCarrierControlProto = `syntax = "proto3";

package scotty;

import "sys/spec.proto";

message GetTicketRequest {
  string id = 1;
}

message GetTicketResponse {
  string body = 1;
}

service Scotty {
  option (spec.service_s2s) = {auth_services: ["*"]};

  // GetTicket returns one ticket.
  rpc GetTicket(GetTicketRequest) returns (GetTicketResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`

func mcpCarrierServiceProto(t *testing.T, protoDir string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(protoDir, "services", "scotty.proto"))
	if err != nil {
		t.Fatalf("read injected proto: %v", err)
	}
	return string(content)
}

// A service hosting an MCP tool gets a real rpc declaration for its carrier,
// with the shared frame types and the import that resolves them, and the
// declaration parses back as an ordinary streaming rpc.
func TestAppendMcpCarrierRpcsInjectsDeclaration(t *testing.T) {
	protoDir := mcpCarrierProtoDir(t, mcpCarrierToolProto)

	modified, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir)
	if err != nil {
		t.Fatalf("AppendMcpCarrierRpcsToProtos: %v", err)
	}
	if len(modified) != 1 || modified[0] != "services/scotty.proto" {
		t.Fatalf("modified = %v, want [services/scotty.proto]", modified)
	}

	text := mcpCarrierServiceProto(t, protoDir)
	want := "rpc BrokerScottyMcpSession(stream mcp.McpRequest) returns (stream mcp.McpResponse) {"
	if !strings.Contains(text, want) {
		t.Errorf("injected proto has no carrier declaration %q:\n%s", want, text)
	}
	if !strings.Contains(text, `import "sys/mcp.proto";`) {
		t.Errorf("injected proto does not import the frame types:\n%s", text)
	}
	for _, opt := range []string{"principal: RPC_PRINCIPAL_USER", "flags: [RPC_FLAG_AUTH_ENTITLED_USERS]"} {
		if !strings.Contains(text, opt) {
			t.Errorf("carrier does not declare %q — it is the one rpc an MCP client authenticates to:\n%s", opt, text)
		}
	}

	// It must land INSIDE the service, or protoc rejects the file outright.
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("injected tree does not parse: %v", err)
	}
	fd := dp.GetAllFiles()["services/scotty.proto"]
	if fd == nil {
		t.Fatal("services/scotty.proto missing from parsed tree")
	}
	var carrier *parser.MethodDescriptor
	for i := range fd.Services[0].Methods {
		if fd.Services[0].Methods[i].Name == parser.McpCarrierRpcName("Scotty") {
			carrier = &fd.Services[0].Methods[i]
		}
	}
	if carrier == nil {
		t.Fatal("BrokerScottyMcpSession is not a method of service Scotty after injection")
	}
	if !carrier.ClientStreaming || !carrier.ServerStreaming {
		t.Errorf("carrier is not bidirectional: client=%v server=%v", carrier.ClientStreaming, carrier.ServerStreaming)
	}
	if carrier.IsMcp {
		t.Error("carrier parsed as an MCP tool; it is the transport, not a tool")
	}
	if !strings.Contains(carrier.Comment, "Model Context Protocol") {
		t.Errorf("carrier lost its doc comment: %q", carrier.Comment)
	}
}

// A service that owns no tool is left byte-for-byte alone. Since auto-exposure
// that means an s2s service: a c2s one owns tools by construction, which
// TestAppendMcpCarrierRpcsInjectsForAutoExposedC2SService is the paired control
// for.
func TestAppendMcpCarrierRpcsSkipsServiceWithoutMcpRpcs(t *testing.T) {
	protoDir := mcpCarrierProtoDir(t, mcpCarrierControlProto)

	modified, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir)
	if err != nil {
		t.Fatalf("AppendMcpCarrierRpcsToProtos: %v", err)
	}
	if len(modified) != 0 {
		t.Fatalf("modified = %v, want none", modified)
	}
	if got := mcpCarrierServiceProto(t, protoDir); got != mcpCarrierControlProto {
		t.Errorf("control proto was rewritten:\n%s", got)
	}
}

// Injection is idempotent: the injectors run over trees that may already have
// been prepared, and a second carrier would be a duplicate rpc name.
func TestAppendMcpCarrierRpcsIsIdempotent(t *testing.T) {
	protoDir := mcpCarrierProtoDir(t, mcpCarrierToolProto)

	if _, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := mcpCarrierServiceProto(t, protoDir)

	modified, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(modified) != 0 {
		t.Errorf("second pass modified %v, want none", modified)
	}
	if second := mcpCarrierServiceProto(t, protoDir); second != first {
		t.Errorf("second pass changed the proto:\n%s", second)
	}
	if n := strings.Count(first, "rpc BrokerScottyMcpSession("); n != 1 {
		t.Errorf("carrier declared %d times, want 1", n)
	}
}

// The carrier's exposure mirrors the union its tools declare, so the transport
// is reachable from wherever its most-exposed tool is called.
func TestAppendMcpCarrierRpcsMirrorsMcpExposure(t *testing.T) {
	external := strings.Replace(mcpCarrierToolProto,
		"option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};",
		"option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP, exposure: RPC_EXPOSURE_EXTERNAL};", 1)
	protoDir := mcpCarrierProtoDir(t, external)

	if _, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendMcpCarrierRpcsToProtos: %v", err)
	}
	if text := mcpCarrierServiceProto(t, protoDir); !strings.Contains(text, "exposure: RPC_EXPOSURE_EXTERNAL") {
		t.Errorf("carrier of an EXTERNAL tool is not EXTERNAL:\n%s", text)
	}

	internal := mcpCarrierProtoDir(t, mcpCarrierToolProto)
	if _, err := AppendMcpCarrierRpcsToProtos(internal, internal); err != nil {
		t.Fatalf("AppendMcpCarrierRpcsToProtos: %v", err)
	}
	if text := mcpCarrierServiceProto(t, internal); !strings.Contains(text, "exposure: RPC_EXPOSURE_INTERNAL") {
		t.Errorf("carrier of an INTERNAL-only tool set is not INTERNAL:\n%s", text)
	}
}

// TestMcpResponseFrameBoundMatchesProto pins McpResponseFrameMaxBytes to the
// bound sys/mcp.proto actually declares. The generated Rust MCP transport uses
// the constant to keep a frame under the limit the generated response
// validation enforces; if the two drift, an over-size result kills the whole
// session instead of failing one tool call.
func TestMcpResponseFrameBoundMatchesProto(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "proto", "sys", "mcp.proto"))
	if err != nil {
		t.Fatalf("read sys/mcp.proto: %v", err)
	}
	re := regexp.MustCompile(`message McpResponse \{[^}]*\(spec\.assert_max_size\) = (\d+)`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("sys/mcp.proto declares no (spec.assert_max_size) on McpResponse.payload")
	}
	declared, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parse declared bound %q: %v", m[1], err)
	}
	if declared != McpResponseFrameMaxBytes {
		t.Fatalf("McpResponseFrameMaxBytes = %d, but sys/mcp.proto declares %d; the generated MCP transport would bound frames at the wrong size",
			McpResponseFrameMaxBytes, declared)
	}
}

// The auto-exposure half of the control above: a c2s service declaring no
// RPC_PRINCIPAL_MCP rpc at all still gets the carrier, because its user RPCs
// are tools. Before auto-exposure this exact proto was the "no carrier" case.
const mcpCarrierAutoExposedProto = `syntax = "proto3";

package scotty;

import "sys/spec.proto";

message GetTicketRequest {
  string id = 1;
}

message GetTicketResponse {
  string body = 1;
}

service Scotty {
  option (spec.service_c2s) = {auth_entitled_users: true};

  // GetTicket returns one ticket.
  rpc GetTicket(GetTicketRequest) returns (GetTicketResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER};
  }
}
`

func TestAppendMcpCarrierRpcsInjectsForAutoExposedC2SService(t *testing.T) {
	protoDir := mcpCarrierProtoDir(t, mcpCarrierAutoExposedProto)

	modified, err := AppendMcpCarrierRpcsToProtos(protoDir, protoDir)
	if err != nil {
		t.Fatalf("AppendMcpCarrierRpcsToProtos: %v", err)
	}
	if len(modified) != 1 || modified[0] != "services/scotty.proto" {
		t.Fatalf("modified = %v, want [services/scotty.proto]", modified)
	}
	if got := mcpCarrierServiceProto(t, protoDir); !strings.Contains(got, parser.McpCarrierRpcName("Scotty")) {
		t.Errorf("carrier not injected into the auto-exposed c2s service:\n%s", got)
	}
}
