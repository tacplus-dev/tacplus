// Copyright (c) 2026 Second Star Cavalier LLC

package collection

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// This package is the single source of truth for collection tier math, trust
// ceilings, and message-size bounding — the per-language emitters (WS-1+) branch
// on its output, so it is pinned here before they lean on it.

// --- TierFor: the Tier-1/2/3 boundaries --------------------------------------

func TestTierFor(t *testing.T) {
	cases := []struct {
		name     string
		maxItems uint32
		itemB    int64
		want     Tier
	}{
		{"empty is inline", 0, 1 << 30, TierInline},
		{"exactly at inline limit", 1, InlineLimit, TierInline},
		{"just over inline -> stream", 2, InlineLimit, TierStream},
		{"exactly at materialize ceiling", 1, MaterializeCeiling, TierStream},
		{"over ceiling -> cursor", 1, MaterializeCeiling + 1, TierCursor},
		{"unbounded item -> cursor", 1, unboundedBytes, TierCursor},
		{"multiplication overflow saturates -> cursor", 1 << 31, 1 << 40, TierCursor},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TierFor(c.maxItems, c.itemB); got != c.want {
				t.Fatalf("TierFor(%d, %d) = %d, want %d", c.maxItems, c.itemB, got, c.want)
			}
		})
	}
}

// --- TrustOf: category + auth -> trust tier ----------------------------------

func TestTrustOf(t *testing.T) {
	cases := []struct {
		name                                 string
		isC2S, isS2S, isC2D, effAuthDisabled bool
		want                                 TrustTier
	}{
		{"c2d is local-trusted", false, false, true, false, TrustC2D},
		{"s2s is internal", false, true, false, false, TrustS2S},
		{"c2s + auth_disabled is anon", true, false, false, true, TrustAnon},
		{"c2s authenticated", true, false, false, false, TrustC2SAuth},
		{"unmarked defaults to s2s", false, false, false, false, TrustS2S},
		{"c2d wins over everything", true, true, true, true, TrustC2D},
		{"s2s wins over c2s", true, true, false, false, TrustS2S},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TrustOf(c.isC2S, c.isS2S, c.isC2D, c.effAuthDisabled); got != c.want {
				t.Fatalf("TrustOf(%v,%v,%v,%v) = %d, want %d",
					c.isC2S, c.isS2S, c.isC2D, c.effAuthDisabled, got, c.want)
			}
		})
	}
}

// --- CeilingsFor: the per-trust policy ---------------------------------------

func TestCeilingsFor(t *testing.T) {
	anon := CeilingsFor(TrustAnon)
	if anon.AllowWatch || anon.AllowCursor || !anon.InlineOnly {
		t.Fatalf("anon must be inline-only with no watch/cursor, got %+v", anon)
	}
	if anon.MaxItems != 100 {
		t.Fatalf("anon MaxItems = %d, want 100", anon.MaxItems)
	}

	auth := CeilingsFor(TrustC2SAuth)
	if !auth.AllowWatch || !auth.AllowCursor || auth.InlineOnly {
		t.Fatalf("authenticated C2S must allow watch+cursor, not inline-only, got %+v", auth)
	}
	if auth.StreamIdleSecs == 0 {
		t.Fatalf("authenticated C2S must have a stream idle timeout, got 0")
	}

	s2s := CeilingsFor(TrustS2S)
	if s2s.MaxItems <= auth.MaxItems {
		t.Fatalf("S2S MaxItems (%d) must exceed C2S-auth (%d)", s2s.MaxItems, auth.MaxItems)
	}

	// Unknown tier falls back to the most restrictive policy.
	unknown := CeilingsFor(TrustTier(999))
	if unknown.AllowWatch || !unknown.InlineOnly {
		t.Fatalf("unknown trust must fall back to restrictive, got %+v", unknown)
	}
}

// --- MaxMessageBytes / ItemBytes ---------------------------------------------

func msg(name string, fields ...model.Field) *model.Message {
	return &model.Message{Name: name, Fields: fields}
}

func TestMaxMessageBytes_SimpleScalar(t *testing.T) {
	m := msg("M", model.Field{Name: "a", Type: "int64"})
	// int64 scalar (10) + tag (2) + len (5) = 17.
	if got := MaxMessageBytes(m, "p", map[string]*model.Message{"p.M": m}); got != 17 {
		t.Fatalf("MaxMessageBytes = %d, want 17", got)
	}
}

