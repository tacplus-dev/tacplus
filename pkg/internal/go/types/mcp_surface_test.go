// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
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

// The handler-interface METHOD is the one part of the suppression that came
// back: real MCP dispatch needs a typed target to call, and the decision doc
// (§8) specifies exactly this shape — one handler method per MCP-marked rpc,
// with the same signature every other unary rpc has. See
// TestGoMcpCarrierIsNotAHandlerMethodButItsToolsAre for the carrier's half.
func TestGoHandlerInterfaceCarriesMcpToolAsAnOrdinaryUnaryMethod(t *testing.T) {
	svc, _, _ := mcpToolAndControlService()

	rendered := generateGoHandlerInterface(svc, "ticket")

	if !strings.Contains(rendered, "GetTicketBrief(ctx context.Context, caller *servicemanager.Caller, req *types.GetTicketBriefRequest) (*types.GetTicketBriefResponse, error)") {
		t.Fatalf("MCP tool is not a handler method, so the generated dispatch has nothing typed to call:\n%s", rendered)
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
	// The tool is still absent from every gRPC-CALLABLE surface: no method on
	// the wrapper, no per-method auth registration. Only the handler seam came
	// back, and it is reached from the generated MCP dispatch instead.
	if strings.Contains(rendered, "func (p *TicketApi) GetTicketBrief") {
		t.Fatalf("MCP tool leaked into the Go gRPC wrapper:\n%s", rendered)
	}
	if !strings.Contains(rendered, "func (p *TicketApi) GetTicket(ctx context.Context") {
		t.Fatalf("non-MCP control rpc is missing from the Go gRPC wrapper:\n%s", rendered)
	}
	if !strings.Contains(rendered, `auth.SetMethodAuth("ticket.TicketApi/GetTicket", []string{"spiffe://*/console"})`) {
		t.Fatalf("non-MCP control rpc lost its per-method auth registration:\n%s", rendered)
	}
	if strings.Contains(rendered, `auth.SetMethodAuth("ticket.TicketApi/GetTicketBrief"`) {
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
