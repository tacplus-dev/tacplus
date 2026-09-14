// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// The MCP tool table was not the only place a proto-authored string is dropped
// between the quotes of a Rust string literal with Go's own quoting. Every site
// below used fmt.Sprintf("%q", ...), which spells a non-printable rune as a bare
// \uXXXX — Go escape syntax that rustc rejects with "incorrect unicode escape
// sequence". They all go through rustQuote now, which spells it \u{XXXX}.
//
// U+200B is the probe throughout, for the same reason the MCP test uses it: it
// is not unicode.IsSpace, so it survives any whitespace flattening between the
// annotation and the literal.
const zwsp = "\u200b"

// (spec.default_string) is free text an author writes, so its constant is the
// site with the widest input of the set.
func TestRustDefaultStringConstantUsesRustUnicodeEscapes(t *testing.T) {
	pf := model.ProtoFile{
		Package: "ticket",
		Messages: []model.Message{{
			Name: "TicketPrefs",
			Fields: []model.Field{{
				Name: "greeting",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"default_string": {Type: model.AnnotationTypeString, StringValue: "hello" + zwsp + "world"},
				},
			}},
		}},
	}

	consts := collectFieldDefaultConstants(pf)
	if len(consts) != 1 {
		t.Fatalf("expected one default constant, got %d: %+v", len(consts), consts)
	}
	if want := `"hello\u{200b}world"`; consts[0].RustValue != want {
		t.Errorf("default_string constant = %s, want %s", consts[0].RustValue, want)
	}
	if strings.Contains(consts[0].RustValue, `\u200b`) {
		t.Errorf("default_string constant carries a Go-only escape rustc rejects: %s", consts[0].RustValue)
	}
}

// A param-key alias reaches the from_map match arms from (spec.json_return_value),
// which is free text like any other string annotation. ToUpper leaves U+200B be.
func TestRustParamKeyAliasLiteralUsesRustUnicodeEscapes(t *testing.T) {
	pf := model.ProtoFile{
		Package: "audit",
		Enums: []model.Enum{{
			Name: "AuditParamKey",
			Values: []model.EnumValue{
				{Name: "AUDIT_PARAM_KEY_REQUIRED_ID", JsonValue: "odd" + zwsp + "id"},
			},
		}},
		Messages: []model.Message{{
			Name:           "AuditParams",
			ParamMapConfig: &model.ParamMapConfig{KeyEnum: "AuditParamKey", Strict: true},
			Fields: []model.Field{
				{Name: "required_id", Type: "int64", ParamKey: "AUDIT_PARAM_KEY_REQUIRED_ID"},
			},
		}},
	}

	var b strings.Builder
	if err := writeFromMap(&b, pf); err != nil {
		t.Fatalf("writeFromMap: %v", err)
	}
	// Only the from_map half here; the to_map keys have their own tests below.
	rendered := b.String()
	fromMap := rendered[:strings.Index(rendered, "_to_map(")]

	if want := `"ODD\u{200b}ID"`; !strings.Contains(fromMap, want) {
		t.Errorf("from_map match arm missing %s:\n%s", want, fromMap)
	}
	if strings.Contains(fromMap, `\u200b`) {
		t.Errorf("from_map carries a Go-only escape rustc rejects:\n%s", fromMap)
	}
	if strings.Contains(fromMap, zwsp) {
		t.Errorf("a raw U+200B survived into the generated from_map:\n%s", fromMap)
	}
}

