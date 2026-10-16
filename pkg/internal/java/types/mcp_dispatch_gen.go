// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// The MCP dispatch seam (BIG GOAL 8 Phase 0, Java).
//
// A service with at least one (spec.rpc) principal: RPC_PRINCIPAL_MCP rpc gets
// a generated Broker<Service>McpSession carrier (pkg/internal/proto/mcp_carrier.go)
// and, from here, everything that runs ON TOP of that stream, emitted into one
// extra file next to the service wrapper:
//
//   - the tool set, derived from the same rpc declarations registry.json's tool
//     entries come from — name, description, and a JSON Schema over the request
//     message (decision doc §5: a typed contract, never a second hand-authored
//     description);
//   - a Java MCP SDK server that answers tools/list and tools/call;
//   - per-tool dispatch that decodes the arguments into the rpc's own typed
//     request, stamps (spec.principal) / (spec.namespace) from the verified
//     Caller, calls the service's typed handler method, filters
//     (spec.store_principal) / (spec.store_namespace) rows out of the response,
//     and frames the typed result back;
//   - the carriage itself: an McpServerTransportProvider over the gRPC bidi
//     StreamObserver pair.
//
// Nothing here is emitted for a service with no MCP rpc. This is Java's half of
// what 0e6d87c6 generated for Rust and 82ccf266 for Go; the three share the
// field-marker doctrine and nothing else, because rmcp, the Go SDK and
// io.modelcontextprotocol.sdk:mcp are unrelated API surfaces.

const (
	// javaMcpSdkArtifact is the MCP SDK the generated dispatch is written
	// against. tacplus writes no pom.xml on the Java path — generated Java lands
	// in the CONSUMER's own Maven module — so the version pin lives in the
	// dependency inventory (packages.KnownPackages / dependencies.yaml) rather
	// than in a manifest this generator writes. Same situation Go's Q12 found.
	javaMcpSdkArtifact = "io.modelcontextprotocol.sdk:mcp"

	// javaMcpSdkJavaVersion is the minimum Java release the SDK itself requires.
	// A consumer module below it cannot build the generated dispatch, so the
	// generated file says so in its own header rather than failing obscurely in
	// the compiler.
	javaMcpSdkJavaVersion = "17"

	// javaMcpServerVersion is the version the generated MCP server reports in
	// its initialize handshake. It describes the generated TERMINATION, not the
	// service, which tacplus has no version for; it moves when this seam's wire
	// behaviour does. Same value and same reasoning as Go's goMcpServerVersion.
	javaMcpServerVersion = "1"
)

// javaMcpInstructions is the server-level instruction block every generated MCP
// surface serves. It states the one rule a model cannot discover from the tool
// schemas alone: the fields the schemas omit are omitted because the server
// computes them, and naming one is an error rather than a way to widen reach.
const javaMcpInstructions = "Every tool here runs as you: the server sets any caller-scoped field " +
	"from your verified identity, and those fields are not tool arguments. Naming one is an " +
	"error, not a way to widen what you can reach."

// javaMcpTool is one RPC_PRINCIPAL_MCP rpc, resolved for emission.
type javaMcpTool struct {
	Method      *parser.MethodInfo
	ToolName    string // the published tool name (registry.json's mcp_tool_name)
	Description string // the rpc's own leading doc comment
	Schema      string // the tool's input JSON Schema, marshalled
	InputType   string // rendered Java request type ("C2sonly.GetTicketBriefRequest")
	OutputType  string // rendered Java response type
	// EmptyRequest / EmptyResponse mirror the handler-interface renderer's own
	// tests, so dispatch calls the handler with the signature it declares.
	EmptyRequest  bool
	EmptyResponse bool
	// InputHelpers / OutputHelpers are the Helpers class qualifiers the ordinary
	// gRPC wrapper calls validate<T> on, or "" when the type has none.
	InputHelpers  string
	OutputHelpers string
	// HandlerMethod is the lowerCamel name of the typed handler method.
	HandlerMethod string
	// RequiredScalars are the request's required scalar fields, with a synthetic
	// value each, used to drive the generated dispatch test.
	RequiredScalars []javaMcpScalarArg
	// ResponseRequiredScalars is the same for the response, so the generated
	// test's stub handler returns something the dispatch's own response
	// validation accepts.
	ResponseRequiredScalars []javaMcpScalarArg
	// RequestSynthesizable / ResponseSynthesizable report whether every required
	// field of the request / response is a scalar this seam can invent a value
	// for. A tool that fails either gets the REFUSAL cases only — there is no one
	// obviously-valid value for a required enum or nested message, and a
	// generated test that guesses wrong is a false failure. Same rule Rust's Q7
	// and Go's Q8 took.
	RequestSynthesizable  bool
	ResponseSynthesizable bool

	// PrincipalField is the request's (spec.principal) field, "" when it
	// declares none. It is absent from Schema and refused as an argument.
	PrincipalField string
	// StorePrincipalRows are the response's repeated (spec.store_principal)
	// record fields, filtered to the caller before the result is returned.
	StorePrincipalRows []javaMcpRowField
	// NamespaceField / StoreNamespaceRows are the same two seams for the tenant
	// boundary.
	NamespaceField     string
	StoreNamespaceRows []javaMcpRowField
	// OwnerDefaultGuard is the (spec.owner_default) refusal rendered by the same
	// helper the gRPC wrapper uses, "" when the tool touches no such type or
	// its access contradicts nothing.
	OwnerDefaultGuard string

	// Page is the PAGE-collection window, nil for every other rpc. A PAGE tool
	// takes page_size/page_token as ordinary arguments and calls the same
	// three-argument handler the gRPC PAGE wrapper calls.
	Page *javaMcpPage
}

// javaMcpPage is what the dispatch needs to drive a PAGE rpc's handler and
// assemble its response envelope. Every value here is read off the same
// collection.Resolved the gRPC PAGE wrapper reads — there is no MCP-specific
// page bound.
type javaMcpPage struct {
	ItemsField  string // Pascal-case response field holding the page's items
	ItemType    string // rendered Java item type (the Page<Item> element)
	DefaultSize uint32 // page_size when the call omits it
	MaxSize     uint32 // the trust-tier ceiling page_size is clamped to
	HasTotal    bool   // the response declares total_count
}

// javaMcpPageOf resolves the PAGE window of a collection rpc, or nil when the
// rpc is not a PAGE read.
func javaMcpPageOf(m *parser.MethodInfo, outputType string, crossPkgOuterClasses map[string]string) *javaMcpPage {
	if !m.IsPageCollection() {
		return nil
	}
	return &javaMcpPage{
		ItemsField:  strutil.ToPascalCase(m.Resolved.PageItemsField),
		ItemType:    javaWatchTypeRef(m.Resolved.PageItemType, outputType, crossPkgOuterClasses),
		DefaultSize: m.Resolved.DefaultPageSize,
		MaxSize:     m.Resolved.MaxPageSize,
		HasTotal:    m.Resolved.PageHasTotal,
	}
}

// javaMcpRowField is a repeated field of a response whose item type declares
// (spec.store_principal) or (spec.store_namespace).
type javaMcpRowField struct {
	Field     string // snake_case response field holding the rows
	ItemField string // snake_case caller-scoped field on the row type
}

// javaMcpScalarArg is one required scalar field of a tool's request, with a
// synthetic value the generated dispatch test passes for it.
type javaMcpScalarArg struct {
	Field string
	// Java is the value as it appears in the generated test's argument map.
	Java string
	// Match is the substring the generated test looks for in the handler's
	// report of the request it was called with.
	Match string
}

