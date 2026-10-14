// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// RED-1 render tests (Java twin): the generated wrapper must sanitize every
// record-bearing egress item after the handler returns, fail-closed from the
// caller's token data grants.

func javaEgressTestSanitizer() *javaEgressSanitizer {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "notes", Type: "string", Number: 1, Annotations: javaSanitizeAnn("may_redact")},
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
	return newJavaEgressSanitizer(files)
}

func TestJavaEgress_Tier2Wrapper_SanitizesItems(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaTier2Method(), "Catalog", "", "Catalog.ListItemsRequest", "Catalog.Item",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		javaEgressTestSanitizer(), map[string]string{}, nil)
	for _, want := range []string{
		"java.util.Set<String> __dataGrants = new java.util.HashSet<>();",
		// Grants come from the caller's VERIFIED token, never from a decode of
		// the bearer header (F11).
		"TokenWrapper __callerToken = Auth.verifiedTokenFromContext();",
		"__dataGrants.addAll(__serviceEntitlements.getDataGrantsList());",
		"final byte[] __hmacKey = this.anonymizeHmacKey;",
		"final var __entItem = Helpers.buildItemEntitlement(__dataGrants);",
		"__out.onNext(Helpers.sanitizeItem(__it.next(), __entItem, __hmacKey));",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaEgress_Tier3Wrapper_SanitizesItems(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaTier3Method(), "Catalog", "", "Catalog.ListItemsRequest", "Catalog.Item",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		javaEgressTestSanitizer(), map[string]string{}, nil)
	if !strings.Contains(got, "__out.onNext(Helpers.sanitizeItem(__it.next(), __entItem, __hmacKey));") {
		t.Fatalf("Tier-3 wrapper must sanitize each lazily-pulled item:\n%s", got)
	}
}

func TestJavaEgress_WatchWrapper_SanitizesSnapshotAndDelta(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaWatchMethod(), "Catalog", "", "Catalog.WatchReq", "Catalog.WatchEvent",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		javaEgressTestSanitizer(), map[string]string{}, nil)
	for _, want := range []string{
		// snapshot item is the record type — sanitized whole.
		".setSnapshotItem(Helpers.sanitizeItem(__source.snapshot.next(), __entItem, __hmacKey))",
		// the delta envelope goes through the local transform Function.
		"final java.util.function.Function<Catalog.WatchChange, Catalog.WatchChange> __sanitizeDelta",
		"if (__cb.hasItem()) { __cb.setItem(Helpers.sanitizeItem(__cb.getItem(), __entItem, __hmacKey)); }",
		".setDelta(__sanitizeDelta.apply(__changes.next()))",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaEgress_PageWrapper_SanitizesItems(t *testing.T) {
	m := javaPageMethod()
	m.Resolved.PageItemType = "catalog.Item"
	got := generateJavaGRPCWrapper(
		m, "Catalog", "", "Catalog.ListEventsRequest", "Catalog.ListEventsPage",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		javaEgressTestSanitizer(), map[string]string{}, nil)
	for _, want := range []string{
		"java.util.List<Catalog.Item> __sanitizedItems = new java.util.ArrayList<>(__page.items.size());",
		"__sanitizedItems.add(Helpers.sanitizeItem(__m, __entItem, __hmacKey));",
		"__builder.addAllItems(__sanitizedItems);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaEgress_UnaryWrapper_SanitizesEnvelopeFields(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "SampleItem", InputType: "SampleRequest", OutputType: "SampleResponse",
		FullInputType: "catalog.SampleRequest", FullOutputType: "catalog.SampleResponse",
		AuthDisabled: true,
	}
	got := generateJavaGRPCWrapper(
		m, "Catalog", "", "Catalog.SampleRequest", "Catalog.SampleResponse",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		javaEgressTestSanitizer(), map[string]string{}, nil)
	for _, want := range []string{
		"Catalog.SampleResponse.Builder __rb = response.toBuilder();",
		"for (int __i = 0; __i < __rb.getItemsCount(); __i++) {",
		"__rb.setItems(__i, Helpers.sanitizeItem(__rb.getItems(__i), __entItem, __hmacKey));",
		"response = __rb.build();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unary wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// A method whose types carry no entitlement must render with zero egress wiring.
func TestJavaEgress_NoEntitlement_NoWiring(t *testing.T) {
	san := newJavaEgressSanitizer([]model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
	}}})
	got := generateJavaGRPCWrapper(
		javaTier2Method(), "Catalog", "", "Catalog.ListItemsRequest", "Catalog.Item",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{},
		san, map[string]string{}, nil)
	if strings.Contains(got, "__dataGrants") || strings.Contains(got, "sanitize") {
		t.Fatalf("no-entitlement method must have no egress sanitize wiring:\n%s", got)
	}
}
