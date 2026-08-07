// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func javaSanitizeAnn(mode string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}}
}

func TestJavaSanitize_RecursiveShape(t *testing.T) {
	address := model.Message{Name: "Address", Fields: []model.Field{
		{Name: "street", Type: "string", Number: 1, Annotations: javaSanitizeAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "email", Type: "string", Number: 2, Annotations: javaSanitizeAnn("may_anonymize")},
		{Name: "ssn", Type: "string", Number: 4, Annotations: javaSanitizeAnn("drop")},
		{Name: "home", Type: "Address", Number: 3, IsMessage: true},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, address}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	n := &javaSanitizeNeeds{}
	generateJavaSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), "Demo", n)
	got := b.String()

	if !n.str {
		t.Error("expected n.str=true (email here has no format annotation)")
	}
	for _, want := range []string{
		"public static Demo.AgentEvent sanitizeAgentEvent(Demo.AgentEvent m, Demo.AgentEventEntitlement ent, byte[] hmacKey) {",
		"Demo.AgentEvent.Builder out = m.toBuilder();",
		"int lvl = ent == null ? 0 : ent.getEmailValue();",
		"if (lvl == 3) { /* RAW — explicit caller grant */ }", // fail-closed: only RAW egresses the value
		"out.setEmail(tacplusPseudonymize(out.getEmail(), hmacKey));",
		"out.clearSsn();",
		"out.setSsnRedacted(true);",
		// recursion is unconditional on the sub-entitlement (null sub = protective)
		"if (out.hasHome()) {",
		"out.setHome(sanitizeAddress(out.getHome(), (ent == null ? null : ent.getHomeSub()), hmacKey));",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A repeated message field iterates element-wise by index.
func TestJavaSanitize_RepeatedMessageElementWise(t *testing.T) {
	contact := model.Message{Name: "Contact", Fields: []model.Field{
		{Name: "phone", Type: "string", Number: 1, Annotations: javaSanitizeAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "contacts", Type: "Contact", Number: 2, IsMessage: true, IsRepeated: true, Annotations: javaSanitizeAnn("may_redact")},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, contact}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	generateJavaSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), "Demo", &javaSanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"if ((ent == null ? 0 : ent.getContactsValue()) != 3) {", // fail-closed: cleared unless RAW-granted
		"for (int i = 0; i < out.getContactsCount(); i++) {",
		"out.setContacts(i, sanitizeContact(out.getContacts(i), (ent == null ? null : ent.getContactsSub()), hmacKey));",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A message-typed ONEOF member recurses through the normal per-member
// accessors (hasX/getX/setX set the oneof case in protobuf-java builders).
func TestJavaSanitize_OneofMemberRecursion(t *testing.T) {
	item := model.Message{Name: "Item", Fields: []model.Field{
		{Name: "notes", Type: "string", Number: 1, Annotations: javaSanitizeAnn("may_redact")},
	}}
	envelope := model.Message{Name: "WatchEvent", Fields: []model.Field{
		{Name: "snapshot_item", Type: "Item", Number: 1, IsMessage: true, IsOneOf: true, OneOfName: "event"},
		{Name: "snapshot_done", Type: "int64", Number: 2, IsOneOf: true, OneOfName: "event"},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{envelope, item}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	generateJavaSanitize(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r), "Demo", &javaSanitizeNeeds{})
	got := b.String()
	for _, want := range []string{
		"if (out.hasSnapshotItem()) {",
		"out.setSnapshotItem(sanitizeItem(out.getSnapshotItem(), (ent == null ? null : ent.getSnapshotItemSub()), hmacKey));",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