// javaMcpCarrierMethod returns the service's generated session-carrier rpc, or
// nil when the service declares no MCP tool. The carrier is identified the same
// way the registry identifies it.
func javaMcpCarrierMethod(svc *parser.ServiceInfo) *parser.MethodInfo {
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

// javaMcpClassName is the class the dispatch lands in, next to the service
// wrapper and in the same package.
func javaMcpClassName(serviceName string) string { return serviceName + "Mcp" }

// javaMcpTools resolves every MCP tool the service publishes, in declaration
// order (the order the tool list is served in).
func javaMcpTools(svc *parser.ServiceInfo, servicePackage, outerClassName string,
	crossPkgOuterClasses, crossPkgHelpers map[string]string, san *javaEgressSanitizer) ([]javaMcpTool, error) {
	var tools []javaMcpTool
	for _, m := range svc.Methods {
		if !m.IsMcpTool() {
			continue
		}
		if err := javaMcpToolGeneratable(svc, m, servicePackage, san); err != nil {
			return nil, err
		}
		schema, err := javaMcpInputSchema(m, servicePackage, san)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		ownerDefaultGuard, err := javaOwnerDefaultGuard(m, san, servicePackage, javaOwnerDefaultMcpSeam("                "), "                ")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		empty := javaMcpEmptyRequest(m)
		scalars, reqOK := []javaMcpScalarArg(nil), true
		if !empty {
			scalars, reqOK = san.mcpRequiredScalarArgs(m.FullInputType, servicePackage)
		}
		respScalars, respOK := []javaMcpScalarArg(nil), true
		if m.FullOutputType != "google.protobuf.Empty" {
			respScalars, respOK = san.mcpRequiredScalarArgs(m.FullOutputType, servicePackage)
		}
		tools = append(tools, javaMcpTool{
			Method:                  m,
			ToolName:                m.McpToolName,
			Description:             parser.McpToolDescription(m.Comment),
			Schema:                  schema,
			InputType:               resolveJavaMethodType(m.FullInputType, m.InputType, servicePackage, outerClassName, crossPkgOuterClasses),
			OutputType:              resolveJavaMethodType(m.FullOutputType, m.OutputType, servicePackage, outerClassName, crossPkgOuterClasses),
			EmptyRequest:            empty,
			EmptyResponse:           m.FullOutputType == "google.protobuf.Empty",
			InputHelpers:            resolveValidationHelpers(m.FullInputType, servicePackage, crossPkgHelpers),
			OutputHelpers:           resolveValidationHelpers(m.FullOutputType, servicePackage, crossPkgHelpers),
			HandlerMethod:           methodNameLowerCamel(m.Name),
			RequiredScalars:         scalars,
			ResponseRequiredScalars: respScalars,
			RequestSynthesizable:    reqOK,
			ResponseSynthesizable:   respOK,
			PrincipalField:          san.mcpAnnotatedStringField(m.FullInputType, servicePackage, "principal"),
			StorePrincipalRows:      san.mcpRowFields(m.FullOutputType, servicePackage, "store_principal"),
			NamespaceField:          san.mcpAnnotatedStringField(m.FullInputType, servicePackage, "namespace"),
			StoreNamespaceRows:      san.mcpRowFields(m.FullOutputType, servicePackage, "store_namespace"),
			OwnerDefaultGuard:       ownerDefaultGuard,
			Page: javaMcpPageOf(m, resolveJavaMethodType(m.FullOutputType, m.OutputType, servicePackage, outerClassName, crossPkgOuterClasses),
				crossPkgOuterClasses),
		})
	}
	return tools, nil
}

// javaMcpToolGeneratable refuses to emit dispatch for a tool whose guards this
// seam cannot render.
//
// Before this seam existed an MCP tool was suppressed from the Java surface
// entirely (fa915df7), so the service wrapper's own refusals were skipped for it
// — nothing served the rpc, so there was nothing to under-enforce. Dispatch
// changes that: the tool IS served now, by this code, so the same refusals the
// wrapper makes for a gRPC rpc apply here, for the same reasons.
//
// Unlike Rust's and Go's twins, min_group_size / store_epoch are NOT refused
// outright here: Java is the language whose wrapper does generate those guards,
// so refusing them would be refusing a shape Java can enforce. They are refused
// only when this seam cannot render the guard, which is what the wrapper's own
// javaMinGroupSizeUnenforceable / javaStoreEpochUnenforceable already decide.
func javaMcpToolGeneratable(svc *parser.ServiceInfo, m *parser.MethodInfo, servicePackage string, san *javaEgressSanitizer) error {
	// An MCP tool is one question and one answer. A streaming or
	// collection-returning rpc has a handler signature this dispatch cannot
	// call (it takes a StreamObserver, an Iterator or a CollectionRuntime.Page),
	// and MCP has no frame to carry it in.
	if m.ClientStreaming || m.ServerStreaming {
		return fmt.Errorf("%s.%s is an RPC_PRINCIPAL_MCP rpc but is streaming; an MCP tool call is one request and one response, so a streaming rpc cannot be published as a tool",
			svc.Name, m.Name)
	}
	if m.Collection != nil && !m.IsPageCollection() {
		return fmt.Errorf("%s.%s is an RPC_PRINCIPAL_MCP rpc but declares method_collection in mode %s; only PAGE has a shape a tool call can drive (page_size/page_token in, next_page_token out) — the other modes hand the handler a stream or a materialized collection, which an MCP tool call has no frame for",
			svc.Name, m.Name, m.Collection.Mode)
	}
	// (spec.record).min_group_size: this seam renders no size-only guard, so a
	// tool carrying one would be dispatched unguarded even though the gRPC
	// wrapper next door guards it.
	if m.MinGroupSize > 0 {
		return fmt.Errorf("%s.%s is an MCP tool carrying (spec.min_group_size) = %d, but the generated MCP dispatch renders no size-only guard; publish it as a gRPC rpc or drop the threshold",
			svc.Name, m.Name, m.MinGroupSize)
	}
	// (spec.record).mutability = MUTABILITY_APPEND_ONLY: no language renders a
	// guard for a row mutation over a write-once record.
	if why := javaAppendOnlyUnenforceable(m, san, servicePackage); why != "" {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool because %s", svc.Name, m.Name, why)
	}
	// (spec.store_epoch): the stamp is rendered by the gRPC wrapper's write
	// path, not by this seam, so a tool over an epoch-bearing write would leave
	// the store epoch unstamped.
	if f := javaMcpStoreEpochField(m.FullInputType, san, servicePackage); f != "" {
		return fmt.Errorf("%s.%s is an MCP tool taking %s, whose field %q carries (spec.store_epoch), but the stamp is rendered by the gRPC wrapper's write path and not by MCP dispatch; a tool call of this write would leave the store epoch unstamped",
			svc.Name, m.Name, m.FullInputType, f)
	}
	// (spec.owner_default): the same decision the gRPC wrapper makes, from the
	// same helper. javaMcpToolDispatch renders the guard next to the
	// (spec.principal) stamp, so what is refused here is only what that guard
	// cannot be rendered against: a request shape with no validation seam at
	// which the caller can be compared to the row's principal. Go's MCP
	// dispatch refuses the same set.
	guard, err := javaOwnerDefaultGuard(m, san, servicePackage, javaOwnerDefaultMcpSeam(""), "")
	if err != nil {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool: %w", svc.Name, m.Name, err)
	}
	if guard != "" && (javaMcpEmptyRequest(m) || m.IsPageCollection()) {
		return fmt.Errorf("%s.%s is an MCP tool touching a type whose (spec.owner_default) contradicts its access, but its request shape has no validation seam at which the caller can be compared to the row's principal",
			svc.Name, m.Name)
	}
	return nil
}

// javaMcpStoreEpochField returns the request's (spec.store_epoch) field, or "".
// It reads the same shared lookup the gRPC wrapper's stamp does
// (parser.StoreEpochField, via storeEpochField) rather than the MCP seam's
// generic annotation probe: the probe answers on the annotation's PRESENCE, so
// an explicit `= false` would refuse a tool the wrapper next door would happily
// serve unstamped-and-unannotated.
func javaMcpStoreEpochField(fullType string, es *javaEgressSanitizer, servicePackage string) string {
	return es.storeEpochField(fullType, servicePackage)
}

// javaMcpEmptyRequest mirrors the handlerEmpty test generateJavaHandlerMethod
// uses, so dispatch calls the handler with the parameter list it declares.
func javaMcpEmptyRequest(m *parser.MethodInfo) bool {
	return m.FullInputType == "google.protobuf.Empty" || m.FullInputType == "trace.TraceRequest"
}

// ── schema derivation ───────────────────────────────────────────────────────

// javaMcpInputSchema builds the tool's argument schema from the rpc's request
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
func javaMcpInputSchema(m *parser.MethodInfo, servicePackage string, san *javaEgressSanitizer) (string, error) {
	var schema map[string]any
	if javaMcpEmptyRequest(m) {
		// A tool taking no arguments. Its rpc's request type carries no fields
		// worth publishing, and the handler interface renders it with no
		// `request` parameter, so there is no message to walk.
		schema = map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}
	} else {
		var err error
		schema, err = san.mcpMessageSchema(m.FullInputType, servicePackage, map[string]bool{})
		if err != nil {
			return "", err
		}
	}
	if m.IsPageCollection() {
		javaMcpAnnotatePageSchema(schema, m.Resolved.DefaultPageSize, m.Resolved.MaxPageSize)
	}
	schema["description"] = parser.McpToolDescription(m.Comment)
	out, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("marshal input schema: %w", err)
	}
	return string(out), nil
}

