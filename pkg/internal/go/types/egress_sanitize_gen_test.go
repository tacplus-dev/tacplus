// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// RED-1 render tests: the generated service WRAPPER must sanitize every
// record-bearing egress item — WATCH snapshot+delta, PAGE, Tier-2/Tier-3, and
// unary responses (Sample<T>Response-style envelopes) — after the handler
// returns, using the caller's token data grants (fail-closed).

// egressTestSanitizer builds the emit-time oracle over a catalog package where
// Item is a record with a MAY_REDACT field, WatchChange is an injected-style
// envelope embedding Item, and SampleResponse embeds repeated Item.
func egressTestSanitizer() *egressSanitizer {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "notes", Type: "string", Number: 1, Annotations: sanitizeRedactAnn("may_redact")},
		}},
		// The generated <T>Entitlement message must exist for HasEntitlement.
		{Name: "ItemEntitlement", Fields: []model.Field{}},
		{Name: "WatchChange", Fields: []model.Field{
			{Name: "type", Type: "int32", Number: 1},
			{Name: "item", Type: "Item", Number: 2, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 3},
		}},
		{Name: "SampleResponse", Fields: []model.Field{
			{Name: "items", Type: "Item", Number: 1, IsMessage: true, IsRepeated: true},
		}},
		{Name: "WatchEvent"}, {Name: "WatchReq"}, {Name: "ListItemsRequest"},
		{Name: "ListEventsRequest"}, {Name: "ListEventsPage"}, {Name: "SampleRequest"},
	}}}
	return newEgressSanitizer(files)
}

func TestEgress_Tier2Wrapper_SanitizesItems(t *testing.T) {
	got := generateGRPCWrapper(tier2Service(), tier2Method(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, egressTestSanitizer())
	for _, want := range []string{
		"__dataGrants := map[string]bool{}",
		"caller.Token().GetServiceEntitlements().GetDataGrants()",
		"__hmacKey := p.anonymizeKey()",
		"__entItem := types.BuildItemEntitlement(__dataGrants)",
		"__san := make([]*types.Item, len(items))",
		"__san[i] = types.SanitizeItem(items[i], __entItem, __hmacKey)",
		"items = __san",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestEgress_Tier3Wrapper_SanitizesItems(t *testing.T) {
	got := generateGRPCWrapper(tier3Service(), tier3Method(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, egressTestSanitizer())
	if !strings.Contains(got, "item = types.SanitizeItem(item, __entItem, __hmacKey)") {
		t.Fatalf("Tier-3 wrapper must sanitize each lazily-pulled item:\n%s", got)
	}
}

func TestEgress_WatchWrapper_SanitizesSnapshotAndDelta(t *testing.T) {
	got := generateGRPCWrapper(watchService(), watchMethod(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, egressTestSanitizer())
	for _, want := range []string{
		// snapshot item is the record type — sanitized whole.
		"item = types.SanitizeItem(item, __entItem, __hmacKey)",
		// the delta envelope comes from the shared replay ring: cloned, then its
		// record-bearing field sanitized.
		"change = proto.Clone(change).(*types.WatchChange)",
		"change.Item = types.SanitizeItem(change.Item, __entItem, __hmacKey)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	// The sanitize must run BEFORE the send expression.
	sanIdx := strings.Index(got, "change.Item = types.SanitizeItem")
	sendIdx := strings.Index(got, "WatchEvent_Delta{Delta: change}")
	if sanIdx == -1 || sendIdx == -1 || sanIdx > sendIdx {
		t.Fatalf("delta sanitize must precede the delta send:\n%s", got)
	}
}

func TestEgress_PageWrapper_SanitizesFreshSlice(t *testing.T) {
	m := pageMethod()
	m.Resolved.PageItemType = "catalog.Item"
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	got := generateGRPCWrapper(svc, m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, egressTestSanitizer())
	for _, want := range []string{
		"__san := make([]*types.Item, len(page.Items))",
		"__san[i] = types.SanitizeItem(page.Items[i], __entItem, __hmacKey)",
		"page.Items = __san",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestEgress_UnaryWrapper_SanitizesEnvelopeFields(t *testing.T) {
	// Sample<T>-style unary: response is an injected envelope with repeated items.
	m := &parser.MethodInfo{
		Name: "SampleItem", InputType: "SampleRequest", OutputType: "SampleResponse",
		FullInputType: "catalog.SampleRequest", FullOutputType: "catalog.SampleResponse",
	}
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	got := generateGRPCWrapper(svc, m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, egressTestSanitizer())
	for _, want := range []string{
		"__entItem := types.BuildItemEntitlement(__dataGrants)",
		"__san := make([]*types.Item, len(resp.Items))",
		"__san[i] = types.SanitizeItem(resp.Items[i], __entItem, __hmacKey)",
		"resp.Items = __san",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unary wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// A method whose types carry no entitlement must render with ZERO egress
// sanitize code — the preamble is emitted only when something needs it.
func TestEgress_NoEntitlement_NoWiring(t *testing.T) {
	san := newEgressSanitizer([]model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
	}}})
	got := generateGRPCWrapper(tier2Service(), tier2Method(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, san)
	if strings.Contains(got, "__dataGrants") || strings.Contains(got, "Sanitize") {
		t.Fatalf("no-entitlement method must have no egress sanitize wiring:\n%s", got)
	}
}
