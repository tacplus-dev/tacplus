// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/imports"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// The MCP dispatch seam (BIG GOAL 8 Phase 0, Go).
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
//   - a Go MCP SDK server that answers tools/list and tools/call;
//   - per-tool dispatch that decodes the arguments into the rpc's own typed
//     request, stamps (spec.principal) / (spec.namespace) from the verified
//     Caller, calls the service's typed handler method, filters
//     (spec.store_principal) / (spec.store_namespace) rows out of the response,
//     and frames the typed result back;
//   - the carriage itself: an mcp.Transport over the gRPC bidi stream.
//
// Nothing here is emitted for a service with no MCP rpc. This is Go's half of
// what 0e6d87c6 generated for Rust; the two share the field-marker doctrine and
// nothing else, because rmcp and github.com/modelcontextprotocol/go-sdk are
// unrelated API surfaces.

const (
	// goMcpSdkModule is the MCP SDK the generated dispatch is written against.
	// Generated Go lands in the CONSUMER's own module — tacplus writes no go.mod
	// on the Go path — so this is the import the consumer's `go mod tidy`
	// resolves, and the version pin lives in the dependency inventory
	// (packages.KnownPackages / dependencies.yaml) rather than in a manifest
	// this generator writes.
	goMcpSdkModule = "github.com/modelcontextprotocol/go-sdk"

	// goMcpSdkGoVersion is the minimum `go` directive the SDK itself declares.
	// A consumer module below it cannot build the generated dispatch, so the
	// generated file says so in its own header rather than failing obscurely in
	// the module loader.
	goMcpSdkGoVersion = "1.25.0"
)

// goMcpInstructions is the server-level instruction block every generated MCP
// surface serves. It states the one rule a model cannot discover from the tool
// schemas alone: the fields the schemas omit are omitted because the server
// computes them, and naming one is an error rather than a way to widen reach.
const goMcpInstructions = "Every tool here runs as you: the server sets any caller-scoped field " +
	"from your verified identity, and those fields are not tool arguments. Naming one is an " +
	"error, not a way to widen what you can reach."

// goMcpTool is one RPC_PRINCIPAL_MCP rpc, resolved for emission.
type goMcpTool struct {
	Method      *parser.MethodInfo
	ToolName    string // the published tool name (registry.json's mcp_tool_name)
	Description string // the rpc's own leading doc comment
	Schema      string // the tool's input JSON Schema, marshalled
	InputType   string // rendered Go request type ("types.X" / "<pkg>_pb.X")
	OutputType  string // rendered Go response type
	// EmptyRequest / EmptyResponse mirror the handler-interface renderer's own
	// tests, so dispatch calls the handler with the signature it declares.
	EmptyRequest  bool
	EmptyResponse bool
	// ValidateInput / ValidateOutput are the generated validators the ordinary
	// gRPC wrapper calls, or "" when the type has none.
	ValidateInput  string
	ValidateOutput string
	// RequiredScalars are the request's required scalar fields, with a synthetic
	// value each, used to drive the generated dispatch test.
	RequiredScalars []goMcpScalarArg

	// PrincipalField is the request's (spec.principal) field, "" when it
	// declares none. It is absent from Schema and refused as an argument.
	PrincipalField string
	// StorePrincipalRows are the response's repeated (spec.store_principal)
	// record fields, filtered to the caller before the result is returned.
	StorePrincipalRows []goMcpRowField
	// NamespaceField / StoreNamespaceRows are the same two seams for the tenant
	// boundary.
	NamespaceField     string
	StoreNamespaceRows []goMcpRowField

	// OwnerDefaultGuard is the (spec.owner_default) refusal rendered by the same
	// helper the gRPC wrapper calls, "" when nothing is declared or nothing is
	// contradicted. It rides the request-validation seam, immediately after the
	// (spec.principal)/(spec.namespace) stamps, for the reason stated there:
	// that is the only point at which both halves of the comparison exist.
	OwnerDefaultGuard string

	// Page is the PAGE-collection window, nil for every other rpc. A PAGE tool
	// takes page_size/page_token as ordinary arguments and calls the same
	// three-argument handler the gRPC PAGE wrapper calls.
	Page *goMcpPage
}

// goMcpPage is what the dispatch needs to drive a PAGE rpc's handler and
// assemble its response envelope. Every value here is read off the same
// collection.Resolved the gRPC PAGE wrapper reads — there is no MCP-specific
// page bound.
type goMcpPage struct {
	ItemsField  string // Pascal-case response field holding the page's items
	ItemType    string // rendered Go item type (the Page[*T] element)
	DefaultSize uint32 // page_size when the call omits it
	MaxSize     uint32 // the trust-tier ceiling page_size is clamped to
	HasTotal    bool   // the response declares total_count
}

// goMcpRowField is a repeated record field of a response whose item type
// declares (spec.store_principal) or (spec.store_namespace).
type goMcpRowField struct {
	Field     string // snake_case response field holding the rows
	ItemField string // snake_case caller-scoped field on the row type
}

// goMcpScalarArg is one required scalar field of a tool's request, with a
// synthetic value the generated dispatch test passes for it.
type goMcpScalarArg struct {
	Field string
	// JSON is the value as it appears in the tool call's arguments object.
	JSON string
	// Match is the substring the generated test looks for in the handler's
	// report of the request it was called with.
	Match string
}