// (spec.auth_services) patterns are author-supplied strings that land in the
// set_method_auth array literal, on both the MCP-carrier and the ordinary-rpc
// registration paths.
func TestRustMethodAuthPatternsUseRustUnicodeEscapes(t *testing.T) {
	pattern := "ticket" + zwsp + "agent"
	targetService := rustMcpOnlyAuthTestService()
	targetService.AuthServices = []string{pattern}
	targetService.Methods = append(targetService.Methods, &parser.MethodInfo{
		Name:           "GetTicket",
		InputType:      "GetTicketRequest",
		OutputType:     "GetTicketResponse",
		FullInputType:  "ticket.GetTicketRequest",
		FullOutputType: "ticket.GetTicketResponse",
	})
	protoInfo := &parser.ProtoInfo{Package: "ticket", Services: []*parser.ServiceInfo{targetService}}
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "ticket/agent.proto",
		PackageName:  "ticket",
		ServiceName:  "TicketAgent",
		GrpcName:     "ticket_agent",
		IsGrpc:       true,
		IsC2S:        true,
		HasMcp:       true,
		AuthServices: []string{pattern},
		OutputDir:    outputDir,
		ParentDir:    "services",
		Config:       &tactype.Config{OutputDir: outputDir},
	}

	if err := generateServiceWrapper(svc, protoInfo, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustMcpTestSanitizer()); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one generated file in %s, got %v (err %v)", outputDir, entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(outputDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	got := string(raw)

	// Both registration paths: the synthesized MCP carrier and the ordinary rpc.
	carrier := parser.McpCarrierRpcName("TicketAgent")
	for _, method := range []string{carrier, "GetTicket"} {
		want := `auth.set_method_auth("ticket.TicketAgent/` + method + `", &["ticket\u{200b}agent"]);`
		if !strings.Contains(got, want) {
			t.Errorf("register_method_auth missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `\u200b`) {
		t.Errorf("generated wrapper carries a Go-only escape rustc rejects:\n%s", got)
	}
	if strings.Contains(got, zwsp) {
		t.Errorf("a raw U+200B survived into the generated wrapper:\n%s", got)
	}
}

// The entitlement audience of a trait-hosted C2S surface is (spec.trait).name —
// free text, and the only Some(...) literal of the set whose input is not a
// proto identifier.
func TestRustRouteIdentityTraitNameUsesRustUnicodeEscapes(t *testing.T) {
	svc := ServiceInfo{
		PackageName: "taskquery",
		ServiceName: "QueryApi",
		IsC2S:       true,
		HostLabel:   "stratum",
	}
	_, entitlementService, err := routeIdentityForService(svc, "que"+zwsp+"ry")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}
	if want := `Some("que\u{200b}ry")`; entitlementService != want {
		t.Errorf("EntitlementService = %s, want %s", entitlementService, want)
	}

	// The same literal builder, used for the capability scope and the
	// target_namespace field name as well.
	if got, want := rustOptionString("ns"+zwsp+"field"), `Some("ns\u{200b}field")`; got != want {
		t.Errorf("rustOptionString = %s, want %s", got, want)
	}
	if got, want := rustOptionString(""), "None"; got != want {
		t.Errorf("rustOptionString(\"\") = %s, want %s", got, want)
	}
}

// The HTTP-method array and the capability-scope array are the two sites whose
// input is provably ASCII — the methods come from a closed proto enum
// (RestMethod) and the scopes are Go constants in the verbs package, so
// switching them to rustQuote is a no-op. This pins that it stayed one.
func TestRustAsciiOnlyQuotedArraysAreUnchanged(t *testing.T) {
	if got, want := formatHttpMethods([]string{"POST", "GET"}), `"POST", "GET"`; got != want {
		t.Errorf("formatHttpMethods = %s, want %s", got, want)
	}
	scopes, _, err := routeCapabilityMetadataFromShape("GetTicket", false, false, "")
	if err != nil {
		t.Fatalf("routeCapabilityMetadataFromShape: %v", err)
	}
	if !strings.HasPrefix(scopes, "&[") || !strings.HasSuffix(scopes, "]") {
		t.Errorf("capability scope array is not a Rust slice literal: %s", scopes)
	}
	for _, r := range scopes {
		if r > 0x7e {
			t.Errorf("capability scope array is no longer ASCII (%q); it needs a real escaping test: %s", r, scopes)
			break
		}
	}
}

