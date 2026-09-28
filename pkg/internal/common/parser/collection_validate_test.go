// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// These tests pin the method_collection generation errors enumerated in
// docs/API.md ("Generation errors") and the approved v1 WATCH contract
// (§ Generated API contract). They drive validateOneCollection directly with
// constructed descriptors + an in-memory message index so each rule is
// exercised in isolation — this is the WS-0 deliverable: the proto-shape
// contract that authors get immediate errors against and that the cross-test
// fixtures are written to match.

// --- fixture builders ---------------------------------------------------------

// itemMsg is the collection element type; "id" exists so order_by:["id"] resolves.
func itemMsg() *model.Message {
	return &model.Message{Name: "Item", Fields: []model.Field{
		{Name: "id", Type: "string"},
		{Name: "name", Type: "string"},
	}}
}

// deltaMsg is a conformant WATCH delta. It carries `revision` because every
// language's wrapper keys the replay ring off one int64 on the delta; a delta
// without one generates a wrapper that fails to compile, which is why
// validateWatchRevisionField now refuses it at generation time.
func deltaMsg() *model.Message {
	return &model.Message{Name: "Delta", Fields: []model.Field{
		{Name: "id", Type: "string"},
		{Name: "revision", Type: "int64"},
	}}
}

// watchEnvelope is a conformant {Method}WatchEvent: snapshot_item / snapshot_done
// / delta in a single oneof.
func watchEnvelope() *model.Message {
	return &model.Message{Name: "WatchFooEvent", Fields: []model.Field{
		{Name: "snapshot_item", Type: "Item", IsMessage: true, IsOneOf: true, OneOfName: "event"},
		{Name: "snapshot_done", Type: "int64", IsOneOf: true, OneOfName: "event"},
		{Name: "delta", Type: "Delta", IsMessage: true, IsOneOf: true, OneOfName: "event"},
	}}
}

func watchRequest() *model.Message {
	return &model.Message{Name: "WatchFooRequest", Fields: []model.Field{
		{Name: "from_revision", Type: "int64"},
	}}
}

// listResponse is a unary SNAPSHOT response: exactly one repeated field.
func listResponse() *model.Message {
	return &model.Message{Name: "ListFooResponse", Fields: []model.Field{
		{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true},
	}}
}

// baseIndex returns a fresh message index per test so a case that mutates a
// message (via the index) cannot leak into another.
func baseIndex() map[string]*model.Message {
	return map[string]*model.Message{
		"Item":            itemMsg(),
		"Delta":           deltaMsg(),
		"WatchFooEvent":   watchEnvelope(),
		"WatchFooRequest": watchRequest(),
		"ListFooResponse": listResponse(),
	}
}

// s2sSvc permits WATCH and Tier-3 (TrustS2S ceilings).
func s2sSvc() *ServiceDescriptor { return &ServiceDescriptor{Name: "Foo", IsS2S: true} }

// c2sAuthSvc is authenticated external: WATCH/cursor allowed, max_items 10k.
func c2sAuthSvc() *ServiceDescriptor { return &ServiceDescriptor{Name: "Foo", IsC2S: true} }

// anonSvc is unauthenticated external: no WATCH, inline-only, max_items 100.
func anonSvc() *ServiceDescriptor {
	return &ServiceDescriptor{Name: "Foo", IsC2S: true, AuthDisabled: true}
}

func runValidate(svc *ServiceDescriptor, m *MethodDescriptor, index map[string]*model.Message) []string {
	m.Name = "DoFoo"
	var errs []string
	validateOneCollection("Foo.DoFoo", svc, m, index, &errs)
	return errs
}

func joined(errs []string) string { return strings.Join(errs, "\n") }

func assertNoErrors(t *testing.T, errs []string) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got:\n%s", joined(errs))
	}
}

func assertHasError(t *testing.T, errs []string, want string) {
	t.Helper()
	if !strings.Contains(joined(errs), want) {
		t.Fatalf("expected an error containing %q, got:\n%s", want, joined(errs))
	}
}

func TestCollectionValidate_UnannotatedUnaryRepeatedResponseFails(t *testing.T) {
	var errs []string
	validateMissingCollectionAnnotation(c2sAuthSvc(), &MethodDescriptor{
		Name:       "ListFoo",
		OutputType: "ListFooResponse",
	}, baseIndex(), &errs)
	assertHasError(t, errs, "Tier-1 collection reads must declare method_collection")
}