// goMcpCarrierMethod returns the service's generated session-carrier rpc, or nil
// when the service declares no MCP tool. The carrier is identified the same way
// the registry identifies it.
func goMcpCarrierMethod(svc *parser.ServiceInfo) *parser.MethodInfo {
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

// goMcpTools resolves every MCP tool the service publishes, in declaration order
// (the order the tool list is served in).
func goMcpTools(svc *parser.ServiceInfo, servicePackage string, san *egressSanitizer) ([]goMcpTool, error) {
	var tools []goMcpTool
	for _, m := range svc.Methods {
		if !m.IsMcpTool() {
			continue
		}
		if err := goMcpToolGeneratable(svc, m, servicePackage, san); err != nil {
			return nil, err
		}
		schema, err := goMcpInputSchema(m, servicePackage, san)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		ownerDefaultGuard, err := goOwnerDefaultGuard(m, san, servicePackage, "s.caller", goMcpOwnerDefaultDeny(svc.Name))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", svc.Name, m.Name, err)
		}
		empty := goMcpEmptyRequest(m)
		var scalars []goMcpScalarArg
		if !empty {
			scalars = san.mcpRequiredScalarArgs(m.FullInputType, servicePackage)
		}
		tools = append(tools, goMcpTool{
			Method:             m,
			ToolName:           m.McpToolName,
			Description:        parser.McpToolDescription(m.Comment),
			Schema:             schema,
			InputType:          goTypeRef(m.FullInputType, m.InputType, servicePackage),
			OutputType:         goTypeRef(m.FullOutputType, m.OutputType, servicePackage),
			EmptyRequest:       empty,
			EmptyResponse:      isEmptyType(m.FullOutputType),
			ValidateInput:      goValidateRef(m.FullInputType, m.InputType, servicePackage),
			ValidateOutput:     goValidateRef(m.FullOutputType, m.OutputType, servicePackage),
			RequiredScalars:    scalars,
			PrincipalField:     san.annotatedStringField(m.FullInputType, servicePackage, "principal"),
			StorePrincipalRows: san.mcpRowFields(m.FullOutputType, servicePackage, "store_principal"),
			NamespaceField:     san.annotatedStringField(m.FullInputType, servicePackage, "namespace"),
			StoreNamespaceRows: san.mcpRowFields(m.FullOutputType, servicePackage, "store_namespace"),
			OwnerDefaultGuard:  ownerDefaultGuard,
			Page:               goMcpPageOf(m, servicePackage),
		})
	}
	return tools, nil
}

// goMcpToolGeneratable refuses to emit dispatch for a tool whose guards this
// seam cannot render.
//
// Before this seam existed an MCP tool was suppressed from the Go surface
// entirely (a9a79e74), so the service wrapper's own refusals were skipped for it
// — nothing served the rpc, so there was nothing to under-enforce. Dispatch
// changes that: the tool IS served now, by this code, so the same refusals the
// wrapper makes for a gRPC rpc apply here, for the same reasons.
func goMcpToolGeneratable(svc *parser.ServiceInfo, m *parser.MethodInfo, servicePackage string, san *egressSanitizer) error {
	// An MCP tool is one question and one answer. A streaming or
	// collection-returning rpc has a handler signature this dispatch cannot
	// call (it takes a grpc stream, an iter.Seq2 or a collection.Page), and MCP
	// has no frame to carry it in.
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
	// wrapper next door guards it (mingroupsize_gen.go). Java's MCP dispatch
	// refuses the same shape for the same reason.
	if m.MinGroupSize > 0 {
		return fmt.Errorf("%s.%s is an MCP tool carrying (spec.min_group_size) = %d, but the generated MCP dispatch renders no size-only guard; publish it as a gRPC rpc or drop the threshold",
			svc.Name, m.Name, m.MinGroupSize)
	}
	// (spec.record).mutability = MUTABILITY_APPEND_ONLY: no language renders a
	// guard for a row mutation over a write-once record.
	if t := goAppendOnlyRecordOnMethod(m, san, servicePackage); t != "" {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool: it carries %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; an append-only record's rows are written once",
			svc.Name, m.Name, t)
	}
	// (spec.store_epoch): the stamp is rendered by the gRPC wrapper's write
	// path, not by this seam, so a tool over an epoch-bearing write would leave
	// the store epoch unstamped. Java's MCP dispatch refuses the same shape for
	// the same reason.
	if f := san.storeEpochField(m.FullInputType, servicePackage); f != "" {
		return fmt.Errorf("%s.%s is an MCP tool taking %s, whose field %q carries (spec.store_epoch), but the stamp is rendered by the gRPC wrapper's write path and not by MCP dispatch; a tool call of this write would leave the store epoch unstamped",
			svc.Name, m.Name, m.FullInputType, f)
	}
	// (spec.owner_default): the same decision the gRPC wrapper makes, from the
	// same helper — an MCP rpc IS verb-classified (ClassifyMethods runs over
	// every method of every service with no IsMcp gate), so a tool has the
	// read/write access the owner rights are compared against exactly as its
	// gRPC twin does. goMcpToolDispatch renders the guard next to the
	// (spec.principal) stamp, so what is refused here is only what that guard
	// cannot be rendered against: a request shape with no validation seam at
	// which the caller can be compared to the row's principal. That is the same
	// set generateServiceWrapper refuses, less the streaming and non-PAGE
	// collection shapes this seam has already refused above.
	guard, err := goOwnerDefaultGuard(m, san, servicePackage, "s.caller", goMcpOwnerDefaultDeny(svc.Name))
	if err != nil {
		return fmt.Errorf("%s.%s must not be generated as an MCP tool: %w", svc.Name, m.Name, err)
	}
	if guard != "" && (goMcpEmptyRequest(m) || m.IsPageCollection()) {
		return fmt.Errorf("%s.%s is an MCP tool touching a type whose (spec.owner_default) contradicts its access, but its request shape has no validation seam at which the caller can be compared to the row's principal",
			svc.Name, m.Name)
	}
	return nil
}

// goMcpOwnerDefaultDeny is this seam's refusal statement, which the shared
// owner_default guard renders its message into. An MCP dispatch reports a denial
// as a tool error rather than as a transport error, the way every other refusal
// in this file does.
func goMcpOwnerDefaultDeny(serviceName string) string {
	return "\t\treturn " + goMcpPrefix(serviceName) + "ToolError(status.Error(codes.PermissionDenied, %s)), nil\n"
}

