// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Go twins of the three Rust MCP follow-ups: the session-free dispatch entry,
// MCP-only services mounted like any other gRPC service, and the carrier's own
// auth policy registered for a service whose every method is a tool or the
// carrier.

func TestGoMcpSurfaceExposesASessionFreeDispatchEntryPoint(t *testing.T) {
	rendered := goMcpRender(t)
	for _, want := range []string{
		"func (s *TicketApiMcpSurface) Call(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {",
		"case \"get_ticket_brief\":\n\t\treturn s.callGetTicketBrief(ctx, arguments)",
		`return nil, status.Error(codes.NotFound, fmt.Sprintf("unknown tool %q", name))`,
		"return s.Call(ctx, tools[0].Name, req.Params.Arguments)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated MCP surface is missing session-free dispatch %q\ngot:\n%s", want, rendered)
		}
	}
}

// A service shaped like docmanageragent: every method is an MCP tool or the
// synthesized session carrier, none is an ordinary rpc, and it declares no
// service_s2s/c2s/c2d, so GrpcName is empty.
func goMcpOnlyService() (ServiceInfo, *parser.ProtoInfo) {
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
	info := &parser.ProtoInfo{Package: "ticket", Services: []*parser.ServiceInfo{target}}
	svc := ServiceInfo{
		ProtoFile:    "svc/ticket.proto",
		PackageName:  "ticket",
		DirName:      "ticket",
		ServiceName:  "TicketAgent",
		HasMcp:       true,
		AuthServices: []string{"ticket-agent"},
		ParentDir:    "svc",
	}
	return svc, info
}

func TestGoMcpOnlyServiceIsMountedLikeAnyGrpcService(t *testing.T) {
	svc, _ := goMcpOnlyService()
	config := &tactype.Config{SelfPackage: "ticket"}
	got, err := resolveServerServices(config, []ServiceInfo{svc})
	if err != nil {
		t.Fatalf("resolveServerServices: %v", err)
	}
	if len(got) != 1 || got[0].ServiceName != "TicketAgent" {
		t.Fatalf("an MCP-only service must be resolved as a server service, got %v", got)
	}
}

func TestGoMcpOnlyServiceRegistersCarrierMethodAuth(t *testing.T) {
	svc, info := goMcpOnlyService()
	outDir := t.TempDir()
	svc.OutputDir = outDir
	svc.Config = &tactype.Config{OutputDir: outDir, ModuleBasePath: "example.com/mod"}
	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goMcpTestSanitizer()); err != nil {
		t.Fatalf("generate Go service wrapper: %v", err)
	}
	wrapperDir := filepath.Join(outDir, svc.Config.GetSmPrefix(), "grpc", "servers", "svc", "ticket")
	entries, err := os.ReadDir(wrapperDir)
	if err != nil {
		t.Fatalf("read %s: %v", wrapperDir, err)
	}
	var got string
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(wrapperDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got += string(raw)
	}
	carrierKey := qualifiedMethodAuthKey("ticket", "TicketAgent", parser.McpCarrierRpcName("TicketAgent"))
	if !strings.Contains(got, "func RegisterTicketAgentMethodAuth(") {
		t.Fatalf("an MCP-only service with a service-level auth_services must still emit RegisterTicketAgentMethodAuth:\n%s", got)
	}
	if !strings.Contains(got, `auth.SetMethodAuth("`+carrierKey+`", []string{"ticket-agent"})`) {
		t.Fatalf("the carrier must carry the service's auth policy under its service-qualified key %q:\n%s", carrierKey, got)
	}
}