// rustc is the arbiter: the fixed literals compile, and the same literals quoted
// the old way do not — so this cannot pass by compiling something the bug never
// touched.
func TestRustQuotedSitesCompile(t *testing.T) {
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the compile check")
	}
	pf := model.ProtoFile{
		Package: "ticket",
		Messages: []model.Message{{
			Name: "TicketPrefs",
			Fields: []model.Field{{
				Name: "greeting",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"default_string": {Type: model.AnnotationTypeString, StringValue: "hello" + zwsp + "world"},
				},
			}},
		}},
	}
	consts := collectFieldDefaultConstants(pf)
	if len(consts) != 1 {
		t.Fatalf("expected one default constant, got %+v", consts)
	}
	_, audience, err := routeIdentityForService(
		ServiceInfo{PackageName: "taskquery", ServiceName: "QueryApi", IsC2S: true, HostLabel: "stratum"},
		"que"+zwsp+"ry")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}

	src := "pub const " + consts[0].Name + ": " + consts[0].RustType + " = " + consts[0].RustValue + ";\n" +
		"pub const AUDIENCE: Option<&str> = " + audience + ";\n"
	if err := rustcAccepts(t, src); err != nil {
		t.Fatalf("rustc rejected the generated literals: %v\n--- src ---\n%s", err, src)
	}

	// The control: the same two literals quoted by strconv.Quote, as they were.
	old := strings.ReplaceAll(src, `\u{200b}`, `\u200b`)
	if old == src {
		t.Fatalf("could not build the pre-fix control from:\n%s", src)
	}
	if want := strconv.Quote("hello" + zwsp + "world"); !strings.Contains(old, want) {
		t.Fatalf("the control is not what strconv.Quote produced (%s):\n%s", want, old)
	}
	if err := rustcAccepts(t, old); err == nil {
		t.Fatalf("rustc accepted the Go-quoted control, so this test proves nothing:\n%s", old)
	}
}

// to_map is the other half of the same generator, and it interpolated the
// (spec.json_return_value) text raw into "%s".to_string() — no escaping at all,
// so a quote or backslash broke the literal outright and a non-printable rune
// embedded as itself. Every field shape gets its own key here, since each of the
// eight insert sites builds the literal on its own.
//
// The probe is U+202E rather than the U+200B used above: rustc accepts a raw
// U+200B in a literal, so it could not distinguish the fix from the raw
// interpolation it replaced, whereas a raw bidi override is a hard error.
func TestRustToMapKeyLiteralsUseRustUnicodeEscapes(t *testing.T) {
	pf := rustToMapEscapeProtoFile()

	var b strings.Builder
	if err := writeFromMap(&b, pf); err != nil {
		t.Fatalf("writeFromMap: %v", err)
	}
	rendered := b.String()
	toMap := rendered[strings.Index(rendered, "_to_map("):]

	keys := rustToMapInsertKeyLiterals(toMap)
	if len(keys) != 8 {
		t.Fatalf("expected eight to_map insert keys, got %d: %q\n%s", len(keys), keys, toMap)
	}
	for _, key := range keys {
		if !strings.Contains(key, `\u{202e}`) {
			t.Errorf("to_map key literal %s is missing the Rust unicode escape", key)
		}
	}
	if strings.Contains(toMap, `\u202e`) {
		t.Errorf("to_map carries a Go-only escape rustc rejects:\n%s", toMap)
	}
	if strings.Contains(toMap, rlo) {
		t.Errorf("a raw U+202E survived into the generated to_map:\n%s", toMap)
	}

	// The characters that break the literal outright rather than mis-escaping it.
	hostile := model.ProtoFile{
		Package: "audit",
		Enums: []model.Enum{{
			Name:   "AuditParamKey",
			Values: []model.EnumValue{{Name: "AUDIT_PARAM_KEY_NAME", JsonValue: `a"b\c`}},
		}},
		Messages: []model.Message{{
			Name:           "AuditParams",
			ParamMapConfig: &model.ParamMapConfig{KeyEnum: "AuditParamKey", Strict: true},
			Fields:         []model.Field{{Name: "name", Type: "string", ParamKey: "AUDIT_PARAM_KEY_NAME"}},
		}},
	}
	var hb strings.Builder
	if err := writeFromMap(&hb, hostile); err != nil {
		t.Fatalf("writeFromMap (hostile): %v", err)
	}
	hostileToMap := hb.String()[strings.Index(hb.String(), "_to_map("):]
	if want := `result.insert("a\"b\\c".to_string()`; !strings.Contains(hostileToMap, want) {
		t.Errorf("to_map did not escape the literal-breaking characters, want %s:\n%s", want, hostileToMap)
	}
}

