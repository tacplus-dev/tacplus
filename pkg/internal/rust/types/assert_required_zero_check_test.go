// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// The Rust emitter enforced (spec.assert_required) only when the declared field
// type was literally "string". Every other shape — numeric, bool, bytes, enum,
// message, proto3-optional — parsed, generated cleanly and emitted NOTHING, so a
// Rust service accepted requests its Go sibling rejects from the same proto.
// spec.proto documents the annotation for scalars and repeated fields alike.
func TestRustAssertRequiredZeroCheckIsTypeCorrect(t *testing.T) {
	fields := []model.Field{
		rustAssertRequiredField(model.Field{Name: "tls_cert", Type: "string"}),
		rustAssertRequiredField(model.Field{Name: "tls_port", Type: "int32"}),
		rustAssertRequiredField(model.Field{Name: "verify_peer", Type: "bool"}),
		rustAssertRequiredField(model.Field{Name: "ca_bundle", Type: "bytes"}),
		rustAssertRequiredField(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
		rustAssertRequiredField(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
		rustAssertRequiredField(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
		rustAssertRequiredField(model.Field{Name: "labels", Type: "string", IsMap: true}),
		rustAssertRequiredField(model.Field{Name: "sni", Type: "string", IsOptional: true}),
	}
	msg := model.Message{Name: "ConnConfig", Fields: fields}

	var b strings.Builder
	for _, f := range fields {
		if err := generateFieldValidation(&b, f, msg, nil); err != nil {
			t.Fatalf("generateFieldValidation(%s): %v", f.Name, err)
		}
	}
	rendered := b.String()

	want := map[string]string{
		"string":   "if self.tls_cert.is_empty() {",
		"int32":    "if self.tls_port == 0 {",
		"bool":     "if !self.verify_peer {",
		"bytes":    "if self.ca_bundle.is_empty() {",
		"enum":     "if self.mode == 0 {",
		"message":  "if self.retry.is_none() {",
		"repeated": "if self.peers.is_empty() {",
		"map":      "if self.labels.is_empty() {",
		"optional": "if self.sni.is_none() {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: assert_required missing %q in generated validation:\n%s", kind, snippet, rendered)
		}
	}

	// The old string-only ladder dropped these entirely; .is_empty() on a numeric,
	// bool, enum or Option<T> would not compile either.
	for _, bad := range []string{
		"self.tls_port.is_empty()", "self.verify_peer.is_empty()",
		"self.mode.is_empty()", "self.retry.is_empty()", "self.sni.is_empty()",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Rust calls .is_empty() on a type without it: %q", bad)
		}
	}
}

func rustAssertRequiredField(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	return f
}
