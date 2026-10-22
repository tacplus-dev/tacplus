// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Java implemented none of the conditional-requirement family
// ((spec.required_if_true) / (spec.required_if_false) / (spec.required_if_eq)),
// while go, rust, python and typescript all enforce it. The annotations parse,
// generate cleanly and produce nothing in Java: a Java service accepted requests
// its go/rust siblings reject from the same proto — a fail-open validation
// divergence, not a missing nicety.
func TestJavaEmitsConditionalRequirementValidation(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			{Name: "enable_tls", Type: "bool"},
			{Name: "mode", Type: "TlsMode", IsEnum: true},
			javaRequiredIf(model.Field{Name: "tls_cert", Type: "string"}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "tls_port", Type: "int32"}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "ca_bundle", Type: "bytes"}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "peers", Type: "string", IsRepeated: true}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "labels", Type: "string", IsMap: true}, "required_if_true", "enable_tls"),
			javaRequiredIf(model.Field{Name: "plain_port", Type: "int32"}, "required_if_false", "enable_tls"),
			javaRequiredIf(model.Field{Name: "client_cert", Type: "string"}, "required_if_eq", "mode=TLS_MODE_MUTUAL"),
		},
	}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":              `if (msg.getEnableTls() && msg.getTlsCert().isEmpty()) {`,
		"numeric":             `if (msg.getEnableTls() && msg.getTlsPort() == 0) {`,
		"bytes":               `if (msg.getEnableTls() && msg.getCaBundle().isEmpty()) {`,
		"message":             `if (msg.getEnableTls() && !msg.hasRetry()) {`,
		"repeated":            `if (msg.getEnableTls() && msg.getPeersCount() == 0) {`,
		"map":                 `if (msg.getEnableTls() && msg.getLabelsMap().isEmpty()) {`,
		"required_if_false":   `if (!msg.getEnableTls() && msg.getPlainPort() == 0) {`,
		"required_if_eq enum": `if ("TLS_MODE_MUTUAL".equals(msg.getMode().name()) && msg.getClientCert().isEmpty()) {`,
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s: missing %q in generated Java validation\n--- rendered ---\n%s", kind, snippet, rendered)
		}
	}

	for _, msgText := range []string{
		`"field tlsCert is required when enable_tls is true"`,
		`"field plainPort is required when enable_tls is false"`,
		`"field clientCert is required when mode is TLS_MODE_MUTUAL"`,
	} {
		if !strings.Contains(rendered, msgText) {
			t.Errorf("missing operator-facing message %s", msgText)
		}
	}
}

func javaRequiredIf(f model.Field, annotation, value string) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		annotation: {Type: model.AnnotationTypeString, StringValue: value},
	}
	return f
}