// javaMcpAnnotatePageSchema tells the model how to drive the two paging fields a
// PAGE request already declares. They are ordinary fields of the request
// message, so they are already in the schema; what the message index cannot say
// is the window's default and its ceiling, which are the wrapper's own resolved
// bounds. The bound is stated, not invented: it is the same clamp the dispatch
// applies at runtime, so a model that asks for more is told what it will get
// rather than silently given less.
func javaMcpAnnotatePageSchema(schema map[string]any, defaultSize, maxSize uint32) {
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

// mcpAnnotatedStringField returns the snake_case name of fullType's field
// carrying the named annotation, or "" when it declares none. At most one such
// field per message is enforced by the parser's principal/namespace validators.
//
// Deliberately NOT routed through recordMessage() the way principalField() next
// door in recordprincipal_gen.go is: the record plane's fallback to
// (spec.record).owner exists so every record type is filtered without a second
// annotation being remembered, and it is scoped to (spec.record) messages. An
// MCP tool's request is not a record, and the same proto must publish the same
// tool guards in Java, Rust and Go — both of which read the annotation directly.
func (es *javaEgressSanitizer) mcpAnnotatedStringField(fullType, servicePackage, annotation string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return ""
	}
	for i := range msg.Fields {
		if _, ok := msg.Fields[i].Annotations[annotation]; ok {
			return msg.Fields[i].Name
		}
	}
	return ""
}

// mcpRowFields returns the repeated message fields of fullType whose item type
// declares the named caller-scoping annotation — the rows dispatch must filter
// to the caller before the result leaves the process.
func (es *javaEgressSanitizer) mcpRowFields(fullType, servicePackage, annotation string) []javaMcpRowField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []javaMcpRowField
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsRepeated || !f.IsMessage || f.IsMap {
			continue
		}
		itemPkg := f.TypePackage
		if itemPkg == "" {
			itemPkg = pkg
		}
		item, ok := es.msgs[itemPkg+"\x00"+f.Type]
		if !ok {
			continue
		}
		for j := range item.Fields {
			if _, ok := item.Fields[j].Annotations[annotation]; ok {
				out = append(out, javaMcpRowField{Field: f.Name, ItemField: item.Fields[j].Name})
				break
			}
		}
	}
	return out
}

// mcpRequiredScalarArgs returns the request's required scalar fields, plus a
// defined value for each singular enum the validator refuses at zero. A request
// whose required set is not entirely scalar returns nil: the generated dispatch
// test synthesizes arguments, and there is no one obviously-valid value for a
// required enum or nested message.
func (es *javaEgressSanitizer) mcpRequiredScalarArgs(fullType, servicePackage string) ([]javaMcpScalarArg, bool) {
	if es == nil {
		return nil, false
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil, false
	}
	var out []javaMcpScalarArg
	for i := range msg.Fields {
		f := &msg.Fields[i]
		// An omitted enum decodes to its zero value, which assert_enum_defined
		// refuses. A repeated or optional enum has no such refusal: its empty
		// state is valid, so it needs no synthesized value.
		if a, ok := f.Annotations["assert_enum_defined"]; ok && a.BoolValue && f.IsEnum && !f.IsRepeated && !f.IsOptional {
			value, ok := es.mcpDefinedEnumName(f, pkg)
			if !ok {
				return nil, false
			}
			out = append(out, javaMcpScalarArg{Field: f.Name, Java: strconv.Quote(value), Match: value})
			continue
		}
		if a, ok := f.Annotations["assert_required"]; !ok || !a.BoolValue {
			continue
		}
		if f.IsRepeated || f.IsMap || f.IsMessage || f.IsEnum {
			return nil, false
		}
		switch javaMcpScalarJSONType(f.Type) {
		case "string":
			v := "mcp-test-" + f.Name
			out = append(out, javaMcpScalarArg{Field: f.Name, Java: strconv.Quote(v), Match: v})
		case "integer":
			out = append(out, javaMcpScalarArg{Field: f.Name, Java: "1", Match: "1"})
		case "number":
			out = append(out, javaMcpScalarArg{Field: f.Name, Java: "1", Match: "1"})
		case "boolean":
			out = append(out, javaMcpScalarArg{Field: f.Name, Java: "true", Match: "true"})
		default:
			return nil, false
		}
	}
	return out, true
}

// mcpDefinedEnumName returns the wire name of the first declared non-zero value
// of an enum field's type: the value an assert_enum_defined field must carry to
// pass validation. The second result is false when the enum declares no such
// value.
func (es *javaEgressSanitizer) mcpDefinedEnumName(f *model.Field, msgPkg string) (string, bool) {
	pkg := f.TypePackage
	if pkg == "" {
		pkg = msgPkg
	}
	en, ok := es.enums[pkg+"\x00"+f.Type]
	if !ok {
		return "", false
	}
	for _, v := range en.Values {
		if v.Number != 0 {
			return v.Name, true
		}
	}
	return "", false
}

