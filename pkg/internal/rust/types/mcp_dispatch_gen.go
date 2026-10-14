// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// The MCP dispatch seam (BIG GOAL 8 Phase 0, Rust).
//
// A service with at least one (spec.rpc) principal: RPC_PRINCIPAL_MCP rpc gets
// a generated Broker<Service>McpSession carrier (pkg/internal/proto/mcp_carrier.go)
// and, from here, everything that runs ON TOP of that stream:
//
//   - the tool set, derived from the same rpc declarations registry.json's tool
//     entries come from — name, description, and a JSON Schema over the request
//     message (decision doc §5: a typed contract, never a second hand-authored
//     description);
//   - an rmcp ServerHandler that answers tools/list and tools/call;
//   - per-tool dispatch that decodes the arguments into the rpc's own typed
//     request, stamps (spec.principal) from the verified Caller, calls the
//     service's typed handler method, and filters (spec.store_principal) rows
//     out of the response — the SAME renderers the ordinary gRPC wrapper uses
//     (principal_gen.go), not a second implementation of the same rule;
//   - the carriage itself: an rmcp worker transport over the gRPC bidi stream.
//
// This is the generalized form of what platform/services/argus hand-wrote in
// mcp.rs / mcp_transport.rs. Nothing here is emitted for a service with no MCP
// rpc.

// rmcpCrateVersion / schemarsCrateVersion pin the MCP SDK the generated
// dispatch is written against.
//
// Pinned here rather than read from the consumer's dependencies.yaml for the
// same reason hmac/sha2/hex are pinned in types_cargo.toml.tmpl: these back a
// generated seam whose code shape is tied to a specific API, so a consumer
// moving the version independently would break generated code rather than
// their own. 3.4.1 is the floor: the auto-exposed ServerHandler below uses
// rmcp::model::ServerConfig (the alias InitializeResult gained in 3.4.1) and
// InitializeResult::default(), neither of which exists in 3.3.0 or earlier —
// a consumer lock resolving 3.2/3.3 failed to compile the generated model
// (scotty/backend, 2026-09-24). Moved from 3.1.4 (the version once proven in
// platform/services/argus) to match what the emitted code actually needs.
const (
	rmcpCrateVersion     = "3.4.1"
	schemarsCrateVersion = "1"
)

// rustMcpTool is one RPC_PRINCIPAL_MCP rpc, resolved for emission.
type rustMcpTool struct {
	Method      *parser.MethodInfo
	ToolName    string // the published tool name (registry.json's mcp_tool_name)
	Description string // the rpc's own leading doc comment
	Schema      string // the tool's input JSON Schema, marshalled
	InputType   string // rendered Rust request type
	OutputType  string // rendered Rust response type
	// EmptyRequest is a tool taking no arguments: its rpc's request is
	// google.protobuf.Empty, which the handler interface renders with no `req`
	// parameter at all.
	EmptyRequest bool
	// RequiredScalars are the request's required scalar fields, with a synthetic
	// value each, used to drive the generated dispatch test.
	RequiredScalars []rustMcpScalarArg

	// PrincipalField is the request's (spec.principal) field, "" when it
	// declares none. It is absent from Schema and refused as an argument.
	PrincipalField string
	// StorePrincipalRows are the response's repeated (spec.store_principal)
	// record fields, filtered to the caller before the result is returned.
	StorePrincipalRows []rustPrincipalListField
	// NamespaceField / StoreNamespaceRows are the same two seams for the tenant
	// boundary: (spec.namespace) on the request, (spec.store_namespace) on the
	// rows the response carries.
	NamespaceField     string
	StoreNamespaceRows []rustNamespaceListField

	// OwnerDefaultGuard is the (spec.owner_default) refusal rendered by the same
	// helper the gRPC wrapper calls, "" when nothing is declared or nothing is
	// contradicted. It rides the request-validation seam, immediately after the
	// (spec.principal) stamp, for the reason stated there: that is the only
	// point at which both halves of the comparison exist.
	OwnerDefaultGuard string

	// Page is the PAGE-collection window, nil for every other rpc. A PAGE tool
	// takes page_size/page_token as ordinary arguments and calls the same
	// three-argument handler the gRPC PAGE wrapper calls.
	Page *rustMcpPage
}

// rustMcpPage is what the dispatch needs to drive a PAGE rpc's handler and
// assemble its response envelope. Every value here is read off the same
// collection::Resolved the gRPC PAGE wrapper reads — there is no MCP-specific
// page bound.
type rustMcpPage struct {
	ItemsField  string // prost field name of the response's items field
	ItemType    string // rendered Rust item type (the Page<T> element)
	DefaultSize uint32 // page_size when the call omits it
	MaxSize     uint32 // the trust-tier ceiling page_size is clamped to
	HasTotal    bool   // the response declares total_count
}

// rustMcpPageOf resolves the PAGE window of a collection rpc, or nil when the
// rpc is not a PAGE read.
func rustMcpPageOf(m *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) *rustMcpPage {
	if !m.IsPageCollection() {
		return nil
	}
	return &rustMcpPage{
		ItemsField:  prostSnakeName(m.Resolved.PageItemsField),
		ItemType:    rustPageItemType(m, servicePkg, crossPkgTypes),
		DefaultSize: m.Resolved.DefaultPageSize,
		MaxSize:     m.Resolved.MaxPageSize,
		HasTotal:    m.Resolved.PageHasTotal,
	}
}

