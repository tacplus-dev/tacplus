// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// The Java MCP dispatch seam. An RPC_PRINCIPAL_MCP rpc is not gRPC-callable
// (fa915df7), but it IS served — over the generated session carrier — so these
// pin what the generated termination does with a tool call. Every case carries a
// non-MCP sibling in the same service as the paired control, so a test that
// would pass by generating nothing at all fails instead.

func javaMcpTestSanitizer() *javaEgressSanitizer {
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
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
		{Name: "GetTicketRosterRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"namespace": {BoolValue: true}}},
		}},
		{Name: "GetTicketRosterResponse", Fields: []model.Field{
			{Name: "rows", Type: "TicketRosterRow", TypePackage: "ticket", Number: 1, IsRepeated: true, IsMessage: true},
			{Name: "note", Type: "string", Number: 2},
		}},
		{Name: "TicketRosterRow", Fields: []model.Field{
			{Name: "row_id", Type: "string", Number: 1},
			{Name: "owner", Type: "string", Number: 2,
				Annotations: map[string]model.AnnotationValue{"store_principal": {BoolValue: true}}},
			{Name: "tenant", Type: "string", Number: 3,
				Annotations: map[string]model.AnnotationValue{"store_namespace": {BoolValue: true}}},
		}},
		{Name: "GetTicketRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1},
		}},
		{Name: "GetTicketResponse", Fields: []model.Field{
			{Name: "body", Type: "string", Number: 1},
		}},
	}}})
}