// mcpMessageSchema renders one message as a JSON Schema object. visiting breaks
// recursion on a message that (directly or otherwise) contains itself; such a
// field is rendered as an unconstrained object rather than expanded forever.
func (es *javaEgressSanitizer) mcpMessageSchema(fullType, servicePackage string, visiting map[string]bool) (map[string]any, error) {
	if es == nil {
		return nil, fmt.Errorf("no message index available to derive an MCP tool schema")
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
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
		if javaMcpServerComputed(f) {
			continue
		}
		properties[f.Name] = es.mcpFieldSchema(f, pkg, servicePackage, visiting)
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
		// json.Marshal sorts map keys but not slices; sort so the emitted schema
		// is byte-stable across runs.
		sort.Strings(required)
		schema["required"] = required
	}
	return schema, nil
}

// javaMcpServerComputed reports whether a field's value is the server's to
// compute from the verified Caller rather than the model's to supply.
func javaMcpServerComputed(f *model.Field) bool {
	for _, a := range []string{"principal", "store_principal", "namespace", "store_namespace"} {
		if _, ok := f.Annotations[a]; ok {
			return true
		}
	}
	return false
}

func (es *javaEgressSanitizer) mcpFieldSchema(f *model.Field, msgPkg, servicePackage string, visiting map[string]bool) map[string]any {
	if f.IsMap {
		return javaMcpDescribed(f, map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": javaMcpScalarJSONType(f.MapValueType)},
		})
	}
	item := es.mcpSingularSchema(f, msgPkg, servicePackage, visiting)
	if f.IsRepeated {
		return javaMcpDescribed(f, map[string]any{"type": "array", "items": item})
	}
	return javaMcpDescribed(f, item)
}

func (es *javaEgressSanitizer) mcpSingularSchema(f *model.Field, msgPkg, servicePackage string, visiting map[string]bool) map[string]any {
	switch {
	case f.IsEnum:
		// The value on the wire is the enum value's own name; the set of names is
		// not carried in the message index, so the schema constrains the type
		// without enumerating them.
		return map[string]any{"type": "string"}
	case f.IsMessage:
		pkg := f.TypePackage
		if pkg == "" {
			pkg = msgPkg
		}
		if visiting[pkg+"\x00"+f.Type] {
			return map[string]any{"type": "object"}
		}
		nested, err := es.mcpMessageSchema(pkg+"."+f.Type, servicePackage, visiting)
		if err != nil {
			return map[string]any{"type": "object"}
		}
		return nested
	default:
		return map[string]any{"type": javaMcpScalarJSONType(f.Type)}
	}
}

// javaMcpDescribed attaches the field's own doc comment, which is the text the
// model reads when deciding what to pass.
func javaMcpDescribed(f *model.Field, schema map[string]any) map[string]any {
	if c := strings.Join(strings.Fields(f.Comment), " "); c != "" {
		schema["description"] = c
	}
	return schema
}