// goMcpPageOf resolves the PAGE window of a collection rpc, or nil when the rpc
// is not a PAGE read. The bounds come from collection.Resolved — the same
// values the gRPC PAGE wrapper renders — so a tool and its gRPC twin clamp
// page_size identically.
func goMcpPageOf(m *parser.MethodInfo, servicePackage string) *goMcpPage {
	if !m.IsPageCollection() {
		return nil
	}
	return &goMcpPage{
		ItemsField:  strutil.ToPascalCase(m.Resolved.PageItemsField),
		ItemType:    goWatchType(m.Resolved.PageItemType, servicePackage),
		DefaultSize: m.Resolved.DefaultPageSize,
		MaxSize:     m.Resolved.MaxPageSize,
		HasTotal:    m.Resolved.PageHasTotal,
	}
}

// goMcpEmptyRequest mirrors the handlerEmpty test the handler-interface renderer
// uses, so dispatch calls the handler with the parameter list it declares.
func goMcpEmptyRequest(m *parser.MethodInfo) bool {
	return isEmptyType(m.FullInputType) || isTraceRequestType(m.FullInputType)
}

// ── schema derivation ───────────────────────────────────────────────────────

// goMcpInputSchema builds the tool's argument schema from the rpc's request
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
func goMcpInputSchema(m *parser.MethodInfo, servicePackage string, san *egressSanitizer) (string, error) {
	var schema map[string]any
	if goMcpEmptyRequest(m) {
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
		schema, err = san.mcpMessageSchema(m.FullInputType, servicePackage, map[string]bool{})
		if err != nil {
			return "", err
		}
	}
	if page := goMcpPageOf(m, servicePackage); page != nil {
		goMcpAnnotatePageSchema(schema, page)
	}
	schema["description"] = parser.McpToolDescription(m.Comment)
	out, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("marshal input schema: %w", err)
	}
	return string(out), nil
}

// goMcpAnnotatePageSchema tells the model how to drive the two paging fields a
// PAGE request already declares. They are ordinary fields of the request
// message, so they are already in the schema; what the message index cannot say
// is the window's default and its ceiling, which are the wrapper's own resolved
// bounds. The bound is stated, not invented: it is the same clamp the dispatch
// applies at runtime, so a model that asks for more is told what it will get
// rather than silently given less.
func goMcpAnnotatePageSchema(schema map[string]any, page *goMcpPage) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	if ps, ok := props["page_size"].(map[string]any); ok {
		ps["description"] = fmt.Sprintf("How many items to return in this page. Optional: %d when omitted, and never more than %d.",
			page.DefaultSize, page.MaxSize)
		ps["maximum"] = page.MaxSize
	}
	if pt, ok := props["page_token"].(map[string]any); ok {
		pt["description"] = "The next_page_token returned by the previous call to this tool. Optional: omit it for the first page. An empty next_page_token in the result means there is no next page."
	}
}

// annotatedStringField returns the snake_case name of fullType's field carrying
// the named annotation, or "" when it declares none. At most one such field per
// message is enforced by the parser's principal/namespace validators.
func (es *egressSanitizer) annotatedStringField(fullType, servicePackage, annotation string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
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

// mcpRowFields returns the repeated record fields of fullType whose item type
// declares the named caller-scoping annotation — the rows dispatch must filter
// to the caller before the result leaves the process.
func (es *egressSanitizer) mcpRowFields(fullType, servicePackage, annotation string) []goMcpRowField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []goMcpRowField
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
				out = append(out, goMcpRowField{Field: f.Name, ItemField: item.Fields[j].Name})
				break
			}
		}
	}
	return out
}

// mcpRequiredScalarArgs returns the request's required scalar fields. A request
// whose required set is not entirely scalar returns nil: the generated dispatch
// test synthesizes arguments, and there is no one obviously-valid value for a
// required enum or nested message.
func (es *egressSanitizer) mcpRequiredScalarArgs(fullType, servicePackage string) []goMcpScalarArg {
	if es == nil {
		return nil
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []goMcpScalarArg
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
			out = append(out, goMcpScalarArg{Field: f.Name, JSON: strconv.Quote(v), Match: v})
		case "integer":
			out = append(out, goMcpScalarArg{Field: f.Name, JSON: "1", Match: "1"})
		case "number":
			out = append(out, goMcpScalarArg{Field: f.Name, JSON: "1", Match: "1"})
		case "boolean":
			out = append(out, goMcpScalarArg{Field: f.Name, JSON: "true", Match: "true"})
		default:
			return nil
		}
	}
	return out
}