// rustMcpCarrierMethod returns the service's generated session-carrier rpc, or
// nil when the service declares no MCP tool. The carrier is identified the same
// way the registry identifies it (registry.go's McpSessionCarrier).
func rustMcpCarrierMethod(svc *parser.ServiceInfo) *parser.MethodInfo {
	if !svc.HasMcpRpcs {
		return nil
	}
	name := parser.McpCarrierRpcName(svc.Name)
	for _, m := range svc.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// rustMcpTools resolves every MCP tool the service publishes, in declaration
// order (the order the tool list is served in).
func rustMcpTools(svc *parser.ServiceInfo, servicePkg string, crossPkgTypes map[string]string, san *rustEgressSanitizer) ([]rustMcpTool, error) {
	var tools []rustMcpTool
	for _, m := range svc.Methods {
		if !m.IsMcpTool() {
			continue
		}
		if err := rustMcpToolGeneratable(svc, m, servicePkg, san); err != nil {
			return nil, err
		}
		schema, err := rustMcpInputSchema(m, servicePkg, san)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		inputType, outputType := resolveRustTypeQualified(m, servicePkg, crossPkgTypes)
		empty := rustMcpEmptyRequest(m)
		var scalars []rustMcpScalarArg
		if !empty {
			scalars = san.mcpRequiredScalarArgs(m.FullInputType, servicePkg)
		}
		ownerDefaultGuard, err := rustOwnerDefaultGuard(m, san, servicePkg)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		tools = append(tools, rustMcpTool{
			EmptyRequest:       empty,
			RequiredScalars:    scalars,
			Method:             m,
			ToolName:           m.McpToolName,
			Description:        parser.McpToolDescription(m.Comment),
			Schema:             schema,
			InputType:          inputType,
			OutputType:         outputType,
			PrincipalField:     san.requestPrincipalField(m.FullInputType, servicePkg),
			StorePrincipalRows: san.principalListFields(m.FullOutputType, servicePkg),
			NamespaceField:     san.requestNamespaceField(m.FullInputType, servicePkg),
			StoreNamespaceRows: san.namespaceListFields(m.FullOutputType, servicePkg),
			OwnerDefaultGuard:  ownerDefaultGuard,
			Page:               rustMcpPageOf(m, servicePkg, crossPkgTypes),
		})
	}
	return tools, nil
}

// rustMcpToolGeneratable refuses to emit dispatch for a tool whose guards this
// seam cannot render.
//
// Before this seam existed an MCP tool was suppressed from the Rust surface
// entirely (fa915df7), so the service wrapper's own refusals were skipped for
// it — nothing served the rpc, so there was nothing to under-enforce. Dispatch
// changes that: the tool IS served now, by this code, so the same refusals the
// wrapper makes for a gRPC rpc apply here, for the same reasons.
func rustMcpToolGeneratable(svc *parser.ServiceInfo, m *parser.MethodInfo, servicePkg string, san *rustEgressSanitizer) error {
	// An MCP tool is one question and one answer. A collection-returning rpc has
	// a handler signature this dispatch cannot call (it takes a cursor or a page
	// window), and MCP has no frame to carry it in.
	if m.Collection != nil && !m.IsPageCollection() {
		return fmt.Errorf("%s.%s is an RPC_PRINCIPAL_MCP rpc but declares method_collection in mode %s; only PAGE has a shape a tool call can drive (page_size/page_token in, next_page_token out) — the other modes hand the handler a stream or a materialized collection, which an MCP tool call has no frame for",
			svc.Name, m.Name, m.Collection.Mode)
	}
	// (spec.record).min_group_size: this seam renders no size-only guard, so a
	// tool carrying one would be dispatched unguarded even though the gRPC
	// wrapper next door guards it (mingroupsize_gen.go). Java's MCP dispatch
	// refuses the same shape for the same reason.
	if m.MinGroupSize > 0 {
		return fmt.Errorf("%s.%s is an MCP tool carrying (spec.min_group_size) = %d, but the generated MCP dispatch renders no size-only guard; publish it as a gRPC rpc or drop the threshold",
			svc.Name, m.Name, m.MinGroupSize)
	}
	// (spec.record).mutability = MUTABILITY_APPEND_ONLY: no language renders a
	// guard for a row mutation over a write-once record.
	if t := rustAppendOnlyRecordOnMethod(m, san, servicePkg); t != "" {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool: it carries %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; an append-only record's rows are written once",
			svc.Name, m.Name, t)
	}
	// (spec.store_epoch): the stamp is rendered by the gRPC wrapper's write
	// path, not by this seam, so a tool over an epoch-bearing write would leave
	// the store epoch unstamped. Java's MCP dispatch refuses the same shape for
	// the same reason.
	if f := san.storeEpochField(m.FullInputType, servicePkg); f != "" {
		return fmt.Errorf("%s.%s is an MCP tool taking %s, whose field %q carries (spec.store_epoch), but the stamp is rendered by the gRPC wrapper's write path and not by MCP dispatch; a tool call of this write would leave the store epoch unstamped",
			svc.Name, m.Name, m.FullInputType, f)
	}
	// (spec.owner_default): the same decision the gRPC wrapper makes, from the
	// same helper. An MCP rpc IS verb-classified — ClassifyMethods runs over
	// every method of every service with no IsMcp gate, and the classification
	// is name/shape-driven, so a tool has the read/write access the owner rights
	// are compared against exactly as its gRPC twin does. (What registry.json
	// omits for a tool entry is the rpc_verb/category FIELDS — an MCP entry is
	// not a gRPC call — not MethodInfo.Class.) rustMcpToolDispatch renders the
	// guard next to the (spec.principal) stamp, so what is refused here is only
	// what that guard cannot be rendered against: a request shape with no
	// validation seam at which the caller can be compared to the row's
	// principal, which is the same set servicewrapper_gen.go refuses.
	guard, err := rustOwnerDefaultGuard(m, san, servicePkg)
	if err != nil {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool: %w", svc.Name, m.Name, err)
	}
	if guard != "" && (rustMcpEmptyRequest(m) || m.StreamingType() != "unary" || m.IsPageCollection()) {
		return fmt.Errorf("%s.%s is an MCP tool touching a type whose (spec.owner_default) contradicts its access, but its %s request shape has no validation seam at which the caller can be compared to the row's principal",
			svc.Name, m.Name, m.StreamingType())
	}
	return nil
}

// rustMcpInputSchema builds the tool's argument schema from the rpc's request
// message.
//
// Three kinds of field are absent from it, and absence is the point: a field
// that is not in the schema is not a shape of tool call the model can make.
//   - (spec.principal) / (spec.store_principal) and (spec.namespace) /
//     (spec.store_namespace): the server computes these from the verified
//     Caller. Dispatch refuses a call that names one anyway, rather than
//     silently overwriting it.
//   - the injected trace_id/noop pair: tacplus's own transport fields, not part
//     of the rpc's user-facing contract.
func rustMcpInputSchema(m *parser.MethodInfo, servicePkg string, san *rustEgressSanitizer) (string, error) {
	var schema map[string]any
	if rustMcpEmptyRequest(m) {
		// A tool taking no arguments. Its rpc's request type carries no fields
		// worth publishing, and the handler interface renders it with no `req`
		// parameter, so there is no message to walk.
		schema = map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}
	} else {
		var err error
		schema, err = san.mcpMessageSchema(m.FullInputType, servicePkg, map[string]bool{})
		if err != nil {
			return "", err
		}
	}
	if m.IsPageCollection() {
		rustMcpAnnotatePageSchema(schema, m.Resolved.DefaultPageSize, m.Resolved.MaxPageSize)
	}
	schema["description"] = parser.McpToolDescription(m.Comment)
	out, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("marshal input schema: %w", err)
	}
	return string(out), nil
}

// rustMcpAnnotatePageSchema tells the model how to drive the two paging fields a
// PAGE request already declares. They are ordinary fields of the request
// message, so they are already in the schema; what the message index cannot say
// is the window's default and its ceiling, which are the wrapper's own resolved
// bounds. The bound is stated, not invented: it is the same clamp the dispatch
// applies at runtime, so a model that asks for more is told what it will get
// rather than silently given less.
func rustMcpAnnotatePageSchema(schema map[string]any, defaultSize, maxSize uint32) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	if ps, ok := props["page_size"].(map[string]any); ok {
		ps["description"] = fmt.Sprintf("How many items to return in this page. Optional: %d when omitted, and never more than %d.",
			defaultSize, maxSize)
		ps["maximum"] = maxSize
	}
	if pt, ok := props["page_token"].(map[string]any); ok {
		pt["description"] = "The next_page_token returned by the previous call to this tool. Optional: omit it for the first page. An empty next_page_token in the result means there is no next page."
	}
}