// javaMcpScalarJSONType maps a proto scalar to its JSON Schema type, following
// the canonical proto3 JSON mapping: 64-bit integers and bytes travel as
// strings.
func javaMcpScalarJSONType(protoType string) string {
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

// javaMcpFileParams is everything the emitter needs that generateServiceWrapper
// has already resolved.
type javaMcpFileParams struct {
	JavaPackage string // the wrapper's own package
	ProtoFile   string
	// FrameType is the resolved Java type of the carrier's frames
	// ("Mcp.McpRequest" / "Mcp.McpResponse"), which the wrapper already imports.
	FrameRequestType  string
	FrameResponseType string
	Imports           string
	// NonToolHandlerDecls are the handler-interface declarations of this
	// service's NON-tool rpcs, verbatim as the wrapper emitted them. The
	// generated test's stub implements the whole interface, and reusing the
	// wrapper's own declarations is what keeps the stub's signatures from
	// drifting out of a second renderer that would have to re-derive every
	// streaming and collection shape.
	NonToolHandlerDecls []string
}

// generateJavaMcpDispatch renders the whole MCP termination for one service: the
// tool table, the per-session surface, the per-tool dispatch and the carriage.
// Returns "" for a service with no MCP rpc.
func generateJavaMcpDispatch(svc *parser.ServiceInfo, tools []javaMcpTool, p javaMcpFileParams) string {
	cls := javaMcpClassName(svc.Name)

	var b strings.Builder
	b.WriteString(javaMcpFileHeader(svc.Name, p))
	b.WriteString(javaMcpToolTable(svc.Name, tools))
	b.WriteString(javaMcpSurface(svc.Name, tools))
	for _, t := range tools {
		b.WriteString(javaMcpToolDispatch(svc.Name, t))
	}
	b.WriteString("    }\n") // close Surface
	b.WriteString(javaMcpResultHelpers())
	b.WriteString(javaMcpTransport(svc.Name, p))
	b.WriteString("}\n")
	_ = cls
	return b.String()
}

func javaMcpFileHeader(serviceName string, p javaMcpFileParams) string {
	return header.Copyright(consts.LangJava) + fmt.Sprintf(`
// Generated by tacplus
//
// CODE GENERATED - DO NOT EDIT
// This file is automatically generated by the tacplus compiler.
// Manual modifications will be overwritten on next generation.
//
// IMPORTANT FOR AGENTS: Do NOT modify this generated code.
// If changes are needed, update the tacplus compiler at:
// github.com/tacplus-dev/tacplus
//
// Source: %[5]s

package %[4]s;

%[6]s

/**
* %[1]sMcp is the whole Model Context Protocol termination for %[1]s.
*
* <p>%[1]s declares at least one (spec.rpc) principal: RPC_PRINCIPAL_MCP rpc, so
* tacplus generates its tool set, its JSON-RPC dispatch, and the carriage over
* %[2]s here. The service implements only the typed handler
* methods on %[1]s.%[1]sHandler — there is no MCP glue for it to hand-write.
*
* <p>Every tool call is authorized by the carrier RPC's own gRPC authorization,
* once, at session open: an MCP session reaches exactly the tools the Caller the
* gateway verified is entitled to reach.
*
* <p>This file needs %[3]s on the classpath, and Java %[7]s or later.
*/
public final class %[1]sMcp {

    private %[1]sMcp() {}

    private static final McpJsonMapper JSON = McpJsonDefaults.getMapper();
`, serviceName, parser.McpCarrierRpcName(serviceName), javaMcpSdkArtifact,
		p.JavaPackage, p.ProtoFile, p.Imports, javaMcpSdkJavaVersion)
}

// javaMcpImports is the import block the dispatch file needs: the SDK, Reactor,
// the gRPC StreamObserver, and the wrapper's OWN proto-type import block
// verbatim.
//
// Reusing the wrapper's block rather than rebuilding one is deliberate: every
// proto type this file names — the tool request/response types, their Helpers
// validators, the carrier's frame types, ProtoException — is a type the wrapper
// next door already names, and a second derivation of the same import set is a
// second thing to drift. Java does not warn on an unused import, so the few the
// dispatch does not use cost nothing.
func javaMcpImports(wrapperTypesImport, serviceManagerImport string) string {
	return strings.Join([]string{
		"import io.grpc.stub.StreamObserver;",
		"",
		"import io.modelcontextprotocol.json.McpJsonDefaults;",
		"import io.modelcontextprotocol.json.McpJsonMapper;",
		"import io.modelcontextprotocol.server.McpServer;",
		"import io.modelcontextprotocol.server.McpServerFeatures;",
		"import io.modelcontextprotocol.server.McpSyncServer;",
		"import io.modelcontextprotocol.spec.McpSchema;",
		"import io.modelcontextprotocol.spec.McpServerSession;",
		"import io.modelcontextprotocol.spec.McpServerTransport;",
		"import io.modelcontextprotocol.spec.McpServerTransportProvider;",
		"",
		"import reactor.core.publisher.Mono;",
		"",
		wrapperTypesImport,
		"import " + serviceManagerImport + ".Caller;",
	}, "\n")
}

// javaMcpToolTable renders the published tool set.
//
// The schema is emitted here, from the same message index the egress sanitizer
// builds, rather than inferred by the SDK from a Java argument class: the tool's
// contract is the rpc's request message, and a second Java mirror of it would be
// one more thing to drift (decision doc §5). McpSchema.Tool.builder(name,
// jsonMapper, schemaJson) takes the generated JSON directly.
func javaMcpToolTable(serviceName string, tools []javaMcpTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
    /**
    * INSTRUCTIONS is served as the MCP server's instruction block. It states the
    * one rule a model cannot read off the tool schemas: the fields they omit are
    * omitted because the server computes them.
    */
    public static final String INSTRUCTIONS = %s;
`, javaQuote(javaMcpInstructions))

	for _, t := range tools {
		fmt.Fprintf(&b, "\n    private static final String SCHEMA_%s = %s;\n",
			strings.ToUpper(strutil.ToSnakeCase(t.Method.Name)), javaQuote(t.Schema))
	}

	fmt.Fprintf(&b, `
    /**
    * tools returns the tools %[1]s publishes, in declaration order.
    *
    * <p>Every schema below omits the fields the server computes from the verified
    * caller — (spec.principal), (spec.namespace) and their store_ forms — and the
    * injected trace_id/noop pair, and declares "additionalProperties": false.
    * Naming one anyway is refused by the dispatch, not silently ignored.
    */
    public static java.util.List<McpSchema.Tool> tools() {
        return java.util.List.of(
`, serviceName)
	for i, t := range tools {
		sep := ","
		if i == len(tools)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "            McpSchema.Tool.builder(%s, JSON, SCHEMA_%s)\n                .description(%s)\n                .build()%s\n",
			javaQuote(t.ToolName), strings.ToUpper(strutil.ToSnakeCase(t.Method.Name)), javaQuote(t.Description), sep)
	}
	b.WriteString("        );\n    }\n")
	return b.String()
}

// javaMcpSurface renders the per-session tool surface: the object every tool
// dispatch hangs off, bound to one verified Caller.
func javaMcpSurface(serviceName string, tools []javaMcpTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
    /**
    * Surface is one MCP session's tool surface, bound to one verified caller.
    *
    * <p>Built per session rather than shared, and holding the Caller as a FIELD
    * rather than reading it from the gRPC Context: the SDK dispatches a tool call
    * on its own scheduler thread, where the wrapper's Context-scoped
    * Auth.callerFromContext() is not in scope. Every server-computed predicate in
    * the dispatch below comes from this field.
    */
    public static final class Surface {
        private final %[1]s.%[1]sHandler handler;
        private final Caller caller;

        /** Builds the surface for a caller the carrier RPC's authorization has already verified. */
        public Surface(%[1]s.%[1]sHandler handler, Caller caller) {
            this.handler = java.util.Objects.requireNonNull(handler, "handler is required");
            this.caller = java.util.Objects.requireNonNull(caller, "an MCP session requires an authenticated caller");
        }

        /**
        * toolSpecifications binds each published tool to its generated dispatch.
        *
        * <p>The raw Tool + call-handler form (rather than a typed helper) is what
        * lets the schema stay the generated one and the arguments decode straight
        * into the rpc's own protobuf request: a typed helper would need a Java
        * mirror class per tool.
        */
        public java.util.List<McpServerFeatures.SyncToolSpecification> toolSpecifications() {
            java.util.List<McpSchema.Tool> published = tools();
            return java.util.List.of(
`, serviceName)
	for i := range tools {
		sep := ","
		if i == len(tools)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, `                McpServerFeatures.SyncToolSpecification.builder()
                    .tool(published.get(%d))
                    .callHandler((exchange, request) -> call(request.name(), request.arguments()))
                    .build()%s
`, i, sep)
	}
	b.WriteString(`            );
        }

        /**
        * call dispatches a tool call by name without an MCP session.
        *
        * <p>An in-process caller (a service acting as its own MCP client, e.g. an
        * agentic loop dispatching its own tool calls) has no session and must not
        * fabricate one just to reach the tool logic. This is the session-free
        * entry point: the tools/call handlers registered by toolSpecifications are
        * thin wrappers around it, so such a caller reuses the generated argument
        * decode, validation and caller-scoping instead of re-deriving it by hand.
        */
        public McpSchema.CallToolResult call(String name, java.util.Map<String, Object> arguments) {
            McpSchema.CallToolRequest request = new McpSchema.CallToolRequest(
                    name, arguments == null ? java.util.Map.of() : arguments);
            switch (name) {
`)
	for _, t := range tools {
		fmt.Fprintf(&b, `                case %s:
                    return %s(request);
`, strconv.Quote(t.ToolName), javaMcpDispatchMethodName(t))
	}
	b.WriteString(`                default:
                    return toolError("unknown tool \"" + name + "\"");
            }
        }

        /**
        * argumentNames is the set of argument names a tool call actually carried.
        * Dispatch consults it BEFORE decoding, so naming a server-computed field is
        * a refusal rather than a value the decoder would happily accept.
        */
        private static java.util.Set<String> argumentNames(McpSchema.CallToolRequest request) {
            java.util.Map<String, Object> args = request.arguments();
            return args == null ? java.util.Set.of() : args.keySet();
        }

        /**
        * decode turns a tool call's argument map into the rpc's own protobuf
        * request.
        *
        * <p>JsonFormat.parser() REJECTS an unknown field by default (the opposite
        * of ignoringUnknownFields()), which is the deny_unknown_fields the schema's
        * "additionalProperties": false asks for, enforced server-side rather than
        * only by a conforming client.
        */
        private static void decode(McpSchema.CallToolRequest request, com.google.protobuf.Message.Builder builder)
                throws java.io.IOException, com.google.protobuf.InvalidProtocolBufferException {
            java.util.Map<String, Object> args = request.arguments();
            if (args == null || args.isEmpty()) {
                return;
            }
            com.google.protobuf.util.JsonFormat.parser().merge(JSON.writeValueAsString(args), builder);
        }
`)
	return b.String()
}

func javaMcpDispatchMethodName(t javaMcpTool) string {
	return "call" + strutil.ToPascalCase(strutil.ToSnakeCase(t.Method.Name))
}

// javaMcpToolDispatch renders one tool's dispatch: refuse the server-computed
// arguments, decode, validate, stamp, call the service's own typed handler
// method, validate, filter, frame.
//
// The guards are the field markers the decision doc (§8) says an MCP tool reuses
// as-is: (spec.principal) and (spec.namespace) are computed from the verified
// Caller on the way in, (spec.store_principal) and (spec.store_namespace) hold
// the rows on the way out. An MCP tool call is scoped exactly like a gRPC call
// against the same Caller — which is the point of reusing the markers rather
// than inventing an MCP-specific owner predicate.
//
// Java's gRPC wrapper renders no request (spec.principal)/(spec.namespace) block
// today — the record plane next door renders the store_ halves only — so unlike
// Rust's dispatch this cannot drop in the wrapper's own emitted blocks. The
// stamps below are Java's first for the request markers. The namespace BACKSTOP
// is not re-invented: javaNamespaceCrash (recordnamespace_gen.go) is called
// verbatim, so there is exactly one halt renderer in the Java generator.
func javaMcpToolDispatch(serviceName string, t javaMcpTool) string {
	var b strings.Builder

	fmt.Fprintf(&b, `
        /** %[1]s dispatches the "%[2]s" tool to %[3]s.%[4]s. */
        McpSchema.CallToolResult %[1]s(McpSchema.CallToolRequest request) {
            try {
`, javaMcpDispatchMethodName(t), t.ToolName, serviceName, t.Method.Name)

	ind := "                "

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
		fmt.Fprintf(&b, `%[1]s// (%[2]s) %[3]s is computed from the verified caller, so it is absent
%[1]s// from this tool's schema; a call that names it is refused.
%[1]sif (argumentNames(request).contains(%[4]s)) {
%[1]s    return toolError("%[3]s is set by the server from %[5]s; it is not a tool argument");
%[1]s}
`, ind, f.marker, f.field, javaQuote(f.field), f.why)
	}

	if t.EmptyRequest {
		fmt.Fprintf(&b, `%[1]sif (!argumentNames(request).isEmpty()) {
%[1]s    return toolError("this tool takes no arguments");
%[1]s}
`, ind)
	} else {
		fmt.Fprintf(&b, "%[1]s%[2]s.Builder __b = %[2]s.newBuilder();\n%[1]sdecode(request, __b);\n%[1]s%[2]s __req = __b.build();\n",
			ind, t.InputType)
		if t.InputHelpers != "" {
			fmt.Fprintf(&b, "%[1]s%[2]s.validate%[3]s(__req);\n", ind, t.InputHelpers, t.Method.InputType)
		}
		if t.PrincipalField != "" {
			b.WriteString(javaMcpRequestScopeBlock(t, t.PrincipalField, "principal", "getSubject",
				"an authenticated caller", "the authenticated caller", ind))
		}
		if t.NamespaceField != "" {
			b.WriteString(javaMcpRequestScopeBlock(t, t.NamespaceField, "namespace", "getNamespace",
				"an authenticated caller carrying a namespace claim", "the authenticated caller's namespace", ind))
		}
		// (spec.owner_default): refuse the access the row's owner does not hold,
		// from the gRPC wrapper's own helper and in the wrapper's own order —
		// after the stamps, because the comparison needs the bound principal.
		b.WriteString(t.OwnerDefaultGuard)
	}

	// The handler call. Same signature the ordinary gRPC wrapper hands an
	// ordinary unary rpc.
	handlerArgs := "caller, __req"
	if t.EmptyRequest {
		handlerArgs = "caller"
	}
	if t.Page != nil {
		// PAGE: the same three-argument handler the gRPC PAGE wrapper calls, with
		// PageParams built from this tool call's own page_size/page_token exactly
		// as the wrapper builds it from the wire request. The envelope is
		// assembled here for the same reason: a PAGE handler returns a
		// CollectionRuntime.Page, not the response message.
		b.WriteString(javaMcpPageDispatch(t, ind))
		if t.OutputHelpers != "" {
			fmt.Fprintf(&b, "%[1]s%[2]s.validate%[3]s(__resp);\n", ind, t.OutputHelpers, t.Method.OutputType)
		}
		b.WriteString(javaMcpRowFilters(t, ind))
		fmt.Fprintf(&b, "%sreturn toolResult(__resp);\n", ind)
	} else if t.EmptyResponse {
		fmt.Fprintf(&b, "%[1]shandler.%[2]s(%[3]s);\n%[1]sreturn emptyToolResult();\n", ind, t.HandlerMethod, handlerArgs)
	} else {
		fmt.Fprintf(&b, "%[1]s%[2]s __resp = handler.%[3]s(%[4]s);\n", ind, t.OutputType, t.HandlerMethod, handlerArgs)
		if t.OutputHelpers != "" {
			fmt.Fprintf(&b, "%[1]s%[2]s.validate%[3]s(__resp);\n", ind, t.OutputHelpers, t.Method.OutputType)
		}
		b.WriteString(javaMcpRowFilters(t, ind))
		fmt.Fprintf(&b, "%sreturn toolResult(__resp);\n", ind)
	}

	fmt.Fprintf(&b, `            } catch (ProtoException e) {
                return toolError(e.getMessage());
            } catch (com.google.protobuf.InvalidProtocolBufferException e) {
                return toolError("invalid arguments: " + e.getMessage());
            } catch (java.io.IOException e) {
                return toolError("could not read the tool arguments: " + e.getMessage());
            }
        }
`)
	return b.String()
}

