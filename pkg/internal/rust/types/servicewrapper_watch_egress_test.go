// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// A WATCH over pinned rows used to be REFUSED at generation ("the principal
// read filter is generated for unary and page responses only") because the
// stream path had no per-item egress hook. These tests pin the hook that
// replaced the refusal: the same (spec.store_principal) drop and
// (spec.store_namespace) abort the unary and PAGE seams apply, rendered inside
// the producer task before every snapshot_item and delta send — for BOTH delta
// shapes the fleet declares (a row-shaped delta, and a change envelope wrapping
// the row) — and the refusal that remains for the one delta shape the hook
// cannot walk.

func rustWatchEgressTestSanitizer() *rustEgressSanitizer {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "owner", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "namespace", Type: "string", Number: 2,
				Annotations: map[string]model.AnnotationValue{"store_namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "revision", Type: "int64", Number: 3},
		}},
		// delta WRAPS the row: <T>WatchChange { type, item, revision }.
		{Name: "WatchChange", Fields: []model.Field{
			{Name: "type", Type: "int32", Number: 1},
			{Name: "item", Type: "Item", Number: 2, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 3},
		}},
		// a delta the hook cannot walk: the pinned rows sit in a repeated field.
		{Name: "BatchChange", Fields: []model.Field{
			{Name: "items", Type: "Item", Number: 1, IsMessage: true, IsRepeated: true},
			{Name: "revision", Type: "int64", Number: 2},
		}},
		{Name: "Unpinned", Fields: []model.Field{
			{Name: "id", Type: "string", Number: 1},
		}},
		{Name: "UnpinnedChange", Fields: []model.Field{
			{Name: "item", Type: "Unpinned", Number: 1, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 2},
		}},
	}}}
	return newRustEgressSanitizer(files)
}

func rustWatchEgressMethod(itemType, changeType string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "WatchItems", InputType: "WatchItemsRequest", OutputType: "WatchItemsEnvelope",
		FullInputType: "catalog.WatchItemsRequest", FullOutputType: "catalog.WatchItemsEnvelope",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"revision"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1000, OrderBy: []string{"revision"},
			WatchOneOf: "event", WatchItemType: itemType, WatchChangeType: changeType,
			WatchRevisionField: "revision",
		},
	}
}

func rustWatchEgressGenerate(t *testing.T, m *parser.MethodInfo) (string, error) {
	t.Helper()
	return generateRustGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsC2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustWatchEgressTestSanitizer())
}

func TestRustWatchWrapperGuardsSnapshotAndWrappedDelta(t *testing.T) {
	got, err := rustWatchEgressGenerate(t, rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"))
	if err != nil {
		t.Fatalf("a WATCH over pinned rows must now generate, got refusal: %v", err)
	}
	for _, want := range []string{
		// Resolved once, fail-closed, before the producer task.
		`let __read_principal = caller.as_ref()`,
		`let __read_namespace = caller.as_ref()`,
		// snapshot_item IS the row.
		"if item.owner != __read_principal { continue; }",
		"if item.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &item.namespace) {",
		// delta WRAPS the row in `item`.
		"if let Some(__row) = change.item.as_ref() { if __row.owner != __read_principal { continue; } }",
		"if let Some(__row) = change.item.as_ref() { if __row.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &__row.namespace) {",
		// The namespace backstop aborts, exactly as on the unary/PAGE seams.
		"std::process::abort();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing egress guard %q:\n%s", want, got)
		}
	}
	// Resolution precedes the spawn; guards precede their frame's send.
	resolveIdx := strings.Index(got, "let __read_principal = caller.as_ref()")
	spawnIdx := strings.Index(got, "tokio::spawn(async move {")
	if resolveIdx == -1 || spawnIdx == -1 || resolveIdx > spawnIdx {
		t.Fatalf("principal/namespace must be resolved before the producer task is spawned:\n%s", got)
	}
	itemGuard := strings.Index(got, "if item.owner != __read_principal")
	itemSend := strings.Index(got, "::SnapshotItem(item)")
	if itemGuard == -1 || itemSend == -1 || itemGuard > itemSend {
		t.Fatalf("snapshot_item guard must precede the snapshot_item send:\n%s", got)
	}
	changeGuard := strings.Index(got, "if let Some(__row) = change.item.as_ref() { if __row.owner")
	changeSend := strings.Index(got, "::Delta(change)")
	if changeGuard == -1 || changeSend == -1 || changeGuard > changeSend {
		t.Fatalf("delta guard must precede the delta send:\n%s", got)
	}
	// Principal before namespace on each frame, the PAGE seam's order.
	itemNs := strings.Index(got, "if item.namespace != __read_namespace")
	if itemNs == -1 || itemGuard > itemNs {
		t.Fatalf("principal guard must precede namespace guard on snapshot_item:\n%s", got)
	}
	// snapshot_done is never guarded: a fully filtered snapshot still ends.
	doneIdx := strings.Index(got, "::SnapshotDone(source_revision)")
	if doneIdx == -1 {
		t.Fatalf("snapshot_done send missing:\n%s", got)
	}
	between := got[itemSend:doneIdx]
	if strings.Contains(between, "__read_principal") || strings.Contains(between, "__read_namespace") {
		t.Fatalf("snapshot_done must not be guarded:\n%s", between)
	}
}