// rustMcpEmptyRequest mirrors the handlerEmpty test the handler-interface
// renderer uses, so dispatch calls the handler with the parameter list it
// actually declares.
func rustMcpEmptyRequest(m *parser.MethodInfo) bool {
	return m.FullInputType == "google.protobuf.Empty" || m.FullInputType == "trace.TraceRequest"
}

// rustMcpScalarArg is one required scalar field of a tool's request, with a
// synthetic value the generated dispatch test passes for it.
type rustMcpScalarArg struct {
	Field string
	// JSON is the value as it appears in the tool call's arguments object.
	JSON string
	// Debug is how the value appears in the request's Debug rendering, which is
	// what the generated test asserts the handler received.
	Debug string
}

// mcpRequiredScalarArgs returns the request's required scalar fields. A request
// whose required set is not entirely scalar returns nil: the generated dispatch
// test synthesizes arguments, and there is no one obviously-valid value for a
// required enum or nested message.
func (es *rustEgressSanitizer) mcpRequiredScalarArgs(fullType, servicePkg string) []rustMcpScalarArg {
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []rustMcpScalarArg
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if a, ok := f.Annotations["assert_required"]; !ok || !a.BoolValue {
			continue
		}
		if f.IsRepeated || f.IsMap || f.IsMessage || f.IsEnum {
			return nil
		}
		switch mcpScalarJSONType(f.Type) {
		case "string":
			v := "mcp-test-" + f.Name
			out = append(out, rustMcpScalarArg{Field: f.Name, JSON: rustQuote(v), Debug: rustQuote(v)})
		case "integer":
			out = append(out, rustMcpScalarArg{Field: f.Name, JSON: "1", Debug: "1"})
		case "number":
			out = append(out, rustMcpScalarArg{Field: f.Name, JSON: "1.0", Debug: "1.0"})
		case "boolean":
			out = append(out, rustMcpScalarArg{Field: f.Name, JSON: "true", Debug: "true"})
		default:
			return nil
		}
	}
	return out
}

// mcpMessageSchema renders one message as a JSON Schema object. visiting breaks
// recursion on a message that (directly or otherwise) contains itself; such a
// field is rendered as an unconstrained object rather than expanded forever.
func (es *rustEgressSanitizer) mcpMessageSchema(fullType, servicePkg string, visiting map[string]bool) (map[string]any, error) {
	if es == nil {
		return nil, fmt.Errorf("no message index available to derive an MCP tool schema")
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil, fmt.Errorf("request type %s is not in this build; an MCP tool's schema is derived from its request message", fullType)
	}
	visiting[pkg+"\x00"+name] = true
	defer delete(visiting, pkg+"\x00"+name)

	properties := map[string]any{}
	var required []string
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if proto.IsTraceField(f.Name, f.Number) {
			continue
		}
		if _, ok := f.Annotations["principal"]; ok {
			continue
		}
		if _, ok := f.Annotations["store_principal"]; ok {
			continue
		}
		if _, ok := f.Annotations["namespace"]; ok {
			continue
		}
		if _, ok := f.Annotations["store_namespace"]; ok {
			continue
		}
		properties[f.Name] = es.mcpFieldSchema(f, pkg, servicePkg, visiting)
		if a, ok := f.Annotations["assert_required"]; ok && a.BoolValue {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		// json.Marshal sorts map keys but not slices; sort so the emitted
		// schema is byte-stable across runs.
		sort.Strings(required)
		schema["required"] = required
	}
	return schema, nil
}

func (es *rustEgressSanitizer) mcpFieldSchema(f *model.Field, msgPkg, servicePkg string, visiting map[string]bool) map[string]any {
	if f.IsMap {
		return es.described(f, map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": mcpScalarJSONType(f.MapValueType)},
		})
	}
	item := es.mcpSingularSchema(f, msgPkg, servicePkg, visiting)
	if f.IsRepeated {
		return es.described(f, map[string]any{"type": "array", "items": item})
	}
	return es.described(f, item)
}

func (es *rustEgressSanitizer) mcpSingularSchema(f *model.Field, msgPkg, servicePkg string, visiting map[string]bool) map[string]any {
	switch {
	case f.IsEnum:
		// The value on the wire is the enum value's own name; the set of names
		// is not carried in the message index, so the schema constrains the
		// type without enumerating them. See Q4 of the questions file.
		return map[string]any{"type": "string"}
	case f.IsMessage:
		pkg := f.TypePackage
		if pkg == "" {
			pkg = msgPkg
		}
		if visiting[pkg+"\x00"+f.Type] {
			return map[string]any{"type": "object"}
		}
		nested, err := es.mcpMessageSchema(pkg+"."+f.Type, servicePkg, visiting)
		if err != nil {
			return map[string]any{"type": "object"}
		}
		return nested
	default:
		return map[string]any{"type": mcpScalarJSONType(f.Type)}
	}
}

// described attaches the field's own doc comment, which is the text the model
// reads when deciding what to pass.
func (es *rustEgressSanitizer) described(f *model.Field, schema map[string]any) map[string]any {
	if c := strings.Join(strings.Fields(f.Comment), " "); c != "" {
		schema["description"] = c
	}
	return schema
}

// mcpScalarJSONType maps a proto scalar to its JSON Schema type, following the
// canonical proto3 JSON mapping: 64-bit integers and bytes travel as strings.
func mcpScalarJSONType(protoType string) string {
	switch protoType {
	case "bool":
		return "boolean"
	case "double", "float":
		return "number"
	case "int32", "sint32", "sfixed32", "uint32", "fixed32":
		return "integer"
	default:
		// string, bytes, int64/uint64 and friends.
		return "string"
	}
}

// ── emission ────────────────────────────────────────────────────────────────

// generateRustMcpDispatch renders the whole MCP termination for one service:
// the tool table, the session surface, and the transport. Returns "" for a
// service with no MCP rpc.
func generateRustMcpDispatch(svc *parser.ServiceInfo, servicePkg string, crossPkgTypes map[string]string, san *rustEgressSanitizer) (string, error) {
	carrier := rustMcpCarrierMethod(svc)
	if carrier == nil {
		return "", nil
	}
	tools, err := rustMcpTools(svc, servicePkg, crossPkgTypes, san)
	if err != nil {
		return "", err
	}
	if len(tools) == 0 {
		// HasMcpRpcs is what produced the carrier, so this cannot happen; if it
		// ever does, a carrier with no tool set is a generation bug, not a
		// service that serves nothing.
		return "", fmt.Errorf("%s has a generated MCP session carrier but no RPC_PRINCIPAL_MCP rpc", svc.Name)
	}
	frameIn, frameOut := resolveRustTypeQualified(carrier, servicePkg, crossPkgTypes)

	var b strings.Builder
	b.WriteString(rustMcpHeader(svc.Name))
	b.WriteString(rustMcpToolTable(svc.Name, tools))
	b.WriteString(rustMcpSurface(svc.Name, tools))
	b.WriteString(rustMcpTransport(svc.Name, frameIn, frameOut, proto.McpResponseFrameMaxBytes))
	b.WriteString(rustMcpTests(svc.Name, svc, tools, servicePkg, crossPkgTypes, carrier))
	return b.String(), nil
}

