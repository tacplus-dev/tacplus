// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// (spec.method_collection).revision_field — the name of the int64 on the WATCH
// delta that the replay ring is keyed by.
//
// Every language's wrapper renders an accessor for it (Rust change.<f>, Java
// change.get<F>(), Go c.Get<F>()). That name used to be the literal "revision"
// in all three generators with nothing checking it, so a delta without such a
// field generated fine and failed later in cargo/javac/go build, pointing at
// generated code instead of at the proto. These tests pin both halves of the
// fix: the name is resolved (default, or declared) and it is PROVEN to exist
// and be a singular int64 before an emitter ever sees it.

// --- resolve ------------------------------------------------------------------

func TestResolveCollections_WatchRevisionFieldDefaults(t *testing.T) {
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "Watch", FullOutputType: "p.WatchEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx())

	if got := svc.Methods[0].Resolved.WatchRevisionField; got != collection.DefaultWatchRevisionField {
		t.Fatalf("WatchRevisionField = %q, want the default %q", got, collection.DefaultWatchRevisionField)
	}
}

func TestResolveCollections_WatchRevisionFieldDeclaredWins(t *testing.T) {
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "Watch", FullOutputType: "p.WatchEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}, RevisionField: "seq"},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx())

	if got := svc.Methods[0].Resolved.WatchRevisionField; got != "seq" {
		t.Fatalf("WatchRevisionField = %q, want the declared %q", got, "seq")
	}
}

// A non-WATCH collection resolves no revision field at all — there is no delta
// and nothing for an emitter to key a ring by.
func TestResolveCollections_RevisionFieldEmptyOffWatch(t *testing.T) {
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "List", FullOutputType: "p.ListResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx(msgWith("ListResponse",
		model.Field{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true})))

	if got := svc.Methods[0].Resolved.WatchRevisionField; got != "" {
		t.Fatalf("WatchRevisionField = %q on a SNAPSHOT, want empty", got)
	}
}

// --- validate -----------------------------------------------------------------

// watchMethodDescriptor is the conformant WATCH shape the baseline test uses,
// with the revision_field left to the caller.
func watchMethodDescriptor(revisionField string) *MethodDescriptor {
	return &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{
			Mode: "WATCH", OrderBy: []string{"id"}, RevisionField: revisionField,
		},
	}
}

// indexWithDelta swaps the baseline index's Delta for one with the given fields,
// so each case below differs only in the delta's shape.
func indexWithDelta(fields ...model.Field) map[string]*model.Message {
	index := baseIndex()
	index["Delta"] = &model.Message{Name: "Delta", Fields: fields}
	return index
}

// The default is a DEFAULT, not an assumption: a delta with no `revision` is
// refused, and the error says how to fix it either way (add the field, or name
// the one the message already has).
func TestCollectionValidate_WatchDeltaMissingDefaultRevision(t *testing.T) {
	index := indexWithDelta(model.Field{Name: "seq", Type: "int64"})
	errs := runValidate(s2sSvc(), watchMethodDescriptor(""), index)
	assertHasError(t, errs, `WATCH delta "Delta" declares no "revision" field`)
	assertHasError(t, errs, "revision_field")
}

// The whole point of the annotation: a row-shaped delta that names its own
// sequence validates, with no second int64 forced onto the row.
func TestCollectionValidate_WatchDeclaredRevisionField(t *testing.T) {
	index := indexWithDelta(
		model.Field{Name: "event_id", Type: "string"},
		model.Field{Name: "seq", Type: "int64"},
	)
	assertNoErrors(t, runValidate(s2sSvc(), watchMethodDescriptor("seq"), index))
}

func TestCollectionValidate_WatchDeclaredRevisionFieldMissing(t *testing.T) {
	index := indexWithDelta(model.Field{Name: "revision", Type: "int64"})
	errs := runValidate(s2sSvc(), watchMethodDescriptor("seq"), index)
	assertHasError(t, errs, `WATCH revision_field "seq" does not exist on delta "Delta"`)
}

// A ring cannot be ordered by a string, a list, or a message.
func TestCollectionValidate_WatchRevisionFieldMustBeInt64(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field model.Field
	}{
		{"string", model.Field{Name: "seq", Type: "string"}},
		{"repeated int64", model.Field{Name: "seq", Type: "int64", IsRepeated: true}},
		{"message", model.Field{Name: "seq", Type: "Cursor", IsMessage: true}},
		{"enum", model.Field{Name: "seq", Type: "Kind", IsEnum: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := runValidate(s2sSvc(), watchMethodDescriptor("seq"), indexWithDelta(tc.field))
			assertHasError(t, errs, `WATCH revision field "seq" on delta "Delta" must be a singular int64`)
		})
	}
}

// revision_field describes a delta, and only WATCH has one.
func TestCollectionValidate_RevisionFieldRejectedOffWatch(t *testing.T) {
	snapshot := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{
			Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10, RevisionField: "seq",
		},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), snapshot, baseIndex()),
		"revision_field is only valid on WATCH, not SNAPSHOT")

	page := &MethodDescriptor{
		InputType: "PageFooRequest", OutputType: "PageFooResponse",
		Collection: &CollectionDescriptor{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, RevisionField: "seq",
		},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), page, baseIndex()),
		"revision_field is only valid on WATCH, not PAGE")
}
