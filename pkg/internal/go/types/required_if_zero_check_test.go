// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// requiredZeroCheck used to return `m.<F> == ""` for every non-message,
// non-repeated field, so a conditional requirement on a numeric, bool, bytes,
// enum or proto3-optional field emitted generated Go that does not compile
// (`m.Timeout == ""` against an int32). Every existing fixture use of the family
// sits on a string field, which is why it never surfaced in a gate.
func TestGoConditionalRequirementZeroCheckIsTypeCorrect(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			{Name: "enable_tls", Type: "bool"},
			requiredIfTrueField(model.Field{Name: "tls_cert", Type: "string"}),
			requiredIfTrueField(model.Field{Name: "tls_port", Type: "int32"}),
			requiredIfTrueField(model.Field{Name: "handshake_timeout", Type: "double"}),
			requiredIfTrueField(model.Field{Name: "verify_peer", Type: "bool"}),
			requiredIfTrueField(model.Field{Name: "ca_bundle", Type: "bytes"}),
			requiredIfTrueField(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
			requiredIfTrueField(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
			requiredIfTrueField(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
			requiredIfTrueField(model.Field{Name: "labels", Type: "string", IsMap: true}),
			requiredIfTrueField(model.Field{Name: "sni", Type: "string", IsOptional: true}),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":   `if m.EnableTls && m.TlsCert == "" {`,
		"int32":    "if m.EnableTls && m.TlsPort == 0 {",
		"double":   "if m.EnableTls && m.HandshakeTimeout == 0 {",
		"bool":     "if m.EnableTls && !m.VerifyPeer {",
		"bytes":    "if m.EnableTls && len(m.CaBundle) == 0 {",
		"enum":     "if m.EnableTls && m.Mode == 0 {",
		"message":  "if m.EnableTls && m.Retry == nil {",
		"repeated": "if m.EnableTls && len(m.Peers) == 0 {",
		"map":      "if m.EnableTls && len(m.Labels) == 0 {",
		"optional": "if m.EnableTls && m.Sni == nil {",
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: missing %q in generated validation", kind, snippet)
		}
	}

	// The specific breakage: a string comparison against a non-string field.
	for _, bad := range []string{
		`m.TlsPort == ""`, `m.HandshakeTimeout == ""`, `m.VerifyPeer == ""`,
		`m.CaBundle == ""`, `m.Mode == ""`, `m.Peers == ""`, `m.Labels == ""`, `m.Sni == ""`,
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Go compares a non-string field to a string literal: %q", bad)
		}
	}
}

func requiredIfTrueField(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"required_if_true": {Type: model.AnnotationTypeString, StringValue: "enable_tls"},
	}
	return f
}