// A write's receipt can legitimately be shaped like a single-repeated-field
// response (e.g. the ids a Delete removed) without being an undeclared
// collection read; only a read verb should trigger the heuristic.
func TestCollectionValidate_UnannotatedUnaryRepeatedResponseOnDeleteIsNotFlagged(t *testing.T) {
	var errs []string
	validateMissingCollectionAnnotation(c2sAuthSvc(), &MethodDescriptor{
		Name:       "DeleteFoo",
		OutputType: "ListFooResponse",
	}, baseIndex(), &errs)
	assertNoErrors(t, errs)
}

func TestValidateMethodCollectionsFailsUnannotatedUnaryRepeatedResponse(t *testing.T) {
	dp := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"catalog.proto": {
			Package: "catalog",
			Services: []ServiceDescriptor{{
				Name:  "Catalog",
				IsC2S: true,
				Methods: []MethodDescriptor{{
					Name:       "ListFoo",
					OutputType: "ListFooResponse",
				}},
			}},
		},
	}}

	err := ValidateMethodCollections(dp, baseIndex())
	if err == nil {
		t.Fatal("ValidateMethodCollections succeeded for an unannotated Tier-1 collection shape")
	}
	if !strings.Contains(err.Error(), "method_collection annotation errors") ||
		!strings.Contains(err.Error(), "Tier-1 collection reads must declare method_collection") {
		t.Fatalf("ValidateMethodCollections error = %v, want Tier-1 collection annotation failure", err)
	}
}

func TestValidateMethodCollectionsForPackagesSkipsUnselectedPackages(t *testing.T) {
	dp := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"catalog.proto": {
			Package: "catalog",
			Services: []ServiceDescriptor{{
				Name:  "Catalog",
				IsC2S: true,
				Methods: []MethodDescriptor{{
					Name:       "ListFoo",
					OutputType: "ListFooResponse",
				}},
			}},
		},
		"audit.proto": {
			Package: "audit",
			Services: []ServiceDescriptor{{
				Name:  "Audit",
				IsC2S: true,
				Methods: []MethodDescriptor{{
					Name:       "ListFoo",
					OutputType: "ListFooResponse",
				}},
			}},
		},
	}}

	if err := ValidateMethodCollectionsForPackages(dp, baseIndex(), "audit"); err == nil ||
		!strings.Contains(err.Error(), "Audit.ListFoo") ||
		strings.Contains(err.Error(), "Catalog.ListFoo") {
		t.Fatalf("scoped validation error = %v, want only Audit.ListFoo", err)
	}
	if err := ValidateMethodCollectionsForPackages(dp, baseIndex(), "billing"); err != nil {
		t.Fatalf("scoped validation for unrelated package returned %v", err)
	}
}

func TestCollectionValidate_UnannotatedUnaryRepeatedWithOtherFieldsAllowed(t *testing.T) {
	idx := baseIndex()
	idx["CompositeResponse"] = &model.Message{Name: "CompositeResponse", Fields: []model.Field{
		{Name: "item", Type: "Item", IsMessage: true},
		{Name: "locations", Type: "Item", IsMessage: true, IsRepeated: true},
	}}
	var errs []string
	validateMissingCollectionAnnotation(c2sAuthSvc(), &MethodDescriptor{
		Name:       "GetFoo",
		OutputType: "CompositeResponse",
	}, idx, &errs)
	assertNoErrors(t, errs)
}

// PC-C-1: a unary read carrying the PAGE envelope fields (page_size/page_token/
// next_page_token) but no (spec.method_collection) must be rejected — otherwise
// the page fields are inert and it generates as an unbounded read.
func TestCollectionValidate_PageShapedWithoutModeFails(t *testing.T) {
	idx := baseIndex()
	idx["PageFooRequest"] = &model.Message{Name: "PageFooRequest", Fields: []model.Field{
		{Name: "page_size", Type: "int32"},
		{Name: "page_token", Type: "string"},
	}}
	idx["PageFooResponse"] = &model.Message{Name: "PageFooResponse", Fields: []model.Field{
		{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true},
		{Name: "next_page_token", Type: "string"},
	}}
	var errs []string
	validateMissingCollectionAnnotation(c2sAuthSvc(), &MethodDescriptor{
		Name:       "PageFoo",
		InputType:  "PageFooRequest",
		OutputType: "PageFooResponse",
	}, idx, &errs)
	assertHasError(t, errs, "carry PAGE envelope fields")
}