func TestMaxMessageBytes_CycleIsUnbounded(t *testing.T) {
	c := msg("C", model.Field{Name: "self", Type: "C", TypePackage: "p", IsMessage: true})
	index := map[string]*model.Message{"p.C": c}
	got := MaxMessageBytes(c, "p", index)
	if got <= MaterializeCeiling {
		t.Fatalf("a self-referential message must bound to ~unbounded (> ceiling), got %d", got)
	}
}

func TestMaxMessageBytes_UnresolvedNestedIsSmallBlob(t *testing.T) {
	// A message field that can't be resolved (e.g. a well-known type) is treated
	// as a small fixed blob, not unbounded — so it does not spuriously force Tier 3.
	m := msg("M", model.Field{Name: "ts", Type: "Timestamp", TypePackage: "google.protobuf", IsMessage: true})
	got := MaxMessageBytes(m, "p", map[string]*model.Message{"p.M": m})
	if got <= 0 || got >= MaterializeCeiling {
		t.Fatalf("unresolved nested type must be a small bounded blob, got %d", got)
	}
}

func TestItemBytes(t *testing.T) {
	// scalar element
	if got := ItemBytes(&model.Field{Name: "s", Type: "string"}, nil); got != DefaultScalarBytes {
		t.Fatalf("ItemBytes(string) = %d, want %d", got, DefaultScalarBytes)
	}
	if got := ItemBytes(&model.Field{
		Name: "s",
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 253},
		},
	}, nil); got != 253 {
		t.Fatalf("ItemBytes(bounded string) = %d, want 253", got)
	}
	if got := ItemBytes(&model.Field{
		Name: "s",
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 4096},
		},
	}, nil); got != 4096 {
		t.Fatalf("ItemBytes(size-bounded string) = %d, want 4096", got)
	}
	// message element resolves through the index (ignores repeated multiplicity)
	item := msg("Item", model.Field{Name: "a", Type: "int64"})
	f := &model.Field{Name: "items", Type: "Item", TypePackage: "p", IsMessage: true, IsRepeated: true}
	if got := ItemBytes(f, map[string]*model.Message{"p.Item": item}); got != 17 {
		t.Fatalf("ItemBytes(message) = %d, want 17 (per-element, not x count)", got)
	}
}

// --- Resolve -----------------------------------------------------------------

func TestResolve_Snapshot(t *testing.T) {
	r := Resolve("SNAPSHOT", 1, 0, 0, []string{"id"}, 100, TrustC2SAuth)
	if r.Tier != TierInline {
		t.Fatalf("small snapshot must be Tier-1, got %d", r.Tier)
	}
	if r.ReplayDepth != 0 {
		t.Fatalf("SNAPSHOT must not set replay depth, got %d", r.ReplayDepth)
	}
	if r.IdleSecs != CeilingsFor(TrustC2SAuth).StreamIdleSecs {
		t.Fatalf("IdleSecs must come from the trust ceiling")
	}
}

func TestResolve_WatchDefaultsReplayDepth(t *testing.T) {
	r := Resolve("WATCH", 0, 0, 0, []string{"id"}, -1, TrustS2S)
	if r.ReplayDepth != DefaultReplayDepth {
		t.Fatalf("WATCH with replay_depth=0 must default to %d, got %d", DefaultReplayDepth, r.ReplayDepth)
	}
	if r.Tier != 0 {
		t.Fatalf("WATCH must not carry a SNAPSHOT tier, got %d", r.Tier)
	}
}

func TestResolve_WatchKeepsExplicitReplayDepth(t *testing.T) {
	r := Resolve("WATCH", 0, 50, 0, []string{"id"}, -1, TrustS2S)
	if r.ReplayDepth != 50 {
		t.Fatalf("explicit replay_depth must be preserved, got %d", r.ReplayDepth)
	}
}

