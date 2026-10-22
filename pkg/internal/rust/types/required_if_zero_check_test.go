// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// The conditional-requirement family emitted an unconditional `.is_empty()` on
// the annotated field, which compiles only for String / Vec / HashMap. A
// required_if_* on a numeric, bool, enum, message or proto3-optional field
// produced generated Rust that does not build. Every existing fixture use of the
// family sits on a string field, which is why no gate caught it.
func TestRustConditionalRequirementZeroCheckIsTypeCorrect(t *testing.T) {
	fields := []model.Field{
		{Name: "enable_tls", Type: "bool"},
		rustRequiredIfTrueField(model.Field{Name: "tls_cert", Type: "string"}),
		rustRequiredIfTrueField(model.Field{Name: "tls_port", Type: "int32"}),
		rustRequiredIfTrueField(model.Field{Name: "verify_peer", Type: "bool"}),
		rustRequiredIfTrueField(model.Field{Name: "ca_bundle", Type: "bytes"}),
		rustRequiredIfTrueField(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
		rustRequiredIfTrueField(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
		rustRequiredIfTrueField(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
		rustRequiredIfTrueField(model.Field{Name: "sni", Type: "string", IsOptional: true}),
	}
	msg := model.Message{Name: "ConnConfig", Fields: fields}

	var b strings.Builder
	for _, f := range fields {
		if len(f.Annotations) == 0 {
			continue
		}
		if err := generateFieldValidation(&b, f, msg, nil, false); err != nil {
			t.Fatalf("generateFieldValidation(%s): %v", f.Name, err)
		}
	}
	rendered := b.String()

	want := map[string]string{
		"string":   "if self.enable_tls && self.tls_cert.is_empty() {",
		"int32":    "if self.enable_tls && self.tls_port == 0 {",
		"bool":     "if self.enable_tls && !self.verify_peer {",
		"bytes":    "if self.enable_tls && self.ca_bundle.is_empty() {",
		"enum":     "if self.enable_tls && self.mode == 0 {",
		"message":  "if self.enable_tls && self.retry.is_none() {",
		"repeated": "if self.enable_tls && self.peers.is_empty() {",
		"optional": "if self.enable_tls && self.sni.is_none() {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: missing %q in generated validation", kind, snippet)
		}
	}

	// The specific breakage: .is_empty() on a type that has no such method.
	for _, bad := range []string{
		"self.tls_port.is_empty()", "self.verify_peer.is_empty()",
		"self.mode.is_empty()", "self.retry.is_empty()", "self.sni.is_empty()",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Rust calls .is_empty() on a type that does not have it: %q", bad)
		}
	}
}

func rustRequiredIfTrueField(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"required_if_true": {Type: model.AnnotationTypeString, StringValue: "enable_tls"},
	}
	return f
}