// mcpMessageSchema renders one message as a JSON Schema object. visiting breaks
// recursion on a message that (directly or otherwise) contains itself; such a
// field is rendered as an unconstrained object rather than expanded forever.
func (es *egressSanitizer) mcpMessageSchema(fullType, servicePackage string, visiting map[string]bool) (map[string]any, error) {
	if es == nil {
		return nil, fmt.Errorf("no message index available to derive an MCP tool schema")
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
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
		if goMcpServerComputed(f) {
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

// goMcpServerComputed reports whether a field's value is the server's to compute
// from the verified Caller rather than the model's to supply.
func goMcpServerComputed(f *model.Field) bool {
	for _, a := range []string{"principal", "store_principal", "namespace", "store_namespace"} {
		if _, ok := f.Annotations[a]; ok {
			return true
		}
	}
	return false
}

func (es *egressSanitizer) mcpFieldSchema(f *model.Field, msgPkg, servicePackage string, visiting map[string]bool) map[string]any {
	if f.IsMap {
		return mcpDescribed(f, map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": mcpScalarJSONType(f.MapValueType)},
		})
	}
	item := es.mcpSingularSchema(f, msgPkg, servicePackage, visiting)
	if f.IsRepeated {
		return mcpDescribed(f, map[string]any{"type": "array", "items": item})
	}
	return mcpDescribed(f, item)
}

func (es *egressSanitizer) mcpSingularSchema(f *model.Field, msgPkg, servicePackage string, visiting map[string]bool) map[string]any {
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
		return map[string]any{"type": mcpScalarJSONType(f.Type)}
	}
}

// mcpDescribed attaches the field's own doc comment, which is the text the model
// reads when deciding what to pass.
func mcpDescribed(f *model.Field, schema map[string]any) map[string]any {
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

// goMcpPrefix is the unexported identifier prefix every per-service MCP symbol
// carries, so two MCP services sharing a wrapper package do not collide.
func goMcpPrefix(serviceName string) string {
	if serviceName == "" {
		return serviceName
	}
	return strings.ToLower(serviceName[:1]) + serviceName[1:] + "Mcp"
}

// goMcpFileName is the file the dispatch lands in, next to the service wrapper.
func goMcpFileName(serviceName string) string {
	return strutil.ToSnakeCase(serviceName) + "_mcp.go"
}

// goMcpFileParams is everything the emitter needs that generateServiceWrapper
// has already resolved: the import paths the wrapper file itself renders.
type goMcpFileParams struct {
	PackageName              string
	TypesModulePath          string
	ServiceManagerImportPath string
	ModuleBasePath           string
	CrossPackageTypes        map[string]string
	ServicePackage           string
	// used is the set of proto packages this file references, so the import
	// block carries exactly those and no more.
	used map[string]bool
}

// generateGoMcpDispatch renders the whole MCP termination for one service: the
// tool table, the per-session surface, the per-tool dispatch and the carriage.
// Returns "" for a service with no MCP rpc.
func generateGoMcpDispatch(svc *parser.ServiceInfo, p goMcpFileParams, san *egressSanitizer) (string, error) {
	carrier := goMcpCarrierMethod(svc)
	if carrier == nil {
		return "", nil
	}
	tools, err := goMcpTools(svc, p.ServicePackage, san)
	if err != nil {
		return "", err
	}
	if len(tools) == 0 {
		// HasMcpRpcs is what produced the carrier, so this cannot happen; if it
		// ever does, a carrier with no tool set is a generation bug, not a
		// service that serves nothing.
		return "", fmt.Errorf("%s has a generated MCP session carrier but no RPC_PRINCIPAL_MCP rpc", svc.Name)
	}

	frameIn := goTypeRef(carrier.FullInputType, carrier.InputType, p.ServicePackage)
	frameOut := goTypeRef(carrier.FullOutputType, carrier.OutputType, p.ServicePackage)

	var b strings.Builder
	b.WriteString(goMcpFileHeader(svc.Name, p, goMcpHasPageTool(tools)))
	b.WriteString(goMcpToolTable(svc.Name, tools))
	b.WriteString(goMcpSurface(svc.Name, tools))
	for _, t := range tools {
		b.WriteString(goMcpToolDispatch(svc.Name, t))
	}
	b.WriteString(goMcpResultHelpers(svc.Name))
	b.WriteString(goMcpTransport(svc.Name, frameIn, frameOut, proto.McpResponseFrameMaxBytes))
	return b.String(), nil
}

// goMcpUsedPackages is the set of proto packages the MCP file references — the
// tool request/response types plus the carrier's frame types. Computed here
// rather than reused from the wrapper's own set so an unrelated package cannot
// ride in on an import goimports would then have to prune.
func goMcpUsedPackages(svc *parser.ServiceInfo, servicePackage string) map[string]bool {
	used := map[string]bool{}
	for _, m := range svc.Methods {
		if !m.IsMcpTool() && m.Name != parser.McpCarrierRpcName(svc.Name) {
			continue
		}
		for _, fullType := range []string{m.FullInputType, m.FullOutputType} {
			if i := strings.LastIndex(fullType, "."); i >= 0 {
				if pkg := fullType[:i]; pkg != servicePackage {
					used[pkg] = true
				}
			}
		}
	}
	return used
}

// goMcpHasPageTool reports whether any tool is a PAGE read, which is what pulls
// the emitted collection runtime into the dispatch file's imports. goimports
// cannot resolve that local package by name, so the import is explicit — the
// same reason the service wrapper imports it explicitly.
func goMcpHasPageTool(tools []goMcpTool) bool {
	for _, t := range tools {
		if t.Page != nil {
			return true
		}
	}
	return false
}

func goMcpFileHeader(serviceName string, p goMcpFileParams, hasPage bool) string {
	var crossImports strings.Builder
	for pkg, dir := range p.CrossPackageTypes {
		if dir == "" || !p.used[pkg] || pkg == "google.protobuf" {
			continue
		}
		var importPath string
		switch {
		case strings.HasPrefix(dir, "services"):
			importPath = fmt.Sprintf("%s/types/%s/%s", p.ModuleBasePath, dir, pkg)
		case filepath.Base(dir) == pkg:
			importPath = fmt.Sprintf("%s/types/%s", p.ModuleBasePath, dir)
		default:
			importPath = fmt.Sprintf("%s/types/%s/%s", p.ModuleBasePath, dir, pkg)
		}
		fmt.Fprintf(&crossImports, "\n\t%s_pb \"%s\"", pkg, importPath)
	}
	if hasPage {
		// A PAGE tool drives the emitted collection runtime (the page_token codec
		// and the page-size clamp). goimports cannot resolve that local package
		// by name, so it is imported explicitly, exactly as the wrapper does.
		fmt.Fprintf(&crossImports, "\n\t\"%s/chassis/collection\"", p.ModuleBasePath)
	}

	return fmt.Sprintf(`// Copyright (c) 2026 Second Star Cavalier LLC
// Generated by service generator
//
// CODE GENERATED - DO NOT EDIT
// This file is automatically generated by the services compiler.
// Manual modifications will be overwritten on next generation.
//
// IMPORTANT FOR AGENTS: Do NOT modify this generated code.
// If changes are needed, update the tacplus compiler at:
// gitlab.com/secondstarcavalier/schematic/tacplus
//
// %[1]s declares at least one (spec.rpc) principal: RPC_PRINCIPAL_MCP rpc, so
// tacplus generates its whole Model Context Protocol termination here: the tool
// set, the JSON-RPC dispatch, and the carriage over %[2]s. The
// service implements only the typed handler methods on %[1]sHandler — there is
// no MCP glue for it to hand-write.
//
// Every tool call is authorized by the carrier RPC's own gRPC authorization,
// once, at session open: an MCP session reaches exactly the tools the Caller the
// gateway verified is entitled to reach.
//
// This file imports %[3]s, whose own module requires
// go >= %[4]s. A consuming module below that cannot build it.

package %[5]s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"%[3]s/jsonrpc"
	"%[3]s/mcp"

	types "%[6]s"
	"%[7]s"%[8]s
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)
`, serviceName, parser.McpCarrierRpcName(serviceName), goMcpSdkModule, goMcpSdkGoVersion,
		p.PackageName, p.TypesModulePath, p.ServiceManagerImportPath, crossImports.String())
}

// goMcpToolTable renders the published tool set.
//
// The schema is emitted here, from the same message index the egress sanitizer
// builds, rather than inferred by the SDK from a Go argument struct: the tool's
// contract is the rpc's request message, and a second Go mirror of it would be
// one more thing to drift (decision doc §5). mcp.Tool.InputSchema takes any
// value that marshals to a JSON Schema, so the generated JSON goes in directly.
func goMcpToolTable(serviceName string, tools []goMcpTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
// %[1]sMcpInstructions is served as the MCP server's instruction block. It
// states the one rule a model cannot read off the tool schemas: the fields they
// omit are omitted because the server computes them.
const %[1]sMcpInstructions = %[2]s

// %[1]sMcpTools returns the tools %[1]s publishes, in declaration order.
//
// Every schema below omits the fields the server computes from the verified
// caller — (spec.principal), (spec.namespace) and their store_ forms — and the
// injected trace_id/noop pair, and declares "additionalProperties": false.
// Naming one anyway is refused by the dispatch, not silently ignored.
func %[1]sMcpTools() []*mcp.Tool {
	return []*mcp.Tool{
`, serviceName, strconv.Quote(goMcpInstructions))
	for _, t := range tools {
		fmt.Fprintf(&b, `		{
			Name:        %s,
			Description: %s,
			InputSchema: json.RawMessage(%s),
		},
`, strconv.Quote(t.ToolName), strconv.Quote(t.Description), strconv.Quote(t.Schema))
	}
	b.WriteString("	}\n}\n")
	return b.String()
}

// goMcpSurface renders the per-session tool surface and the MCP server that
// routes tools/call into it.
func goMcpSurface(serviceName string, tools []goMcpTool) string {
	surface := serviceName + "McpSurface"
	prefix := goMcpPrefix(serviceName)

	var b strings.Builder
	fmt.Fprintf(&b, `
// %[1]s is one MCP session's tool surface, bound to one verified caller.
//
// Built per session rather than shared: every server-computed predicate in the
// dispatch below comes from this caller, which is a property of the session and
// not of the service.
type %[1]s struct {
	handler %[2]sHandler
	caller  *servicemanager.Caller
}

// New%[1]s builds the surface for a caller the carrier RPC's authorization has
// already verified.
func New%[1]s(handler %[2]sHandler, caller *servicemanager.Caller) *%[1]s {
	return &%[1]s{handler: handler, caller: caller}
}

// mcpServer builds this session's MCP server with every tool registered.
//
// Server.AddTool (rather than the generic mcp.AddTool) is what lets the schema
// stay the generated one and the arguments decode straight into the rpc's own
// protobuf request: the typed helper would need a Go mirror struct per tool.
func (s *%[1]s) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: %[3]s, Version: %[4]s}, &mcp.ServerOptions{
		Instructions: %[2]sMcpInstructions,
	})
	tools := %[2]sMcpTools()
`, surface, serviceName, strconv.Quote(serviceName), strconv.Quote(goMcpServerVersion))

	for i, t := range tools {
		fmt.Fprintf(&b, `	srv.AddTool(tools[%d], func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.%s(ctx, req.Params.Arguments)
	})
`, i, goMcpDispatchFnName(t))
	}

	fmt.Fprintf(&b, `	return srv
}

// %[1]sArgumentNames is the set of argument names a tool call actually carried.
// Dispatch consults it BEFORE decoding, so naming a server-computed field is a
// refusal rather than a value protojson would happily overwrite.
func %[1]sArgumentNames(arguments json.RawMessage) (map[string]bool, error) {
	if len(arguments) == 0 {
		return map[string]bool{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &raw); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("tool arguments are not a JSON object: %%v", err))
	}
	named := make(map[string]bool, len(raw))
	for k := range raw {
		named[k] = true
	}
	return named, nil
}
`, prefix)
	return b.String()
}

// goMcpServerVersion is the version the generated MCP server reports in its
// initialize handshake. It describes the generated TERMINATION, not the service,
// which tacplus has no version for; it moves when this seam's wire behaviour
// does.
const goMcpServerVersion = "1"

func goMcpDispatchFnName(t goMcpTool) string {
	return "call" + t.Method.Name
}

// goMcpToolDispatch renders one tool's dispatch: refuse the server-computed
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
// Go's gRPC wrapper renders no principal/namespace block today (the seam is
// generated for Java and Rust only), so unlike Rust's dispatch this cannot drop
// in the wrapper's own emitted blocks; the renderers below are Go's first, and
// exist for this seam. They read the same annotations and enforce the same rule.
func goMcpToolDispatch(serviceName string, t goMcpTool) string {
	prefix := goMcpPrefix(serviceName)
	surface := serviceName + "McpSurface"
	var b strings.Builder

	fmt.Fprintf(&b, `
