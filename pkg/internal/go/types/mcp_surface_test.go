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

// An RPC_PRINCIPAL_MCP rpc declares a tool, not a gRPC call, so tacplus maps it
// to no ordinary callable surface. Java and Rust already suppress it (commit
// fa915df7); these pin the Go side. Each case carries a non-MCP sibling in the
// same service as the paired control, so a test that passes by suppressing
// everything fails.

func mcpToolAndControlService() (*parser.ServiceInfo, *parser.MethodInfo, *parser.MethodInfo) {
	tool := &parser.MethodInfo{
		Name:           "GetTicketBrief",
		InputType:      "GetTicketBriefRequest",
		OutputType:     "GetTicketBriefResponse",
		FullInputType:  "ticket.GetTicketBriefRequest",
		FullOutputType: "ticket.GetTicketBriefResponse",
		HttpPath:       "", // the parser leaves an MCP rpc with no REST route
		AuthServices:   []string{"spiffe://*/assistant"},
		IsMcp:          true,
	}
	control := &parser.MethodInfo{
		Name:           "GetTicket",
		InputType:      "GetTicketRequest",
		OutputType:     "GetTicketResponse",
		FullInputType:  "ticket.GetTicketRequest",
		FullOutputType: "ticket.GetTicketResponse",
		HttpPath:       "/ticket/TicketApi/GetTicket",
		HttpMethods:    []string{"POST"},
		AuthServices:   []string{"spiffe://*/console"},
	}
	return &parser.ServiceInfo{Name: "TicketApi", Methods: []*parser.MethodInfo{tool, control}}, tool, control
}

func TestGoHandlerInterfaceOmitsMcpTool(t *testing.T) {
	svc, _, _ := mcpToolAndControlService()

	rendered := generateGoHandlerInterface(svc, "ticket")

	if strings.Contains(rendered, "GetTicketBrief") {
		t.Fatalf("MCP tool reached the Go handler interface; it is not gRPC-callable:\n%s", rendered)
	}
	if !strings.Contains(rendered, "GetTicket(ctx context.Context") {
		t.Fatalf("non-MCP control rpc is missing from the Go handler interface:\n%s", rendered)
	}
}

func TestGoServiceWrapperOmitsMcpToolMethodAndAuth(t *testing.T) {
	svc, _, _ := mcpToolAndControlService()
	info := &parser.ProtoInfo{Package: "ticket", Services: []*parser.ServiceInfo{svc}}
	outDir := t.TempDir()
	wrapperSvc := ServiceInfo{
		ProtoFile:   "svc/ticket.proto",
		PackageName: "ticket",
		DirName:     "helpdesk",
		ServiceName: "TicketApi",
		GrpcName:    "ticket",
		ParentDir:   "svc",
		Config:      &tactype.Config{OutputDir: outDir},
	}

	if err := generateServiceWrapper(wrapperSvc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil); err != nil {
		t.Fatalf("generate Go service wrapper: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, wrapperSvc.Config.GetSmPrefix(), "grpc", "servers", "svc", "helpdesk", "ticket_api.go"))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	rendered := string(raw)

	// The wrapper embeds types.Unimplemented<Service>Server, so a method it does
	// not define already answers UNIMPLEMENTED — no refusal stub is emitted, the
	// way Java's ImplBase needs none and Rust's tonic trait does.
	if !strings.Contains(rendered, "types.UnimplementedTicketApiServer") {
		t.Fatalf("wrapper no longer embeds the Unimplemented server; suppression would make it non-total:\n%s", rendered)
	}
	if strings.Contains(rendered, "GetTicketBrief") {
		t.Fatalf("MCP tool leaked into the Go gRPC wrapper:\n%s", rendered)
	}
	if !strings.Contains(rendered, "func (p *TicketApi) GetTicket(ctx context.Context") {
		t.Fatalf("non-MCP control rpc is missing from the Go gRPC wrapper:\n%s", rendered)
	}
	if !strings.Contains(rendered, `auth.SetMethodAuth("GetTicket", []string{"spiffe://*/console"})`) {
		t.Fatalf("non-MCP control rpc lost its per-method auth registration:\n%s", rendered)
	}
	if strings.Contains(rendered, `auth.SetMethodAuth("GetTicketBrief"`) {
		t.Fatalf("MCP tool registered per-method auth for a gRPC method that does not exist:\n%s", rendered)
	}
}

// The MCP tool has no HttpPath, and generateGoW2SClient refuses any method
// without one. Skipping it is what keeps a w2s-enabled service that declares an
// MCP tool generatable at all — and its types must drop out of the import block
// with it, since the w2s client is rendered by format.Source, which does not
// prune an unused import.
func TestGoW2SClientImportsSkipMcpToolTypes(t *testing.T) {
	svc, tool, _ := mcpToolAndControlService()
	tool.FullInputType = "assistant.GetTicketBriefRequest"
	tool.FullOutputType = "assistant.GetTicketBriefResponse"

	imports := collectGoW2SReferencedTypeImports(
		svc, "ticket",
		map[string]string{"assistant": "svc/assistant"},
		"example.com/mod", "svc", "ticket",
	)

	if strings.Contains(imports.crossPackageImports, "assistant") {
		t.Fatalf("MCP-tool-only package reached the Go w2s client imports; format.Source will not prune it:\n%s", imports.crossPackageImports)
	}
	if !imports.hasTypesImport {
		t.Fatal("non-MCP control rpc's own-package types no longer pull the types import")
	}
}