// javaMcpPageDispatch renders the PAGE window: decode the tool call's
// page_token, clamp its page_size, call the keyset handler, assemble the
// response envelope.
//
// Every line of it is the gRPC PAGE wrapper's own (servicewrapper_gen.go's PAGE
// branch), against the same collection runtime and the same resolved bounds —
// the tool and its gRPC twin are one handler driven two ways, not two paging
// implementations.
func javaMcpPageDispatch(t javaMcpTool, ind string) string {
	total := ""
	if t.Page.HasTotal {
		total = fmt.Sprintf(`%[1]s// total_count is reported only when the handler computes it (>= 0); a
%[1]s// negative total leaves the field at 0 = "unknown", not "empty".
%[1]sif (__page.total >= 0) {
%[1]s    __pb.setTotalCount(__page.total);
%[1]s}
`, ind)
	}
	return fmt.Sprintf(`%[1]s// Decode the opaque page_token into a keyset cursor (bad token -> INVALID_ARGUMENT).
%[1]sCollectionRuntime.Cursor __after = CollectionRuntime.decodePageToken(__req.getPageToken());
%[1]s// Resolve the effective page size: default when unset, clamped to the trust ceiling.
%[1]slong __limit = CollectionRuntime.effectivePageSize(__req.getPageSize(), %[2]dL, %[3]dL);
%[1]sCollectionRuntime.Page<%[4]s> __page = handler.%[5]s(caller, __req, new CollectionRuntime.PageParams(__limit, __after));
%[1]s%[6]s.Builder __pb = %[6]s.newBuilder();
%[1]s__pb.addAll%[7]s(__page.items);
%[1]s// An empty next_page_token in the result means there is no next page.
%[1]s__pb.setNextPageToken(CollectionRuntime.encodePageToken(__page.nextAfter));
%[8]s%[1]s%[6]s __resp = __pb.build();
`, ind, t.Page.DefaultSize, t.Page.MaxSize, t.Page.ItemType, t.HandlerMethod, t.OutputType, t.Page.ItemsField, total)
}

// javaMcpRequestScopeBlock renders the stamp-or-refuse check for one
// caller-scoped request field. Empty is stamped from the caller, equal passes,
// different is refused: a forged value is never silently overwritten, and a
// session with no verified identity cannot reach the handler at all.
func javaMcpRequestScopeBlock(t javaMcpTool, field, marker, accessor, needs, mismatch, ind string) string {
	local := "__" + marker
	pf := javaPascalField(field)
	return fmt.Sprintf(`%[1]s// (spec.%[2]s) %[3]s: bind the field to %[4]s.
%[1]sfinal String %[5]s = caller.%[6]s();
%[1]sif (%[5]s.isEmpty()) {
%[1]s    return toolError("%[7]s requires %[8]s");
%[1]s}
%[1]sif (__req.get%[9]s().isEmpty()) {
%[1]s    __req = __req.toBuilder().set%[9]s(%[5]s).build();
%[1]s} else if (!%[5]s.equals(__req.get%[9]s())) {
%[1]s    return toolError("%[3]s does not match %[4]s");
%[1]s}
`, ind, marker, field, mismatch, local, accessor, t.Method.Name, needs, pf)
}

