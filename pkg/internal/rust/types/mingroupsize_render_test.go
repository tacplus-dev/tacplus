// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// (spec.record).min_group_size is a threshold on a RECORD, stamped by the
// record-map injector onto the record's size-only RPCs. These tests pin what the
// Rust wrapper does with the stamp, and are the twin of
// java/types/mingroupsize_render_test.go and
// go/types/mingroupsize_render_test.go: the refusal shape (PERMISSION_DENIED,
// no count in the text), that it reaches the stream as well as the unary count,
// and that an undeclared record's wrapper is byte-for-byte what it was before.

// rustMinGroupSizeSanitizer indexes the two size-only response shapes the
// injector emits: RecordCount (Get<T>Count / Subscribe<T>Count) and QueryStats
// (Update<T>Query, whose size is the match count of the NEW predicate).
func rustMinGroupSizeSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "query", Messages: []model.Message{
		{Name: "RecordCount", Fields: []model.Field{
			{Name: "count", Type: "int64", Number: 1},
		}},
		{Name: "QueryStats", Fields: []model.Field{
			{Name: "query_id", Type: "string", Number: 1},
			{Name: "size", Type: "int64", Number: 2},
		}},
		{Name: "QueryHandle", Fields: []model.Field{
			{Name: "query_id", Type: "string", Number: 1},
		}},
	}}})
}

func rustMinGroupSizeMethod(name, output string, min int32, serverStreaming bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            name,
		InputType:       "QueryHandle",
		OutputType:      output,
		FullInputType:   "query.QueryHandle",
		FullOutputType:  "query." + output,
		ServerStreaming: serverStreaming,
		AuthDisabled:    true,
		MinGroupSize:    min,
	}
}

func renderRustMinGroupSize(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "RecordQuery"},
		m, "query", "vservices", map[string]string{}, "query",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustMinGroupSizeSanitizer())
}

