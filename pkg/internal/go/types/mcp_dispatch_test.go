// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
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
		// A SNAPSHOT collection hands the handler a materialized collection (or a
		// cursor, or a stream), so dispatch would render a call against a
		// signature it does not have. PAGE is the one mode a tool call can
		// drive; every other one is still refused.
		{"method_collection", func(m *parser.MethodInfo) {
			m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT"}
		}, "declares method_collection in mode SNAPSHOT"},
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

// A tool whose request carries an assert_enum_defined field the caller never
// sets: the validator refuses the zero value an omitted enum decodes to, so the
// generated gate must synthesize a defined value or its call never reaches the
// handler. The paired required string stays the synthesized control.
func TestGoMcpDispatchSynthesizesADefinedValueForAnEnumDefinedField(t *testing.T) {
	enumDefined := map[string]model.AnnotationValue{"assert_enum_defined": {BoolValue: true}}
	san := newEgressSanitizer([]model.ProtoFile{{Package: "ticket",
		Enums: []model.Enum{{Name: "TicketMode", Values: []model.EnumValue{
			{Name: "TICKET_MODE_UNSPECIFIED", Number: 0},
			{Name: "TICKET_MODE_READ", Number: 1},
		}}},
		Messages: []model.Message{
			{Name: "GetTicketBriefRequest", Fields: []model.Field{
				{Name: "ticket_id", Type: "string", Number: 1,
					Annotations: map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}},
				{Name: "mode", Type: "TicketMode", Number: 2, IsEnum: true, Annotations: enumDefined},
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
	svc := goMcpTestService()
	tools, err := goMcpTools(svc, "ticket", san)
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	rendered := generateGoMcpDispatchTests(svc, tools, goMcpTestParams())

	if !strings.Contains(rendered, `\"mode\": \"TICKET_MODE_READ\"`) {
		t.Fatalf("the generated gate does not send a defined value for the enum-defined field:\n%s", rendered)
	}
}

// goMcpPageSanitizer is the fixture's message set with the tool's request and
// response reshaped as a PAGE pair — page_size/page_token in, one repeated items
// field plus next_page_token/total_count out. Collection validation requires
// exactly this shape of a PAGE rpc, so the schema the emitter derives is derived
// from it.
func goMcpPageSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1, Comment: "The ticket to summarize."},
			{Name: "page_size", Type: "int32", Number: 2},
			{Name: "page_token", Type: "string", Number: 3},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{
			{Name: "items", Type: "TicketBrief", Number: 1, IsRepeated: true, IsMessage: true},
			{Name: "next_page_token", Type: "string", Number: 2},
			{Name: "total_count", Type: "int64", Number: 3},
		}},
		{Name: "TicketBrief", Fields: []model.Field{
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

// goMcpPageTool turns the fixture's tool into a PAGE collection read: the shape
// phase 2 makes generable. Resolved is what the emitters read, and it is the
// SAME struct the gRPC PAGE wrapper reads — the bounds below are the wrapper's
// own, not an MCP-specific budget.
func goMcpPageTool(m *parser.MethodInfo) {
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

// A PAGE rpc published as a tool drives the SAME four-argument handler the gRPC
// PAGE wrapper drives (ctx, caller, req, PageParams), with PageParams built from
// the tool call's own page_size/page_token, and assembles the same envelope
// (items + next_page_token + total_count).
func TestGoMcpDispatchDrivesAPageHandler(t *testing.T) {
	svc := goMcpTestService()
	goMcpPageTool(svc.Methods[0])

	rendered, err := generateGoMcpDispatch(svc, goMcpTestParams(), goMcpPageSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}

	for _, want := range []string{
		"after, ptErr := collection.DecodePageToken(req.GetPageToken())",
		"limit := collection.EffectivePageSize(int(req.GetPageSize()), 50, 10000)",
		"page, err := s.handler.GetTicketBrief(sanitizeContext(ctx), s.caller, req, collection.PageParams{Limit: limit, After: after})",
		"nextPageToken, encErr := collection.EncodePageToken(page.NextAfter)",
		"Items:           page.Items,",
		"NextPageToken: nextPageToken,",
		"if page.Total >= 0 {",
		// The emitted collection runtime is imported explicitly; goimports
		// cannot resolve that local package by name.
		`"example.com/mod/chassis/collection"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the paged MCP dispatch is missing %q\ngot:\n%s", want, rendered)
		}
	}

	// The three-argument unary call must NOT be rendered for it.
	if strings.Contains(rendered, "s.handler.GetTicketBrief(sanitizeContext(ctx), s.caller, req)\n") {
		t.Fatalf("a PAGE tool still renders the unary handler call:\n%s", rendered)
	}
}

// The tool schema tells the model how to drive the page: the two fields are
// already there (they are ordinary fields of a PAGE request), and the bound is
// the wrapper's resolved one, stated rather than invented.
func TestGoMcpPageToolSchemaStatesTheWindow(t *testing.T) {
	svc := goMcpTestService()
	goMcpPageTool(svc.Methods[0])

	tools, err := goMcpTools(svc, "ticket", goMcpPageSanitizer())
	if err != nil {
		t.Fatalf("resolve Go MCP tools: %v", err)
	}
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

// (spec.owner_default) on the MCP path. An MCP rpc IS verb-classified — nothing
// gates ClassifyMethods on IsMcp — so this seam makes the SAME decision the gRPC
// wrapper makes, from the same helper: CREATE is exempt, a permitted access
// emits nothing, and a contradicted one renders the wrapper's own guard. Before
// this the seam rendered nothing at all, so a contradicted write was served as a
// tool while its gRPC twin was refused.

// goMcpOwnerDefaultSanitizer indexes a Note row whose owner may read but not
// write it, plus the two request shapes the tool reaches it through.
func goMcpOwnerDefaultSanitizer() *egressSanitizer {
	ann := map[string]model.AnnotationValue{
		"owner_default":       {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_read":  {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_write": {Type: model.AnnotationTypeBool, BoolValue: false},
	}
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	storePrincipal := map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newEgressSanitizer([]model.ProtoFile{{Package: "note", Messages: []model.Message{
		{Name: "Note", Annotations: ann, Fields: []model.Field{
			{Name: "note_id", Type: "string", Number: 1},
			{Name: "owner", Type: "string", Number: 2, Annotations: storePrincipal},
		}},
		{Name: "NoteRequest", Fields: []model.Field{
			{Name: "note_id", Type: "string", Number: 1},
			{Name: "owner", Type: "string", Number: 2, Annotations: principal},
		}},
		{Name: "BareNoteRequest", Fields: []model.Field{
			{Name: "note_id", Type: "string", Number: 1},
		}},
		{Name: "NoteResponse", Fields: []model.Field{
			{Name: "note", Type: "Note", Number: 1, IsMessage: true},
		}},
	}}})
}

// goMcpOwnerDefaultService publishes one MCP tool over the Note row, plus the
// carrier the synthesizer would have injected for it.
func goMcpOwnerDefaultService(name string, class *verbs.Classification, input string) *parser.ServiceInfo {
	tool := &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "NoteResponse",
		FullInputType:  "note." + input,
		FullOutputType: "note.NoteResponse",
		IsMcp:          true,
		McpToolName:    "the_tool",
		Comment:        name + " touches a row whose owner rights are declared.",
		Class:          class,
	}
	carrier := &parser.MethodInfo{
		Name:            parser.McpCarrierRpcName("Notes"),
		InputType:       "McpRequest",
		OutputType:      "McpResponse",
		FullInputType:   "mcp.McpRequest",
		FullOutputType:  "mcp.McpResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}
	return &parser.ServiceInfo{
		Name:       "Notes",
		HasMcpRpcs: true,
		Methods:    []*parser.MethodInfo{tool, carrier},
	}
}

func goMcpOwnerDefaultRender(svc *parser.ServiceInfo) (string, error) {
	p := goMcpTestParams()
	p.PackageName = "note"
	p.ServicePackage = "note"
	p.TypesModulePath = "example.com/mod/types/svc/note"
	return generateGoMcpDispatch(svc, p, goMcpOwnerDefaultSanitizer())
}

// A CREATE is exempt by construction — a row that does not exist yet has no
// owner for the option to speak about — and so is a READ the owner holds.
func TestGoMcpDispatchGeneratesAnExemptOwnerDefaultTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class *verbs.Classification
	}{
		{"CreateNote", &verbs.Classification{Verb: "Create", Pattern: verbs.PatternCreate, Access: verbs.AccessWrite}},
		{"GetNote", &verbs.Classification{Verb: "Get", Pattern: verbs.PatternGet, Access: verbs.AccessRead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := goMcpOwnerDefaultRender(goMcpOwnerDefaultService(tc.name, tc.class, "NoteRequest"))
			if err != nil {
				t.Fatalf("an MCP tool exempt from (spec.owner_default) was refused generation: %v", err)
			}
			if !strings.Contains(rendered, "func (s *NotesMcpSurface) call"+tc.name+"(") {
				t.Fatalf("no dispatch was emitted for the tool:\n%s", rendered)
			}
			if strings.Contains(rendered, "spec.owner_default") {
				t.Fatalf("%s emitted an owner_default refusal it is exempt from:\n%s", tc.name, rendered)
			}
		})
	}
}

// An UPDATE of a {write:false} type is what the option governs, and the MCP
// dispatch renders the wrapper's own guard for it rather than serving the tool
// unguarded: scoped to the owner, at the request-validation seam.
func TestGoMcpDispatchGuardsAContradictedOwnerDefaultWrite(t *testing.T) {
	rendered, err := goMcpOwnerDefaultRender(goMcpOwnerDefaultService("UpdateNote",
		&verbs.Classification{Verb: "Update", Pattern: verbs.PatternUpdate, Access: verbs.AccessWrite}, "NoteRequest"))
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	for _, want := range []string{
		`s.caller.Token().GetServiceEntitlements().GetPrincipalScope()`,
		`if __scopeIsSelf && req.Owner == __odCaller {`,
		`notesMcpToolError(status.Error(codes.PermissionDenied, "UpdateNote: (spec.owner_default) on Note (write) denies the owner this operation")), nil`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("MCP dispatch missing the owner_default guard %q:\n%s", want, rendered)
		}
	}
	// The guard belongs after the (spec.principal) stamp — that is the only point
	// at which both halves of the comparison exist — and before the handler call.
	stamp := strings.Index(rendered, "req.Owner = __principal")
	guard := strings.Index(rendered, "if __scopeIsSelf && req.Owner == __odCaller {")
	call := strings.Index(rendered, "s.handler.UpdateNote(sanitizeContext(ctx), s.caller, req)")
	if stamp < 0 || call < 0 || stamp > guard || guard > call {
		t.Fatalf("the guard is not at the request-validation seam (stamp=%d guard=%d call=%d):\n%s", stamp, guard, call, rendered)
	}
}

// The refusals that remain are the ones the guard genuinely cannot be rendered
// for: a verb that did not classify at all, and a write whose request names no
// principal to compare the caller against.
func TestGoMcpDispatchRefusesAnUnenforceableOwnerDefault(t *testing.T) {
	update := &verbs.Classification{Verb: "Update", Pattern: verbs.PatternUpdate, Access: verbs.AccessWrite}
	for _, tc := range []struct {
		name  string
		class *verbs.Classification
		input string
		match string
	}{
		{"unclassified", nil, "NoteRequest", "did not classify"},
		{"no request principal", update, "BareNoteRequest", "(spec.principal)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := goMcpOwnerDefaultRender(goMcpOwnerDefaultService("UpdateNote", tc.class, tc.input)); err == nil {
				t.Fatal("an MCP tool whose (spec.owner_default) cannot be enforced generated dispatch anyway")
			} else if !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}
