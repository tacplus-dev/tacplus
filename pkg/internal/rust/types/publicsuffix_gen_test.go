// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
)

func boolAnn() model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
}

func notInAnn(n ...int32) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeIntSlice, IntSliceValue: n}
}

// signInTestMessage covers each of the three annotations on every field shape
// prost renders: String, Option<String>, Vec<String>, i32, Option<i32>, Vec<i32>.
var signInTestMessage = model.Message{Name: "Conf", Fields: []model.Field{
	{Name: "domain", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn()}},
	{Name: "domains", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn()}},
	{Name: "maybe_domain", Type: "string", IsOptional: true, Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn()}},
	{Name: "subject", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn()}},
	{Name: "subjects", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn()}},
	{Name: "protocol", Type: "IdpProtocol", IsEnum: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": notInAnn(2, 3)}},
	{Name: "protocols", Type: "IdpProtocol", IsEnum: true, IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": notInAnn(2)}},
	{Name: "maybe_protocol", Type: "IdpProtocol", IsEnum: true, IsOptional: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": notInAnn(2)}},
}}

var signInTestEnums = []model.Enum{{Name: "IdpProtocol", Values: []model.EnumValue{
	{Name: "IDP_PROTOCOL_UNSPECIFIED", Number: 0},
	{Name: "IDP_PROTOCOL_OIDC", Number: 1},
	{Name: "IDP_PROTOCOL_SAML", Number: 2},
}}}

func renderRustSignInChecks(t *testing.T, msg model.Message) (string, error) {
	t.Helper()
	var b strings.Builder
	for _, f := range msg.Fields {
		if err := generateFieldValidation(&b, f, msg, signInTestEnums, false); err != nil {
			return b.String(), err
		}
	}
	return b.String(), nil
}

// The generated Rust helpers of (spec.assert_registrable_domain) and
// (spec.assert_not_address), with the emitted field checks of all three
// annotations, compiled warning-free by the real rustc and run over the shared
// vectors (tacplus#224).
func TestGeneratedRustRegistrableDomainAndNotAddress(t *testing.T) {
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the executable assert_registrable_domain check")
	}
	checks, err := renderRustSignInChecks(t, signInTestMessage)
	if err != nil {
		t.Fatal(err)
	}
	var helpers strings.Builder
	writeRustSignInHelpers(&helpers, true, true)

	var in, addr strings.Builder
	for _, v := range publicsuffix.Vectors {
		fmt.Fprintf(&in, "        %q,\n", v.In)
	}
	for _, v := range publicsuffix.AddressVectors {
		fmt.Fprintf(&addr, "        %q,\n", v.In)
	}
	program := helpers.String() + `
#[derive(Default)]
struct Conf {
    domain: String,
    domains: Vec<String>,
    maybe_domain: Option<String>,
    subject: String,
    subjects: Vec<String>,
    protocol: i32,
    protocols: Vec<i32>,
    maybe_protocol: Option<i32>,
}

impl Conf {
    fn validate(&self) -> Result<(), String> {
` + checks + `        Ok(())
    }
}

fn main() {
    for v in [
` + in.String() + `    ] {
        match validate_registrable_domain(v) {
            Ok(()) => println!("accept"),
            Err(e) => println!("{}", e),
        }
    }
    for v in [
` + addr.String() + `    ] {
        println!("{}", validate_not_address(v).is_err());
    }
    let cases: Vec<Conf> = vec![
        Conf::default(),
        Conf { domain: "example.com".into(), domains: vec!["".into(), "acme.github.io".into()], maybe_domain: Some("".into()), subject: "248289761001".into(), protocol: 1, protocols: vec![1], maybe_protocol: Some(1), ..Default::default() },
        Conf { domain: "co.uk".into(), ..Default::default() },
        Conf { domains: vec!["example.com".into(), "a..com".into()], ..Default::default() },
        Conf { maybe_domain: Some("com".into()), ..Default::default() },
        Conf { subject: "jody@acme.com".into(), ..Default::default() },
        Conf { subjects: vec!["x".into(), "@".into()], ..Default::default() },
        Conf { protocol: 2, ..Default::default() },
        Conf { protocol: 3, ..Default::default() },
        Conf { protocols: vec![1, 2], ..Default::default() },
        Conf { maybe_protocol: Some(2), ..Default::default() },
    ];
    for c in cases {
        match c.validate() {
            Ok(()) => println!("ok"),
            Err(e) => println!("{}", e),
        }
    }
}
`
	dir := t.TempDir()
	src := filepath.Join(dir, "main.rs")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "check")
	if out, err := exec.Command("rustc", "--edition", "2021", "-D", "warnings", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("rustc on the generated helpers failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("running the generated helpers: %v", err)
	}
	var want []string
	for _, v := range publicsuffix.Vectors {
		want = append(want, v.Outcome())
	}
	for _, v := range publicsuffix.AddressVectors {
		want = append(want, fmt.Sprint(v.Refused))
	}
	want = append(want,
		"ok",
		"ok",
		"field domain must be a registrable domain name: a public suffix",
		"field domains[1] must be a registrable domain name: not a domain name",
		"field maybe_domain must be a registrable domain name: a public suffix",
		"field subject must not be address-shaped: address-shaped",
		"field subjects[1] must not be address-shaped: address-shaped",
		"field protocol must not be IDP_PROTOCOL_SAML",
		"field protocol must not be 3",
		"field protocols[1] must not be IDP_PROTOCOL_SAML",
		"field maybe_protocol must not be IDP_PROTOCOL_SAML",
	)
	got := strings.TrimSpace(string(out))
	if got != strings.Join(want, "\n") {
		t.Fatalf("outcomes differ\ngot:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// The field checks the Rust validation emitter writes for the three
// annotations, the helpers emitted only when used, and the shapes refused at
// generation.
func TestRustValidationEmitsSignInAsserts(t *testing.T) {
	code, err := renderRustSignInChecks(t, signInTestMessage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"// Validate domain (assert_registrable_domain)",
		"validate_registrable_domain(&self.domain).map_err(|e| format!(\"field domain must be a registrable domain name: {}\", e))?;",
		"for (i, val) in self.domains.iter().enumerate() {",
		"if let Some(ref val) = self.maybe_domain {",
		"validate_not_address(val).map_err(|e| format!(\"field subjects[{}] must not be address-shaped: {}\", i, e))?;",
		"if self.protocol == 2 {",
		"if self.protocol == 3 {",
		"if *val == 2 {",
		"if self.maybe_protocol == Some(2) {",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated validation lacks %q\n%s", want, code)
		}
	}

	// Helpers: only what a module uses.
	for _, tc := range []struct {
		fields       []model.Field
		domain, addr bool
	}{
		{signInTestMessage.Fields[:1], true, false},
		{signInTestMessage.Fields[3:4], false, true},
		{signInTestMessage.Fields[5:6], false, false},
		{[]model.Field{{Name: "d", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": {Type: model.AnnotationTypeBool}}}}, false, false},
	} {
		usage := &validatorUsage{}
		trackValidatorUsage(model.Message{Name: "M", Fields: tc.fields}, usage)
		if usage.needsRegistrableDomain != tc.domain || usage.needsNotAddress != tc.addr {
			t.Errorf("fields %+v: usage domain=%v addr=%v, want %v %v", tc.fields, usage.needsRegistrableDomain, usage.needsNotAddress, tc.domain, tc.addr)
		}
	}
	var h strings.Builder
	writeRustSignInHelpers(&h, true, false)
	if !strings.Contains(h.String(), `"co.uk",`) || !strings.Contains(h.String(), "version "+publicsuffix.Version()) || strings.Contains(h.String(), "validate_not_address") {
		t.Errorf("registrable-domain helpers wrong:\n%.600s", h.String())
	}

	for _, bad := range []model.Field{
		{Name: "n", Type: "int64", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn()}},
		{Name: "n", Type: "bytes", Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn()}},
		{Name: "m", Type: "", IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn()}},
		{Name: "n", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": notInAnn(1)}},
		{Name: "e", Type: "IdpProtocol", IsEnum: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": notInAnn(1, 0)}},
	} {
		if _, err := renderRustSignInChecks(t, model.Message{Name: "Bad", Fields: []model.Field{bad}}); err == nil {
			t.Errorf("field %+v: no generation error", bad)
		}
	}
}

// The MCP dispatch gate synthesizes values the validator accepts: a required
// assert_registrable_domain string gets a registrable name (a single label is
// a public suffix), and an assert_enum_defined enum skips the values its
// assert_enum_not_in refuses.
func TestRustMcpDispatchSynthesizesValuesTheSignInAssertsAccept(t *testing.T) {
	san := newRustEgressSanitizer([]model.ProtoFile{{Package: "ticket",
		Enums: []model.Enum{{Name: "TicketMode", Values: []model.EnumValue{
			{Name: "TICKET_MODE_UNSPECIFIED", Number: 0},
			{Name: "TICKET_MODE_READ", Number: 1},
			{Name: "TICKET_MODE_WRITE", Number: 2},
		}}},
		Messages: []model.Message{
			{Name: "GetTicketBriefRequest", Fields: []model.Field{
				{Name: "ticket_id", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{
					"assert_required": boolAnn(), "assert_registrable_domain": boolAnn()}},
				{Name: "mode", Type: "TicketMode", Number: 2, IsEnum: true, Annotations: map[string]model.AnnotationValue{
					"assert_enum_defined": boolAnn(), "assert_enum_not_in": notInAnn(1)}},
			}},
			{Name: "GetTicketBriefResponse", Fields: []model.Field{{Name: "summary", Type: "string", Number: 1}}},
			{Name: "GetTicketRequest", Fields: []model.Field{{Name: "ticket_id", Type: "string", Number: 1}}},
			{Name: "GetTicketResponse", Fields: []model.Field{{Name: "body", Type: "string", Number: 1}}},
		}}})
	svc, _, _, _ := rustMcpTestService()
	got, err := generateRustMcpDispatch(svc, "ticket", map[string]string{}, san)
	if err != nil {
		t.Fatalf("generateRustMcpDispatch(%s): %v", svc.Name, err)
	}
	for _, want := range []string{`\"ticket_id\": \"example.com\"`, `\"mode\": \"TICKET_MODE_WRITE\"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("the generated gate lacks %s\ngot:\n%s", want, got)
		}
	}
}
