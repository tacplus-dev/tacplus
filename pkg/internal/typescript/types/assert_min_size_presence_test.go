// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// (spec.assert_min_size) on a string/bytes field is a MINIMUM byte length, so the
// unset value — "" / an empty Uint8Array, measured as length 0 — is exactly the
// case it exists to reject. Every other target measures it that way: go
// `len(m.X) < N`, java `getX().getBytes(UTF_8).length < N`, python
// `len(msg.x.encode('utf-8')) < N`, rust `self.x.len() < N`.
//
// TypeScript guarded the comparison with `if (m.x && ...)`, copied from the
// assert_max_size branch above it where a truthiness guard is harmless (an absent
// value cannot exceed a maximum). On a minimum it inverts the annotation: the
// emptier the value, the more likely it passes, so a client could send "" for a
// field every other language rejects. The same emitter's repeated/map branches
// already use `!m.x || ...` and reject the absent collection, so a single
// assert_min_size case disagreed with itself.
//
// Presence is honoured only where go and rust honour it: a proto3 `optional`
// field that is undefined carries no value to measure.
func TestTypeScriptMinSizeMeasuresEmptyValues(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			tsMinSize(model.Field{Name: "tenant_id", Type: "string"}, 5),
			tsMinSize(model.Field{Name: "ca_bundle", Type: "bytes"}, 4),
			tsMinSize(model.Field{Name: "sni", Type: "string", IsOptional: true}, 3),
			tsMinSize(model.Field{Name: "token", Type: "bytes", IsOptional: true}, 2),
			tsMinSize(model.Field{Name: "peers", Type: "string", IsRepeated: true}, 1),
			tsMinSize(model.Field{Name: "labels", Type: "string", IsMap: true}, 1),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":          "if (new TextEncoder().encode(m.tenantId).length < 5) {",
		"bytes":           "if (m.caBundle.length < 4) {",
		"optional string": "if (m.sni !== undefined && new TextEncoder().encode(m.sni).length < 3) {",
		"optional bytes":  "if (m.token !== undefined && m.token.length < 2) {",
		"repeated":        "if (!m.peers || m.peers.length < 1) {",
		"map":             "if (!m.labels || Object.keys(m.labels).length < 1) {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: assert_min_size missing %q in generated validation:\n%s", kind, snippet, rendered)
		}
	}

	// A truthiness guard on a MINIMUM lets the empty value — the smallest one
	// there is — skip the check entirely. (The same guard on the assert_max_size
	// checks this message also emits is fine, so match the `< N` comparison.)
	for _, bad := range []string{
		"if (m.tenantId && new TextEncoder().encode(m.tenantId).length < 5) {",
		"if (m.caBundle && m.caBundle.length < 4) {",
		"if (m.sni && new TextEncoder().encode(m.sni).length < 3) {",
		"if (m.token && m.token.length < 2) {",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated TypeScript guards assert_min_size behind truthiness (%q), so the empty value "+
				"passes a minimum every other target rejects:\n%s", bad, rendered)
		}
	}
}

func tsMinSize(f model.Field, n int64) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_min_size": {Type: model.AnnotationTypeInt, IntValue: n},
	}
	return f
}
