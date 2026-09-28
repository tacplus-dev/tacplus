// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// ResolveCollections is where the per-method codegen view (mode/tier/bounds) is
// computed once for all emitters. These tests pin that the right Tier/Mode lands
// on MethodInfo.Resolved so the WS-1+ emitters branch correctly.

// idx builds a message index keyed by both FQN ("p.Name") and bare name, the way
// collection.BuildMessageIndex does, so lookups by FullOutputType resolve.
func idx(msgs ...*model.Message) map[string]*model.Message {
	m := map[string]*model.Message{}
	for _, ms := range msgs {
		m["p."+ms.Name] = ms
		if _, ok := m[ms.Name]; !ok {
			m[ms.Name] = ms
		}
	}
	return m
}

func msgWith(name string, fields ...model.Field) *model.Message {
	return &model.Message{Name: name, Fields: fields}
}

func TestResolveCollections_WatchDefaults(t *testing.T) {
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "Watch", FullOutputType: "p.WatchEvent", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx())

	r := svc.Methods[0].Resolved
	if r == nil {
		t.Fatal("Resolved must be set for an annotated method")
	}
	if r.Mode != "WATCH" {
		t.Fatalf("Mode = %q, want WATCH", r.Mode)
	}
	if r.ReplayDepth != collection.DefaultReplayDepth {
		t.Fatalf("ReplayDepth = %d, want default %d", r.ReplayDepth, collection.DefaultReplayDepth)
	}
	if r.Tier != 0 {
		t.Fatalf("WATCH must not carry a SNAPSHOT tier, got %d", r.Tier)
	}
	if r.IdleSecs != collection.CeilingsFor(collection.TrustS2S).StreamIdleSecs {
		t.Fatalf("IdleSecs must come from the S2S trust ceiling, got %d", r.IdleSecs)
	}
}

func TestResolveCollections_SnapshotUnaryTier1(t *testing.T) {
	item := msgWith("Item", model.Field{Name: "id", Type: "string"})
	list := msgWith("ListResp", model.Field{Name: "items", Type: "Item", TypePackage: "p", IsMessage: true, IsRepeated: true})
	svc := &ServiceInfo{Name: "Foo", IsC2S: true, Methods: []*MethodInfo{{
		Name: "List", FullOutputType: "p.ListResp", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx(item, list))

	r := svc.Methods[0].Resolved
	if r == nil || r.Tier != collection.TierInline {
		t.Fatalf("small unary snapshot must be Tier-1 inline, got %+v", r)
	}
	if !svc.Methods[0].IsRetrySafeUnary() {
		t.Fatalf("Tier-1 snapshot read must be retry-safe")
	}
}

func TestResolveCollections_SnapshotStreamTier2(t *testing.T) {
	// Streamed item is the output message itself; ~32KB string * 1000 items
	// ≈ 31MB → between InlineLimit (1MB) and MaterializeCeiling (64MB) → Tier 2.
	item := msgWith("Item", model.Field{Name: "id", Type: "string"})
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "List", FullOutputType: "p.Item", ServerStreaming: true,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1000},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx(item))

	r := svc.Methods[0].Resolved
	if r == nil || r.Tier != collection.TierStream {
		t.Fatalf("large streamed snapshot must be Tier-2 stream, got %+v", r)
	}
	if svc.Methods[0].IsRetrySafeUnary() {
		t.Fatalf("streamed snapshot must not use unary retry policy")
	}
}

func TestCollectionShapeHelpersRequireWireCardinality(t *testing.T) {
	unarySnapshot := &MethodInfo{
		Name: "List", FullOutputType: "p.ListResp",
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 10000},
		Resolved:   &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor},
	}
	if unarySnapshot.IsTier2SnapshotCollection() || unarySnapshot.IsTier3SnapshotCollection() {
		t.Fatalf("unary SNAPSHOT must not render server-streaming collection wrappers")
	}

	unaryWatch := &MethodInfo{
		Name:       "Watch",
		Collection: &CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
		Resolved:   &collection.Resolved{Mode: "WATCH"},
	}
	if unaryWatch.IsWatchCollection() {
		t.Fatalf("unary WATCH metadata must not render a watch stream")
	}

	streamPage := &MethodInfo{
		Name:            "List",
		ServerStreaming: true,
		Collection:      &CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 100},
		Resolved:        &collection.Resolved{Mode: "PAGE"},
	}
	if streamPage.IsPageCollection() {
		t.Fatalf("server-streaming PAGE metadata must not render unary page wrappers")
	}
}

func TestResolveCollections_NoAnnotationLeavesNil(t *testing.T) {
	svc := &ServiceInfo{Name: "Foo", IsS2S: true, Methods: []*MethodInfo{{
		Name: "Plain", FullOutputType: "p.Resp",
	}}}
	ResolveCollections([]*ServiceInfo{svc}, idx())
	if svc.Methods[0].Resolved != nil {
		t.Fatalf("a method without method_collection must have nil Resolved")
	}
	if svc.Methods[0].IsRetrySafeUnary() {
		t.Fatalf("unannotated unary must not retry by default")
	}
}

// TestResolveCollections_UnaryItemTierAgreesWithValidator pins the ONE piece of
// math the validator and the emitters both depend on. The validator refuses a
// unary SNAPSHOT that does not route to Tier-1 ("declare this method
// server-streaming"); every emitter renders the Tier-2 server-streaming seam off
// ResolveCollections's tier. When the two computed item bytes separately they
// could disagree on a same-package item — which carries no TypePackage, so
// collection.ItemBytes could only try the bare name, and the bare name in a
// multi-package index belongs to whichever package was indexed first. A unary
// ListLinks then passed validation as Tier-1 and reached the Go emitter as
// Tier-2, generating a server-streaming wrapper for a unary RPC.
func TestResolveCollections_UnaryItemTierAgreesWithValidator(t *testing.T) {
	// Two packages define "Link". "a.Link" is indexed first, so the bare name
	// resolves to it; the response in package "q" means its own "q.Link".
	otherLink := msgWith("Link", model.Field{Name: "blob", Type: "bytes"})
	ownLink := msgWith("Link", model.Field{Name: "id", Type: "int64"})
	resp := msgWith("ListLinksResponse",
		model.Field{Name: "links", Type: "Link", IsMessage: true, IsRepeated: true})
	index := map[string]*model.Message{
		"a.Link":              otherLink,
		"Link":                otherLink,
		"q.Link":              ownLink,
		"q.ListLinksResponse": resp,
		"ListLinksResponse":   resp,
	}

	svc := &ServiceInfo{Name: "Blob", IsS2S: true, Methods: []*MethodInfo{{
		Name: "ListLinks", FullOutputType: "q.ListLinksResponse", ServerStreaming: false,
		Collection: &CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 100},
	}}}
	ResolveCollections([]*ServiceInfo{svc}, index)

	m := svc.Methods[0]
	if m.Resolved == nil {
		t.Fatal("Resolved must be set for an annotated method")
	}
	// 100 x a two-field int64 item is nowhere near InlineLimit: Tier-1, which is
	// what the validator computes for the same method and so lets through.
	if m.Resolved.Tier != collection.TierInline {
		t.Fatalf("unary SNAPSHOT item resolved to Tier %d, want Tier-1 inline (the tier the validator computes for the same method); the two item-bytes computations have diverged", int(m.Resolved.Tier))
	}
	if m.IsTier2SnapshotCollection() {
		t.Fatalf("a unary method must never report as a Tier-2 SNAPSHOT collection")
	}
}
