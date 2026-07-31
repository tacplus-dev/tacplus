// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// These tests pin the SPIFFE identity annotation generation gate (plan D5):
// an annotated SPIFFE field must be constructible from declared proto fields
// and proto-defined constants, or generation fails.

func identityResourceMsg(res *model.IdentitySpiffeResourceValue) *MessageDescriptor {
	return &MessageDescriptor{
		Name: "Event",
		Fields: []FieldDescriptor{
			{Name: "tenant_namespace", Type: "string"},
			{Name: "event_id", Type: "string"},
			{Name: "owner_spiffe", Type: "string", Annotations: map[string]model.AnnotationValue{
				"identity_spiffe_resource": {
					Type:                   model.AnnotationTypeIdentityResource,
					IdentitySpiffeResource: res,
				},
			}},
		},
	}
}

func runSpiffeMessage(t *testing.T, msg *MessageDescriptor) []string {
	t.Helper()
	var errs []string
	validateSpiffeMessage("connector", msg, &errs)
	return errs
}

func TestIdentitySpiffeResourceValid(t *testing.T) {
	msg := identityResourceMsg(&model.IdentitySpiffeResourceValue{
		NamespaceField: "tenant_namespace",
		Kind:           "gitlab-event",
		IdField:        "event_id",
	})
	if errs := runSpiffeMessage(t, msg); len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
}

func TestIdentitySpiffeResourceMissingSiblings(t *testing.T) {
	cases := map[string]*model.IdentitySpiffeResourceValue{
		"missing namespace_field": {NamespaceField: "", Kind: "gitlab-event", IdField: "event_id"},
		"unknown namespace_field": {NamespaceField: "nope", Kind: "gitlab-event", IdField: "event_id"},
		"missing id_field":        {NamespaceField: "tenant_namespace", Kind: "gitlab-event", IdField: ""},
		"unknown id_field":        {NamespaceField: "tenant_namespace", Kind: "gitlab-event", IdField: "nope"},
		"missing kind":            {NamespaceField: "tenant_namespace", Kind: "", IdField: "event_id"},
		"reserved kind":           {NamespaceField: "tenant_namespace", Kind: "data", IdField: "event_id"},
		"bad kind grammar":        {NamespaceField: "tenant_namespace", Kind: "gitlab/event", IdField: "event_id"},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			if errs := runSpiffeMessage(t, identityResourceMsg(res)); len(errs) == 0 {
				t.Fatal("expected a generation error, got none")
			}
		})
	}
}

func TestIdentitySpiffeResourceSiblingTypes(t *testing.T) {
	msg := identityResourceMsg(&model.IdentitySpiffeResourceValue{
		NamespaceField: "tenant_namespace",
		Kind:           "gitlab-event",
		IdField:        "event_id",
	})
	// id_field names an int field -> reject.
	msg.Fields[1].Type = "int64"
	errs := runSpiffeMessage(t, msg)
	if len(errs) == 0 || !strings.Contains(errs[0], "singular string field") {
		t.Fatalf("expected singular-string error, got: %v", errs)
	}

	// annotated field itself must be a singular string.
	msg = identityResourceMsg(&model.IdentitySpiffeResourceValue{
		NamespaceField: "tenant_namespace",
		Kind:           "gitlab-event",
		IdField:        "event_id",
	})
	msg.Fields[2].IsRepeated = true
	if errs := runSpiffeMessage(t, msg); len(errs) == 0 {
		t.Fatal("expected error for repeated annotated field")
	}

	// self-reference rejected.
	msg = identityResourceMsg(&model.IdentitySpiffeResourceValue{
		NamespaceField: "owner_spiffe",
		Kind:           "gitlab-event",
		IdField:        "event_id",
	})
	if errs := runSpiffeMessage(t, msg); len(errs) == 0 {
		t.Fatal("expected error for self-referencing namespace_field")
	}
}

func TestIdentityBoolAnnotationsRequireString(t *testing.T) {
	for _, name := range []string{"identity_namespace", "identity_subject", "identity_spiffe_subject"} {
		t.Run(name, func(t *testing.T) {
			msg := &MessageDescriptor{
				Name: "Claims",
				Fields: []FieldDescriptor{
					{Name: "ns", Type: "int64", Annotations: map[string]model.AnnotationValue{
						name: {Type: model.AnnotationTypeBool, BoolValue: true},
					}},
				},
			}
			if errs := runSpiffeMessage(t, msg); len(errs) == 0 {
				t.Fatal("expected error for non-string identity field")
			}
			msg.Fields[0].Type = "string"
			if errs := runSpiffeMessage(t, msg); len(errs) != 0 {
				t.Fatalf("expected no errors for string field, got: %v", errs)
			}
		})
	}
}
