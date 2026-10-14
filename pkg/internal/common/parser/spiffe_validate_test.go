// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
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

// The tests above drive validateSpiffeMessage directly. These drive the
// exported gate the generator actually calls, so the file walk and the error
// aggregation are exercised too — not just the per-message rules.

func spiffeParser(files map[string]*FileDescriptor) *DescriptorParser {
	return &DescriptorParser{descriptors: files}
}

func TestValidateSpiffeIdentityAnnotationsNilParser(t *testing.T) {
	if err := ValidateSpiffeIdentityAnnotations(nil); err != nil {
		t.Fatalf("ValidateSpiffeIdentityAnnotations(nil) = %v, want nil", err)
	}
}

func TestValidateSpiffeIdentityAnnotationsAcceptsWellFormedFiles(t *testing.T) {
	dp := spiffeParser(map[string]*FileDescriptor{
		"connector.proto": {
			Path:    "connector.proto",
			Package: "connector",
			Messages: []MessageDescriptor{*identityResourceMsg(&model.IdentitySpiffeResourceValue{
				NamespaceField: "tenant_namespace",
				Kind:           "gitlab-event",
				IdField:        "event_id",
			})},
		},
	})
	if err := ValidateSpiffeIdentityAnnotations(dp); err != nil {
		t.Fatalf("ValidateSpiffeIdentityAnnotations rejected a well-formed file: %v", err)
	}
}

// A resource SPIFFE naming a sibling that does not exist cannot be constructed
// at runtime, so generation must fail — and the error must name the file's
// package and message so the author can find it.
func TestValidateSpiffeIdentityAnnotationsRejectsUnconstructibleIdentity(t *testing.T) {
	dp := spiffeParser(map[string]*FileDescriptor{
		"connector.proto": {
			Path:    "connector.proto",
			Package: "connector",
			Messages: []MessageDescriptor{*identityResourceMsg(&model.IdentitySpiffeResourceValue{
				NamespaceField: "no_such_field",
				Kind:           "gitlab-event",
				IdField:        "event_id",
			})},
		},
	})
	err := ValidateSpiffeIdentityAnnotations(dp)
	if err == nil {
		t.Fatal("ValidateSpiffeIdentityAnnotations accepted an unconstructible resource identity, want error")
	}
	if !strings.Contains(err.Error(), "spiffe identity annotation errors") {
		t.Fatalf("error = %q, want the aggregated gate message", err.Error())
	}
	if !strings.Contains(err.Error(), "connector.Event.owner_spiffe") {
		t.Fatalf("error = %q, want it to locate the offending field", err.Error())
	}
}

// Violations in more than one message are reported together.
func TestValidateSpiffeIdentityAnnotationsReportsEveryMessage(t *testing.T) {
	bad := identityResourceMsg(&model.IdentitySpiffeResourceValue{
		NamespaceField: "tenant_namespace",
		Kind:           "data", // reserved SPIFFE tag
		IdField:        "event_id",
	})
	other := &MessageDescriptor{
		Name: "Claims",
		Fields: []FieldDescriptor{
			{Name: "ns", Type: "int64", Annotations: map[string]model.AnnotationValue{
				"identity_namespace": {Type: model.AnnotationTypeBool, BoolValue: true},
			}},
		},
	}
	dp := spiffeParser(map[string]*FileDescriptor{
		"connector.proto": {
			Path:     "connector.proto",
			Package:  "connector",
			Messages: []MessageDescriptor{*bad, *other},
		},
	})
	err := ValidateSpiffeIdentityAnnotations(dp)
	if err == nil {
		t.Fatal("ValidateSpiffeIdentityAnnotations accepted two broken messages, want error")
	}
	for _, want := range []string{"connector.Event.owner_spiffe", "connector.Claims.ns"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %s", err.Error(), want)
		}
	}
}