func rustMcpHeader(serviceName string) string {
	return fmt.Sprintf(`
// ── MCP tool surface ────────────────────────────────────────────────────────
//
// %[1]s declares at least one (spec.rpc) principal: RPC_PRINCIPAL_MCP rpc, so
// tacplus generates its whole Model Context Protocol termination here: the tool
// set, the JSON-RPC dispatch, and the carriage over %[2]s. The
// service implements only the typed handler methods on %[1]sHandler — there is
// no MCP glue for it to hand-write.
//
// Every tool call is authorized by the carrier RPC's own gRPC authorization,
// once, at session open: an MCP session reaches exactly the tools the Caller
// the gateway verified is entitled to reach.
`, serviceName, parser.McpCarrierRpcName(serviceName))
}

// rustMcpToolTable renders the published tool set as a static table.
func rustMcpToolTable(serviceName string, tools []rustMcpTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
/// One MCP tool %[1]s publishes.
pub struct %[1]sMcpTool {
    /// The tool name on the wire.
    pub name: &'static str,
    /// The rpc's own doc comment — the text the model reads to decide whether
    /// to call it.
    pub description: &'static str,
    /// The tool's argument JSON Schema, derived from the rpc's request message.
    ///
    /// Fields the server computes are ABSENT from it rather than merely
    /// ignored: the (spec.principal) / (spec.store_principal) markers and
    /// tacplus's injected trace fields. `+"`additionalProperties: false`"+` and the
    /// dispatch check below make naming one anyway an error, not a silent
    /// override.
    pub input_schema: &'static str,
}

/// The tools %[1]s publishes, in declaration order.
pub static %[2]s: &[%[1]sMcpTool] = &[
`, serviceName, rustMcpToolTableName(serviceName))
	for _, t := range tools {
		fmt.Fprintf(&b, `    %sMcpTool {
        name: %s,
        description: %s,
        input_schema: %s,
    },
`, serviceName, rustQuote(t.ToolName), rustQuote(t.Description), rustQuote(t.Schema))
	}
	b.WriteString("];\n")
	return b.String()
}

// rustMcpToolTableName is derived with prostSnakeName, the same snake-casing the
// generated method and module names use, so the constants read alongside them
// (C2S_ONLY_API_MCP_TOOLS next to serve_c2s_only_api_mcp_session).
func rustMcpToolTableName(serviceName string) string {
	return strings.ToUpper(prostSnakeName(serviceName)) + "_MCP_TOOLS"
}

// rustMcpSurface renders the per-session tool surface: the rmcp ServerHandler
// and one typed dispatch method per tool.
func rustMcpSurface(serviceName string, tools []rustMcpTool) string {
	surface := serviceName + "McpSurface"
	var b strings.Builder

	fmt.Fprintf(&b, `
/// One MCP session's tool surface, bound to one verified caller.
///
/// Built per session rather than shared: every server-computed predicate below
/// comes from `+"`caller`"+`, which is a property of the session and not of the
/// service.
pub struct %[1]s {
    handler: Arc<dyn %[2]sHandler>,
    caller: servicemanager::auth::Caller,
}

impl %[1]s {
    /// Build the surface for a caller the carrier RPC's authorization has
    /// already verified.
    pub fn new(handler: Arc<dyn %[2]sHandler>, caller: servicemanager::auth::Caller) -> Self {
        Self { handler, caller }
    }

    /// The published tool set, as rmcp serves it on tools/list.
    pub fn tools() -> Vec<rmcp::model::Tool> {
        %[3]s
            .iter()
            .map(|t| {
                // The schema is generated, so a parse failure here is a
                // generator bug rather than a runtime condition.
                let schema: rmcp::model::JsonObject = serde_json::from_str(t.input_schema)
                    .expect("generated MCP tool schema is not a JSON object");
                rmcp::model::Tool::new(t.name, t.description, Arc::new(schema))
            })
            .collect()
    }
`, surface, serviceName, rustMcpToolTableName(serviceName))

	for _, t := range tools {
		b.WriteString(rustMcpToolDispatch(serviceName, t))
	}

	// The tools/call router.
	var arms strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&arms, `            %s => self.%s(arguments).await,
`, rustQuote(t.ToolName), rustMcpDispatchFnName(t))
	}

	fmt.Fprintf(&b, `
    /// Dispatch a tool call by name without an MCP session.
    ///
    /// An in-process caller (a service acting as its own MCP client, e.g. an
    /// agentic loop dispatching its own tool calls) has no
    /// `+"`rmcp::service::RequestContext<RoleServer>`"+` and, per design, must not
    /// create one just to reach %[1]s's tool logic — `+"`rmcp::Peer::<R>::new`"+` is
    /// crate-private in rmcp, so a live session cannot be fabricated. This is
    /// the session-free entry point: `+"`call_tool`"+` below is a thin wrapper
    /// around it for real MCP sessions.
    pub async fn call(
        &self,
        name: &str,
        arguments: rmcp::model::JsonObject,
    ) -> Result<serde_json::Value, tonic::Status> {
        match name {
%[2]s            other => Err(tonic::Status::not_found(format!("unknown tool {other:?}"))),
        }
    }
}

impl rmcp::ServerHandler for %[1]s {
    fn get_info(&self) -> rmcp::model::ServerConfig {
        // Built from Default rather than a struct literal:
        // rmcp::model::ServerConfig is #[non_exhaustive], so a literal does not
        // compile outside rmcp. (ServerInfo is the deprecated alias of it.)
        let mut info = rmcp::model::ServerConfig::default();
        info.capabilities = rmcp::model::ServerCapabilities::builder().enable_tools().build();
        info.instructions = Some(%[3]s.to_string());
        info
    }

    async fn list_tools(
        &self,
        _request: Option<rmcp::model::PaginatedRequestParams>,
        _context: rmcp::service::RequestContext<rmcp::RoleServer>,
    ) -> Result<rmcp::model::ListToolsResult, rmcp::ErrorData> {
        Ok(rmcp::model::ListToolsResult::with_all_items(Self::tools()))
    }

    async fn call_tool(
        &self,
        request: rmcp::model::CallToolRequestParams,
        _context: rmcp::service::RequestContext<rmcp::RoleServer>,
    ) -> Result<rmcp::model::CallToolResponse, rmcp::ErrorData> {
        let arguments = request.arguments.unwrap_or_default();
        match self.call(request.name.as_ref(), arguments).await {
            Ok(value) => Ok(rmcp::model::CallToolResult::structured(value).into()),
            // A refusal the caller must be able to read — a missing argument,
            // a caller-scoped field they tried to set — is a tool result, not a
            // protocol error: MCP clients render protocol errors opaquely.
            Err(status) => Ok(rmcp::model::CallToolResult::error(vec![
                rmcp::model::ContentBlock::text(status.message().to_string()),
            ])
            .into()),
        }
    }
}
`, surface, arms.String(), rustQuote(rustMcpInstructions))

	return b.String()
}

// rustMcpInstructions is the server-level instruction block every generated MCP
// surface serves. It states the one rule a model cannot discover from the tool
// schemas alone: the fields the schemas omit are omitted because the server
// computes them, and naming one is an error rather than a way to widen reach.
const rustMcpInstructions = "Every tool here runs as you: the server sets any caller-scoped field " +
	"from your verified identity, and those fields are not tool arguments. Naming one is an " +
	"error, not a way to widen what you can reach."

func rustMcpDispatchFnName(t rustMcpTool) string {
	return "call_" + t.ToolName
}

// rustMcpToolDispatch renders one tool's dispatch: decode, stamp, call the
// service's own typed handler method, filter, encode.
//
// Every guard here is rendered by the SAME helper the ordinary gRPC wrapper
// calls for a USER/SERVICE rpc — request validation, the (spec.principal) and
// (spec.namespace) stamps, the (spec.store_principal) and (spec.store_namespace)
// egress filters, response validation. An MCP tool call is authorized and
// scoped exactly like a gRPC call against the same Caller, which is the whole
// point of reusing the field markers rather than inventing an MCP-specific
// owner predicate (decision doc section 8).
func rustMcpToolDispatch(serviceName string, t rustMcpTool) string {
	methodSnake := prostSnakeName(t.Method.Name)

	// The caller local the shared renderers read. They take Option<&Caller>;
	// the session refuses to open without one, so this is always Some.
	var b strings.Builder

	fmt.Fprintf(&b, `
    /// Dispatch the `+"`%s`"+` tool to %s.%s.
    async fn %s(
        &self,
        arguments: rmcp::model::JsonObject,
    ) -> Result<serde_json::Value, tonic::Status> {
`, t.ToolName, serviceName, t.Method.Name, rustMcpDispatchFnName(t))

	// A server-computed field is absent from the schema; naming it anyway is
	// refused rather than silently overwritten. The model must not be able to
	// aim this rpc at another principal or tenant, however its arguments were
	// shaped.
	for _, f := range []struct{ marker, field, why string }{
		{"spec.principal", t.PrincipalField, "your verified identity"},
		{"spec.namespace", t.NamespaceField, "your verified namespace claim"},
	} {
		if f.field == "" {
			continue
		}
		fmt.Fprintf(&b, `        // (%[1]s) %[2]s is computed from the verified caller, so it is
        // absent from this tool's schema; a call that names it is refused.
        if arguments.contains_key(%[3]s) {
            return Err(tonic::Status::invalid_argument(
                "%[2]s is set by the server from %[4]s; it is not a tool argument",
            ));
        }
`, f.marker, f.field, rustQuote(f.field), f.why)
	}

	b.WriteString("        let caller: Option<&servicemanager::auth::Caller> = Some(&self.caller);\n")
	if t.EmptyRequest {
		// A tool that takes no arguments; its handler method declares no `req`.
		fmt.Fprintf(&b, `        if !arguments.is_empty() {
            return Err(tonic::Status::invalid_argument("this tool takes no arguments"));
        }
        let resp = self.handler.%s(caller).await?;
`, methodSnake)
	} else {
		fmt.Fprintf(&b, `        let req: %s = serde_json::from_value(serde_json::Value::Object(arguments))
            .map_err(|e| tonic::Status::invalid_argument(format!("invalid arguments: {}", e)))?;
`, t.InputType)
		b.WriteString(rustServerRequestValidationBlock("req", t.Method.FullInputType))
		if t.PrincipalField != "" {
			b.WriteString(rustRequestPrincipalBlock(t.PrincipalField, t.Method.Name, "req", "        "))
		}
		if t.NamespaceField != "" {
			b.WriteString(rustRequestNamespaceBlock(t.NamespaceField, t.Method.Name, "req", "        "))
		}
		b.WriteString(t.OwnerDefaultGuard)
		if t.Page != nil {
			b.WriteString(rustMcpPageDispatch(t, methodSnake))
		} else {
			fmt.Fprintf(&b, "        let resp = self.handler.%s(caller, req).await?;\n", methodSnake)
		}
	}
	b.WriteString(rustServerResponseValidationBlock("resp", t.Method.FullOutputType))
	if len(t.StorePrincipalRows) > 0 {
		b.WriteString(rustResponsePrincipalFilterBlock(t.StorePrincipalRows, t.Method.Name))
	}
	if len(t.StoreNamespaceRows) > 0 {
		// Never gated: the synthesized RecordMap plane emits only
		// RPC_PRINCIPAL_SERVICE rpcs, never RPC_PRINCIPAL_MCP, so an MCP tool
		// can never be the record plane.
		b.WriteString(rustResponseNamespaceFilterBlock(t.StoreNamespaceRows, t.Method.Name, false))
	}
	b.WriteString(`        serde_json::to_value(&resp)
            .map_err(|e| tonic::Status::internal(format!("could not encode the result: {}", e)))
    }
`)
	return b.String()
}

// rustMcpPageDispatch renders the PAGE window: decode the tool call's
// page_token, clamp its page_size, call the keyset handler, assemble the
// response envelope.
//
// Every line of it is the gRPC PAGE wrapper's own (servicewrapper_gen.go's PAGE
// branch), against the same collection runtime and the same resolved bounds —
// the tool and its gRPC twin are one handler driven two ways, not two paging
// implementations.
func rustMcpPageDispatch(t rustMcpTool, methodSnake string) string {
	total := ""
	if t.Page.HasTotal {
		total = `
        // total_count is reported only when the handler computes it (>= 0); a
        // negative total leaves the field at 0 = "unknown", not "empty".
        if page.total >= 0 {
            resp.total_count = page.total;
        }`
	}
	return fmt.Sprintf(`        // Decode the opaque page_token into a keyset cursor (bad token -> INVALID_ARGUMENT).
        let after = collection::decode_page_token(&req.page_token)?;
        // Resolve the effective page size: default when unset, clamped to the trust ceiling.
        let limit = collection::effective_page_size(req.page_size as i64, %[1]d, %[2]d);
        let page = self.handler.%[3]s(caller, req, collection::PageParams { limit, after }).await?;
        let mut resp = <%[4]s>::default();
        resp.%[5]s = page.items;
        // An empty next_page_token in the result means there is no next page.
        resp.next_page_token = collection::encode_page_token(page.next_after.as_ref());%[6]s
`, t.Page.DefaultSize, t.Page.MaxSize, methodSnake, t.OutputType, t.Page.ItemsField, total)
}

// rustMcpTransport renders the carriage: one MCP session over one gRPC bidi
// stream, as an rmcp worker transport.
func rustMcpTransport(serviceName, frameIn, frameOut string, maxFrameBytes int) string {
	surface := serviceName + "McpSurface"
	prefix := serviceName + "Mcp"
	return fmt.Sprintf(`
/// The client's half of a %[7]s stream.
pub type %[2]sRequestStream =
    std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<%[3]s, tonic::Status>> + Send>>;

/// This service's half of the same stream.
pub type %[2]sResponseStream =
    std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<%[4]s, tonic::Status>> + Send>>;

/// The largest outbound frame this transport will put on the wire.
///
/// Generated from (spec.assert_max_size) on `+"`mcp.McpResponse.payload`"+`, which is
/// what the generated response validation enforces. Past that value validation
/// fails the whole STREAM — killing the session over one large tool result — so
/// an over-size frame is turned into a JSON-RPC error on that one request
/// instead, and the session survives.
const %[5]s_MAX_FRAME_BYTES: usize = %[6]d;

/// Frames buffered in each direction before the producer waits. A session is
/// one client asking one question at a time; this is slack, not throughput.
const %[5]s_FRAME_BUFFER: usize = 16;

/// Why one session's transport stopped.
#[derive(Debug)]
pub enum %[2]sTransportError {
    /// The stream is gone: the client hung up, or this half was dropped.
    Closed,
    /// The client's half failed; carries the gRPC status, which includes a
    /// frame that failed request validation.
    Inbound(tonic::Status),
    /// The transport task could not be joined.
    Join(tokio::task::JoinError),
}

impl std::fmt::Display for %[2]sTransportError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Closed => write!(f, "the MCP session's stream is closed"),
            Self::Inbound(status) => write!(f, "reading the MCP session's stream: {status}"),
            Self::Join(e) => write!(f, "joining the MCP session's transport task: {e}"),
        }
    }
}

