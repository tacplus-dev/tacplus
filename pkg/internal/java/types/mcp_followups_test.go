// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestJavaMcpSurfaceExposesASessionFreeDispatchEntryPoint(t *testing.T) {
	rendered := javaMcpRender(t)
	for _, want := range []string{
		"public McpSchema.CallToolResult call(String name, java.util.Map<String, Object> arguments) {",
		"new McpSchema.CallToolRequest(",
		"case \"get_ticket_brief\":\n                    return callGetTicketBrief(request);",
		`return toolError("unknown tool \"" + name + "\"");`,
		".callHandler((exchange, request) -> call(request.name(), request.arguments()))",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated MCP surface is missing session-free dispatch %q\ngot:\n%s", want, rendered)
		}
	}
}

func javaMcpOnlyService(t *testing.T) (ServiceInfo, *parser.ProtoInfo) {
	t.Helper()
	tool := &parser.MethodInfo{
		Name:           "GetTicketBrief",
		InputType:      "GetTicketBriefRequest",
		OutputType:     "GetTicketBriefResponse",
		FullInputType:  "ticket.GetTicketBriefRequest",
		FullOutputType: "ticket.GetTicketBriefResponse",
		IsMcp:          true,
		McpToolName:    "get_ticket_brief",
		Comment:        "GetTicketBrief summarizes one ticket for an assistant.",
	}
	carrier := &parser.MethodInfo{
		Name:            parser.McpCarrierRpcName("TicketAgent"),
		InputType:       "McpRequest",
		OutputType:      "McpResponse",
		FullInputType:   "mcp.McpRequest",
		FullOutputType:  "mcp.McpResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}
	target := &parser.ServiceInfo{
		Name:         "TicketAgent",
		IsC2S:        true,
		HasMcpRpcs:   true,
		AuthServices: []string{"ticket-agent"},
		Methods:      []*parser.MethodInfo{tool, carrier},
	}
	outputDir := t.TempDir()
	wrapperDir := filepath.Join(outputDir, "src", "main", "java", "com", "example", "s2sm", "internal", "grpc", "servers", "svc", "ticket")
	typesDir := filepath.Join(outputDir, "src", "main", "java", "com", "example", "types", "svc", "ticket", "types")
	svc := ServiceInfo{
		ProtoFile:      "svc/ticket.proto",
		PackageName:    "ticket",
		ServiceName:    "TicketAgent",
		HasMcp:         true,
		IsC2S:          true,
		AuthServices:   []string{"ticket-agent"},
		OutputDir:      wrapperDir,
		TypesOutputDir: typesDir,
		ParentDir:      "svc",
		Config:         &tactype.Config{OutputDir: outputDir, JavaPackageBase: "com.example"},
	}
	return svc, &parser.ProtoInfo{Package: "ticket", Services: []*parser.ServiceInfo{target}}
}

func TestJavaMcpOnlyServiceIsMountedLikeAnyGrpcService(t *testing.T) {
	svc, _ := javaMcpOnlyService(t)
	got := resolveServerServices(&tactype.Config{SelfPackage: "ticket"}, []ServiceInfo{svc})
	if len(got) != 1 || got[0].ServiceName != "TicketAgent" {
		t.Fatalf("an MCP-only service must be resolved as a server service, got %v", got)
	}
	if grpc := filterGrpcAnnotated([]ServiceInfo{svc}); len(grpc) != 1 {
		t.Fatalf("filterGrpcAnnotated must keep an MCP-only service, got %v", grpc)
	}
}

func TestJavaMcpOnlyServiceRegistersCarrierMethodAuth(t *testing.T) {
	svc, info := javaMcpOnlyService(t)
	if _, err := generateServiceWrapper(svc, info, nil, proto.SkippedTraceTypes{}, javaMcpTestSanitizer()); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(svc.OutputDir, "TicketAgent.java"))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	got := string(raw)
	carrier := parser.McpCarrierRpcName("TicketAgent")
	if !strings.Contains(got, "registerMethodAuth(") {
		t.Fatalf("an MCP-only service with a service-level auth_services must still emit registerMethodAuth:\n%s", got)
	}
	// Registered under the same service-qualified key the wrapper passes to
	// Authorizer.authorize() for the carrier, so the two cannot disagree.
	key := javaMethodAuthKey("ticket", "TicketAgent", carrier, "svc")
	if !strings.Contains(got, `authorizer.setMethodAuth("`+key+`", java.util.List.of("ticket-agent"));`) {
		t.Fatalf("the carrier must carry the service's auth policy under its service-qualified key %q:\n%s", key, got)
	}
}
