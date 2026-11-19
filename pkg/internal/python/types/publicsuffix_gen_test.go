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

func pySignInField(name, typ string, repeated bool, annotation string, v model.AnnotationValue) model.Field {
	return model.Field{Name: name, Type: typ, IsRepeated: repeated, IsEnum: typ == "IdpProtocol" || typ == "E",
		Annotations: map[string]model.AnnotationValue{annotation: v}}
}

var pyBoolTrue = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}

// The generated assert_registrable_domain / assert_not_address helpers, and
// the field checks that call them, run with the real python3 over the shared
// vectors (tacplus#224): a public suffix and a malformed name are refused with
// the shared texts through invalid_argument, a registrable name accepted; an
// address-shaped value refused.
func TestGeneratedPythonRegistrableDomainAndNotAddress(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed; skipping the executable assert_registrable_domain check")
	}
	msg := model.Message{Name: "Conf", Fields: []model.Field{
		pySignInField("domain", "string", false, "assert_registrable_domain", pyBoolTrue),
		pySignInField("subjects", "string", true, "assert_not_address", pyBoolTrue),
	}}
	blocks, err := buildValidationBlocks(msg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var checks strings.Builder
	for _, bl := range blocks {
		checks.WriteString(bl.Code)
	}
	var in, addr strings.Builder
	for _, v := range publicsuffix.Vectors {
		fmt.Fprintf(&in, "    %q,\n", v.In)
	}
	for _, v := range publicsuffix.AddressVectors {
		fmt.Fprintf(&addr, "    %q,\n", v.In)
	}
	program := `class ProtoError(Exception):
    pass


def invalid_argument(message):
    return ProtoError(message)


class Msg:
    def __init__(self, domain="", subjects=()):
        self.domain = domain
        self.subjects = list(subjects)


def check(msg):
` + checks.String() + pyPublicSuffixHelpers() + `

for v in [
` + in.String() + `]:
    try:
        check(Msg(domain=v))
        print("accept")
    except ProtoError as e:
        text = str(e)
        print("a public suffix" if text.endswith("a public suffix") else "not a domain name" if text.endswith("not a domain name") else text)
for v in [
` + addr.String() + `]:
    try:
        check(Msg(subjects=["ok", v]))
        print(False)
    except ProtoError as e:
        assert "subjects[1]" in str(e) and "address-shaped" in str(e), str(e)
        print(True)
check(Msg())  # empty values are not checked
`
	dir := t.TempDir()
	src := filepath.Join(dir, "check.py")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("python3", "-I", src).CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	var want []string
	for _, v := range publicsuffix.Vectors {
		want = append(want, v.Outcome())
	}
	for _, v := range publicsuffix.AddressVectors {
		if v.Refused {
			want = append(want, "True")
		} else {
			want = append(want, "False")
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outcomes differ\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The field checks the Python validation emitter writes for the three
// annotations, the helpers appended only to a file that uses them, and the
// shapes it refuses at generation.
func TestPythonValidationEmitsSignInAsserts(t *testing.T) {
	enums := []model.Enum{{Name: "IdpProtocol", Values: []model.EnumValue{
		{Name: "IDP_PROTOCOL_UNSPECIFIED", Number: 0}, {Name: "IDP_PROTOCOL_OIDC", Number: 1}, {Name: "IDP_PROTOCOL_SAML", Number: 2}}}}
	notIn := model.AnnotationValue{Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2}}
	optProtocol := pySignInField("opt_protocol", "IdpProtocol", false, "assert_enum_not_in", notIn)
	optProtocol.IsOptional = true
	msg := model.Message{Name: "Conf", Fields: []model.Field{
		pySignInField("domain", "string", false, "assert_registrable_domain", pyBoolTrue),
		pySignInField("domains", "string", true, "assert_registrable_domain", pyBoolTrue),
		pySignInField("subject", "string", false, "assert_not_address", pyBoolTrue),
		pySignInField("subjects", "string", true, "assert_not_address", pyBoolTrue),
		pySignInField("protocol", "IdpProtocol", false, "assert_enum_not_in", notIn),
		pySignInField("protocols", "IdpProtocol", true, "assert_enum_not_in", notIn),
		optProtocol,
	}}
	blocks, err := buildValidationBlocks(msg, enums)
	if err != nil {
		t.Fatal(err)
	}
	var code strings.Builder
	for _, bl := range blocks {
		code.WriteString(bl.Code)
	}
	for _, want := range []string{
		"    if msg.domain:\n        _validate_registrable_domain(msg.domain, 'domain')",
		"    for i, val in enumerate(msg.domains):\n        if val:\n            _validate_registrable_domain(val, f\"domains[{i}]\")",
		"    if msg.subject:\n        _validate_not_address(msg.subject, 'subject')",
		"_validate_not_address(val, f\"subjects[{i}]\")",
		"    if int(msg.protocol) == 2:\n        raise invalid_argument(\"field protocol must not be IDP_PROTOCOL_SAML\")",
		"        if int(val) == 2:\n            raise invalid_argument(f\"field protocols[{i}] must not be IDP_PROTOCOL_SAML\")",
		"    if msg.HasField('opt_protocol') and int(msg.opt_protocol) == 2:",
	} {
		if !strings.Contains(code.String(), want) {
			t.Errorf("generated validation lacks %q\n%s", want, code.String())
		}
	}
	if strings.Contains(code.String(), "ValueError") {
		t.Errorf("generated validation raises ValueError\n%s", code.String())
	}

	helpers := pyPublicSuffixHelpers()
	if strings.Contains(helpers, "raise ValueError") || !strings.Contains(helpers, `    "co.uk",`) || !strings.Contains(helpers, publicsuffix.Version()) {
		t.Errorf("helpers: want invalid_argument only and the table at version %s", publicsuffix.Version())
	}
	var withHelpers, without strings.Builder
	if err := writeValidation(&withHelpers, model.ProtoFile{Messages: []model.Message{msg}, Enums: enums}); err != nil {
		t.Fatal(err)
	}
	plain := model.Message{Name: "Plain", Fields: []model.Field{{Name: "n", Type: "string"}}}
	if err := writeValidation(&without, model.ProtoFile{Messages: []model.Message{plain}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withHelpers.String(), "_PUBLIC_SUFFIX_RULES = frozenset((") {
		t.Errorf("a file using assert_registrable_domain lacks the helpers")
	}
	if strings.Contains(without.String(), "_PUBLIC_SUFFIX_RULES") {
		t.Errorf("a file not using the annotations carries the table")
	}

	for _, bad := range []model.Field{
		pySignInField("n", "int64", false, "assert_registrable_domain", pyBoolTrue),
		pySignInField("n", "bytes", true, "assert_not_address", pyBoolTrue),
		{Name: "m", Type: "string", IsMap: true, MapValueType: "string", Annotations: map[string]model.AnnotationValue{"assert_not_address": pyBoolTrue}},
		pySignInField("n", "string", false, "assert_enum_not_in", model.AnnotationValue{Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{1}}),
		pySignInField("e", "E", false, "assert_enum_not_in", model.AnnotationValue{Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{3, 0}}),
	} {
		if _, err := buildValidationBlocks(model.Message{Name: "Bad", Fields: []model.Field{bad}}, nil); err == nil {
			t.Errorf("field %+v: no generation error", bad)
		}
	}
}
