// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// A service shaped exactly like docmanageragent: every method is either an
// MCP tool or the synthesized session carrier, none is an ordinary rpc. Both
// the tool and the carrier `continue` out of the auth-registration loop in
// generateServiceWrapper, so a service-level AuthServices annotation used to
// vanish — ServiceManager still called the now-undefined
// register_method_auth (generator.go's hasMethodAuth only checks
// len(AuthServices) > 0, not whether any method actually rendered a line),
// and even once that compiled, the carrier itself carried no auth policy at
// all.
func rustMcpOnlyAuthTestService() *parser.ServiceInfo {
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
	return &parser.ServiceInfo{
		Name:         "TicketAgent",
		IsC2S:        true,
		HasMcpRpcs:   true,
		AuthServices: []string{"ticket-agent"},
		Methods:      []*parser.MethodInfo{tool, carrier},
	}
}

func TestRustMcpOnlyServiceRegistersCarrierMethodAuth(t *testing.T) {
	targetService := rustMcpOnlyAuthTestService()
	protoInfo := &parser.ProtoInfo{
		Package:  "ticket",
		Services: []*parser.ServiceInfo{targetService},
	}
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "ticket/agent.proto",
		PackageName:  "ticket",
		ServiceName:  "TicketAgent",
		GrpcName:     "ticket_agent",
		IsGrpc:       true,
		IsC2S:        true,
		HasMcp:       true,
		AuthServices: []string{"ticket-agent"},
		OutputDir:    outputDir,
		ParentDir:    "services",
		Config:       &tactype.Config{OutputDir: outputDir},
	}

	if err := generateServiceWrapper(svc, protoInfo, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustMcpTestSanitizer()); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}

	entries, err := os.ReadDir(svc.OutputDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one generated file in %s, got %v (err %v)", svc.OutputDir, entries, err)
	}
	rendered, err := os.ReadFile(filepath.Join(svc.OutputDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	got := string(rendered)

	// Every method in this service is MCP tool or carrier, so
	// register_method_auth must still be emitted (matching generator.go's
	// hasMethodAuth, which is true whenever the service has methods and a
	// service-level AuthServices) and must carry the carrier's own line —
	// not just be a dangling call with an empty function.
	carrierName := parser.McpCarrierRpcName("TicketAgent")
	wantLine := `auth.set_method_auth("` + carrierName + `", &["ticket-agent"]);`
	if !strings.Contains(got, "pub fn register_method_auth(") {
		t.Fatalf("MCP-only service with AuthServices must still emit register_method_auth:\n%s", got)
	}
	if !strings.Contains(got, wantLine) {
		t.Fatalf("register_method_auth must register the MCP carrier %q itself:\n%s", wantLine, got)
	}
}
