// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func sanitizeRedactAnn(mode string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}}
}

// The generated Sanitize<T> must: apply an is-* field unconditionally, gate a may-*
// field on the caller's level, set the injected indicator, and recurse into a
// nested message via the sub-entitlement — exactly like the serialization helpers.
func TestGenerateSanitize_RecursiveShape(t *testing.T) {
	address := model.Message{Name: "Address", Fields: []model.Field{
		{Name: "street", Type: "string", Number: 1, Annotations: sanitizeRedactAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "email", Type: "string", Number: 2, Annotations: sanitizeRedactAnn("may_anonymize")},
		{Name: "ssn", Type: "string", Number: 4, Annotations: sanitizeRedactAnn("drop")},
		{Name: "home", Type: "Address", Number: 3, IsMessage: true},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, address}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	n := &sanitizeNeeds{}
	writeGoSanitizeFunc(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), n)
	got := b.String()

	if !n.str {
		t.Error("expected n.str=true (email here has no format annotation → plain pseudonymize)")
	}
	for _, want := range []string{
		"func SanitizeAgentEvent(m *AgentEvent, ent *AgentEventEntitlement, hmacKey []byte) *AgentEvent {",
		"out := proto.Clone(m).(*AgentEvent)",
		"switch lvl := int32(ent.GetEmail()); {",        // may-* gated on caller level
		"case lvl == 3: // RAW — explicit caller grant", // only RAW egresses the value
		"out.Email = tacplusPseudonymize(out.Email, hmacKey)",
		"out.EmailRedacted = true",
		`out.Ssn = ""`, // is-redact: unconditional
		"out.SsnRedacted = true",
		"if out.GetHome() != nil {", // recursion is unconditional on the sub-entitlement (nil sub = protective)
		"out.Home = SanitizeAddress(out.Home, ent.GetHomeSub(), hmacKey)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// ssn is is-redact → applied unconditionally, NOT behind an ent-level switch.
	if strings.Contains(got, "int32(ent.GetSsn())") {
		t.Errorf("is-* field must not be gated on a caller level:\n%s", got)
	}
}

// A repeated message field must recurse ELEMENT-WISE (a for-range loop calling the
// element type's Sanitize), not treat the slice as a single message.
func TestGenerateSanitize_RepeatedMessageElementWise(t *testing.T) {
	contact := model.Message{Name: "Contact", Fields: []model.Field{
		{Name: "phone", Type: "string", Number: 1, Annotations: sanitizeRedactAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "contacts", Type: "Contact", Number: 2, IsMessage: true, IsRepeated: true},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, contact}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	writeGoSanitizeFunc(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), &sanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"for i := range out.Contacts {",
		"out.Contacts[i] = SanitizeContact(out.Contacts[i], ent.GetContactsSub(), hmacKey)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A message-typed ONEOF member recurses through the oneof representation: the
// sanitized copy is re-wrapped into the member's wrapper struct (the getter is
// nil for any other active variant). Scalar oneof members are untouched.
func TestGenerateSanitize_OneofMemberRecursion(t *testing.T) {
	item := model.Message{Name: "Item", Fields: []model.Field{
		{Name: "notes", Type: "string", Number: 1, Annotations: sanitizeRedactAnn("may_redact")},
	}}
	envelope := model.Message{Name: "WatchEvent", Fields: []model.Field{
		{Name: "snapshot_item", Type: "Item", Number: 1, IsMessage: true, IsOneOf: true, OneOfName: "event"},
		{Name: "snapshot_done", Type: "int64", Number: 2, IsOneOf: true, OneOfName: "event"},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{envelope, item}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	writeGoSanitizeFunc(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), &sanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"if v := out.GetSnapshotItem(); v != nil {",
		"out.Event = &WatchEvent_SnapshotItem{SnapshotItem: SanitizeItem(v, ent.GetSnapshotItemSub(), hmacKey)}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SnapshotDone") {
		t.Errorf("scalar oneof member must be untouched:\n%s", got)
	}
}