// javaMcpTestService is two MCP tools, their non-MCP sibling, and the carrier
// the synthesizer would have injected for them.
func javaMcpTestService() *parser.ServiceInfo {
	brief := &parser.MethodInfo{
		Name:           "GetTicketBrief",
		InputType:      "GetTicketBriefRequest",
		OutputType:     "GetTicketBriefResponse",
		FullInputType:  "ticket.GetTicketBriefRequest",
		FullOutputType: "ticket.GetTicketBriefResponse",
		IsMcp:          true,
		McpToolName:    "get_ticket_brief",
		Comment:        "GetTicketBrief summarizes one ticket for an assistant.",
	}
	roster := &parser.MethodInfo{
		Name:           "GetTicketRoster",
		InputType:      "GetTicketRosterRequest",
		OutputType:     "GetTicketRosterResponse",
		FullInputType:  "ticket.GetTicketRosterRequest",
		FullOutputType: "ticket.GetTicketRosterResponse",
		IsMcp:          true,
		McpToolName:    "get_ticket_roster",
		Comment:        "GetTicketRoster lists the tickets on this tenant's roster.",
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
	return &parser.ServiceInfo{
		Name:       "TicketApi",
		Methods:    []*parser.MethodInfo{brief, roster, control, carrier},
		HasMcpRpcs: true,
	}
}

func javaMcpTestFileParams() javaMcpFileParams {
	return javaMcpFileParams{
		JavaPackage:       "com.example.s2sm.internal.grpc.servers.svc.ticket",
		ProtoFile:         "svc/ticket.proto",
		FrameRequestType:  "Mcp.McpRequest",
		FrameResponseType: "Mcp.McpResponse",
		Imports:           "import com.example.types.svc.ticket.*;",
		NonToolHandlerDecls: []string{
			"        Ticket.GetTicketResponse getTicket(Caller caller, Ticket.GetTicketRequest request) throws ProtoException;\n",
		},
	}
}

func javaMcpResolvedTools(t *testing.T) []javaMcpTool {
	t.Helper()
	tools, err := javaMcpTools(javaMcpTestService(), "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	return tools
}

func javaMcpRender(t *testing.T) string {
	t.Helper()
	return generateJavaMcpDispatch(javaMcpTestService(), javaMcpResolvedTools(t), javaMcpTestFileParams())
}

func TestJavaMcpDispatchPublishesTheToolAndDispatchesToTheHandler(t *testing.T) {
	rendered := javaMcpRender(t)

	if !strings.Contains(rendered, `McpSchema.Tool.builder("get_ticket_brief", JSON, SCHEMA_GET_TICKET_BRIEF)`) {
		t.Fatalf("the MCP tool is not in the published tool set:\n%s", rendered)
	}
	if !strings.Contains(rendered, "handler.getTicketBrief(caller, __req)") {
		t.Fatalf("the tool does not dispatch to the service's own typed handler method:\n%s", rendered)
	}
	// The non-MCP sibling is neither published nor routed.
	if strings.Contains(rendered, "handler.getTicket(caller") || strings.Contains(rendered, `"get_ticket"`) {
		t.Fatalf("a non-MCP rpc in the same service reached the MCP surface:\n%s", rendered)
	}
	// The carriage is generated, not left to the service.
	if !strings.Contains(rendered, "public static StreamObserver<Mcp.McpRequest> serve(") {
		t.Fatalf("no generated session server for the carrier rpc:\n%s", rendered)
	}
	if !strings.Contains(rendered, "implements McpServerTransportProvider") ||
		!strings.Contains(rendered, "implements McpServerTransport") {
		t.Fatalf("no generated transport over the gRPC stream:\n%s", rendered)
	}
	// The carrier is not itself published as a tool.
	if strings.Contains(rendered, `"broker_ticket_api_mcp_session"`) {
		t.Fatalf("the session carrier was published as a tool:\n%s", rendered)
	}
}

func TestJavaMcpToolSchemaOmitsServerComputedFields(t *testing.T) {
	tools := javaMcpResolvedTools(t)
	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatalf("the emitted tool schema is not JSON: %v\n%s", err, tools[0].Schema)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("the tool schema has no properties:\n%s", tools[0].Schema)
	}
	// (spec.principal) is the server's to compute; the model cannot name it.
	if _, ok := props["requester"]; ok {
		t.Fatalf("the (spec.principal) field is in the tool schema:\n%s", tools[0].Schema)
	}
	// tacplus's own transport fields are not part of the rpc's contract.
	for _, injected := range []string{"trace_id", "noop"} {
		if _, ok := props[injected]; ok {
			t.Fatalf("the injected %s field is in the tool schema:\n%s", injected, tools[0].Schema)
		}
	}
	// The user-facing fields ARE, with their own doc comments.
	if _, ok := props["ticket_id"]; !ok {
		t.Fatalf("a user-facing field is missing from the tool schema:\n%s", tools[0].Schema)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("the tool schema does not deny unknown fields:\n%s", tools[0].Schema)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "ticket_id" {
		t.Fatalf("the tool schema's required set is wrong:\n%s", tools[0].Schema)
	}
}

func TestJavaMcpDispatchStampsAndRefusesTheCallerScopedFields(t *testing.T) {
	rendered := javaMcpRender(t)

	// Refused BY NAME, before the decode — a (spec.principal) field is a KNOWN
	// field of the message, so the decoder would accept it happily.
	for _, want := range []string{
		`if (argumentNames(request).contains("requester")) {`,
		`if (argumentNames(request).contains("tenant")) {`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("a server-computed argument is not refused by name (%s):\n%s", want, rendered)
		}
	}
	// Then stamped from the caller the session is bound to — not from the gRPC
	// Context, which is not in scope on the SDK's dispatch thread.
	for _, want := range []string{
		"final String __principal = caller.getSubject();",
		"__req = __req.toBuilder().setRequester(__principal).build();",
		"final String __namespace = caller.getNamespace();",
		"__req = __req.toBuilder().setTenant(__namespace).build();",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the caller-scoped stamp is missing (%s):\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Auth.verifiedTokenFromContext()") ||
		strings.Contains(rendered, "Auth.callerNamespaceFromContext()") {
		t.Fatalf("the dispatch read the caller off the gRPC Context, which is not in scope on the SDK's dispatch thread:\n%s", rendered)
	}
}

func TestJavaMcpDispatchGuardOrderIsRefuseDecodeValidateStampCall(t *testing.T) {
	rendered := javaMcpRender(t)
	brief := rendered[strings.Index(rendered, "callGetTicketBrief(McpSchema.CallToolRequest request)"):]
	brief = brief[:strings.Index(brief, "callGetTicketRoster")]

	order := []string{
		`argumentNames(request).contains("requester")`,   // refuse
		"decode(request, __b);",                          // decode
		"Helpers.validateGetTicketBriefRequest(__req);",  // validate request
		"setRequester(__principal)",                      // stamp
		"handler.getTicketBrief(caller, __req)",          // call
		"Helpers.validateGetTicketBriefResponse(__resp)", // validate response
	}
	at := 0
	for _, step := range order {
		i := strings.Index(brief[at:], step)
		if i < 0 {
			t.Fatalf("the dispatch is missing guard step %q:\n%s", step, brief)
		}
		at += i
	}
}

func TestJavaMcpDispatchFiltersCallerScopedRows(t *testing.T) {
	rendered := javaMcpRender(t)

	// (spec.store_principal): drop the row and serve the rest.
	if !strings.Contains(rendered, "if (!__rowPrincipal.equals(__fb.getRows(__pi).getOwner())) { __fb.removeRows(__pi); }") {
		t.Fatalf("the (spec.store_principal) egress row filter is missing:\n%s", rendered)
	}
	// (spec.store_namespace): drop the row AND halt — a foreign tenant's row
	// reaching this comparison means the store's own query is leaking. The halt
	// is the record plane's own renderer (javaNamespaceCrash), not a second one.
	if !strings.Contains(rendered, "Runtime.getRuntime().halt(1);") {
		t.Fatalf("the (spec.store_namespace) backstop does not halt the JVM:\n%s", rendered)
	}
	if !strings.Contains(rendered, "FATAL: (spec.store_namespace) backstop fired") {
		t.Fatalf("the (spec.store_namespace) backstop does not say why it fired:\n%s", rendered)
	}
	// halt, not exit: shutdown hooks draining in-flight work would let the very
	// rows this fired over finish leaving the process.
	if strings.Contains(rendered, "System.exit(1)") {
		t.Fatalf("the namespace backstop must halt, not exit:\n%s", rendered)
	}
	// The principal filter runs first, over the whole row set, and the namespace
	// backstop then runs over what it left.
	pi := strings.Index(rendered, "__rowPrincipal.equals")
	ni := strings.Index(rendered, "__rowNamespace.equals")
	if pi < 0 || ni < 0 || pi > ni {
		t.Fatalf("the principal filter must run before the namespace backstop:\n%s", rendered)
	}
}

func TestJavaMcpDispatchRefusesAToolItCannotGuard(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m *parser.MethodInfo)
		want   string
	}{
		{"streaming", func(m *parser.MethodInfo) { m.ServerStreaming = true }, "is streaming"},
		{"min_group_size", func(m *parser.MethodInfo) { m.MinGroupSize = 5 }, "min_group_size"},
		// A SNAPSHOT collection hands the handler a materialized collection (or a
		// cursor, or a stream), so dispatch would render a call against a
		// signature it does not have. PAGE is the one mode a tool call can
		// drive; every other one is still refused.
		{"method_collection", func(m *parser.MethodInfo) {
			m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT"}
		}, "declares method_collection in mode SNAPSHOT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := javaMcpTestService()
			tc.mutate(svc.Methods[0])
			_, err := javaMcpTools(svc, "ticket", "Ticket",
				map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpTestSanitizer())
			if err == nil {
				t.Fatalf("a tool this seam cannot guard was generated anyway")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say why: %v", err)
			}
		})
	}
}