// javaMcpRowFilters renders the egress row filters over the response: rows the
// caller does not own, or that belong to another tenant, never leave the
// process.
//
// The namespace form carries the same backstop the record plane renders —
// literally the same renderer (javaNamespaceCrash): a row stamped with another
// tenant's namespace means the store's own query is leaking, and the JVM is
// halted outright rather than allowed to keep serving.
func javaMcpRowFilters(t javaMcpTool, ind string) string {
	if len(t.StorePrincipalRows) == 0 && len(t.StoreNamespaceRows) == 0 {
		return ""
	}
	var b strings.Builder
	if len(t.StorePrincipalRows) > 0 {
		fmt.Fprintf(&b, `%[1]s// (spec.store_principal): hold the rows to the caller's principal. The
%[1]s// subject is the same value EventBus stamps into a row's owner field at
%[1]s// publish time. Fail closed: no verified subject, no rows.
%[1]sfinal String __rowPrincipal = caller.getSubject();
%[1]sif (__rowPrincipal.isEmpty()) {
%[1]s    return toolError("%[2]s returns rows owned by a principal and the caller presented no verified subject");
%[1]s}
`, ind, t.Method.Name)
	}
	if len(t.StoreNamespaceRows) > 0 {
		fmt.Fprintf(&b, `%[1]s// (spec.store_namespace): the tenant boundary is the caller's namespace
%[1]s// claim, compared for EXACT equality. Fail closed: no verified namespace,
%[1]s// no rows.
%[1]sfinal String __rowNamespace = caller.getNamespace();
%[1]sif (__rowNamespace.isEmpty()) {
%[1]s    return toolError("%[2]s returns rows owned by a namespace and the caller presented no verified namespace claim");
%[1]s}
`, ind, t.Method.Name)
	}
	fmt.Fprintf(&b, "%[1]s%[2]s.Builder __fb = __resp.toBuilder();\n", ind, t.OutputType)
	for _, f := range t.StorePrincipalRows {
		p := javaPascalField(f.Field)
		fmt.Fprintf(&b, `%[1]sfor (int __pi = __fb.get%[2]sCount() - 1; __pi >= 0; __pi--) {
%[1]s    if (!__rowPrincipal.equals(__fb.get%[2]s(__pi).get%[3]s())) { __fb.remove%[2]s(__pi); }
%[1]s}
`, ind, p, javaPascalField(f.ItemField))
	}
	for _, f := range t.StoreNamespaceRows {
		p := javaPascalField(f.Field)
		fmt.Fprintf(&b, `%[1]sfor (int __ni = __fb.get%[2]sCount() - 1; __ni >= 0; __ni--) {
%[1]s    if (!__rowNamespace.equals(__fb.get%[2]s(__ni).get%[3]s())) {
%[1]s        __fb.remove%[2]s(__ni);
%[4]s%[1]s    }
%[1]s}
`, ind, p, javaPascalField(f.ItemField), javaNamespaceCrash(t.Method.Name, "rows in "+f.Field, ind+"        "))
	}
	fmt.Fprintf(&b, "%s__resp = __fb.build();\n", ind)
	return b.String()
}

// javaMcpResultHelpers renders the framing every tool shares.
//
// A tool failure is a tool RESULT with isError set, not a JSON-RPC protocol
// error. That is the SDK's own documented convention — the model has to be able
// to SEE that a call failed in order to self-correct — and it means a refused
// argument or a permission denial is legible to the model rather than opaque.
func javaMcpResultHelpers() string {
	return fmt.Sprintf(`
    /**
    * toolError frames a failed tool call as a readable tool result. Only an
    * unroutable call (an unknown tool name) is a protocol error; the SDK answers
    * that one itself.
    */
    static McpSchema.CallToolResult toolError(String message) {
        return McpSchema.CallToolResult.builder()
        .content(java.util.List.of(McpSchema.TextContent.builder(message == null ? "" : message).build()))
        .isError(true)
        .build();
    }

    /** emptyToolResult is the result of a tool whose rpc returns google.protobuf.Empty. */
    static McpSchema.CallToolResult emptyToolResult() {
        return McpSchema.CallToolResult.builder()
        .content(java.util.List.of(McpSchema.TextContent.builder("{}").build()))
        .build();
    }

    /**
    * toolResult frames a typed response.
    *
    * <p>preservingProtoFieldNames keeps the result's field names the same
    * snake_case ones the tool's input schema publishes, so the model reads back
    * what it wrote.
    */
    static McpSchema.CallToolResult toolResult(com.google.protobuf.Message response) {
        String json;
        try {
            json = com.google.protobuf.util.JsonFormat.printer().preservingProtoFieldNames().print(response);
        } catch (com.google.protobuf.InvalidProtocolBufferException e) {
            return toolError("could not encode the result: " + e.getMessage());
        }
        // The frame bound is on the transport, but a result that cannot be framed
        // fails THIS call rather than the session: %[1]d bytes is what the generated
        // response validation on mcp.McpResponse.payload enforces, and failing that
        // validation fails the whole stream.
        if (json.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %[1]d) {
            return toolError("the result is over this transport's %[1]d-byte frame limit; ask for less at a time");
        }
        Object structured;
        try {
            structured = JSON.readValue(json, java.util.Map.class);
        } catch (java.io.IOException e) {
            return toolError("could not encode the result: " + e.getMessage());
        }
        return McpSchema.CallToolResult.builder()
        .content(java.util.List.of(McpSchema.TextContent.builder(json).build()))
        .structuredContent(structured)
        .build();
    }
`, proto.McpResponseFrameMaxBytes)
}