impl std::error::Error for %[2]sTransportError {}

type %[2]sOutbound = tokio::sync::mpsc::Sender<Result<%[4]s, tonic::Status>>;

/// Serve one MCP session over one gRPC stream.
///
/// The returned stream is this service's half of the RPC. The session runs on
/// its own task until the client's half ends or that half is dropped, so
/// nothing here outlives the RPC.
pub fn serve_%[8]s_mcp_session(
    handler: Arc<dyn %[1]sHandler>,
    caller: servicemanager::auth::Caller,
    inbound: %[2]sRequestStream,
) -> %[2]sResponseStream {
    let (outbound, responses) = tokio::sync::mpsc::channel(%[5]s_FRAME_BUFFER);
    let worker = %[2]sSessionWorker { inbound, outbound };
    let surface = %[9]s::new(handler, caller);
    tokio::spawn(async move {
        match rmcp::serve_server(surface, worker).await {
            Ok(session) => {
                if let Err(e) = session.waiting().await {
                    tracing::warn!(error = %%e, "an MCP session's task did not join");
                }
            }
            // Ordinary: a client that connects and hangs up without completing
            // the handshake lands here.
            Err(e) => {
                tracing::debug!(error = %%e, "an MCP session ended before it was initialized")
            }
        }
    });
    Box::pin(tokio_stream::wrappers::ReceiverStream::new(responses))
}