// %[1]s dispatches the %[2]q tool to %[3]s.%[4]s.
func (s *%[5]s) %[1]s(ctx context.Context, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	named, err := %[6]sArgumentNames(arguments)
	if err != nil {
		return %[6]sToolError(err), nil
	}
`, goMcpDispatchFnName(t), t.ToolName, serviceName, t.Method.Name, surface, prefix)

	// `named` is read only by the refusals below and by the no-argument check;
	// a tool with neither would leave it unused, which Go rejects.
	if t.PrincipalField == "" && t.NamespaceField == "" && !t.EmptyRequest {
		b.WriteString("\t_ = named\n")
	}

	// A server-computed field is absent from the schema; naming it anyway is
	// refused rather than silently overwritten. The model must not be able to aim
	// this rpc at another principal or tenant, however its arguments were shaped.
	for _, f := range []struct{ marker, field, why string }{
		{"spec.principal", t.PrincipalField, "your verified identity"},
		{"spec.namespace", t.NamespaceField, "your verified namespace claim"},
	} {
		if f.field == "" {
			continue
		}
		fmt.Fprintf(&b, `	// (%[1]s) %[2]s is computed from the verified caller, so it is absent
	// from this tool's schema; a call that names it is refused.
	if named[%[3]s] {
		return %[4]sToolError(status.Error(codes.InvalidArgument,
			"%[2]s is set by the server from %[5]s; it is not a tool argument")), nil
	}
`, f.marker, f.field, strconv.Quote(f.field), prefix, f.why)
	}

	if t.EmptyRequest {
		// A tool that takes no arguments; its handler method declares no req.
		fmt.Fprintf(&b, `	if len(named) > 0 {
		return %sToolError(status.Error(codes.InvalidArgument, "this tool takes no arguments")), nil
	}
`, prefix)
	} else {
		// protojson rejects an unknown field outright, which is the
		// deny_unknown_fields the decision doc asks for: a smuggled argument is an
		// error, not something quietly dropped.
		fmt.Fprintf(&b, `	req := &%[1]s{}
	if len(arguments) > 0 {
		if err := protojson.Unmarshal(arguments, req); err != nil {
			return %[2]sToolError(status.Error(codes.InvalidArgument,
				fmt.Sprintf("invalid arguments: %%v", err))), nil
		}
	}
`, t.InputType, prefix)
		if t.ValidateInput != "" {
			fmt.Fprintf(&b, `	if err := %s(req); err != nil {
		return %sToolError(status.Error(codes.InvalidArgument,
			fmt.Sprintf("validation failed: %%v", err))), nil
	}
`, t.ValidateInput, prefix)
		}
		if t.PrincipalField != "" {
			b.WriteString(goMcpRequestScopeBlock(prefix, t.Method.Name, t.PrincipalField,
				"principal", "GetSubject", "an authenticated caller", "the authenticated caller"))
		}
		if t.NamespaceField != "" {
			b.WriteString(goMcpRequestScopeBlock(prefix, t.Method.Name, t.NamespaceField,
				"namespace", "GetNamespace", "an authenticated caller carrying a namespace claim", "the authenticated caller's namespace"))
		}
		// (spec.owner_default): refuse the access the row's owner does not hold,
		// from the gRPC wrapper's own helper and in the wrapper's own order —
		// after the stamps, because the comparison needs the bound principal.
		b.WriteString(t.OwnerDefaultGuard)
	}

	// PAGE: the same three-argument handler the gRPC PAGE wrapper calls, with
	// PageParams built from this tool call's own page_size/page_token exactly as
	// the wrapper builds it from the wire request. The envelope is assembled
	// here for the same reason: a PAGE handler returns a collection.Page, not
	// the response message.
	if t.Page != nil {
		b.WriteString(goMcpPageDispatch(prefix, t))
	} else {
		// The handler call. Same signature and same sanitized context the ordinary
		// gRPC wrapper hands an ordinary unary rpc.
		handlerArgs := "sanitizeContext(ctx), s.caller, req"
		if t.EmptyRequest {
			handlerArgs = "sanitizeContext(ctx), s.caller"
		}
		if t.EmptyResponse {
			fmt.Fprintf(&b, `	if err := s.handler.%s(%s); err != nil {
		return %sToolError(err), nil
	}
	return %sEmptyToolResult(), nil
}
`, t.Method.Name, handlerArgs, prefix, prefix)
			return b.String()
		}

		fmt.Fprintf(&b, `	resp, err := s.handler.%s(%s)
	if err != nil {
		return %sToolError(err), nil
	}