func TestRustWatchWrapperGuardsRowShapedDelta(t *testing.T) {
	// `ChatEvent delta = 3;` — the append-only shape where the delta IS the row.
	got, err := rustWatchEgressGenerate(t, rustWatchEgressMethod("catalog.Item", "catalog.Item"))
	if err != nil {
		t.Fatalf("a WATCH whose delta is the pinned row itself must generate: %v", err)
	}
	for _, want := range []string{
		"if change.owner != __read_principal { continue; }",
		"if change.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &change.namespace) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("row-shaped delta missing egress guard %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "change.item") {
		t.Fatalf("a row-shaped delta must be guarded directly, not through an `item` field:\n%s", got)
	}
}

func TestRustWatchWrapperEmitsNoGuardOverUnpinnedRows(t *testing.T) {
	// weather's shape: fleet-shared rows, no pins, nothing to resolve or guard.
	got, err := rustWatchEgressGenerate(t, rustWatchEgressMethod("catalog.Unpinned", "catalog.UnpinnedChange"))
	if err != nil {
		t.Fatalf("unpinned WATCH must generate: %v", err)
	}
	for _, absent := range []string{"__read_principal", "__read_namespace", "std::process::abort()"} {
		if strings.Contains(got, absent) {
			t.Fatalf("unpinned WATCH must not carry %q:\n%s", absent, got)
		}
	}
}

func TestRustWatchEgressRefusesDeltaShapeItCannotWalk(t *testing.T) {
	san := rustWatchEgressTestSanitizer()
	_, err := san.watchEgress("Catalog", "WatchItems", "catalog.Item", "catalog.BatchChange", "catalog")
	if err == nil {
		t.Fatal("a delta carrying pinned rows in a repeated field must be refused: the hook walks one singular field and cannot hold a list to the caller")
	}
	for _, want := range []string{"catalog.BatchChange", "repeated", "WATCH egress filter"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name the shape (%q):\n%v", want, err)
		}
	}
}

func TestRustNonWatchStreamOverPinnedRowsStillRefused(t *testing.T) {
	// A plain server stream (no collection) returning pinned rows has no
	// per-item hook and keeps the fail-closed refusal — only WATCH gained one.
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "owner", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		}},
	}}}
	san := newRustEgressSanitizer(files)
	m := &parser.MethodInfo{
		Name: "StreamItems", InputType: "StreamItemsRequest", OutputType: "Item",
		FullInputType: "catalog.StreamItemsRequest", FullOutputType: "catalog.Item",
		ServerStreaming: true,
	}
	err := rustStreamEgressRefusal("Catalog", m, san, "catalog")
	if err == nil {
		t.Fatal("a plain server stream over pinned rows must still be refused: it has no per-item egress hook")
	}
	for _, want := range []string{"Catalog.StreamItems", "(spec.store_principal)", "server-streaming"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name the RPC, the annotation and the shape (%q):\n%v", want, err)
		}
	}
	// A unary method over the same rows is not this check's business — the
	// unary seam filters it — and an unpinned stream has nothing to refuse.
	if err := rustStreamEgressRefusal("Catalog", &parser.MethodInfo{Name: "GetItem", FullOutputType: "catalog.Item"}, san, "catalog"); err != nil {
		t.Fatalf("unary over pinned rows must not be refused here: %v", err)
	}
	if err := rustStreamEgressRefusal("Catalog", rustWatchEgressMethod("catalog.Unpinned", "catalog.UnpinnedChange"), rustWatchEgressTestSanitizer(), "catalog"); err != nil {
		t.Fatalf("unpinned WATCH must not be refused: %v", err)
	}
	// And the WATCH shapes route through the same entry point.
	if err := rustStreamEgressRefusal("Catalog", rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"), rustWatchEgressTestSanitizer(), "catalog"); err != nil {
		t.Fatalf("pinned WATCH with a walkable delta must not be refused: %v", err)
	}
	if err := rustStreamEgressRefusal("Catalog", rustWatchEgressMethod("catalog.Item", "catalog.BatchChange"), rustWatchEgressTestSanitizer(), "catalog"); err == nil {
		t.Fatal("pinned WATCH with an unwalkable delta must be refused through the same entry point")
	}
}

