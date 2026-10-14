// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// The Python emitter's assert_required switch listed "string" and four integer
// types and nothing else, so bool, bytes, float/double, enum, message, repeated
// and map fields emitted no check at all. The integer arm was also wrong for a
// REPEATED integer field (`msg.<f> == 0` against a repeated container is always
// False), which is the same silent drop wearing a check's clothes. A singular
// message field needs HasField: protobuf's Python messages define no __bool__, so
// `not msg.<f>` is False even when the sub-message was never set.
func TestPythonAssertRequiredCoversEveryFieldShape(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			pyAssertRequired(model.Field{Name: "tls_cert", Type: "string"}),
			pyAssertRequired(model.Field{Name: "tls_port", Type: "int32"}),
			pyAssertRequired(model.Field{Name: "verify_peer", Type: "bool"}),
			pyAssertRequired(model.Field{Name: "ca_bundle", Type: "bytes"}),
			pyAssertRequired(model.Field{Name: "mode", Type: "TlsMode", IsEnum: true}),
			pyAssertRequired(model.Field{Name: "retry", Type: "RetryConfig", IsMessage: true}),
			pyAssertRequired(model.Field{Name: "peers", Type: "string", IsRepeated: true}),
			pyAssertRequired(model.Field{Name: "labels", Type: "string", IsMap: true}),
			pyAssertRequired(model.Field{Name: "sni", Type: "string", IsOptional: true}),
		},
	}

	blocks, err := buildValidationBlocks(msg, nil)
	if err != nil {
		t.Fatalf("buildValidationBlocks: %v", err)
	}
	var rendered strings.Builder
	for _, block := range blocks {
		rendered.WriteString(block.Code)
	}
	out := rendered.String()

	want := map[string]string{
		"string":   "if not msg.tls_cert:",
		"numeric":  "if not msg.tls_port:",
		"bool":     "if not msg.verify_peer:",
		"bytes":    "if not msg.ca_bundle:",
		"enum":     "if not msg.mode:",
		"message":  `if not msg.HasField("retry"):`,
		"repeated": "if len(msg.peers) == 0:",
		"map":      "if len(msg.labels) == 0:",
		"optional": `if not msg.HasField("sni"):`,
	}
	for kind, snippet := range want {
		if !strings.Contains(out, snippet) {
			t.Errorf("%s field: assert_required missing %q in generated validation:\n%s", kind, snippet, out)
		}
	}

	// A repeated container is never == 0, and a message is always truthy.
	for _, bad := range []string{
		"if msg.peers == 0:", "if msg.tls_port == 0:", "if not msg.retry:",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("generated Python contains an assert_required check that never fires: %q:\n%s", bad, out)
		}
	}
}

func pyAssertRequired(f model.Field) model.Field {
	f.Annotations = map[string]model.AnnotationValue{
		"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	return f
}
