// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// The Rust MCP dispatch seam. An RPC_PRINCIPAL_MCP rpc is not gRPC-callable
// (fa915df7), but it IS served — over the generated session carrier — so these
// pin what the generated termination does with a tool call. Every case carries a
// non-MCP sibling in the same service as the paired control, so a test that
// would pass by generating nothing at all fails instead.

func rustMcpTestSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
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

// rustMcpTestService is a service with one MCP tool, its generated session
// carrier, and one ordinary rpc as the control.
func rustMcpTestService() (*parser.ServiceInfo, *parser.MethodInfo, *parser.MethodInfo, *parser.MethodInfo) {
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
	svc := &parser.ServiceInfo{
		Name:       "TicketApi",
		IsC2S:      true,
		HasMcpRpcs: true,
		Methods:    []*parser.MethodInfo{tool, control, carrier},
	}
	return svc, tool, control, carrier
}

func mustRustMcpDispatch(t *testing.T, svc *parser.ServiceInfo) string {
	t.Helper()
	out, err := generateRustMcpDispatch(svc, "ticket", map[string]string{}, rustMcpTestSanitizer())
	if err != nil {
		t.Fatalf("generateRustMcpDispatch(%s): %v", svc.Name, err)
	}
	return out
}

func TestRustMcpDispatchPublishesTheToolAndDispatchesToTheHandler(t *testing.T) {
	svc, _, _, _ := rustMcpTestService()

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		// The tool is published under its registry.json tool name, with the
		// rpc's own doc comment as the description.
		`pub static TICKET_API_MCP_TOOLS: &[TicketApiMcpTool]`,
		`name: "get_ticket_brief"`,
		`description: "GetTicketBrief summarizes one ticket for an assistant."`,
		// tools/list and tools/call are answered by an rmcp ServerHandler.
		`impl rmcp::ServerHandler for TicketApiMcpSurface`,
		`fn list_tools(`,
		`fn call_tool(`,
		`"get_ticket_brief" => self.call_get_ticket_brief(arguments).await,`,
		// The tool call is decoded into the rpc's own typed request and
		// dispatched to the service's own typed handler method.
		`let req: GetTicketBriefRequest = serde_json::from_value(`,
		`let resp = self.handler.get_ticket_brief(caller, req).await?;`,
		// And the session is carried by a generated rmcp worker transport.
		`pub fn serve_ticket_api_mcp_session(`,
		`impl rmcp::transport::worker::Worker for TicketApiMcpSessionWorker`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated MCP dispatch is missing %q\ngot:\n%s", want, got)
		}
	}

	// The control rpc is an ordinary gRPC call and gets no tool of its own.
	for _, unwanted := range []string{`"get_ticket"`, `call_get_ticket(`} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("the non-MCP control rpc reached the MCP tool surface (%q):\n%s", unwanted, got)
		}
	}
}

// An in-process caller (a service acting as its own MCP client, e.g. an
// agentic loop dispatching its own tool calls) has no
// rmcp::service::RequestContext<RoleServer> and must not fabricate one just to
// reach the generated tool logic — rmcp::Peer::<R>::new is crate-private, so a
// live session cannot be built from outside rmcp. The surface must therefore
// expose a session-free entry point, with call_tool as a thin wrapper around
// it rather than the only way in.
func TestRustMcpSurfaceExposesASessionFreeDispatchEntryPoint(t *testing.T) {
	svc, _, _, _ := rustMcpTestService()

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		`pub async fn call(`,
		`        name: &str,`,
		`        arguments: rmcp::model::JsonObject,`,
		`) -> Result<serde_json::Value, tonic::Status> {`,
		`self.call(request.name.as_ref(), arguments).await`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated MCP surface is missing session-free dispatch %q\ngot:\n%s", want, got)
		}
	}
}

// A service with no RPC_PRINCIPAL_MCP rpc is the paired control for the whole
// seam: it has no carrier, so it gets no MCP termination at all.
func TestRustMcpDispatchIsEmptyWithoutAnMcpRpc(t *testing.T) {
	_, _, control, _ := rustMcpTestService()
	svc := &parser.ServiceInfo{Name: "TicketApi", IsC2S: true, Methods: []*parser.MethodInfo{control}}

	got := mustRustMcpDispatch(t, svc)

	if got != "" {
		t.Fatalf("a service with no MCP rpc got an MCP termination:\n%s", got)
	}
}

