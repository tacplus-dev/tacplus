// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// The Go MCP dispatch seam. An RPC_PRINCIPAL_MCP rpc is not gRPC-callable
// (a9a79e74), but it IS served — over the generated session carrier — so these
// pin what the generated termination does with a tool call. Every case carries a
// non-MCP sibling in the same service as the paired control, so a test that
// would pass by generating nothing at all fails instead.

func goMcpTestSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "requester", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"principal": {BoolValue: true}}},
			{Name: "ticket_id", Type: "string", Number: 2, Comment: "The ticket to summarize.",
				Annotations: map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}},
			{Name: "max_lines", Type: "int32", Number: 3},
			{Name: "trace_id", Type: "string", Number: 200},
			{Name: "noop", Type: "bool", Number: 201},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{
			{Name: "summary", Type: "string", Number: 1},
		}},
		{Name: "GetTicketRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1},
		}},
		{Name: "GetTicketResponse", Fields: []model.Field{
			{Name: "body", Type: "string", Number: 1},
		}},
	}}})
}

// goMcpTestService is one MCP tool, its non-MCP sibling, and the carrier the
// synthesizer would have injected for them.
func goMcpTestService() *parser.ServiceInfo {
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
	control := &parser.MethodInfo{
		Name:           "GetTicket",
		InputType:      "GetTicketRequest",
		OutputType:     "GetTicketResponse",
		FullInputType:  "ticket.GetTicketRequest",
		FullOutputType: "ticket.GetTicketResponse",
		HttpPath:       "/ticket/TicketApi/GetTicket",
		HttpMethods:    []string{"POST"},
	}
	carrier := &parser.MethodInfo{
		Name:            parser.McpCarrierRpcName("TicketApi"),
		InputType:       "McpRequest",
		OutputType:      "McpResponse",
		FullInputType:   "mcp.McpRequest",
		FullOutputType:  "mcp.McpResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}
	return &parser.ServiceInfo{
		Name:       "TicketApi",
		Methods:    []*parser.MethodInfo{tool, control, carrier},
		HasMcpRpcs: true,
	}
}

func goMcpTestParams() goMcpFileParams {
	return goMcpFileParams{
		PackageName:              "ticket",
		TypesModulePath:          "example.com/mod/types/svc/ticket",
		ServiceManagerImportPath: "example.com/mod/chassis/servicemanager",
		ModuleBasePath:           "example.com/mod",
		CrossPackageTypes:        map[string]string{"mcp": "sys/mcp"},
		ServicePackage:           "ticket",
		used:                     map[string]bool{"mcp": true},
	}
}

func goMcpRender(t *testing.T) string {
	t.Helper()
	rendered, err := generateGoMcpDispatch(goMcpTestService(), goMcpTestParams(), goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	return rendered
}

func TestGoMcpDispatchPublishesTheToolAndDispatchesToTheHandler(t *testing.T) {
	rendered := goMcpRender(t)

	if !strings.Contains(rendered, `Name:        "get_ticket_brief"`) {
		t.Fatalf("the MCP tool is not in the published tool set:\n%s", rendered)
	}
	if !strings.Contains(rendered, "s.handler.GetTicketBrief(sanitizeContext(ctx), s.caller, req)") {
		t.Fatalf("the tool does not dispatch to the service's own typed handler method:\n%s", rendered)
	}
	// The non-MCP sibling is neither published nor routed.
	if strings.Contains(rendered, "GetTicket(sanitizeContext") || strings.Contains(rendered, `"get_ticket"`) {
		t.Fatalf("a non-MCP rpc in the same service reached the MCP surface:\n%s", rendered)
	}
	// The carriage is generated, not left to the service.
	if !strings.Contains(rendered, "func serveTicketApiMcpSession(") {
		t.Fatalf("no generated session server for the carrier rpc:\n%s", rendered)
	}
	if !strings.Contains(rendered, "func (c *ticketApiMcpConn) Read(") {
		t.Fatalf("no generated transport over the gRPC stream:\n%s", rendered)
	}
}

func TestGoMcpDispatchIsEmptyWithoutAnMcpRpc(t *testing.T) {
	svc := goMcpTestService()
	// The paired control: the same service with the MCP marker taken off. The
	// carrier would not have been synthesized for it either.
	svc.HasMcpRpcs = false
	svc.Methods[0].IsMcp = false

	rendered, err := generateGoMcpDispatch(svc, goMcpTestParams(), goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	if rendered != "" {
		t.Fatalf("a service with no MCP rpc got an MCP termination:\n%s", rendered)
	}
}

func TestGoMcpToolSchemaOmitsServerComputedFields(t *testing.T) {
	tools, err := goMcpTools(goMcpTestService(), "ticket", goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected exactly the one MCP tool, got %d", len(tools))
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatalf("the generated schema is not JSON: %v\n%s", err, tools[0].Schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("the generated schema has no properties object:\n%s", tools[0].Schema)
	}
	// (spec.principal) is the server's to compute, so it is not a shape of call
	// the model can make.
	if _, ok := props["requester"]; ok {
		t.Fatalf("the (spec.principal) field is a tool argument:\n%s", tools[0].Schema)
	}
	// tacplus's own transport fields are not part of the rpc's contract.
	for _, injected := range []string{"trace_id", "noop"} {
		if _, ok := props[injected]; ok {
			t.Fatalf("the injected %s field is a tool argument:\n%s", injected, tools[0].Schema)
		}
	}
	// The paired control: the ordinary fields ARE published, with their comments.
	if _, ok := props["ticket_id"]; !ok {
		t.Fatalf("an ordinary request field is missing from the schema:\n%s", tools[0].Schema)
	}
	if _, ok := props["max_lines"]; !ok {
		t.Fatalf("an ordinary request field is missing from the schema:\n%s", tools[0].Schema)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("the schema does not close itself to unknown arguments:\n%s", tools[0].Schema)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "ticket_id" {
		t.Fatalf("the required set is not the declared one:\n%s", tools[0].Schema)
	}
}

func TestGoMcpDispatchStampsAndRefusesThePrincipal(t *testing.T) {
	rendered := goMcpRender(t)

	if !strings.Contains(rendered, `if named["requester"] {`) {
		t.Fatalf("a tool call naming the (spec.principal) field is not refused:\n%s", rendered)
	}
	if !strings.Contains(rendered, "req.Requester = __principal") {
		t.Fatalf("the (spec.principal) field is not stamped from the verified caller:\n%s", rendered)
	}
	if !strings.Contains(rendered, "__principal = s.caller.Token().GetSubject()") {
		t.Fatalf("the stamp does not read the caller's verified subject:\n%s", rendered)
	}
	if !strings.Contains(rendered, `} else if req.Requester != __principal {`) {
		t.Fatalf("a mismatched (spec.principal) is silently overwritten rather than refused:\n%s", rendered)
	}
}

func TestGoMcpCarrierIsNotAHandlerMethodButItsToolsAre(t *testing.T) {
	svc := goMcpTestService()
	rendered := generateGoHandlerInterface(svc, "ticket")

	// Q1: the MCP tool is back on the handler interface, with the same signature
	// any other unary rpc has — that is what dispatch calls.
	if !strings.Contains(rendered, "GetTicketBrief(ctx context.Context, caller *servicemanager.Caller, req *types.GetTicketBriefRequest) (*types.GetTicketBriefResponse, error)") {
		t.Fatalf("the MCP tool is not a handler method, so dispatch has nothing typed to call:\n%s", rendered)
	}
	// Q2: the carrier is not. Nothing about carrying a session is the service's
	// to write, and a Go interface has no default body to give it one.
	if strings.Contains(rendered, parser.McpCarrierRpcName("TicketApi")) {
		t.Fatalf("the generated MCP session carrier reached the handler interface:\n%s", rendered)
	}
	// The paired control: an ordinary rpc is still there.
	if !strings.Contains(rendered, "GetTicket(ctx context.Context") {
		t.Fatalf("a non-MCP rpc fell off the handler interface:\n%s", rendered)
	}
}

func TestGoMcpDispatchRefusesAToolItCannotGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*parser.MethodInfo)
		match string
	}{
		{"streaming", func(m *parser.MethodInfo) { m.ServerStreaming = true }, "is streaming"},
		{"min_group_size", func(m *parser.MethodInfo) { m.MinGroupSize = 5 }, "(spec.min_group_size)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := goMcpTestService()
			tc.mut(svc.Methods[0])
			_, err := generateGoMcpDispatch(svc, goMcpTestParams(), goMcpTestSanitizer())
			if err == nil {
				t.Fatalf("an MCP tool this seam cannot guard was generated anyway")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestGoMcpDispatchGeneratesItsOwnTests(t *testing.T) {
	svc := goMcpTestService()
	tools, err := goMcpTools(svc, "ticket", goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	rendered := generateGoMcpDispatchTests(svc, tools, goMcpTestParams())

	for _, want := range []string{
		"func TestTicketApiMcpToolSetIsPublished(",
		"func TestTicketApiMcpGetTicketBriefReachesItsTypedHandler(",
		"func TestTicketApiMcpGetTicketBriefRefusesAClientSuppliedRequester(",
		"func TestTicketApiMcpSessionServesItsTools(",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the generated gate is missing %s:\n%s", want, rendered)
		}
	}
	// The gate drives a REAL call, with the required scalar synthesized and the
	// caller-scoped field left to the server.
	if !strings.Contains(rendered, `{\"ticket_id\": \"mcp-test-ticket_id\"}`) {
		t.Fatalf("the generated gate does not synthesize the tool's required arguments:\n%s", rendered)
	}
	// The paired control: the non-MCP sibling is asserted ABSENT from the tool set.
	if !strings.Contains(rendered, `if _, ok := byName["get_ticket"]; ok {`) {
		t.Fatalf("the generated gate does not check that a non-MCP rpc stays unpublished:\n%s", rendered)
	}
}