func TestResolve_PageSetsCeilingAndDefault(t *testing.T) {
	// PAGE: page_size ceiling = trust MaxItems; default applies when annotation set.
	r := Resolve("PAGE", 0, 0, 50, []string{"id"}, -1, TrustC2SAuth)
	if r.Mode != "PAGE" || r.Tier != 0 || r.ReplayDepth != 0 {
		t.Fatalf("PAGE must carry no tier/replay, got tier=%d replay=%d", r.Tier, r.ReplayDepth)
	}
	if r.MaxPageSize != CeilingsFor(TrustC2SAuth).MaxItems {
		t.Fatalf("PAGE MaxPageSize must equal trust ceiling MaxItems=%d, got %d", CeilingsFor(TrustC2SAuth).MaxItems, r.MaxPageSize)
	}
	if r.DefaultPageSize != 50 {
		t.Fatalf("PAGE default_page_size must be preserved, got %d", r.DefaultPageSize)
	}
}

func TestResolve_PageDefaultFallbackAndClamp(t *testing.T) {
	// Unset default falls back; a default over the ceiling is clamped down.
	r := Resolve("PAGE", 0, 0, 0, []string{"id"}, -1, TrustC2SAuth)
	if r.DefaultPageSize != DefaultPageSizeFallback {
		t.Fatalf("unset default_page_size must fall back to %d, got %d", DefaultPageSizeFallback, r.DefaultPageSize)
	}
	r2 := Resolve("PAGE", 0, 0, 999999, []string{"id"}, -1, TrustAnon) // anon ceiling = 100
	if r2.DefaultPageSize != r2.MaxPageSize {
		t.Fatalf("over-ceiling default must clamp to MaxPageSize=%d, got %d", r2.MaxPageSize, r2.DefaultPageSize)
	}
}

// --- BuildMessageIndex -------------------------------------------------------

func TestBuildMessageIndex(t *testing.T) {
	files := []model.ProtoFile{
		{Package: "p", Messages: []model.Message{{Name: "A"}, {Name: "B"}}},
		{Package: "q", Messages: []model.Message{{Name: "A"}}}, // same bare name, different pkg
	}
	index := BuildMessageIndex(files)
	for _, fqn := range []string{"p.A", "p.B", "q.A"} {
		if index[fqn] == nil {
			t.Fatalf("expected FQN key %q in index", fqn)
		}
	}
	// bare-name fallback exists and points at the first package's A.
	if index["A"] == nil {
		t.Fatalf("expected bare-name fallback %q in index", "A")
	}
	if index["A"] != index["p.A"] {
		t.Fatalf("bare-name fallback must keep the first-seen message (p.A)")
	}
}

func TestItemBytes_RepeatedStringIgnoresCountAnnotationAsByteBound(t *testing.T) {
	// assert_max_size on a repeated/map field is an ELEMENT COUNT, not a
	// per-element byte length (see pkg/proto/sys/spec.proto and the runtime
	// validator). Sizing an element with it under-counts the worst case, since
	// nothing bounds an individual string's length inside the repeated field.
	f := &model.Field{
		Name: "tags", Type: "string", IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
		},
	}
	if got := ItemBytes(f, nil); got != DefaultScalarBytes {
		t.Fatalf("ItemBytes(repeated string, assert_max_size=8) = %d, want %d", got, DefaultScalarBytes)
	}

	// Whole-field bound: 8 elements x (32768 + 2 tag + 5 len) = 262200, not
	// 8 x (8 + 7) = 120.
	m := msg("M", *f)
	const want = 8 * (DefaultScalarBytes + 2 + 5)
	if got := MaxMessageBytes(m, "p", map[string]*model.Message{"p.M": m}); got != want {
		t.Fatalf("MaxMessageBytes(repeated string x8) = %d, want %d", got, want)
	}
}

func TestItemBytes_SingularStringStillUsesAssertMaxSizeAsByteBound(t *testing.T) {
	f := &model.Field{
		Name: "name", Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
		},
	}
	if got := ItemBytes(f, nil); got != 8 {
		t.Fatalf("ItemBytes(singular string, assert_max_size=8) = %d, want 8", got)
	}
	m := msg("M", *f)
	if got := MaxMessageBytes(m, "p", map[string]*model.Message{"p.M": m}); got != 15 {
		t.Fatalf("MaxMessageBytes(singular bounded string) = %d, want 15", got)
	}
}