`, t.Method.Name, handlerArgs, prefix)
	}
	if t.ValidateOutput != "" {
		fmt.Fprintf(&b, `	if err := %s(resp); err != nil {
		return %sToolError(status.Error(codes.Internal,
			fmt.Sprintf("response validation failed: %%v", err))), nil
	}
`, t.ValidateOutput, prefix)
	}
	for i, f := range t.StorePrincipalRows {
		b.WriteString(goMcpRowFilterBlock(prefix, t.Method.Name, f, i, false))
	}
	for i, f := range t.StoreNamespaceRows {
		b.WriteString(goMcpRowFilterBlock(prefix, t.Method.Name, f, i, true))
	}
	fmt.Fprintf(&b, "	return %sToolResult(resp)\n}\n", prefix)
	return b.String()
}

// goMcpPageDispatch renders the PAGE window: decode the tool call's page_token,
// clamp its page_size, call the keyset handler, assemble the response envelope.
//
// Every line of it is the gRPC PAGE wrapper's own (servicewrapper.go's PAGE
// branch), against the same collection runtime and the same resolved bounds —
// the tool and its gRPC twin are one handler driven two ways, not two paging
// implementations.
func goMcpPageDispatch(prefix string, t goMcpTool) string {
	total := ""
	if t.Page.HasTotal {
		total = `
	// total_count is reported only when the handler computes it (Total >= 0);
	// a negative Total leaves total_count at 0 = "unknown", not "empty".
	if page.Total >= 0 {
		resp.TotalCount = page.Total
	}`
	}
	return fmt.Sprintf(`	// Decode the opaque page_token into a keyset cursor (bad token -> INVALID_ARGUMENT).
	after, ptErr := collection.DecodePageToken(req.GetPageToken())
	if ptErr != nil {
		return %[1]sToolError(ptErr), nil
	}
	// Resolve the effective page size: default when unset, clamped to the trust ceiling.
	limit := collection.EffectivePageSize(int(req.GetPageSize()), %[2]d, %[3]d)
	page, err := s.handler.%[4]s(sanitizeContext(ctx), s.caller, req, collection.PageParams{Limit: limit, After: after})
	if err != nil {
		return %[1]sToolError(err), nil
	}
	// Encode the next cursor; an empty token in the result means there is no next page.
	nextPageToken, encErr := collection.EncodePageToken(page.NextAfter)
	if encErr != nil {
		return %[1]sToolError(status.Error(codes.Internal,
			fmt.Sprintf("page token encode failed: %%v", encErr))), nil
	}
	resp := &%[5]s{
		%[6]s:           page.Items,
		NextPageToken: nextPageToken,
	}%[7]s