/// The frames of one session, moving between the gRPC stream and rmcp.
struct %[2]sSessionWorker {
    inbound: %[2]sRequestStream,
    outbound: %[2]sOutbound,
}

/// Put one of rmcp's frames on the wire, within the size bound.
async fn %[8]s_mcp_emit(
    outbound: &%[2]sOutbound,
    message: rmcp::service::TxJsonRpcMessage<rmcp::RoleServer>,
) -> Result<(), %[2]sTransportError> {
    let Some(payload) = %[8]s_mcp_within_bound(&message) else {
        // Only a frame naming no request at all is dropped, and rmcp does not
        // produce an over-size one: a notification carries no result.
        tracing::warn!("dropping an over-size MCP frame that names no request");
        return Ok(());
    };
    %[8]s_mcp_send(outbound, payload).await
}

/// Answer a frame that could not be decoded. It named no request, so the error
/// carries no id.
async fn %[8]s_mcp_emit_parse_error(
    outbound: &%[2]sOutbound,
    detail: String,
) -> Result<(), %[2]sTransportError> {
    let error = rmcp::model::JsonRpcError::new(
        None,
        rmcp::ErrorData::new(rmcp::model::ErrorCode::PARSE_ERROR, detail, None),
    );
    let payload = serde_json::to_string(
        &rmcp::service::TxJsonRpcMessage::<rmcp::RoleServer>::Error(error),
    )
    .map_err(|_| %[2]sTransportError::Closed)?;
    %[8]s_mcp_send(outbound, payload).await
}

async fn %[8]s_mcp_send(
    outbound: &%[2]sOutbound,
    payload: String,
) -> Result<(), %[2]sTransportError> {
    outbound
        .send(Ok(%[4]s {
            payload,
            ..Default::default()
        }))
        .await
        .map_err(|_| %[2]sTransportError::Closed)
}

/// The wire form of one outbound frame, or the error frame that stands in for
/// it when it is too large. `+"`None`"+` means the frame was over-size and named no
/// request to report that on, so there is nothing to send.
fn %[8]s_mcp_within_bound(
    message: &rmcp::service::TxJsonRpcMessage<rmcp::RoleServer>,
) -> Option<String> {
    let oversize = |detail: String| -> Option<String> {
        let id = %[8]s_mcp_frame_id(message)?;
        let error = rmcp::model::JsonRpcError::new(
            Some(id),
            rmcp::ErrorData::new(rmcp::model::ErrorCode::INTERNAL_ERROR, detail, None),
        );
        // An error frame is a fixed handful of bytes; if IT does not serialize
        // there is nothing left to fall back to.
        serde_json::to_string(&rmcp::service::TxJsonRpcMessage::<rmcp::RoleServer>::Error(error))
            .ok()
    };
    match serde_json::to_string(message) {
        Ok(payload) if payload.len() <= %[5]s_MAX_FRAME_BYTES => Some(payload),
        Ok(payload) => oversize(format!(
            "the result is {} bytes, over this transport's {}-byte frame limit; ask for less at a time",
            payload.len(),
            %[5]s_MAX_FRAME_BYTES
        )),
        Err(e) => oversize(format!("could not serialize the result: {e}")),
    }
}

/// The request id an outbound frame is about, if it is about one.
fn %[8]s_mcp_frame_id(
    message: &rmcp::service::TxJsonRpcMessage<rmcp::RoleServer>,
) -> Option<rmcp::model::RequestId> {
    match message {
        rmcp::model::JsonRpcMessage::Request(r) => Some(r.id.clone()),
        rmcp::model::JsonRpcMessage::Response(r) => Some(r.id.clone()),
        rmcp::model::JsonRpcMessage::Error(e) => e.id.clone(),
        rmcp::model::JsonRpcMessage::Notification(_) => None,
    }
}

impl rmcp::transport::worker::Worker for %[2]sSessionWorker {
    type Error = %[2]sTransportError;
    type Role = rmcp::RoleServer;

    fn err_closed() -> Self::Error {
        %[2]sTransportError::Closed
    }

    fn err_join(e: tokio::task::JoinError) -> Self::Error {
        %[2]sTransportError::Join(e)
    }