func TestCollectionValidate_UnannotatedServerStreamIsNotForcedCollection(t *testing.T) {
	var errs []string
	validateMissingCollectionAnnotation(c2sAuthSvc(), &MethodDescriptor{
		Name:            "TailFoo",
		OutputType:      "Item",
		ServerStreaming: true,
	}, baseIndex(), &errs)
	assertNoErrors(t, errs)
}

// --- valid baselines (must produce zero errors) -------------------------------

func TestCollectionValidate_ValidWatch(t *testing.T) {
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertNoErrors(t, runValidate(s2sSvc(), m, baseIndex()))
}

func TestCollectionValidate_ValidSnapshotUnary(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10},
	}
	assertNoErrors(t, runValidate(c2sAuthSvc(), m, baseIndex()))
}

// --- WS-0 WATCH generation errors --------------------------------------------

func TestCollectionValidate_WatchMissingFromRevision(t *testing.T) {
	idx := baseIndex()
	idx["WatchFooRequest"] = &model.Message{Name: "WatchFooRequest"} // drop from_revision
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(s2sSvc(), m, idx), "from_revision")
}

func TestCollectionValidate_WatchEnvelopeMissingDelta(t *testing.T) {
	idx := baseIndex()
	idx["WatchFooEvent"] = &model.Message{Name: "WatchFooEvent", Fields: []model.Field{
		{Name: "snapshot_item", Type: "Item", IsMessage: true, IsOneOf: true, OneOfName: "event"},
		{Name: "snapshot_done", Type: "int64", IsOneOf: true, OneOfName: "event"},
	}}
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(s2sSvc(), m, idx), "conformant envelope")
}

func TestCollectionValidate_WatchEnvelopeFieldsNotSameOneof(t *testing.T) {
	idx := baseIndex()
	env := watchEnvelope()
	env.Fields[2].OneOfName = "other" // delta in a different oneof
	idx["WatchFooEvent"] = env
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(s2sSvc(), m, idx), "same oneof")
}

func TestCollectionValidate_WatchSnapshotDoneNotInt64(t *testing.T) {
	idx := baseIndex()
	env := watchEnvelope()
	env.Fields[1].Type = "string" // snapshot_done must be int64
	idx["WatchFooEvent"] = env
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(s2sSvc(), m, idx), "snapshot_done must be int64")
}

func TestCollectionValidate_WatchNotServerStreaming(t *testing.T) {
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(s2sSvc(), m, baseIndex()), "must be declared server-streaming")
}

func TestCollectionValidate_WatchWithMaxItems(t *testing.T) {
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}, MaxItems: 5},
	}
	assertHasError(t, runValidate(s2sSvc(), m, baseIndex()), "max_items is only valid on SNAPSHOT")
}

func TestCollectionValidate_WatchForbiddenAtAnonTrust(t *testing.T) {
	m := &MethodDescriptor{
		InputType: "WatchFooRequest", OutputType: "WatchFooEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(anonSvc(), m, baseIndex()), "WATCH is not permitted")
}

// --- Phase 1-3 rules (regression guard: WS-0 verifies these still hold) -------

func TestCollectionValidate_SnapshotRequiresMaxItems(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 0},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "requires a positive max_items")
}

func TestCollectionValidate_SnapshotWithReplayDepth(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10, ReplayDepth: 8},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "replay_depth is only valid on WATCH")
}

func TestCollectionValidate_MissingOrderBy(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", MaxItems: 10},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "requires order_by")
}

func TestCollectionValidate_OrderByUnknownField(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"nope"}, MaxItems: 10},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "does not exist on the collection item type")
}

func TestCollectionValidate_OrderByResolvesUnaryItemInResponsePackage(t *testing.T) {
	index := map[string]*model.Message{
		"Message": {
			Name: "Message",
			Fields: []model.Field{
				{Name: "id", Type: "string"},
			},
		},
		"other.Message": {
			Name: "Message",
			Fields: []model.Field{
				{Name: "id", Type: "string"},
			},
		},
		"chat.Message": {
			Name: "Message",
			Fields: []model.Field{
				{Name: "message_ts", Type: "string"},
			},
		},
		"chat.ListMessagesRequest": {
			Name: "ListMessagesRequest",
			Fields: []model.Field{
				{Name: "page_size", Type: "int32"},
				{Name: "page_token", Type: "string"},
			},
		},
		"chat.ListMessagesResponse": {
			Name: "ListMessagesResponse",
			Fields: []model.Field{
				{Name: "messages", Type: "Message", IsMessage: true, IsRepeated: true},
				{Name: "next_page_token", Type: "string"},
				{Name: "total_count", Type: "int64"},
			},
		},
	}

	errs := runValidate(c2sAuthSvc(), &MethodDescriptor{
		InputType:  "chat.ListMessagesRequest",
		OutputType: "chat.ListMessagesResponse",
		Collection: &CollectionDescriptor{
			Mode:            "PAGE",
			OrderBy:         []string{"message_ts"},
			DefaultPageSize: 100,
		},
	}, index)
	assertNoErrors(t, errs)
}

