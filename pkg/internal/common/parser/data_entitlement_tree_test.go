// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func redactAnn(mode string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}}
}

func msgField(name, typ string) model.Field {
	return model.Field{Name: name, Type: typ, IsMessage: true}
}

// Field-by-field recursion: Outer has a message-typed field `inner` with NO mode
// of its own, but the referenced Inner carries a redact field. BuildEntitlementTree
// must recurse through `inner` and surface Inner.secret.
func TestBuildEntitlementTree_FieldByFieldRecursion(t *testing.T) {
	inner := model.Message{
		Name: "Inner",
		Fields: []model.Field{
			{Name: "secret", Type: "string", Annotations: redactAnn("drop")},
			{Name: "public", Type: "string"},
		},
	}
	outer := model.Message{
		Name:   "Outer",
		Fields: []model.Field{msgField("inner", "Inner")},
	}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{outer, inner}}}
	r := NewMessageResolver(files)

	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if !tree.IsRelevant() {
		t.Fatal("Outer should be relevant: it reaches Inner.secret through a message-typed field")
	}
	if len(tree.Fields) != 1 {
		t.Fatalf("want 1 entitlement field (inner), got %d", len(tree.Fields))
	}
	inField := tree.Fields[0]
	if inField.Mode != "" {
		t.Errorf("inner itself carries no mode, got %q", inField.Mode)
	}
	if inField.Nested == nil {
		t.Fatal("inner must recurse into Inner's entitlement sub-tree")
	}
	if len(inField.Nested.Fields) != 1 || inField.Nested.Fields[0].Field.Name != "secret" {
		t.Fatalf("Inner sub-tree should surface exactly [secret], got %+v", inField.Nested.Fields)
	}
}

// Whole-message redact: a REDACT mode directly on the message-typed field clears
// the whole sub-message. When the sub-message has no annotations of its own,
// Nested stays nil (nothing to recurse into) but the field is still present.
func TestBuildEntitlementTree_WholeMessageRedact(t *testing.T) {
	inner := model.Message{Name: "Inner", Fields: []model.Field{{Name: "a", Type: "string"}}}
	outer := model.Message{
		Name: "Outer",
		Fields: []model.Field{
			{Name: "inner", Type: "Inner", IsMessage: true, Annotations: redactAnn("drop")},
		},
	}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{outer, inner}}}
	r := NewMessageResolver(files)

	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if len(tree.Fields) != 1 || tree.Fields[0].Mode != "drop" {
		t.Fatalf("want inner with whole-message drop, got %+v", tree.Fields)
	}
	if tree.Fields[0].Nested != nil {
		t.Errorf("Inner has no annotations, so no sub-tree expected; got %+v", tree.Fields[0].Nested)
	}
}

// A self-referential message must not send the walk into an infinite loop.
func TestBuildEntitlementTree_CycleGuard(t *testing.T) {
	node := model.Message{
		Name: "Node",
		Fields: []model.Field{
			{Name: "secret", Type: "string", Annotations: redactAnn("drop")},
			{Name: "next", Type: "Node", IsMessage: true}, // self-reference
		},
	}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{node}}}
	r := NewMessageResolver(files)

	// Must terminate.
	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if !tree.IsRelevant() {
		t.Fatal("Node has a redact field, should be relevant")
	}
	// `next` re-enters Node (already on path) → included but not recursed (Nested nil).
	var nextField *EntitlementField
	for i := range tree.Fields {
		if tree.Fields[i].Field.Name == "next" {
			nextField = &tree.Fields[i]
		}
	}
	if nextField != nil && nextField.Nested != nil {
		t.Error("cycle back-edge must not recurse (Nested must be nil to break the cycle)")
	}
}

// The message-level (spec.data_entitlement) default lands on the node.
func TestBuildEntitlementTree_MessageLevelDefault(t *testing.T) {
	msg := model.Message{
		Name:        "Event",
		Annotations: map[string]model.AnnotationValue{"data_entitlement": {Type: model.AnnotationTypeString, StringValue: "may_redact"}},
		Fields:      []model.Field{{Name: "id", Type: "string"}},
	}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{msg}}}
	r := NewMessageResolver(files)

	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if tree.DefaultMode != "may_redact" {
		t.Fatalf("message-level default should be may_redact, got %q", tree.DefaultMode)
	}
	if !tree.IsRelevant() {
		t.Fatal("a message-level default makes the node relevant")
	}
}