`, prefix, t.Page.DefaultSize, t.Page.MaxSize, t.Method.Name, t.OutputType, t.Page.ItemsField, total)
}

// goMcpRequestScopeBlock renders the stamp-or-refuse check for one
// caller-scoped request field. Empty is stamped from the caller, equal passes,
// different is refused: a forged value is never silently overwritten, and a
// session with no verified identity cannot reach the handler at all.
func goMcpRequestScopeBlock(prefix, method, field, marker, accessor, needs, mismatch string) string {
	local := "__" + marker
	return fmt.Sprintf(`	// (spec.%[1]s) %[2]s: bind the field to %[3]s.
	%[4]s := ""
	if s.caller != nil {
		%[4]s = s.caller.Token().%[5]s()
	}
	if %[4]s == "" {
		return %[6]sToolError(status.Error(codes.Unauthenticated,
			"%[7]s requires %[8]s")), nil
	}
	if req.%[9]s == "" {
		req.%[9]s = %[4]s
	} else if req.%[9]s != %[4]s {
		return %[6]sToolError(status.Error(codes.InvalidArgument,
			"%[2]s does not match %[3]s")), nil
	}
`, marker, field, mismatch, local, accessor, prefix, method, needs, toGoFieldName(field))
}

// goMcpRowFilterBlock renders the egress row filter for one repeated record
// field: rows the caller does not own, or that belong to another tenant, never
// leave the process.
//
// The namespace form carries the same backstop Java and Rust render. A row
// stamped with another tenant's namespace means the store's own query is
// leaking, and the process is halted outright rather than allowed to keep
// serving: os.Exit runs no deferred work, so in-flight requests cannot drain the
// very rows the filter just caught.
func goMcpRowFilterBlock(prefix, method string, f goMcpRowField, idx int, isNamespace bool) string {
	marker, local, accessor := "store_principal", "__readPrincipal", "GetSubject"
	needs := "an authenticated caller"
	if isNamespace {
		marker, local, accessor = "store_namespace", "__readNamespace", "GetNamespace"
		needs = "an authenticated caller carrying a namespace claim"
	}
	kept := fmt.Sprintf("__%sRows%d", strings.TrimPrefix(marker, "store_"), idx)
	field := toGoFieldName(f.Field)
	itemGetter := "Get" + toGoFieldName(f.ItemField)

	var b strings.Builder
	if idx == 0 {
		fmt.Fprintf(&b, `	// (spec.%[1]s): hold the rows to %[2]s.
	%[3]s := ""
	if s.caller != nil {
		%[3]s = s.caller.Token().%[4]s()
	}
	if %[3]s == "" {
		return %[5]sToolError(status.Error(codes.Unauthenticated,
			"%[6]s requires %[7]s")), nil
	}
`, marker, map[bool]string{true: "the caller's tenant", false: "the caller's principal"}[isNamespace],
			local, accessor, prefix, method, needs)
	}
	// resp.Field[:0:0] keeps the element type without naming it and forces the
	// first append to allocate, so the handler's own backing array is never
	// written through.
	fmt.Fprintf(&b, `	%[1]s := resp.%[2]s[:0:0]
	for _, __row := range resp.%[2]s {
		if __row.%[3]s() == %[4]s {
			%[1]s = append(%[1]s, __row)
		}
	}
`, kept, field, itemGetter, local)
	if isNamespace {
		fmt.Fprintf(&b, `	if len(%[1]s) != len(resp.%[2]s) {
		fmt.Fprintf(os.Stderr, "FATAL: (spec.store_namespace) backstop fired: %[3]s returned rows in %[2]s stamped with a namespace other than the caller's (%%s); the store's own query is leaking across tenants\n", %[4]s)
		os.Exit(1)
	}
`, kept, field, method, local)
	}
	fmt.Fprintf(&b, "	resp.%s = %s\n", field, kept)
	return b.String()
}

// goMcpResultHelpers renders the framing every tool shares.
//
// A tool failure is a tool RESULT with IsError set, not a JSON-RPC protocol
// error. That is the Go SDK's own stated convention (CallToolResult.IsError:
// "any errors that originate from the tool should be reported inside the Content
// field ... otherwise, the LLM would not be able to see that an error occurred
// and self-correct"), and it means a refused argument or a permission denial is
// legible to the model rather than opaque.
func goMcpResultHelpers(serviceName string) string {
	prefix := goMcpPrefix(serviceName)
	return fmt.Sprintf(`
// %[1]sToolError frames a failed tool call as a readable tool result. Only an
// unroutable call (an unknown tool name) is a protocol error; the SDK answers
// that one itself.
func %[1]sToolError(err error) *mcp.CallToolResult {
	msg := err.Error()
	if st, ok := status.FromError(err); ok {
		msg = st.Message()
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// %[1]sEmptyToolResult is the result of a tool whose rpc returns
// google.protobuf.Empty: it succeeded and carries nothing.
func %[1]sEmptyToolResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "{}"}}}
}