func TestJavaMcpDispatchIsEmptyWithoutAnMcpRpc(t *testing.T) {
	svc := javaMcpTestService()
	svc.HasMcpRpcs = false
	if javaMcpCarrierMethod(svc) != nil {
		t.Fatalf("a service with no MCP rpc resolved a session carrier")
	}
}

func TestJavaMcpDispatchGeneratesItsOwnGate(t *testing.T) {
	rendered := generateJavaMcpDispatchTest(javaMcpTestService(), javaMcpResolvedTools(t), javaMcpTestFileParams())

	// The two cases the seam exists for.
	if !strings.Contains(rendered, "getTicketBriefReachesItsTypedHandler()") {
		t.Fatalf("no generated case proving a tool call reaches the typed handler:\n%s", rendered)
	}
	if !strings.Contains(rendered, "getTicketBriefRefusesAClientSuppliedRequester()") {
		t.Fatalf("no generated case proving a client-supplied principal is refused:\n%s", rendered)
	}
	// The stamped value is asserted against what the HANDLER received, not
	// against what the dispatch claims to have sent.
	if !strings.Contains(rendered, `check(stub.lastRequest.contains("spiffe://example.test/mcp-test-caller")`) {
		t.Fatalf("the generated gate does not read the stamp back off the handler's own request:\n%s", rendered)
	}
	// And a whole session over the generated carrier, as raw JSON-RPC frames.
	if !strings.Contains(rendered, "mcpSessionServesItsTools()") ||
		!strings.Contains(rendered, "TicketApiMcp.serve(stub, testCaller(), responses)") {
		t.Fatalf("no generated case driving a whole session over the carrier:\n%s", rendered)
	}
	if !strings.Contains(rendered, `outbound.get(outbound.size() - 1).contains("-32700")`) {
		t.Fatalf("the generated session case does not assert an undecodable frame is answered:\n%s", rendered)
	}
	// The stub implements the WHOLE handler interface, so a misrouted dispatch
	// is a failure rather than a compile error deferred to the consumer.
	if !strings.Contains(rendered, "getTicket is not an MCP tool and must not be reached by tool dispatch") {
		t.Fatalf("the generated stub does not refuse a non-tool rpc:\n%s", rendered)
	}
	if !strings.Contains(rendered, "System.exit(1);") || !strings.Contains(rendered, "PASS TicketApiMcpTest") {
		t.Fatalf("the generated gate is not a main-method self-test:\n%s", rendered)
	}
}

