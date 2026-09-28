// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Like Rust, the Java emitter enforced (spec.assert_required) only for a field
// whose declared type was literally "string". Numeric, bool, bytes, enum, message,
// map and proto3-optional fields parsed, generated cleanly and emitted no check —
// a fail-open validation divergence from Go on the same proto.
func TestJavaAssertRequiredCoversEveryFieldShape(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			javaAssertRequired(model.Field{Name: "tls_cert", Type: "string"}),
			javaAssertRequired(model.Field{Name: "tls_port", Type: "int32"}),
			javaAssertRequired(model.Field{Name: "verify_peer", Type: "bool"}),
			javaAssertRequired(model.Field{Name: "ca_bundle", Type: "bytes"}),
			javaAssertRequired(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
			javaAssertRequired(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
			javaAssertRequired(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
			javaAssertRequired(model.Field{Name: "labels", Type: "string", IsMap: true}),
			javaAssertRequired(model.Field{Name: "sni", Type: "string", IsOptional: true}),
		},
	}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	want := map[string]string{
		"string":   `if (msg.getTlsCert().isEmpty()) {`,
		"numeric":  `if (msg.getTlsPort() == 0) {`,
		"bool":     `if (!msg.getVerifyPeer()) {`,
		"bytes":    `if (msg.getCaBundle().isEmpty()) {`,
		"enum":     `if (msg.getModeValue() == 0) {`,
		"message":  `if (!msg.hasRetry()) {`,
		"repeated": `if (msg.getPeersCount() == 0) {`,
		"map":      `if (msg.getLabelsMap().isEmpty()) {`,
		"optional": `if (!msg.hasSni()) {`,
	}
	for kind, snippet := range want {
		if !strings.Contains(rendered, snippet) {
			t.Errorf("%s field: assert_required missing %q in generated validation:\n%s", kind, snippet, rendered)
		}
	}

	// isEmpty() exists on String/ByteString/Map but not on an int, boolean or enum.
	for _, bad := range []string{
		`msg.getTlsPort().isEmpty()`, `msg.getVerifyPeer().isEmpty()`, `msg.getMode().isEmpty()`,
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Java calls isEmpty() on a type without it: %q", bad)
		}
	}
}

func javaAssertRequired(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	return f
}