// %[1]sToolResult frames a typed response.
//
// UseProtoNames keeps the result's field names the same snake_case ones the
// tool's input schema publishes, so the model reads back what it wrote.
func %[1]sToolResult(resp proto.Message) (*mcp.CallToolResult, error) {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(resp)
	if err != nil {
		return %[1]sToolError(status.Error(codes.Internal,
			fmt.Sprintf("could not encode the result: %%v", err))), nil
	}
	// The frame bound is on the transport, but a result that cannot be framed
	// fails THIS call rather than the session: %[2]d bytes is what the generated
	// response validation on mcp.McpResponse.payload enforces, and failing that
	// validation fails the whole stream.
	if len(data) > %[2]d {
		return %[1]sToolError(status.Error(codes.ResourceExhausted,
			fmt.Sprintf("the result is %%d bytes, over this transport's %[2]d-byte frame limit; ask for less at a time", len(data)))), nil
	}
	var structured any
	if err := json.Unmarshal(data, &structured); err != nil {
		return %[1]sToolError(status.Error(codes.Internal,
			fmt.Sprintf("could not encode the result: %%v", err))), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: structured,
	}, nil
}
`, prefix, proto.McpResponseFrameMaxBytes)
}

// goMcpTransport renders the carriage: one MCP session over one gRPC bidi
// stream, as an mcp.Transport.
//
// The session runs inside the gRPC handler's own goroutine — Server.Run blocks
// until the client's half ends — so nothing here outlives the RPC and there is
// no task to leak.
func goMcpTransport(serviceName, frameIn, frameOut string, maxFrameBytes int) string {
	prefix := goMcpPrefix(serviceName)
	surface := serviceName + "McpSurface"
	return fmt.Sprintf(`
// %[1]sMaxFrameBytes is the largest outbound frame this transport will put on
// the wire.
//
// Generated from (spec.assert_max_size) on mcp.McpResponse.payload, which is
// what the generated response validation enforces. Past that value validation
// fails the whole STREAM, killing the session over one large tool result.
const %[1]sMaxFrameBytes = %[2]d

// serve%[3]sMcpSession serves one MCP session over one gRPC stream.
func serve%[3]sMcpSession(ctx context.Context, handler %[3]sHandler, caller *servicemanager.Caller, stream grpc.BidiStreamingServer[%[4]s, %[5]s]) error {
	surface := New%[6]s(handler, caller)
	err := surface.mcpServer().Run(ctx, &%[1]sTransport{conn: &%[1]sConn{stream: stream}})
	// The ordinary end of a session: the client hung up, or the RPC was
	// cancelled. Neither is a failed RPC.
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Canceled {
		return nil
	}
	return err
}

// %[1]sTransport hands the SDK the one connection this RPC carries. A Transport
// is connected exactly once, so the second Connect is a bug, not a reconnect.
type %[1]sTransport struct {
	conn *%[1]sConn
}

func (t *%[1]sTransport) Connect(context.Context) (mcp.Connection, error) {
	if t.conn == nil {
		return nil, errors.New("this MCP session's transport is already connected")
	}
	conn := t.conn
	t.conn = nil
	return conn, nil
}

// %[1]sConn is one MCP session's frames, moving between the gRPC stream and the
// SDK. One complete JSON-RPC message per stream message, never fragmented.
type %[1]sConn struct {
	stream grpc.BidiStreamingServer[%[4]s, %[5]s]
	// mu serializes Send: the SDK may write a response and a notification
	// concurrently, and a gRPC stream tolerates only one writer.
	mu     sync.Mutex
	closed bool
}

// SessionID is unused: this transport carries exactly one session, whose
// identity is the gRPC stream's own.
func (c *%[1]sConn) SessionID() string { return "" }

func (c *%[1]sConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, err := c.stream.Recv()
		if err != nil {
			return nil, err
		}
		msg, decodeErr := jsonrpc.DecodeMessage([]byte(frame.GetPayload()))
		if decodeErr == nil {
			return msg, nil
		}
		// Not fatal: one unreadable frame does not say the next one is
		// unreadable too, and JSON-RPC has an error frame for exactly this. It
		// names no request, so it carries no id.
		detail, merr := json.Marshal("could not decode the frame: " + decodeErr.Error())
		if merr != nil {
			return nil, decodeErr
		}
		if werr := c.send(fmt.Sprintf(%[7]s, jsonrpc.CodeParseError, detail)); werr != nil {
			return nil, werr
		}
	}
}

func (c *%[1]sConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	if len(data) > %[1]sMaxFrameBytes {
		return fmt.Errorf("an MCP frame of %%d bytes is over the %%d-byte limit on mcp.McpResponse.payload", len(data), %[1]sMaxFrameBytes)
	}
	return c.send(string(data))
}

func (c *%[1]sConn) send(payload string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return mcp.ErrConnectionClosed
	}
	return c.stream.Send(&%[5]s{Payload: payload})
}

// Close stops this half writing. The stream itself is the gRPC handler's to end:
// it is torn down when serve%[3]sMcpSession returns, which is what unblocks a
// Read parked in Recv.
func (c *%[1]sConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
`, prefix, maxFrameBytes, serviceName, frameIn, frameOut, surface,
		"`"+`{"jsonrpc":"2.0","id":null,"error":{"code":%d,"message":%s}}`+"`")
}

// goIsMcpCarrier reports whether method is svc's generated MCP session carrier.
func goIsMcpCarrier(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	c := goMcpCarrierMethod(svc)
	return c != nil && c.Name == method.Name
}

// writeGoMcpDispatchFile emits <service>_mcp.go beside the service wrapper, for
// a service that declares at least one MCP tool. A service that declares none
// gets no file at all.
func writeGoMcpDispatchFile(svc *parser.ServiceInfo, outputDir string, p goMcpFileParams, san *egressSanitizer) error {
	if goMcpCarrierMethod(svc) == nil {
		return nil
	}
	src, err := generateGoMcpDispatch(svc, p, san)
	if err != nil {
		return fmt.Errorf("failed to generate MCP dispatch for %s: %w", svc.Name, err)
	}
	outputPath := filepath.Join(outputDir, goMcpFileName(svc.Name))
	formatted, err := imports.Process(outputPath, []byte(src), nil)
	if err != nil {
		zap.L().Warn("failed to goimports MCP dispatch, trying format.Source", zap.Error(err))
		formatted, err = format.Source([]byte(src))
		if err != nil {
			zap.L().Warn("failed to format MCP dispatch", zap.Error(err))
			formatted = []byte(src)
		}
	}
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write MCP dispatch for %s: %w", svc.Name, err)
	}
	return nil
}