// The WATCH seam carries the same runtime gate the unary and PAGE seams carry
// on the s2s record plane: the guard is emitted, and __ns_pinned decides per
// request whether it fires, so a /system peer keeps its fleet-wide watch.
func TestRustWatchWrapperRecordPlaneS2SCarriesTheRuntimeGate(t *testing.T) {
	got, err := generateRustGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true, SynthesizedKind: parser.SynthesizedKindRecordMap},
		rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"),
		"catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustWatchEgressTestSanitizer())
	if err != nil {
		t.Fatalf("the s2s record plane's WATCH must generate: %v", err)
	}
	for _, want := range []string{
		`let __ns_pinned = !__read_namespace.starts_with("/system");`,
		"if __ns_pinned && item.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &item.namespace) {",
		"if let Some(__row) = change.item.as_ref() { if __ns_pinned && __row.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &__row.namespace) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the record-plane WATCH must carry %q:\n%s", want, got)
		}
	}
}

// The c2s half of the same seam pins unconditionally.
func TestRustWatchWrapperC2SHasNoRuntimeGate(t *testing.T) {
	got, err := rustWatchEgressGenerate(t, rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"))
	if err != nil {
		t.Fatalf("c2s WATCH must generate: %v", err)
	}
	if strings.Contains(got, "__ns_pinned") {
		t.Fatalf("the c2s WATCH must carry no runtime gate:\n%s", got)
	}
	if !strings.Contains(got, "if item.namespace != __read_namespace && !__read_namespace_linked.iter().any(|__ns| __ns == &item.namespace) {") {
		t.Fatalf("the c2s WATCH must pin unconditionally:\n%s", got)
	}
}

// A PLAIN s2s surface — neither c2s nor the synthesized record plane — gets no
// (spec.store_namespace) comparison on its WATCH, exactly as it gets none on its
// unary and PAGE seams, and exactly as Go's goWatchEgressFilter and Java's
// javaNamespaceWatchGuard call site already gate their own.
//
// The namespace comparison is against the AUTHENTICATED CALLER's namespace. On
// such a surface every caller is a fleet peer authorized by identity through
// auth_services, and its reads were never tenant-scoped — so the comparison is
// not a tenant boundary there, it is a guaranteed mismatch against any row the
// service legitimately serves. The unary seam has always answered that by
// emitting no filter; the WATCH seam emitted the guard unconditionally, which
// turned the SAME read into std::process::abort() the first frame it produced.
func TestRustWatchWrapperPlainS2SEmitsNoNamespaceGuard(t *testing.T) {
	got, err := generateRustGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"),
		"catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustWatchEgressTestSanitizer())
	if err != nil {
		t.Fatalf("a plain s2s WATCH over pinned rows must generate: %v", err)
	}
	for _, absent := range []string{
		"__read_namespace",
		"(spec.store_namespace)",
		"std::process::abort();",
		"__ns_pinned",
	} {
		if strings.Contains(got, absent) {
			t.Fatalf("a plain s2s WATCH must carry no namespace guard, found %q:\n%s", absent, got)
		}
	}
	// The principal guard is untouched by this gate: the Rust unary, PAGE and
	// WATCH seams all emit (spec.store_principal) ungated, so the WATCH seam
	// stays consistent with its own siblings.
	for _, want := range []string{
		"let __read_principal = caller.as_ref()",
		"if item.owner != __read_principal { continue; }",
		"if let Some(__row) = change.item.as_ref() { if __row.owner != __read_principal { continue; } }",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("a plain s2s WATCH must keep the principal guard %q:\n%s", want, got)
		}
	}
}

// The same surface over rows pinned ONLY by namespace resolves nothing at all:
// the gate must leave no orphaned preamble behind once the namespace half is
// dropped and no guard remains.
func TestRustWatchWrapperPlainS2SNamespaceOnlyRowEmitsNothing(t *testing.T) {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "namespace", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"store_namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "revision", Type: "int64", Number: 2},
		}},
		{Name: "WatchChange", Fields: []model.Field{
			{Name: "item", Type: "Item", Number: 1, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 2},
		}},
	}}}
	got, err := generateRustGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustWatchEgressMethod("catalog.Item", "catalog.WatchChange"),
		"catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newRustEgressSanitizer(files))
	if err != nil {
		t.Fatalf("a plain s2s WATCH over namespace-only rows must generate: %v", err)
	}
	for _, absent := range []string{
		"__read_namespace",
		"__read_principal",
		"(spec.store_namespace)",
		"(spec.store_principal)",
		"std::process::abort();",
	} {
		if strings.Contains(got, absent) {
			t.Fatalf("a plain s2s WATCH over namespace-only rows must carry no egress guard, found %q:\n%s", absent, got)
		}
	}
}
