// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func rustSanitizeAnn(mode string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}}
}

func TestRustSanitize_RecursiveShape(t *testing.T) {
	address := model.Message{Name: "Address", Fields: []model.Field{
		{Name: "street", Type: "string", Number: 1, Annotations: rustSanitizeAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "email", Type: "string", Number: 2, Annotations: rustSanitizeAnn("may_anonymize")},
		{Name: "ssn", Type: "string", Number: 4, Annotations: rustSanitizeAnn("drop")},
		{Name: "home", Type: "Address", Number: 3, IsMessage: true},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, address}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	n := &rustSanitizeNeeds{}
	generateRustSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), n)
	got := b.String()

	if !n.str {
		t.Error("expected n.str=true (email here has no format annotation)")
	}
	for _, want := range []string{
		"pub fn sanitize_agent_event(m: &AgentEvent, ent: Option<&AgentEventEntitlement>, hmac_key: &[u8]) -> AgentEvent {",
		"let mut out = m.clone();",
		"match ent.map(|e| e.email).unwrap_or(0) {",
		"3 => {} // RAW — explicit caller grant", // fail-closed: only RAW egresses the value
		"out.email = tacplus_pseudonymize(&out.email, hmac_key);",
		"out.ssn = Default::default();",
		"out.ssn_redacted = true;",
		// recursion is unconditional on the sub-entitlement (None sub = protective)
		"if let Some(v) = out.home.as_ref() {",
		"out.home = Some(sanitize_address(v, ent.and_then(|e| e.home_sub.as_ref()), hmac_key));",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A repeated message field clears as an empty Vec and recurses element-wise.
func TestRustSanitize_RepeatedMessageElementWise(t *testing.T) {
	contact := model.Message{Name: "Contact", Fields: []model.Field{
		{Name: "phone", Type: "string", Number: 1, Annotations: rustSanitizeAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "contacts", Type: "Contact", Number: 2, IsMessage: true, IsRepeated: true, Annotations: rustSanitizeAnn("may_redact")},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, contact}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	generateRustSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), &rustSanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"out.contacts = Vec::new();", // whole-list clear
		"if ent.map(|e| e.contacts).unwrap_or(0) != 3 {", // fail-closed: cleared unless RAW-granted
		"let sub = ent.and_then(|e| e.contacts_sub.as_ref());",
		"for v in out.contacts.iter_mut() {",
		"*v = sanitize_contact(v, sub, hmac_key);",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A message-typed ONEOF member recurses through the prost oneof enum: the
// sanitized copy is re-wrapped into the member's variant. Scalar oneof members
// are untouched.
func TestRustSanitize_OneofMemberRecursion(t *testing.T) {
	item := model.Message{Name: "Item", Fields: []model.Field{
		{Name: "notes", Type: "string", Number: 1, Annotations: rustSanitizeAnn("may_redact")},
	}}
	envelope := model.Message{Name: "WatchEvent", Fields: []model.Field{
		{Name: "snapshot_item", Type: "Item", Number: 1, IsMessage: true, IsOneOf: true, OneOfName: "event"},
		{Name: "snapshot_done", Type: "int64", Number: 2, IsOneOf: true, OneOfName: "event"},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{envelope, item}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	generateRustSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), &rustSanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"if let Some(watch_event::Event::SnapshotItem(v)) = out.event.as_ref() {",
		"out.event = Some(watch_event::Event::SnapshotItem(sanitize_item(v, ent.and_then(|e| e.snapshot_item_sub.as_ref()), hmac_key)));",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SnapshotDone") {
		t.Errorf("scalar oneof member must be untouched:\n%s", got)
	}
}