    async fn run(
        self,
        mut context: rmcp::transport::worker::WorkerContext<Self>,
    ) -> Result<(), rmcp::transport::worker::WorkerQuitReason<Self::Error>> {
        let %[2]sSessionWorker {
            mut inbound,
            outbound,
        } = self;
        // Taken out of the context up front: the select! below holds a borrow
        // of from_handler_rx across its arms.
        let to_handler = context.to_handler_tx.clone();
        let cancelled = context.cancellation_token.clone();
        loop {
            tokio::select! {
                _ = cancelled.cancelled() => {
                    return Err(rmcp::transport::worker::WorkerQuitReason::Cancelled)
                }
                frame = inbound.next() => match frame {
                    None => {
                        return Err(rmcp::transport::worker::WorkerQuitReason::TransportClosed)
                    }
                    Some(Err(status)) => {
                        return Err(rmcp::transport::worker::WorkerQuitReason::fatal(
                            %[2]sTransportError::Inbound(status),
                            "reading the client's half of the stream",
                        ))
                    }
                    Some(Ok(frame)) => {
                        match serde_json::from_str::<
                            rmcp::service::RxJsonRpcMessage<rmcp::RoleServer>,
                        >(&frame.payload)
                        {
                            Ok(message) => to_handler.send(message).await.map_err(|_| {
                                rmcp::transport::worker::WorkerQuitReason::HandlerTerminated
                            })?,
                            // Not fatal: one unreadable frame does not say the
                            // next one is unreadable too, and MCP has an error
                            // frame for exactly this.
                            Err(e) => %[8]s_mcp_emit_parse_error(
                                &outbound,
                                format!("could not decode the frame: {e}"),
                            )
                            .await
                            .map_err(rmcp::transport::worker::WorkerQuitReason::fatal_context(
                                "answering an undecodable frame",
                            ))?,
                        }
                    }
                },
                from_handler = context.from_handler_rx.recv() => {
                    let Some(request) = from_handler else {
                        return Err(
                            rmcp::transport::worker::WorkerQuitReason::HandlerTerminated
                        );
                    };
                    let sent = %[8]s_mcp_emit(&outbound, request.message).await;
                    // The responder is gone when rmcp stopped waiting on this
                    // send; the send still happened, so there is nothing to do.
                    let _ = request.responder.send(sent);
                }
            }
        }
    }
}
`, serviceName, prefix, frameIn, frameOut,
		strings.ToUpper(prostSnakeName(serviceName)), maxFrameBytes,
		parser.McpCarrierRpcName(serviceName), prostSnakeName(serviceName), surface)
}

// generateRustMcpCarrierWrapper renders the carrier RPC's gRPC method on the
// ValidationWrapper.
//
// This is the one place the ordinary wrapper's handler delegation is replaced:
// the carrier has no handler method to call, because nothing about carrying an
// MCP session is the service's to write. It authorizes exactly as the ordinary
// bidi wrapper does — the carrier is a real rpc with a real auth model — then
// hands the stream to the generated session server.
func generateRustMcpCarrierWrapper(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg, parentDir string, crossPkgTypes map[string]string) string {
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	fullMethod := fmt.Sprintf("/%s/%s.%s/%s", parentDir, servicePkg, svc.Name, method.Name)
	authLine := rustAuthLine(svc, method, fullMethod)
	requestStreamBlock := rustValidatedRequestStreamBlock(inputType, method.FullInputType, "", "", "", method.Name)
	streamType := fmt.Sprintf(responseStreamFmt, outputType)

	return fmt.Sprintf(`    type %[1]sStream = %[2]s;

    async fn %[3]s(
        &self,
        request: tonic::Request<tonic::Streaming<%[4]s>>,
    ) -> Result<tonic::Response<Self::%[1]sStream>, tonic::Status> {
        %[5]s
        let request_id = servicemanager::auth::extract_request_id(&request);
        %[6]s
        tracing::debug!(method = "%[7]s", request_id = %%request_id, "rpc");
        // Fail closed: every server-computed predicate in the tool surface comes
        // from this caller, so a session with no verified identity would run its
        // tools unscoped.
        let caller = caller.ok_or_else(|| {
            tonic::Status::unauthenticated("an MCP session requires an authenticated caller")
        })?;
%[8]s
        Ok(tonic::Response::new(serve_%[9]s_mcp_session(
            self.handler.clone(),
            caller,
            request.into_inner(),
        )))
    }`, method.Name, streamType, prostSnakeName(method.Name), inputType,
		authLine, rustCallerExtraction, fullMethod, requestStreamBlock,
		prostSnakeName(svc.Name))
}

// rustMcpTests renders the tests every generated MCP surface carries.
//
// Generated rather than hand-written for the same reason the dispatch itself
// is: they assert the properties that must not silently regress in ANY service
// — that a tool call reaches its own typed handler with the arguments decoded,
// and that a server-computed field is stamped from the caller and refused as an
// argument. A service that grows an MCP tool gets the gate with it.
func rustMcpTests(serviceName string, svc *parser.ServiceInfo, tools []rustMcpTool, servicePkg string, crossPkgTypes map[string]string, mcpCarrier *parser.MethodInfo) string {
	surface := serviceName + "McpSurface"
	var b strings.Builder

	fmt.Fprintf(&b, `
#[cfg(test)]
mod %[1]s_mcp_tests {
    use super::*;

    /// The subject the test caller's token carries. Every server-stamped
    /// principal below must come out equal to it.
    const MCP_TEST_SUBJECT: &str = "spiffe://example/tenants/acme/users/alice";

    fn mcp_test_caller() -> servicemanager::auth::Caller {
        let mut token = token::Token::new();
        token
            .set_subject(MCP_TEST_SUBJECT)
            .expect("a freshly built token is mutable");
        servicemanager::auth::Caller::new(token, "mcp-test".to_string())
    }

    /// A handler that reports what its MCP tools were called with, by failing
    /// with the request's own Debug rendering. Reporting through the error is
    /// what lets the assertions below read the request the dispatch built
    /// without the handler having to synthesize a valid response for it.
    struct McpReportingHandler;

    #[tonic::async_trait]
    impl %[2]sHandler for McpReportingHandler {
`, prostSnakeName(serviceName), serviceName)

	for _, m := range svc.Methods {
		if m == mcpCarrier {
			continue
		}
		if !m.IsMcpTool() {
			b.WriteString("    " + generateRustHandlerStubMethod(m, servicePkg, serviceName, crossPkgTypes))
			b.WriteString("\n")
			continue
		}
		methodSnake := prostSnakeName(m.Name)
		inputType, outputType := resolveRustTypeQualified(m, servicePkg, crossPkgTypes)
		if rustMcpEmptyRequest(m) {
			fmt.Fprintf(&b, `        async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>) -> Result<%s, tonic::Status> {
            Err(tonic::Status::internal("MCP_CALLED %s"))
        }
`, methodSnake, outputType, m.Name)
			continue
		}
		if m.IsPageCollection() {
			// A PAGE tool's handler is the keyset producer the gRPC PAGE wrapper
			// calls, so the stub declares that signature — and reports the window
			// it was handed, which is what proves the tool call's page_size
			// reached PageParams rather than being dropped.
			fmt.Fprintf(&b, `        async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, req: %s, page: collection::PageParams) -> Result<collection::Page<%s>, tonic::Status> {
            Err(tonic::Status::internal(format!("MCP_CALLED {:?} page_limit={}", req, page.limit)))
        }
