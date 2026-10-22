// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// (spec.assert_required) is documented as "must not be empty/zero value" for
// scalars and "at least one element" for repeated fields, but the Go emitter
// hand-rolled its own type ladder instead of the requiredZeroCheck one the
// conditional family uses. It emitted `m.<F> <= 0` for any field whose declared
// type is numeric — including a REPEATED numeric, where the comparison is against
// a slice — and `m.<F> == ""` for a proto3-optional string, where the field is a
// *string. Both are generated Go that does not compile. Enum and map fields fell
// off the ladder entirely and emitted nothing at all: a required field silently
// unenforced.
func TestGoAssertRequiredZeroCheckIsTypeCorrect(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			assertRequiredField(model.Field{Name: "tls_cert", Type: "string"}),
			assertRequiredField(model.Field{Name: "tls_port", Type: "int32"}),
			assertRequiredField(model.Field{Name: "handshake_timeout", Type: "double"}),
			assertRequiredField(model.Field{Name: "verify_peer", Type: "bool"}),
			assertRequiredField(model.Field{Name: "ca_bundle", Type: "bytes"}),
			assertRequiredField(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
			assertRequiredField(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
			assertRequiredField(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
			assertRequiredField(model.Field{Name: "ports", Type: "int32", IsRepeated: true}),
			assertRequiredField(model.Field{Name: "flags", Type: "bool", IsRepeated: true}),
			assertRequiredField(model.Field{Name: "labels", Type: "string", IsMap: true}),
			assertRequiredField(model.Field{Name: "sni", Type: "string", IsOptional: true}),
			assertRequiredField(model.Field{Name: "backlog", Type: "int32", IsOptional: true}),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":           `if m.TlsCert == "" {`,
		"int32":            "if m.TlsPort == 0 {",
		"double":           "if m.HandshakeTimeout == 0 {",
		"bool":             "if !m.VerifyPeer {",
		"bytes":            "if len(m.CaBundle) == 0 {",
		"enum":             "if m.Mode == 0 {",
		"message":          "if m.Retry == nil {",
		"repeated string":  "if len(m.Peers) == 0 {",
		"repeated numeric": "if len(m.Ports) == 0 {",
		"repeated bool":    "if len(m.Flags) == 0 {",
		"map":              "if len(m.Labels) == 0 {",
		"optional string":  "if m.Sni == nil {",
		"optional numeric": "if m.Backlog == nil {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: missing %q in generated validation:\n%s", kind, snippet, rendered)
		}
	}

	// The specific breakages: a slice/pointer compared with a scalar operator, and
	// `<= 0` over-rejecting a legitimately-set negative number.
	for _, bad := range []string{
		"m.Ports <= 0", "m.Ports == 0", "!m.Flags",
		`m.Sni == ""`, "m.Backlog <= 0", "m.Backlog == 0",
		"m.TlsPort <= 0", "m.HandshakeTimeout <= 0",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Go contains type-incorrect assert_required fragment %q:\n%s", bad, rendered)
		}
	}
}

// assert_required on an enum or map field used to emit no check whatsoever, so
// the annotation parsed, generated cleanly and enforced nothing.
func TestGoAssertRequiredIsNotSilentlyDropped(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		field model.Field
		want  string
	}{
		{"enum", model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}, "field Mode is required"},
		{"map", model.Field{Name: "labels", Type: "string", IsMap: true}, "field Labels is required"},
		{"repeated numeric", model.Field{Name: "ports", Type: "int32", IsRepeated: true}, "field Ports is required"},
	} {
		var b strings.Builder
		msg := model.Message{Name: "Cfg", Fields: []model.Field{assertRequiredField(tc.field)}}
		if err := generateValidationHelpers(&b, msg); err != nil {
			t.Fatalf("%s: generateValidationHelpers: %v", tc.kind, err)
		}
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("%s field: assert_required emitted no check (missing %q):\n%s", tc.kind, tc.want, b.String())
		}
	}
}

func assertRequiredField(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	return f
}
