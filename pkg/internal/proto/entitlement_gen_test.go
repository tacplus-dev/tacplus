// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

func redactAnn(mode string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}}
}

// A record with a may_anonymize leaf, an is_* leaf, and a message-typed field whose
// nested type is itself redactable exercises every emission rule.
func TestEmitEntitlementMessage_Shape(t *testing.T) {
	address := model.Message{Name: "Address", Fields: []model.Field{
		{Name: "street", Type: "string", Number: 1, Annotations: redactAnn("may_redact")},
		{Name: "zip", Type: "string", Number: 2},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "event_id", Type: "string", Number: 1},
		{Name: "email", Type: "string", Number: 2, Annotations: redactAnn("may_anonymize")},
		{Name: "ssn", Type: "string", Number: 4, Annotations: redactAnn("drop")}, // is_* → no caller field
		{Name: "home", Type: "Address", Number: 3, IsMessage: true},              // recurse into Address
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, address}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	usesLevel := emitEntitlementMessage(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r))
	got := b.String()

	if !usesLevel {
		t.Error("a may_anonymize field emits an EntitlementLevel, so usesLevel must be true")
	}
	want := []string{
		"message AgentEventEntitlement {",
		"entitlement.EntitlementLevel email = 2;", // may_anonymize leaf, mirrors source #2
		"AddressEntitlement home_sub = 50003;",    // recursion, 50000 + source #3
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	// is_* (ssn/drop) is always applied → no caller-settable field.
	if strings.Contains(got, "ssn") {
		t.Errorf("is_* field must not appear in the entitlement (no caller choice):\n%s", got)
	}
	// event_id is unannotated → absent.
	if strings.Contains(got, "event_id") {
		t.Errorf("unannotated field must not appear:\n%s", got)
	}
}

// A record with ONLY hard-floor (is-*) fields yields an empty <T>Entitlement that
// references no EntitlementLevel — so usesLevel is false and the caller must NOT
// add the (then-unused) sys/entitlement.proto import.
func TestEmitEntitlementMessage_IsStarOnlyNeedsNoImport(t *testing.T) {
	event := model.Message{Name: "FixtureEvent", Fields: []model.Field{
		{Name: "payload", Type: "string", Number: 3, Annotations: redactAnn("drop")},
		{Name: "email", Type: "string", Number: 6, Annotations: redactAnn("pseudonymize")},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	usesLevel := emitEntitlementMessage(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r))
	got := b.String()
	if usesLevel {
		t.Errorf("is-* only message must not emit EntitlementLevel (usesLevel=false):\n%s", got)
	}
	if strings.Contains(got, "EntitlementLevel") {
		t.Errorf("no EntitlementLevel field expected:\n%s", got)
	}
	if !strings.Contains(got, "message FixtureEventEntitlement {") {
		t.Errorf("the (empty) entitlement message must still be emitted:\n%s", got)
	}
}