`, methodSnake, inputType, rustPageItemType(m, servicePkg, crossPkgTypes))
			continue
		}
		fmt.Fprintf(&b, `        async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, req: %s) -> Result<%s, tonic::Status> {
            Err(tonic::Status::internal(format!("MCP_CALLED {:?}", req)))
        }
`, methodSnake, inputType, outputType)
	}
	b.WriteString("    }\n")

	// The tool set is published.
	fmt.Fprintf(&b, `
    /// Every tool this service publishes — declared RPC_PRINCIPAL_MCP or
    /// auto-exposed user rpc — reaches the published tool set, with a schema
    /// rmcp can serve.
    #[test]
    fn the_mcp_tool_set_is_published() {
        let tools = %[1]s::tools();
        assert_eq!(tools.len(), %[2]d, "the published tool set is not the declared one");
`, surface, len(tools))
	for _, t := range tools {
		fmt.Fprintf(&b, `        assert!(
            tools.iter().any(|t| t.name == %[1]s),
            "%[2]s is an MCP tool of this service but is not published"
        );
`, rustQuote(t.ToolName), t.ToolName)
	}
	b.WriteString("    }\n")

	for _, t := range tools {
		b.WriteString(rustMcpToolTests(surface, t))
	}

	b.WriteString("}\n")
	return b.String()
}

// rustMcpTestPageSize is the page_size the generated gate asks a PAGE tool for.
// Below every resolved default and ceiling, so the clamp passes it through
// unchanged and the assertion reads the window the tool call actually asked for.
const rustMcpTestPageSize = 3

// rustMcpToolTests renders one tool's dispatch tests. A tool whose required
// arguments cannot be synthesized (a required enum or nested message) gets the
// refusal test only — there is no obviously-valid call to drive.
func rustMcpToolTests(surface string, t rustMcpTool) string {
	var b strings.Builder

	// A PAGE tool is always drivable: page_size alone is a valid call.
	drivable := t.EmptyRequest || t.Page != nil || len(t.RequiredScalars) > 0 || t.PrincipalField != ""
	if drivable {
		var args []string
		for _, a := range t.RequiredScalars {
			args = append(args, fmt.Sprintf("%s: %s", rustQuote(a.Field), a.JSON))
		}
		if t.Page != nil {
			args = append(args, fmt.Sprintf("%q: %d", "page_size", rustMcpTestPageSize))
		}
		argsJSON := "{" + strings.Join(args, ", ") + "}"

		fmt.Fprintf(&b, `
    /// A tool call reaches its own typed handler, with the arguments decoded
    /// into the rpc's request message.
    #[tokio::test]
    async fn %[1]s_reaches_its_typed_handler() {
        let surface = %[2]s::new(Arc::new(McpReportingHandler), mcp_test_caller());
        let arguments: rmcp::model::JsonObject =
            serde_json::from_str(%[3]s).expect("generated tool arguments are a JSON object");
        let reported = surface
            .%[4]s(arguments)
            .await
            .expect_err("the reporting handler returned Ok");
        assert!(
            reported.message().starts_with("MCP_CALLED"),
            "the tool call did not reach its typed handler: {}",
            reported.message()
        );
`, t.ToolName, surface, rustQuote(argsJSON), rustMcpDispatchFnName(t))

		for _, a := range t.RequiredScalars {
			fmt.Fprintf(&b, `        assert!(
            reported.message().contains(%[1]s),
            "the handler did not receive %[2]s: {}",
            reported.message()
        );
`, rustQuote(a.Debug), a.Field)
		}
		if t.Page != nil {
			fmt.Fprintf(&b, `        // PAGE: the tool call's own page_size reached the handler as the
        // window of a collection::PageParams — the same three-argument call
        // the gRPC PAGE wrapper makes.
        assert!(
            reported.message().contains("page_limit=%[1]d"),
            "the tool call's page_size did not reach PageParams: {}",
            reported.message()
        );
`, rustMcpTestPageSize)
		}
		if t.PrincipalField != "" {
			fmt.Fprintf(&b, `        // (spec.principal): stamped by the dispatch from the verified caller,
        // so the handler sees it without the tool call ever carrying it.
        assert!(
            reported.message().contains(MCP_TEST_SUBJECT),
            "%[1]s was not stamped from the caller: {}",
            reported.message()
        );
`, t.PrincipalField)
		}
		b.WriteString("    }\n")
	}

	for _, f := range []struct{ marker, field string }{
		{"spec.principal", t.PrincipalField},
		{"spec.namespace", t.NamespaceField},
	} {
		if f.field == "" {
			continue
		}
		fmt.Fprintf(&b, `
    /// (%[5]s) %[3]s is the server's to set. A tool call that names it is
    /// refused outright rather than silently overridden, and the handler is
    /// never reached.
    #[tokio::test]
    async fn %[1]s_refuses_a_client_supplied_%[3]s() {
        let surface = %[2]s::new(Arc::new(McpReportingHandler), mcp_test_caller());
        let mut arguments = rmcp::model::JsonObject::new();
        arguments.insert(
            %[4]s.to_string(),
            serde_json::Value::String("spiffe://example/tenants/acme/users/mallory".to_string()),
        );
        let refused = surface
            .%[6]s(arguments)
            .await
            .expect_err("a client-supplied %[3]s was accepted");
        assert!(
            !refused.message().starts_with("MCP_CALLED"),
            "a client-supplied %[3]s reached the handler: {}",
            refused.message()
        );
        assert!(
            refused.message().contains("not a tool argument"),
            "unexpected refusal: {}",
            refused.message()
        );
    }
`, t.ToolName, surface, f.field, rustQuote(f.field), f.marker, rustMcpDispatchFnName(t))
	}

	return b.String()
}

// rustQuote renders s as a Rust string literal, quotes included.
//
// Not strconv.Quote: Go writes a non-printable rune as a bare \uXXXX, which is
// not Rust escape syntax — rustc rejects "a b" with "incorrect unicode
// escape sequence". Rust spells it \u{a0}. The tool name, description and
// schema this quotes are proto-derived text, so a doc comment carrying a
// non-breaking space (or any other non-printable rune) would otherwise emit
// Rust that does not compile. Java's javaQuote exists for the same reason,
// with UTF-16 surrogate pairs in place of the braces.
//
// Escaping the whole non-printable class, rather than only what Rust's grammar
// forces, also keeps invisible format characters — bidi overrides above all,
// which rustc denies outright inside a literal — out of the generated source.
func rustQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r > 0x7e && !unicode.IsPrint(r) {
			fmt.Fprintf(&b, `\u{%x}`, r)
			continue
		}
		// Everything a Rust literal escapes below U+007F is already rustStringEscape's
		// table; printable Unicode above it embeds as itself, Rust source being UTF-8.
		b.WriteString(rustStringEscape(string(r)))
	}
	b.WriteByte('"')
	return b.String()
}
