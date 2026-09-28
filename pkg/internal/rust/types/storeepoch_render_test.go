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

// (spec.store_epoch) on a REQUEST field says the row's last-write clock is the
// store's: the generated wrapper refuses a caller-supplied value and stamps its
// own before the handler runs. These tests pin what the Rust wrapper does with
// the marker, and are the twin of java/types/storeepoch_render_test.go and
// go/types/storeepoch_render_test.go.

func rustStoreEpochBoolAnn(v bool) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"store_epoch": {BoolValue: v}}
}

// rustStoreEpochSanitizer indexes three request shapes: one declaring the epoch,
// one declaring nothing, and one declaring `= false` — which is "not annotated",
// not "annotated with false".
func rustStoreEpochSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		{Name: "WriteTaskRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
			{Name: "store_epoch_ms", Type: "int64", Number: 2, Annotations: rustStoreEpochBoolAnn(true)},
		}},
		{Name: "PlainRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
		}},
		{Name: "OptedOutRequest", Fields: []model.Field{
			{Name: "store_epoch_ms", Type: "int64", Number: 1, Annotations: rustStoreEpochBoolAnn(false)},
		}},
		{Name: "WriteTaskResponse", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
		}},
	}}})
}

func rustStoreEpochMethod(name, input string, clientStreaming, serverStreaming bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            name,
		InputType:       input,
		OutputType:      "WriteTaskResponse",
		FullInputType:   "tasks." + input,
		FullOutputType:  "tasks.WriteTaskResponse",
		ClientStreaming: clientStreaming,
		ServerStreaming: serverStreaming,
		AuthDisabled:    true,
	}
}

func renderRustStoreEpoch(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Tasks"},
		m, "tasks", "vservices", map[string]string{}, "tasks",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustStoreEpochSanitizer())
}

// The stamp itself: refuse a supplied value, then write the store's clock —
// both BEFORE the handler call, which is the whole point of the seam.
func TestRustStoreEpoch_UnaryStampsBeforeTheHandler(t *testing.T) {
	got := renderRustStoreEpoch(t, rustStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false))
	for _, want := range []string{
		"// (spec.store_epoch) store_epoch_ms: the row's last-write clock is the STORE's, never",
		"if req.store_epoch_ms != 0 {",
		`"store_epoch_ms is stamped by the store and must not be supplied by the caller"`,
		"req.store_epoch_ms = std::time::SystemTime::now()",
		".duration_since(std::time::UNIX_EPOCH)",
		".as_millis() as i64;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WriteTask wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	stamp := strings.Index(got, "req.store_epoch_ms = std::time::SystemTime::now()")
	call := strings.Index(got, "self.handler.write_task(")
	if stamp < 0 || call < 0 || stamp > call {
		t.Fatalf("the stamp must run before the handler call (stamp=%d handler=%d)\ngot:\n%s", stamp, call, got)
	}
}

// Unlike (spec.principal) there is no "equal passes" case: the store owns the
// value outright, so a correct-looking supplied value is still a refusal.
func TestRustStoreEpoch_HasNoEqualPassesCase(t *testing.T) {
	got := renderRustStoreEpoch(t, rustStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false))
	if strings.Contains(got, "does not match") {
		t.Fatal("the store epoch stamp must not render a compare-and-accept branch; any supplied value is refused")
	}
}

// A request declaring no epoch field — or declaring `= false` — renders no
// stamp at all. The annotation is additive.
func TestRustStoreEpoch_UndeclaredRequestRendersNothing(t *testing.T) {
	for _, input := range []string{"PlainRequest", "OptedOutRequest"} {
		got := renderRustStoreEpoch(t, rustStoreEpochMethod("WriteTask", input, false, false))
		if strings.Contains(got, "spec.store_epoch") {
			t.Fatalf("%s must render no stamp\ngot:\n%s", input, got)
		}
	}
}

// A streamed write would need a per-item stamp no wrapper renders, so it fails
// generation instead — the same set Java and Go refuse, for the same reason. A
// request that declares no epoch field is never refused for its shape.
func TestRustStoreEpoch_StreamingShapesAreUnenforceable(t *testing.T) {
	san := rustStoreEpochSanitizer()
	const why = "the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render"

	if got := rustStoreEpochUnenforceable(rustStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false), san, "tasks"); got != "" {
		t.Fatalf("a unary epoch-bearing write is enforceable; got %q", got)
	}
	for _, m := range []*parser.MethodInfo{
		rustStoreEpochMethod("StreamWriteTask", "WriteTaskRequest", true, false),
		rustStoreEpochMethod("WatchWriteTask", "WriteTaskRequest", false, true),
	} {
		if got := rustStoreEpochUnenforceable(m, san, "tasks"); got != why {
			t.Fatalf("%s: want %q, got %q", m.Name, why, got)
		}
	}
	for _, m := range []*parser.MethodInfo{
		rustStoreEpochMethod("StreamPlain", "PlainRequest", true, false),
		rustStoreEpochMethod("WatchPlain", "PlainRequest", false, true),
	} {
		if got := rustStoreEpochUnenforceable(m, san, "tasks"); got != "" {
			t.Fatalf("%s declares no epoch field and must not be refused; got %q", m.Name, got)
		}
	}
}

// WHICH FIELD is read off the shared parser.StoreEpochField, and `= false` means
// unannotated there too.
func TestRustStoreEpochFieldLookup(t *testing.T) {
	san := rustStoreEpochSanitizer()
	for _, tc := range []struct{ typ, want string }{
		{"tasks.WriteTaskRequest", "store_epoch_ms"},
		{"tasks.PlainRequest", ""},
		{"tasks.OptedOutRequest", ""},
		{"tasks.NoSuchRequest", ""},
		{"", ""},
	} {
		if got := san.storeEpochField(tc.typ, "tasks"); got != tc.want {
			t.Errorf("storeEpochField(%q) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}
