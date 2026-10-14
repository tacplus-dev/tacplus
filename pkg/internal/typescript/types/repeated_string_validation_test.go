// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// The string-format assert_* validators apply per ELEMENT on a repeated field:
// go loops `for _, v := range m.X { if v != "" { ... } }`, java
// `for (String val : msg.getXList())`, rust `for val in &self.x`, python
// `for val in msg.x`. TypeScript's assert_email / assert_fqdn / assert_host /
// assert_base64 / assert_semver cases ignored field.IsRepeated entirely and
// emitted the scalar form, so a `repeated string` field rendered
// `emailRegex.test(m.contactEmails)` — a `string[]` handed to a `string`
// parameter, which does not type-check, and would silently regex the
// comma-joined array if it ever ran. The sibling assert_listen_addr /
// assert_file_path / assert_url / assert_enc_type / assert_ipaddr_type cases in
// this same emitter already branch on IsRepeated, so the emitter disagreed with
// itself field-by-field.
func TestTypeScriptStringValidatorsLoopOverRepeatedFields(t *testing.T) {
	msg := model.Message{
		Name: "ContactBook",
		Fields: []model.Field{
			tsRepeatedAssert("contact_emails", "assert_email"),
			tsRepeatedAssert("domains", "assert_fqdn"),
			tsRepeatedAssert("peers", "assert_host"),
			tsRepeatedAssert("signatures", "assert_base64"),
			tsRepeatedAssert("versions", "assert_semver"),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"assert_email":  "for (let i = 0; i < m.contactEmails.length; i++) {",
		"assert_fqdn":   "for (let i = 0; i < m.domains.length; i++) {",
		"assert_host":   "for (let i = 0; i < m.peers.length; i++) {",
		"assert_base64": "for (let i = 0; i < m.signatures.length; i++) {",
		"assert_semver": "for (let i = 0; i < m.versions.length; i++) {",
	}
	for ann, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s on a repeated field: missing per-element loop %q in generated validation:\n%s",
				ann, snippet, rendered)
		}
	}

	// The exact pre-fix fragments: the regex applied to the array itself.
	for _, bad := range []string{
		"emailRegex.test(m.contactEmails)",
		"fqdnRegex.test(m.domains)",
		"ipv4Regex.test(m.peers)",
		"base64Regex.test(m.signatures)",
		"semverRegex.test(m.versions)",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated TypeScript applies a string regex to the whole repeated field (%q), "+
				"which does not type-check and validates the joined array rather than each element:\n%s",
				bad, rendered)
		}
	}

	// Per-element failures must name the offending index, matching the
	// assert_listen_addr / assert_enum_defined repeated branches.
	for _, idx := range []string{
		"field contactEmails[${i}]",
		"field domains[${i}]",
		"field peers[${i}]",
		"field signatures[${i}]",
		"field versions[${i}]",
	} {
		if !strings.Contains(rendered, idx) {
			t.Errorf("repeated validation error message missing element index %q:\n%s", idx, rendered)
		}
	}
}

// The scalar form must stay scalar — the fix is a branch, not a replacement.
func TestTypeScriptStringValidatorsKeepScalarForm(t *testing.T) {
	msg := model.Message{
		Name: "Contact",
		Fields: []model.Field{
			tsScalarAssert("contact_email", "assert_email"),
			tsScalarAssert("domain", "assert_fqdn"),
			tsScalarAssert("peer", "assert_host"),
			tsScalarAssert("signature", "assert_base64"),
			tsScalarAssert("version", "assert_semver"),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, snippet := range []string{
		"if (!emailRegex.test(m.contactEmail)) {",
		"if (!fqdnRegex.test(m.domain)) {",
		"if (!ipv4Regex.test(m.peer) && !ipv6Regex.test(m.peer) && !fqdnRegex.test(m.peer)) {",
		"if (!base64Regex.test(m.signature)) {",
		"if (!semverRegex.test(m.version)) {",
	} {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("scalar validation missing %q in generated validation:\n%s", snippet, rendered)
		}
	}
}

func tsRepeatedAssert(name, annotation string) model.Field {
	f := tsScalarAssert(name, annotation)
	f.IsRepeated = true
	return f
}

func tsScalarAssert(name, annotation string) model.Field {
	return model.Field{
		Name: name,
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeBool, BoolValue: true},
		},
	}
}