// rustc is the arbiter for the to_map keys too: the escaped literals compile and
// the raw interpolation they replaced does not.
func TestRustToMapKeyLiteralsCompile(t *testing.T) {
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the compile check")
	}
	var b strings.Builder
	if err := writeFromMap(&b, rustToMapEscapeProtoFile()); err != nil {
		t.Fatalf("writeFromMap: %v", err)
	}
	rendered := b.String()
	keys := rustToMapInsertKeyLiterals(rendered[strings.Index(rendered, "_to_map("):])
	if len(keys) != 8 {
		t.Fatalf("expected eight to_map insert keys, got %d: %q", len(keys), keys)
	}

	src := "pub const TO_MAP_KEYS: &[&str] = &[\n" + strings.Join(keys, ",\n") + ",\n];\n"
	if err := rustcAccepts(t, src); err != nil {
		t.Fatalf("rustc rejected the generated to_map keys: %v\n--- src ---\n%s", err, src)
	}

	// The control: the key text interpolated raw, as to_map did before.
	old := strings.ReplaceAll(src, `\u{202e}`, rlo)
	if old == src {
		t.Fatalf("could not build the pre-fix control from:\n%s", src)
	}
	if err := rustcAccepts(t, old); err == nil {
		t.Fatalf("rustc accepted the raw-interpolation control, so this test proves nothing:\n%s", old)
	}
}

// One message covering every field shape generateToMapMethod branches on, each
// with its own json_return_value carrying U+202E.
func rustToMapEscapeProtoFile() model.ProtoFile {
	shapes := []struct {
		field model.Field
		key   string
	}{
		{model.Field{Name: "ids", Type: "int64", IsRepeated: true}, "ids"},
		{model.Field{Name: "tags", Type: "string", IsRepeated: true}, "tags"},
		{model.Field{Name: "opt_count", Type: "int64", IsOptional: true}, "opt_count"},
		{model.Field{Name: "count", Type: "int64"}, "count"},
		{model.Field{Name: "opt_blob", Type: "bytes", IsOptional: true}, "opt_blob"},
		{model.Field{Name: "blob", Type: "bytes"}, "blob"},
		{model.Field{Name: "opt_name", Type: "string", IsOptional: true}, "opt_name"},
		{model.Field{Name: "name", Type: "string"}, "name"},
	}

	var values []model.EnumValue
	var fields []model.Field
	for _, shape := range shapes {
		enumName := "AUDIT_PARAM_KEY_" + strings.ToUpper(shape.key)
		values = append(values, model.EnumValue{Name: enumName, JsonValue: shape.key + rlo + "key"})
		field := shape.field
		field.ParamKey = enumName
		fields = append(fields, field)
	}

	return model.ProtoFile{
		Package:  "audit",
		Enums:    []model.Enum{{Name: "AuditParamKey", Values: values}},
		Messages: []model.Message{{Name: "AuditParams", ParamMapConfig: &model.ParamMapConfig{KeyEnum: "AuditParamKey", Strict: true}, Fields: fields}},
	}
}

// rustToMapInsertKeyLiterals pulls the quoted key out of every
// result.insert(<literal>.to_string(), ...) the generator emitted.
var rustToMapInsertKeyRe = regexp.MustCompile(`result\.insert\(("(?:[^"\\]|\\.)*")\.to_string\(\)`)

func rustToMapInsertKeyLiterals(toMap string) []string {
	var keys []string
	for _, m := range rustToMapInsertKeyRe.FindAllStringSubmatch(toMap, -1) {
		keys = append(keys, m[1])
	}
	return keys
}

// rlo is a raw bidi override: not unicode.IsPrint, and rustc rejects it
// unescaped inside a string literal.
const rlo = "\u202e"