// The tool's schema is the rpc's request message minus every field the server
// computes. Absence is the enforcement: a field that is not in the schema is
// not a shape of tool call the model can make.
func TestRustMcpToolSchemaOmitsServerComputedFields(t *testing.T) {
	_, tool, _, _ := rustMcpTestService()

	raw, err := rustMcpInputSchema(tool, "ticket", rustMcpTestSanitizer())
	if err != nil {
		t.Fatalf("rustMcpInputSchema: %v", err)
	}
	var schema struct {
		Type                 string                    `json:"type"`
		AdditionalProperties bool                      `json:"additionalProperties"`
		Required             []string                  `json:"required"`
		Properties           map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("the generated tool schema is not valid JSON (%v): %s", err, raw)
	}

	if schema.Type != "object" || schema.AdditionalProperties {
		t.Fatalf("tool schema must be a closed object; got type=%q additionalProperties=%v", schema.Type, schema.AdditionalProperties)
	}
	for _, absent := range []string{"requester", "trace_id", "noop"} {
		if _, ok := schema.Properties[absent]; ok {
			t.Fatalf("%q is server-computed but reached the tool schema, making it an LLM-writable argument: %s", absent, raw)
		}
	}
	for field, wantType := range map[string]string{"ticket_id": "string", "max_lines": "integer"} {
		p, ok := schema.Properties[field]
		if !ok {
			t.Fatalf("%q is an ordinary request field but is missing from the tool schema: %s", field, raw)
		}
		if p["type"] != wantType {
			t.Fatalf("%q has type %v in the tool schema, want %q", field, p["type"], wantType)
		}
	}
	if p := schema.Properties["ticket_id"]; p["description"] != "The ticket to summarize." {
		t.Fatalf("the field's own doc comment did not reach the tool schema: %v", p)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "ticket_id" {
		t.Fatalf("(spec.assert_required) did not reach the schema's required set: %v", schema.Required)
	}
}

// The (spec.principal) field is stamped from the verified caller by the SAME
// renderer the ordinary gRPC wrapper uses, and a tool call that names it is
// refused rather than silently overridden.
func TestRustMcpDispatchStampsAndRefusesThePrincipal(t *testing.T) {
	svc, _, _, _ := rustMcpTestService()

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		`if arguments.contains_key("requester")`,
		`"requester is set by the server from your verified identity; it is not a tool argument"`,
		// principal_gen.go's own block, verbatim — not a second implementation.
		`// (spec.principal) requester: bind the field to the authenticated caller.`,
		`req.requester = __principal;`,
		`} else if req.requester != __principal {`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated MCP dispatch is missing the principal guard %q\ngot:\n%s", want, got)
		}
	}
}

// The service is never asked to hand-write MCP glue: the carrier rpc is served
// from the generated dispatch, so it is absent from the handler trait, while the
// tool it carries IS a handler method.
func TestRustMcpCarrierIsNotAHandlerMethodButItsToolsAre(t *testing.T) {
	svc, tool, control, carrier := rustMcpTestService()

	if got := rustMcpCarrierMethod(svc); got != carrier {
		t.Fatalf("rustMcpCarrierMethod resolved %v, want the generated carrier", got)
	}
	if rustMcpCarrierMethod(&parser.ServiceInfo{Name: "TicketApi", Methods: []*parser.MethodInfo{control}}) != nil {
		t.Fatal("a service with no MCP rpc resolved a session carrier")
	}

	toolMethod := generateRustHandlerMethod(tool, "ticket", map[string]string{})
	if !strings.Contains(toolMethod, "async fn get_ticket_brief(&self, caller: Option<&servicemanager::auth::Caller>, req: GetTicketBriefRequest)") {
		t.Fatalf("the MCP tool's handler method is not the ordinary typed shape: %s", toolMethod)
	}

	wrapper := generateRustMcpCarrierWrapper(svc, carrier, "ticket", "svc", map[string]string{})
	for _, want := range []string{
		// It authorizes exactly as an ordinary rpc does...
		`self.authorize_method("/svc/ticket.TicketApi/BrokerTicketApiMcpSession", &request)`,
		// ...fails closed without a verified caller...
		`tonic::Status::unauthenticated("an MCP session requires an authenticated caller")`,
		// ...and hands the stream to the generated session server rather than
		// to a handler method.
		`Ok(tonic::Response::new(serve_ticket_api_mcp_session(`,
		`self.handler.clone(),`,
	} {
		if !strings.Contains(wrapper, want) {
			t.Fatalf("generated carrier wrapper is missing %q\ngot:\n%s", want, wrapper)
		}
	}
	if strings.Contains(wrapper, "self.handler.broker_ticket_api_mcp_session") {
		t.Fatalf("the carrier still delegates to a hand-written handler method:\n%s", wrapper)
	}
}

// Dispatch means the tool is really served now, so the refusals the service
// wrapper makes for a gRPC rpc it cannot guard apply to an MCP tool too. Before
// this seam the tool was suppressed entirely and they were skipped for it.
func TestRustMcpDispatchRefusesAToolItCannotGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*parser.MethodInfo)
		match string
	}{
		{"min_group_size", func(m *parser.MethodInfo) { m.MinGroupSize = 5 }, "min_group_size"},
		// A PAGE collection hands the handler a page window, so dispatch would
		// render a 2-argument call against a 3-argument handler: uncallable
		// generated code. Go and Java refuse the same shape.
		{"method_collection", func(m *parser.MethodInfo) {
			m.Collection = &parser.CollectionDescriptor{Mode: "PAGE"}
		}, "declares method_collection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tool, _, _ := rustMcpTestService()
			tc.mut(tool)

			_, err := generateRustMcpDispatch(svc, "ticket", map[string]string{}, rustMcpTestSanitizer())
			if err == nil {
				t.Fatal("an MCP tool this seam cannot guard generated dispatch anyway")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// The generated tests are part of the seam: a service that grows an MCP tool
// gets the regression gate with it.
func TestRustMcpDispatchGeneratesItsOwnTests(t *testing.T) {
	svc, _, _, _ := rustMcpTestService()

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		`mod ticket_api_mcp_tests {`,
		`async fn get_ticket_brief_reaches_its_typed_handler()`,
		`async fn get_ticket_brief_refuses_a_client_supplied_requester()`,
		`fn the_mcp_tool_set_is_published()`,
		// The reporting handler must implement the control rpc too, or the
		// generated test module would not compile.
		`async fn get_ticket(&self, _caller: Option<&servicemanager::auth::Caller>, _req: GetTicketRequest)`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated MCP tests are missing %q\ngot:\n%s", want, got)
		}
	}
}