// Get<T>Count is the surface the acceptance item names: an exact count and no
// rows. Below the threshold the wrapper refuses with PERMISSION_DENIED, before
// the response reaches validation, and the message names neither the count nor
// any bound tighter than the declared threshold.
func TestRustMinGroupSize_UnaryCountRefusesBelowThreshold(t *testing.T) {
	got := renderRustMinGroupSize(t, rustMinGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false))
	for _, want := range []string{
		"// (spec.record).min_group_size: a response that carries only a size, over",
		"if resp.count < 5 {",
		`return Err(tonic::Status::permission_denied("min_group_size: a count over fewer than 5 records is a membership disclosure; widen the query's filter"));`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("GetTaskActivityCount wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// Update<T>Query returns QueryStats.size — the live match count of the NEWLY
// installed predicate. Guarding Count and not this one makes the guard
// bypassable in one extra RPC, so the same threshold applies, against that
// message's own size field rather than a hardcoded "count".
func TestRustMinGroupSize_RefineIsGuardedOnItsOwnSizeField(t *testing.T) {
	got := renderRustMinGroupSize(t, rustMinGroupSizeMethod("UpdateTaskActivityQuery", "QueryStats", 5, false))
	if !strings.Contains(got, "if resp.size < 5 {") {
		t.Fatalf("UpdateTaskActivityQuery wrapper does not guard QueryStats.size\ngot:\n%s", got)
	}
	if !strings.Contains(got, "permission_denied") {
		t.Fatalf("UpdateTaskActivityQuery refusal is not PERMISSION_DENIED\ngot:\n%s", got)
	}
}

// Subscribe<T>Count is the same arithmetic delivered repeatedly. The guard wraps
// the response stream ahead of the per-item validation so a below-threshold
// frame TERMINATES the stream carrying its own status code — a stream that
// merely went quiet at the threshold would disclose the crossing by silence.
func TestRustMinGroupSize_SubscribeStreamIsGuarded(t *testing.T) {
	got := renderRustMinGroupSize(t, rustMinGroupSizeMethod("SubscribeTaskActivityCount", "RecordCount", 5, true))
	for _, want := range []string{
		"let response = response.map(|stream| {",
		"if msg.count < 5 {",
		"permission_denied",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("SubscribeTaskActivityCount wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	guard := strings.Index(got, "if msg.count < 5 {")
	validate := strings.Index(got, "response validation failed")
	if guard < 0 || validate < 0 || guard > validate {
		t.Fatalf("the guard must run on a frame before that frame is validated\ngot:\n%s", got)
	}
}

// The annotation is additive: a record that declares no threshold generates the
// wrapper it generated before the annotation existed.
func TestRustMinGroupSize_UndeclaredEmitsNoGuard(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		rustMinGroupSizeMethod("GetTaskActivityCount", "RecordCount", 0, false),
		rustMinGroupSizeMethod("UpdateTaskActivityQuery", "QueryStats", 0, false),
		rustMinGroupSizeMethod("SubscribeTaskActivityCount", "RecordCount", 0, true),
	} {
		got := renderRustMinGroupSize(t, m)
		for _, forbidden := range []string{"min_group_size", "permission_denied"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s declares no threshold but its wrapper contains %q\ngot:\n%s", m.Name, forbidden, got)
			}
		}
	}
}

// A response the wrapper cannot read a single size off is unguardable, and a
// size-only RPC that cannot be guarded must fail generation rather than ship the
// disclosure — the same fail-closed stance (spec.store_principal) takes for the
// shapes its egress filter cannot reach. Same set Java and Go refuse.
func TestRustMinGroupSize_UnguardableShapeIsAGenerationError(t *testing.T) {
	san := rustMinGroupSizeSanitizer()
	if why := rustMinGroupSizeUnenforceable(rustMinGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false), san, "query"); why != "" {
		t.Fatalf("a plain Get<T>Count is guardable, got refusal %q", why)
	}
	unknown := rustMinGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false)
	unknown.FullOutputType = "query.QueryHandle" // no integer field at all
	if why := rustMinGroupSizeUnenforceable(unknown, san, "query"); why == "" {
		t.Fatal("expected a generation error for a min_group_size response with no size field")
	}
	clientStream := rustMinGroupSizeMethod("UploadTaskActivityCounts", "RecordCount", 5, false)
	clientStream.ClientStreaming = true
	if why := rustMinGroupSizeUnenforceable(clientStream, san, "query"); why == "" {
		t.Fatal("expected a generation error for a min_group_size client-streaming method")
	}
}

// The generation-time refusal is reached through generateServiceWrapper, so a
// build that declares a threshold on an unguardable shape fails rather than
// hosting it. This is what the old blanket "Java only" refusal is narrowed to.
func TestRustMinGroupSize_HostOfAnUnguardableShapeFailsGeneration(t *testing.T) {
	method := rustMinGroupSizeMethod("GetTaskActivityCount", "QueryHandle", 5, false)
	method.FullOutputType = "query.QueryHandle"
	info := &parser.ProtoInfo{
		Package:  "query",
		Services: []*parser.ServiceInfo{{Name: "RecordQuery", Methods: []*parser.MethodInfo{method}}},
	}
	svc := ServiceInfo{
		ProtoFile:   "vservices/query.proto",
		PackageName: "query",
		ServiceName: "RecordQuery",
		GrpcName:    "query",
		IsGrpc:      true,
		OutputDir:   t.TempDir(),
		ParentDir:   "vservices",
	}
	err := generateServiceWrapper(svc, info, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustMinGroupSizeSanitizer())
	if err == nil {
		t.Fatal("a Rust host of an unguardable min_group_size shape generated a wrapper")
	}
	if !strings.Contains(err.Error(), "spec.min_group_size") || !strings.Contains(err.Error(), "RecordQuery.GetTaskActivityCount") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}
