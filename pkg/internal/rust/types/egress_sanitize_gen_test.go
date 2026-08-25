// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// RED-1 render tests (Rust twin): the generated wrapper must sanitize every
// record-bearing egress item after the handler returns, fail-closed from the
// caller's token data grants.

func rustEgressTestSanitizer() *rustEgressSanitizer {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "notes", Type: "string", Number: 1, Annotations: rustSanitizeAnn("may_redact")},
		}},
		{Name: "ItemEntitlement", Fields: []model.Field{}},
		{Name: "WatchChange", Fields: []model.Field{
			{Name: "type", Type: "int32", Number: 1},
			{Name: "item", Type: "Item", Number: 2, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 3},
		}},
		{Name: "SampleResponse", Fields: []model.Field{
			{Name: "items", Type: "Item", Number: 1, IsMessage: true, IsRepeated: true},
		}},
	}}}
	return newRustEgressSanitizer(files)
}

func TestRustEgress_Tier2Wrapper_SanitizesItems(t *testing.T) {
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustTier2Method(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustEgressTestSanitizer())
	for _, want := range []string{
		"__data_grants: std::collections::HashSet<String>",
		`se.get("data_grants")`,
		"let __hmac_key: Vec<u8> = self.anonymize_hmac_key.read().await.clone();",
		"let __ent_item = build_item_entitlement(&__data_grants);",
		"let items: Vec<_> = items.into_iter().map(|m| sanitize_item(&m, Some(&__ent_item), &__hmac_key)).collect();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustEgress_WatchWrapper_SanitizesSnapshotAndDelta(t *testing.T) {
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustWatchMethod(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustEgressTestSanitizer())
	for _, want := range []string{
		// snapshot item is the record type — sanitized whole.
		"let item = sanitize_item(&item, Some(&__ent_item), &__hmac_key);",
		// the delta envelope has its record-bearing field sanitized.
		"let mut change = change;",
		"change.item = change.item.as_ref().map(|v| sanitize_item(v, Some(&__ent_item), &__hmac_key));",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	sanIdx := strings.Index(got, "change.item = change.item.as_ref()")
	sendIdx := strings.Index(got, "::Delta(change)")
	if sanIdx == -1 || sendIdx == -1 || sanIdx > sendIdx {
		t.Fatalf("delta sanitize must precede the delta send:\n%s", got)
	}
}

func TestRustEgress_LazyStreamWrapper_SanitizesItems(t *testing.T) {
	// Tier-3 routes to the lazy server-streaming seam in Rust.
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustTier3Method(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustEgressTestSanitizer())
	for _, want := range []string{
		"let response = response.map(|stream| {",
		"stream.map(move |item| item.map(|msg| sanitize_item(&msg, Some(&__ent_item), &__hmac_key)))",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lazy-stream wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustEgress_PageWrapper_SanitizesItems(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "ListEvents", InputType: "ListEventsRequest", OutputType: "ListEventsPage",
		FullInputType: "catalog.ListEventsRequest", FullOutputType: "catalog.ListEventsPage",
		Collection: &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50},
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "catalog.Item", PageItemsField: "items",
		},
	}
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustEgressTestSanitizer())
	for _, want := range []string{
		"let mut page = page;",
		"page.items = page.items.iter().map(|m| sanitize_item(m, Some(&__ent_item), &__hmac_key)).collect();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustEgress_UnaryWrapper_SanitizesEnvelopeFields(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "SampleItem", InputType: "SampleRequest", OutputType: "SampleResponse",
		FullInputType: "catalog.SampleRequest", FullOutputType: "catalog.SampleResponse",
	}
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustEgressTestSanitizer())
	for _, want := range []string{
		"let mut resp = resp;",
		"for v in resp.items.iter_mut() {",
		"*v = sanitize_item(v, Some(&__ent_item), &__hmac_key);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unary wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// A method whose types carry no entitlement must render with zero egress wiring.
func TestRustEgress_NoEntitlement_NoWiring(t *testing.T) {
	san := newRustEgressSanitizer([]model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
	}}})
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustTier2Method(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, san)
	if strings.Contains(got, "__data_grants") || strings.Contains(got, "sanitize_") {
		t.Fatalf("no-entitlement method must have no egress sanitize wiring:\n%s", got)
	}
}