func TestItemBytes_RepeatedStringUsesElementMaxSize(t *testing.T) {
	// assert_element_max_size is the per-element BYTE bound; assert_max_size on
	// the same field stays the element COUNT.
	f := &model.Field{
		Name: "domains", Type: "string", IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size":         {Type: model.AnnotationTypeInt, IntValue: 32},
			"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: 253},
		},
	}
	if got := ItemBytes(f, nil); got != 253 {
		t.Fatalf("ItemBytes(repeated string, assert_element_max_size=253) = %d, want 253", got)
	}

	// Whole-field bound: 32 elements x (253 + 2 tag + 5 len) = 8320.
	m := msg("M", *f)
	const want = 32 * (253 + 2 + 5)
	if got := MaxMessageBytes(m, "p", map[string]*model.Message{"p.M": m}); got != want {
		t.Fatalf("MaxMessageBytes(repeated string x32 @253B) = %d, want %d", got, want)
	}
}

func TestItemBytes_MapValueUsesElementMaxSize(t *testing.T) {
	f := &model.Field{
		Name: "labels", Type: "string", IsMap: true,
		MapKeyType: "string", MapValueType: "string",
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size":         {Type: model.AnnotationTypeInt, IntValue: 4},
			"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: 64},
		},
	}
	// key keeps the unbounded-string default; the value takes the declared bound.
	if got := ItemBytes(f, nil); got != DefaultScalarBytes+64 {
		t.Fatalf("ItemBytes(map<string,string>, assert_element_max_size=64) = %d, want %d", got, DefaultScalarBytes+64)
	}
}

func TestItemBytes_ElementMaxSizeAbsentStillFallsBackToDefault(t *testing.T) {
	f := &model.Field{
		Name: "tags", Type: "string", IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
		},
	}
	if got := ItemBytes(f, nil); got != DefaultScalarBytes {
		t.Fatalf("ItemBytes(no element bound) = %d, want %d", got, DefaultScalarBytes)
	}
}

// TestTierFor_ProviderInfoWithElementMaxSize pins the oauth Auth.ListProviders
// shape that motivated the annotation: a PUBLIC_NO_AUTH SNAPSHOT of 16
// ProviderInfo items. Without a per-element bound each of the 32 domains is
// sized at the 32 KB unbounded default and the method routes to Tier 2, which
// its anonymous trust ceiling forbids. Only ProviderInfo's declared fields are
// modelled here; the generator additionally injects trace_id/noop, so the
// end-to-end item size is larger than the number asserted below (and still
// Tier 1).
func TestTierFor_ProviderInfoWithElementMaxSize(t *testing.T) {
	domains := func(withElementBound bool) model.Field {
		ann := map[string]model.AnnotationValue{
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 32},
		}
		if withElementBound {
			ann["assert_element_max_size"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 253}
		}
		return model.Field{Name: "domains", Type: "string", IsRepeated: true, Annotations: ann}
	}
	providerInfo := func(withElementBound bool) *model.Message {
		return msg("ProviderInfo",
			model.Field{Name: "name", Type: "string", Annotations: map[string]model.AnnotationValue{
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 128},
			}},
			domains(withElementBound),
			model.Field{Name: "provider_index", Type: "int32"},
		)
	}

	bounded := providerInfo(true)
	// name 128+7=135, domains 32x(253+7)=8320, provider_index 5+7=12.
	const wantItem = 135 + 8320 + 12
	gotItem := MaxMessageBytes(bounded, "oauth", map[string]*model.Message{"oauth.ProviderInfo": bounded})
	if gotItem != wantItem {
		t.Fatalf("ProviderInfo itemBytes = %d, want %d", gotItem, wantItem)
	}
	if tier := TierFor(16, gotItem); tier != TierInline {
		t.Fatalf("TierFor(16, %d) = %d, want TierInline", gotItem, tier)
	}
	if total := int64(16) * gotItem; total > InlineLimit {
		t.Fatalf("16 x %d = %d exceeds InlineLimit %d", gotItem, total, InlineLimit)
	}

	unbounded := providerInfo(false)
	gotUnbounded := MaxMessageBytes(unbounded, "oauth", map[string]*model.Message{"oauth.ProviderInfo": unbounded})
	if tier := TierFor(16, gotUnbounded); tier != TierStream {
		t.Fatalf("without the element bound TierFor(16, %d) = %d, want TierStream", gotUnbounded, tier)
	}
}