func TestJavaQuoteEscapesForJava(t *testing.T) {
	cases := map[string]string{
		`plain`:            `"plain"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		"line\nbreak":      `"line\nbreak"`,
		"caf\u00e9":        `"caf\u00e9"`,
		"emoji \U0001F600": `"emoji \ud83d\ude00"`,
	}
	for in, want := range cases {
		if got := javaQuote(in); got != want {
			t.Fatalf("javaQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// javaMcpPageSanitizer is the fixture's message set with GetTicketBrief's
// request and response reshaped as a PAGE pair — page_size/page_token in, one
// repeated items field plus next_page_token/total_count out. Collection
// validation requires exactly this shape of a PAGE rpc, so the schema the
// emitter derives is derived from it.
func javaMcpPageSanitizer() *javaEgressSanitizer {
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1, Comment: "The ticket to summarize."},
			{Name: "page_size", Type: "int32", Number: 2},
			{Name: "page_token", Type: "string", Number: 3},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{
			{Name: "items", Type: "TicketBrief", TypePackage: "ticket", Number: 1, IsRepeated: true, IsMessage: true},
			{Name: "next_page_token", Type: "string", Number: 2},
			{Name: "total_count", Type: "int64", Number: 3},
		}},
		{Name: "TicketBrief", Fields: []model.Field{
			{Name: "summary", Type: "string", Number: 1},
		}},
		{Name: "GetTicketRosterRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"namespace": {BoolValue: true}}},
		}},
		{Name: "GetTicketRosterResponse", Fields: []model.Field{
			{Name: "rows", Type: "TicketRosterRow", TypePackage: "ticket", Number: 1, IsRepeated: true, IsMessage: true},
			{Name: "note", Type: "string", Number: 2},
		}},
		{Name: "TicketRosterRow", Fields: []model.Field{
			{Name: "row_id", Type: "string", Number: 1},
		}},
		{Name: "GetTicketRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1},
		}},
		{Name: "GetTicketResponse", Fields: []model.Field{
			{Name: "body", Type: "string", Number: 1},
		}},
	}}})
}

// javaMcpPageTool turns the fixture's first tool into a PAGE collection read:
// the shape phase 2 makes generable. Resolved is what the emitters read, and it
// is the SAME struct the gRPC PAGE wrapper reads — the bounds below are the
// wrapper's own, not an MCP-specific budget.
func javaMcpPageTool(m *parser.MethodInfo) {
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"ticket_id"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode:            "PAGE",
		OrderBy:         []string{"ticket_id"},
		DefaultPageSize: 50,
		MaxPageSize:     10000,
		PageItemType:    "ticket.TicketBrief",
		PageItemsField:  "items",
		PageHasTotal:    true,
	}
}

func javaMcpPageService(t *testing.T) (*parser.ServiceInfo, []javaMcpTool) {
	t.Helper()
	svc := javaMcpTestService()
	javaMcpPageTool(svc.Methods[0])
	tools, err := javaMcpTools(svc, "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpPageSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	return svc, tools
}

// A PAGE rpc published as a tool drives the SAME three-argument handler the gRPC
// PAGE wrapper drives, with PageParams built from the tool call's own
// page_size/page_token, and assembles the same envelope (items +
// next_page_token + total_count).
func TestJavaMcpDispatchDrivesAPageHandler(t *testing.T) {
	svc, tools := javaMcpPageService(t)

	rendered := generateJavaMcpDispatch(svc, tools, javaMcpTestFileParams())

	for _, want := range []string{
		"CollectionRuntime.Cursor __after = CollectionRuntime.decodePageToken(__req.getPageToken());",
		"long __limit = CollectionRuntime.effectivePageSize(__req.getPageSize(), 50L, 10000L);",
		"handler.getTicketBrief(caller, __req, new CollectionRuntime.PageParams(__limit, __after));",
		"__pb.addAllItems(__page.items);",
		"__pb.setNextPageToken(CollectionRuntime.encodePageToken(__page.nextAfter));",
		"__pb.setTotalCount(__page.total);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the paged MCP dispatch is missing %q:\n%s", want, rendered)
		}
	}

	// The generated gate drives it, against a stub declaring the PAGE handler's
	// own signature, and proves the window reached PageParams.
	gate := generateJavaMcpDispatchTest(svc, tools, javaMcpTestFileParams())
	for _, want := range []string{
		"CollectionRuntime.Page<Ticket.TicketBrief> getTicketBrief(Caller caller, Ticket.GetTicketBriefRequest request, CollectionRuntime.PageParams page)",
		`"page_size", 3`,
		"page_limit=3",
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("the generated gate is missing %q:\n%s", want, gate)
		}
	}

	// The two-argument unary call must NOT be rendered for it.
	if strings.Contains(rendered, "handler.getTicketBrief(caller, __req)") {
		t.Fatalf("a PAGE tool still renders the unary handler call:\n%s", rendered)
	}
}

// The tool schema tells the model how to drive the page: the two fields are
// already there (they are ordinary fields of a PAGE request), and the bound is
// the wrapper's resolved one, stated rather than invented.
func TestJavaMcpPageToolSchemaStatesTheWindow(t *testing.T) {
	_, tools := javaMcpPageService(t)

	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatalf("the emitted tool schema is not JSON: %v\n%s", err, tools[0].Schema)
	}
	props, _ := schema["properties"].(map[string]any)
	ps, _ := props["page_size"].(map[string]any)
	if ps == nil {
		t.Fatalf("page_size is not a tool argument:\n%s", tools[0].Schema)
	}
	if ps["maximum"] != float64(10000) {
		t.Fatalf("page_size does not carry the resolved ceiling: %v", ps["maximum"])
	}
	if desc, _ := ps["description"].(string); !strings.Contains(desc, "50 when omitted") {
		t.Fatalf("page_size does not state the default: %q", desc)
	}
	if pt, _ := props["page_token"].(map[string]any); pt == nil {
		t.Fatalf("page_token is not a tool argument:\n%s", tools[0].Schema)
	}
}