// A message with no annotations anywhere in its reachable tree is not relevant.
func TestBuildEntitlementTree_Irrelevant(t *testing.T) {
	inner := model.Message{Name: "Inner", Fields: []model.Field{{Name: "a", Type: "string"}}}
	outer := model.Message{Name: "Outer", Fields: []model.Field{msgField("inner", "Inner")}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{outer, inner}}}
	r := NewMessageResolver(files)

	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if tree.IsRelevant() {
		t.Fatal("no annotations anywhere → node must be irrelevant")
	}
	if len(tree.Fields) != 0 {
		t.Fatalf("no entitlement fields expected, got %+v", tree.Fields)
	}
}

// A repeated message-typed field recurses ELEMENT-WISE: the walk carries both a
// whole-list redact level (its own mode) AND a nested sub-entitlement applied to
// each element.
func TestBuildEntitlementTree_RepeatedMessageRecursesElementWise(t *testing.T) {
	inner := model.Message{Name: "Inner", Fields: []model.Field{
		{Name: "secret", Type: "string", Annotations: redactAnn("drop")},
	}}
	outer := model.Message{Name: "Outer", Fields: []model.Field{
		{Name: "items", Type: "Inner", IsMessage: true, IsRepeated: true, Annotations: redactAnn("may_redact")},
	}}
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{outer, inner}}}
	r := NewMessageResolver(files)

	tree := BuildEntitlementTree(&files[0].Messages[0], "demo", r)
	if len(tree.Fields) != 1 {
		t.Fatalf("want 1 field (items), got %d", len(tree.Fields))
	}
	if tree.Fields[0].Mode != "may_redact" {
		t.Errorf("the whole-list redact level must survive, got %q", tree.Fields[0].Mode)
	}
	if tree.Fields[0].Nested == nil {
		t.Fatal("a repeated message field must recurse element-wise (Nested must be non-nil)")
	}
	if len(tree.Fields[0].Nested.Fields) != 1 || tree.Fields[0].Nested.Fields[0].Field.Name != "secret" {
		t.Fatalf("element sub-tree should surface [secret], got %+v", tree.Fields[0].Nested.Fields)
	}
}

// HasEntitlement gates Sanitize<T> emission: only a message with a generated
// <T>Entitlement (author record / reachable sub-message) qualifies; an infra
// wrapper that merely embeds a record does not.
func TestMessageResolver_HasEntitlement(t *testing.T) {
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{
		{Name: "FixtureEvent"},
		{Name: "FixtureEventEntitlement"}, // generated by the entitlement pass
		{Name: "FixtureEventBus"},         // infra wrapper, no entitlement type
	}}}
	r := NewMessageResolver(files)
	if !r.HasEntitlement("FixtureEvent") {
		t.Error("FixtureEvent has a FixtureEventEntitlement → must qualify")
	}
	if r.HasEntitlement("FixtureEventBus") {
		t.Error("FixtureEventBus has no entitlement type → must NOT qualify")
	}
}

// The reserved data-entitlement band (>= RedactIndicatorFieldBase) is off-limits
// to author fields, since injected indicators live there.
func TestValidateReservedFieldNumbers(t *testing.T) {
	ok := []model.ProtoFile{{Package: "demo", Messages: []model.Message{{
		Name:   "Event",
		Fields: []model.Field{{Name: "id", Type: "string", Number: 200}},
	}}}}
	if err := ValidateReservedFieldNumbers(ok); err != nil {
		t.Fatalf("a normal field number must pass, got: %v", err)
	}

	bad := []model.ProtoFile{{Package: "demo", Messages: []model.Message{{
		Name:   "Event",
		Fields: []model.Field{{Name: "sneaky", Type: "string", Number: RedactIndicatorFieldBase + 3}},
	}}}}
	err := ValidateReservedFieldNumbers(bad)
	if err == nil || !strings.Contains(err.Error(), "Event.sneaky") || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("a field in the reserved band must fail loud naming the field, got: %v", err)
	}
}

// Resolver handles a cross-package message-typed field via TypePackage.
func TestMessageResolver_CrossPackage(t *testing.T) {
	files := []model.ProtoFile{
		{Package: "outer", Messages: []model.Message{{Name: "Outer", Fields: []model.Field{
			{Name: "inner", Type: "Inner", IsMessage: true, TypePackage: "inner"},
		}}}},
		{Package: "inner", Messages: []model.Message{{Name: "Inner", Fields: []model.Field{
			{Name: "secret", Type: "string", Annotations: redactAnn("drop")},
		}}}},
	}
	r := NewMessageResolver(files)
	got, ok := r.Resolve("outer", files[0].Messages[0].Fields[0])
	if !ok || got.Name != "Inner" {
		t.Fatalf("cross-package resolve failed: ok=%v got=%+v", ok, got)
	}
}
