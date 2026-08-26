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
// Java wrapper does with the stamp: the refusal shape (PERMISSION_DENIED, no
// count in the text), that it reaches the stream as well as the unary count, and
// that an undeclared record's wrapper is byte-for-byte what it was before.

// minGroupSizeSanitizer indexes the two size-only response shapes the injector
// emits: RecordCount (Get<T>Count / Subscribe<T>Count) and QueryStats
// (Update<T>Query, whose size is the match count of the NEW predicate).
func minGroupSizeSanitizer() *javaEgressSanitizer {
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "query", Messages: []model.Message{
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

func minGroupSizeMethod(name, output string, min int32, serverStreaming bool) *parser.MethodInfo {
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

func renderMinGroupSize(m *parser.MethodInfo) string {
	return generateJavaGRPCWrapper(
		m,
		"RecordQuery",
		"query",
		"Query.QueryHandle",
		"Query."+m.OutputType,
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{},
		"query",
		"vservices",
		map[string]string{},
		proto.SkippedTraceTypes{},
		minGroupSizeSanitizer(),
		nil,
		nil,
	)
}

// Get<T>Count is the surface the acceptance item names: an exact count and no
// rows. Below the threshold the wrapper refuses with PERMISSION_DENIED, before
// the response reaches validation, and the message names neither the count nor
// any bound tighter than the declared threshold.
func TestJavaMinGroupSize_UnaryCountRefusesBelowThreshold(t *testing.T) {
	got := renderMinGroupSize(minGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false))
	for _, want := range []string{
		"// (spec.record).min_group_size: a response that carries only a size, over",
		"if (response.getCount() < 5L) {",
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "min_group_size: a count over fewer than 5 records is a membership disclosure; widen the query's filter");`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("GetTaskActivityCount wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	guard := strings.Index(got, "if (response.getCount() < 5L) {")
	validate := strings.Index(got, "Helpers.validateRecordCount(response);")
	if guard < 0 || validate < 0 || guard > validate {
		t.Fatalf("the guard must run on the handler's response before response validation\ngot:\n%s", got)
	}
}

// Update<T>Query returns QueryStats.size — the live match count of the NEWLY
// installed predicate. Guarding Count and not this one makes the guard
// bypassable in one extra RPC, so the same threshold applies, against that
// message's own size field rather than a hardcoded "count".
func TestJavaMinGroupSize_RefineIsGuardedOnItsOwnSizeField(t *testing.T) {
	got := renderMinGroupSize(minGroupSizeMethod("UpdateTaskActivityQuery", "QueryStats", 5, false))
	if !strings.Contains(got, "if (response.getSize() < 5L) {") {
		t.Fatalf("UpdateTaskActivityQuery wrapper does not guard QueryStats.size\ngot:\n%s", got)
	}
	if !strings.Contains(got, "ProtoException.PERMISSION_DENIED") {
		t.Fatalf("UpdateTaskActivityQuery refusal is not PERMISSION_DENIED\ngot:\n%s", got)
	}
}

// Subscribe<T>Count is the same arithmetic delivered repeatedly. The guard sits
// in front of the validating delegate so a below-threshold frame TERMINATES the
// stream carrying its own status code — a stream that merely went quiet at the
// threshold would disclose the crossing by silence.
func TestJavaMinGroupSize_SubscribeStreamIsGuarded(t *testing.T) {
	got := renderMinGroupSize(minGroupSizeMethod("SubscribeTaskActivityCount", "RecordCount", 5, true))
	for _, want := range []string{
		"guardedResponseObserver(validatingResponseObserver(responseObserver, value -> Helpers.validateRecordCount(value))",
		"if (value.getCount() < 5L) {",
		"ProtoException.PERMISSION_DENIED",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("SubscribeTaskActivityCount wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// The annotation is additive: a record that declares no threshold generates the
// wrapper it generated before the annotation existed. This is the same
// build-time control the record_map stamping test asserts one hop earlier.
func TestJavaMinGroupSize_UndeclaredEmitsNoGuard(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		minGroupSizeMethod("GetTaskActivityCount", "RecordCount", 0, false),
		minGroupSizeMethod("UpdateTaskActivityQuery", "QueryStats", 0, false),
		minGroupSizeMethod("SubscribeTaskActivityCount", "RecordCount", 0, true),
	} {
		got := renderMinGroupSize(m)
		for _, forbidden := range []string{"min_group_size", "guardedResponseObserver", "PERMISSION_DENIED"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("%s declares no threshold but its wrapper contains %q\ngot:\n%s", m.Name, forbidden, got)
			}
		}
	}
}

// A response the wrapper cannot read a single size off is unguardable, and a
// size-only RPC that cannot be guarded must fail generation rather than ship the
// disclosure — the same fail-closed stance (spec.store_principal) takes for the
// shapes its egress filter cannot reach.
func TestJavaMinGroupSize_UnguardableShapeIsAGenerationError(t *testing.T) {
	san := minGroupSizeSanitizer()
	if why := javaMinGroupSizeUnenforceable(minGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false), san, "query"); why != "" {
		t.Fatalf("a plain Get<T>Count is guardable, got refusal %q", why)
	}
	unknown := minGroupSizeMethod("GetTaskActivityCount", "RecordCount", 5, false)
	unknown.FullOutputType = "query.QueryHandle" // no integer field at all
	if why := javaMinGroupSizeUnenforceable(unknown, san, "query"); why == "" {
		t.Fatal("expected a generation error for a min_group_size response with no size field")
	}
	clientStream := minGroupSizeMethod("UploadTaskActivityCounts", "RecordCount", 5, false)
	clientStream.ClientStreaming = true
	if why := javaMinGroupSizeUnenforceable(clientStream, san, "query"); why == "" {
		t.Fatal("expected a generation error for a min_group_size client-streaming method")
	}
}