func TestCollectionValidate_UnaryMultipleRepeatedFieldsAmbiguous(t *testing.T) {
	idx := baseIndex()
	idx["ListFooResponse"] = &model.Message{Name: "ListFooResponse", Fields: []model.Field{
		{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true},
		{Name: "extras", Type: "Item", IsMessage: true, IsRepeated: true},
	}}
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, idx), "multiple repeated fields")
}

func TestCollectionValidate_BadMode(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse",
		Collection: &CollectionDescriptor{Mode: "LIVE", OrderBy: []string{"id"}},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "requires mode SNAPSHOT, WATCH, or PAGE")
}

// --- PC-C-1: trust-ceiling and tier<->declaration branches ------------------
// These four cross-field checks in validateOneCollection had no negative test:
// (a) max_items over the caller-trust ceiling, (b) an anonymous (inline-only)
// caller forced to server-stream, (c) a unary declaration whose size routes to
// a streaming tier, and (d) a Tier-3 cursor at a trust level that forbids it.

// bigItemIndex returns a base index whose Item carries a bounded `blob` string
// of maxBytes, so a SNAPSHOT's tier can be driven deterministically.
func bigItemIndex(maxBytes int64) map[string]*model.Message {
	idx := baseIndex()
	idx["Item"] = &model.Message{Name: "Item", Fields: []model.Field{
		{Name: "id", Type: "string"},
		{Name: "blob", Type: "string", Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {IntValue: maxBytes},
		}},
	}}
	// The unary SNAPSHOT response embeds that Item.
	idx["ListFooResponse"] = &model.Message{Name: "ListFooResponse", Fields: []model.Field{
		{Name: "items", Type: "Item", IsMessage: true, IsRepeated: true},
	}}
	return idx
}

// (a) max_items above the authenticated-external ceiling (10k) is rejected even
// though the tiny item still fits inline.
func TestCollectionValidate_MaxItemsOverCeiling(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "ListFooResponse", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 20000},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, baseIndex()), "exceeds the ceiling for this caller-trust level")
}

// (b) an anonymous (auth_disabled) caller is inline-only: a server-streaming
// SNAPSHOT is rejected regardless of size.
func TestCollectionValidate_AnonInlineOnlyForbidsStreaming(t *testing.T) {
	m := &MethodDescriptor{
		OutputType: "Item", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 50},
	}
	assertHasError(t, runValidate(anonSvc(), m, baseIndex()), "single inline Tier-1 unary response, not server-streaming")
}

// (c) a UNARY declaration whose max_items x item size exceeds one inline
// message (routes to Tier 2) must be declared server-streaming.
func TestCollectionValidate_UnaryButRoutesToStreamingTier(t *testing.T) {
	// 2 MB item x 1 -> ~2 MB > 1 MB InlineLimit, < 64 MB -> Tier 2 (stream).
	m := &MethodDescriptor{
		OutputType: "ListFooResponse", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1},
	}
	assertHasError(t, runValidate(c2sAuthSvc(), m, bigItemIndex(2<<20)), "declare this method server-streaming")
}

// (d) a SNAPSHOT that routes to a Tier-3 cursor is rejected at a caller-trust
// level that does not permit cursors (anonymous). max_items sits AT the anon
// ceiling (100) so the ceiling check does not fire — isolating the cursor rule.
func TestCollectionValidate_CursorTierForbiddenAtAnonTrust(t *testing.T) {
	// 1 MB item x 100 -> ~100 MB > 64 MB MaterializeCeiling -> Tier 3 (cursor).
	m := &MethodDescriptor{
		OutputType: "ListFooResponse", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 100},
	}
	assertHasError(t, runValidate(anonSvc(), m, bigItemIndex(1<<20)), "requires a Tier-3 cursor stream, which is not permitted")
}
