// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The generated Build<T>Entitlement must be fail-closed: only a matching data
// grant sets RAW; everything else stays UNSPECIFIED (0). Nested sub-entitlements
// are constructed inline (never via a cross-call, which would loop forever on
// mutually recursive record types), and hard-floor fields get no grant check.
func TestGenerateEntitlementBuilder_FailClosedShape(t *testing.T) {
	contact := model.Message{Name: "Contact", Fields: []model.Field{
		{Name: "phone", Type: "string", Number: 1, Annotations: sanitizeRedactAnn("may_redact")},
	}}
	event := model.Message{Name: "AgentEvent", Fields: []model.Field{
		{Name: "email", Type: "string", Number: 2, Annotations: sanitizeRedactAnn("may_anonymize")},
		{Name: "ssn", Type: "string", Number: 4, Annotations: sanitizeRedactAnn("drop")},
		{Name: "contact", Type: "Contact", Number: 3, IsMessage: true},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{event, contact}}}
	r := parser.NewMessageResolver(files)

	var b strings.Builder
	writeGoEntitlementBuilder(&b, parser.BuildEntitlementTree(&files[0].Messages[0], "demo", r))
	got := b.String()

	for _, want := range []string{
		"func BuildAgentEventEntitlement(dataGrants map[string]bool) *AgentEventEntitlement {",
		"ent := &AgentEventEntitlement{}",
		`if dataGrants["DEMO_AGENT_EVENT_EMAIL"] {`,
		"ent.Email = 3 // ENTITLEMENT_LEVEL_RAW",
		"ent.ContactSub = &ContactEntitlement{}", // inline construction, no cross-call
		`if dataGrants["DEMO_CONTACT_PHONE"] {`,  // grant names the OWNING message
		"ent.ContactSub.Phone = 3 // ENTITLEMENT_LEVEL_RAW",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// A hard-floor (drop) field is caller-independent — no grant can loosen it.
	if strings.Contains(got, "SSN") || strings.Contains(got, "ent.Ssn") {
		t.Errorf("hard-floor field must have no grant check:\n%s", got)
	}
	// Nested builders must be inline — a Build<Sub> cross-call would loop forever
	// on mutually recursive record types.
	if strings.Contains(got, "BuildContactEntitlement(") {
		t.Errorf("nested sub-entitlement must be constructed inline, not via cross-call:\n%s", got)
	}
}