// javaMcpTransport renders the carriage: one MCP session over one gRPC bidi
// StreamObserver pair, as an McpServerTransportProvider.
//
// The SDK's provider contract is "created first, then handed a session factory
// when the server is built" — so the provider here is per-RPC, not per-process:
// one carrier RPC is one session, and the provider exists only long enough for
// McpServer.sync(...).build() to hand it the factory.
func javaMcpTransport(serviceName string, p javaMcpFileParams) string {
	return fmt.Sprintf(`
    /**
    * MAX_FRAME_BYTES is the largest outbound frame this transport will put on
    * the wire.
    *
    * <p>Generated from (spec.assert_max_size) on mcp.McpResponse.payload, which is
    * what the generated response validation enforces. Past that value validation
    * fails the whole STREAM, killing the session over one large tool result.
    */
    public static final int MAX_FRAME_BYTES = %[2]d;

    /**
    * serve serves one MCP session over one gRPC bidi stream.
    *
    * <p>This is what the generated %[3]s wrapper method delegates
    * to. The service never sees it.
    */
    public static StreamObserver<%[4]s> serve(
            %[1]s.%[1]sHandler handler, Caller caller, StreamObserver<%[5]s> responseObserver) {
        Surface surface = new Surface(handler, caller);
        SessionTransport transport = new SessionTransport(responseObserver);
        TransportProvider provider = new TransportProvider(transport);
        McpSyncServer server = McpServer.sync(provider)
        .serverInfo(%[6]s, %[7]s)
        .instructions(INSTRUCTIONS)
        .capabilities(McpSchema.ServerCapabilities.builder().tools(false).build())
        .tools(surface.toolSpecifications())
        .build();
        return new SessionObserver(provider.session(), server, transport, responseObserver);
    }

    /**
    * TransportProvider hands the SDK the one connection this RPC carries. A
    * provider is handed its session factory exactly once, when the server is
    * built, so the second call is a bug rather than a reconnect.
    */
    private static final class TransportProvider implements McpServerTransportProvider {
        private final SessionTransport transport;
        private volatile McpServerSession session;

        TransportProvider(SessionTransport transport) {
            this.transport = transport;
        }

        McpServerSession session() {
            if (this.session == null) {
                throw new IllegalStateException("the MCP server was built without opening a session");
            }
            return this.session;
        }

        @Override
        public void setSessionFactory(McpServerSession.Factory sessionFactory) {
            if (this.session != null) {
                throw new IllegalStateException("this MCP session's transport is already connected");
            }
            this.session = sessionFactory.create(this.transport);
        }

        @Override
        public Mono<Void> notifyClients(String method, Object params) {
            McpServerSession open = this.session;
            if (open == null) {
                return Mono.error(new IllegalStateException("no session to notify"));
            }
            return open.sendNotification(method, params);
        }

        @Override
        public Mono<Void> closeGracefully() {
            McpServerSession open = this.session;
            return open == null ? Mono.empty() : open.closeGracefully();
        }
    }

    /**
    * SessionTransport is this session's outbound half: one complete JSON-RPC
    * message per gRPC stream message, never fragmented.
    */
    private static final class SessionTransport implements McpServerTransport {
        private final StreamObserver<%[5]s> responseObserver;
        private final Object sendLock = new Object();
        private volatile boolean closed;

        SessionTransport(StreamObserver<%[5]s> responseObserver) {
            this.responseObserver = responseObserver;
        }

        @Override
        public Mono<Void> sendMessage(McpSchema.JSONRPCMessage message) {
            return Mono.fromRunnable(() -> {
                try {
                    sendRaw(JSON.writeValueAsString(message));
                } catch (java.io.IOException e) {
                    throw new IllegalStateException("could not encode an MCP frame", e);
                }
            });
        }

        /**
        * sendRaw puts one already-encoded JSON-RPC frame on the wire.
        *
        * <p>Separate from sendMessage because the parse-error frame cannot go
        * through it: JSON-RPC answers an undecodable frame with "id": null, and
        * McpSchema.JSONRPCResponse refuses to be constructed with a null id.
        */
        void sendRaw(String payload) {
            if (payload.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > MAX_FRAME_BYTES) {
                throw new IllegalStateException(
                "an MCP frame is over the " + MAX_FRAME_BYTES + "-byte limit on mcp.McpResponse.payload");
            }
            // A gRPC StreamObserver tolerates exactly one writer: the SDK may
            // answer a request and push a notification concurrently.
            synchronized (this.sendLock) {
                if (this.closed) {
                    return;
                }
                this.responseObserver.onNext(%[5]s.newBuilder().setPayload(payload).build());
            }
        }

        @Override
        public <T> T unmarshalFrom(Object data, io.modelcontextprotocol.json.TypeRef<T> typeRef) {
            return JSON.convertValue(data, typeRef);
        }

        @Override
        public Mono<Void> closeGracefully() {
            return Mono.fromRunnable(() -> this.closed = true);
        }

        void markClosed() {
            synchronized (this.sendLock) {
                this.closed = true;
            }
        }
    }

    /**
    * SessionObserver is this session's inbound half: every frame the client
    * sends is decoded and handed to the SDK session.
    *
    * <p>An undecodable frame is answered with a JSON-RPC parse error and the
    * session SURVIVES it — one unreadable frame does not say the next one is
    * unreadable too, and JSON-RPC has an error frame for exactly this.
    */
    private static final class SessionObserver implements StreamObserver<%[4]s> {
        private final McpServerSession session;
        private final McpSyncServer server;
        private final SessionTransport transport;
        private final StreamObserver<%[5]s> responseObserver;

        SessionObserver(McpServerSession session, McpSyncServer server, SessionTransport transport,
                StreamObserver<%[5]s> responseObserver) {
            this.session = session;
            this.server = server;
            this.transport = transport;
            this.responseObserver = responseObserver;
        }

        @Override
        public void onNext(%[4]s frame) {
            McpSchema.JSONRPCMessage message;
            try {
                message = McpSchema.deserializeJsonRpcMessage(JSON, frame.getPayload());
            } catch (java.io.IOException | RuntimeException e) {
                // Not fatal: one unreadable frame does not say the next one is
                // unreadable too. The error names no request, so it carries no id.
                String detail;
                try {
                    detail = JSON.writeValueAsString("could not decode the frame: " + e.getMessage());
                } catch (java.io.IOException encodeError) {
                    detail = "\"could not decode the frame\"";
                }
                this.transport.sendRaw("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":"
                + McpSchema.ErrorCodes.PARSE_ERROR + ",\"message\":" + detail + "}}");
                return;
            }
            this.session.handle(message).block();
        }

        @Override
        public void onError(Throwable t) {
            this.transport.markClosed();
            this.server.closeGracefully();
        }

        @Override
        public void onCompleted() {
            this.server.closeGracefully();
            this.transport.markClosed();
            this.responseObserver.onCompleted();
        }
    }
`, serviceName, proto.McpResponseFrameMaxBytes, parser.McpCarrierRpcName(serviceName),
		p.FrameRequestType, p.FrameResponseType, javaQuote(serviceName), javaQuote(javaMcpServerVersion))
}

// javaQuote renders a Java string literal. Java's escapes are a subset of JSON's
// for the characters that appear here (the generated schemas are JSON text, tool
// names and doc comments), so strconv.Quote's Go escaping is compatible — except
// that Go escapes non-ASCII as \u{...}-free \uXXXX only above the BMP, where
// Java needs surrogate pairs. AsciiQuote-ing everything sidesteps that.
func javaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r > 0x7e {
				// Java string literals take UTF-16 code units, so anything
				// outside the BMP is written as its surrogate pair.
				for _, u := range utf16Units(r) {
					fmt.Fprintf(&b, `\u%04x`, u)
				}
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// utf16Units returns r as one or two UTF-16 code units.
func utf16Units(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{uint16(0xd800 + (r >> 10)), uint16(0xdc00 + (r & 0x3ff))}
}

// javaIsMcpCarrier reports whether method is svc's generated MCP session
// carrier.
func javaIsMcpCarrier(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	c := javaMcpCarrierMethod(svc)
	return c != nil && c.Name == method.Name
}

// writeJavaMcpDispatchFile emits <Service>Mcp.java beside the service wrapper,
// for a service that declares at least one MCP tool. A service that declares
// none gets no file at all.
func writeJavaMcpDispatchFile(svc *parser.ServiceInfo, tools []javaMcpTool, outputDir string, p javaMcpFileParams) error {
	if javaMcpCarrierMethod(svc) == nil {
		return nil
	}
	src := generateJavaMcpDispatch(svc, tools, p)
	outputPath := filepath.Join(outputDir, javaMcpClassName(svc.Name)+".java")
	if err := os.WriteFile(outputPath, []byte(src), 0644); err != nil {
		return fmt.Errorf("failed to write MCP dispatch for %s: %w", svc.Name, err)
	}
	return nil
}
