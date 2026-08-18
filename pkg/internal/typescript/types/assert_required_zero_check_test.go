// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// The TypeScript requirement family tested `!m.<f>` for every field shape, which
// is JavaScript truthiness rather than a zero test. protobuf-es renders repeated
// fields as arrays, maps as objects and bytes as Uint8Array — all three are TRUTHY
// when empty, so `(spec.assert_required)` on any of them passed an unset field.
// spec.proto documents the repeated case explicitly: "must have at least one
// element".
func TestTypeScriptAssertRequiredZeroCheckIsTypeCorrect(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			tsAssertRequired(model.Field{Name: "tls_cert", Type: "string"}),
			tsAssertRequired(model.Field{Name: "tls_port", Type: "int32"}),
			tsAssertRequired(model.Field{Name: "max_bytes", Type: "int64"}),
			tsAssertRequired(model.Field{Name: "verify_peer", Type: "bool"}),
			tsAssertRequired(model.Field{Name: "ca_bundle", Type: "bytes"}),
			tsAssertRequired(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
			tsAssertRequired(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
			tsAssertRequired(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
			tsAssertRequired(model.Field{Name: "labels", Type: "string", IsMap: true}),
			tsAssertRequired(model.Field{Name: "sni", Type: "string", IsOptional: true}),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":   `if (m.tlsCert === "") {`,
		"numeric":  "if (m.tlsPort === 0) {",
		"bigint":   "if (m.maxBytes === 0n) {",
		"bool":     "if (!m.verifyPeer) {",
		"bytes":    "if (m.caBundle.length === 0) {",
		"enum":     "if (Number(m.mode) === 0) {",
		"message":  "if (m.retry === undefined) {",
		"repeated": "if (m.peers.length === 0) {",
		"map":      "if (Object.keys(m.labels).length === 0) {",
		"optional": "if (m.sni === undefined) {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: assert_required missing %q in generated validation:\n%s", kind, snippet, rendered)
		}
	}

	// An empty array, an empty object and an empty Uint8Array are all truthy, so
	// these fragments read as "required" while enforcing nothing. `m.mode === 0`
	// does not type-check against a protobuf-es enum member.
	for _, bad := range []string{
		"if (!m.peers) {", "if (!m.labels) {", "if (!m.caBundle) {",
		"if (!m.retry) {", "m.mode === 0", "if (!m.maxBytes) {",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated TypeScript contains a truthiness/typing-unsound assert_required fragment %q:\n%s", bad, rendered)
		}
	}
}

func tsAssertRequired(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	return f
}
