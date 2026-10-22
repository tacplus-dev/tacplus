// Copyright (c) 2026 Jody Scott
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

// (spec.store_epoch) on a REQUEST field says the row's last-write clock is the
// store's: the generated wrapper refuses a caller-supplied value and stamps its
// own before the handler runs. These tests pin what the Go wrapper does with the
// marker, and are the twin of java/types/storeepoch_render_test.go and
// rust/types/storeepoch_render_test.go.

func boolAnn(v bool) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"store_epoch": {BoolValue: v}}
}

// goStoreEpochSanitizer indexes three request shapes: one declaring the epoch,
// one declaring nothing, and one declaring `= false` — which is "not annotated",
// not "annotated with false".
func goStoreEpochSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		{Name: "WriteTaskRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
			{Name: "store_epoch_ms", Type: "int64", Number: 2, Annotations: boolAnn(true)},
		}},
		{Name: "PlainRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
		}},
		{Name: "OptedOutRequest", Fields: []model.Field{
			{Name: "store_epoch_ms", Type: "int64", Number: 1, Annotations: boolAnn(false)},
		}},
		{Name: "WriteTaskResponse", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
		}},
	}}})
}

func goStoreEpochMethod(name, input string, clientStreaming, serverStreaming bool) *parser.MethodInfo {
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

func renderGoStoreEpoch(m *parser.MethodInfo) string {
	return generateGRPCWrapper(&parser.ServiceInfo{Name: "Tasks"}, m, "tasks", "vservices", "tasks",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{},
		goStoreEpochSanitizer())
}

// The stamp itself: refuse a supplied value, then write the store's clock —
// both BEFORE the handler call, which is the whole point of the seam.
func TestGoStoreEpoch_UnaryStampsBeforeTheHandler(t *testing.T) {
	got := renderGoStoreEpoch(goStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false))
	for _, want := range []string{
		"// (spec.store_epoch) store_epoch_ms: the row's last-write clock is the STORE's, never",
		"if req.StoreEpochMs != 0 {",
		`return nil, status.Error(codes.InvalidArgument, "store_epoch_ms is stamped by the store and must not be supplied by the caller")`,
		"req.StoreEpochMs = time.Now().UnixMilli()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WriteTask wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	stamp := strings.Index(got, "req.StoreEpochMs = time.Now().UnixMilli()")
	call := strings.Index(got, "p.handler.WriteTask(")
	if stamp < 0 || call < 0 || stamp > call {
		t.Fatalf("the stamp must run before the handler call (stamp=%d handler=%d)\ngot:\n%s", stamp, call, got)
	}
}

// Unlike (spec.principal) there is no "equal passes" case: the store owns the
// value outright, so a correct-looking supplied value is still a refusal.
func TestGoStoreEpoch_HasNoEqualPassesCase(t *testing.T) {
	got := renderGoStoreEpoch(goStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false))
	if strings.Contains(got, "does not match") {
		t.Fatal("the store epoch stamp must not render a compare-and-accept branch; any supplied value is refused")
	}
}

// A request declaring no epoch field — or declaring `= false` — renders no
// stamp at all. The annotation is additive.
func TestGoStoreEpoch_UndeclaredRequestRendersNothing(t *testing.T) {
	for _, input := range []string{"PlainRequest", "OptedOutRequest"} {
		got := renderGoStoreEpoch(goStoreEpochMethod("WriteTask", input, false, false))
		if strings.Contains(got, "spec.store_epoch") {
			t.Fatalf("%s must render no stamp\ngot:\n%s", input, got)
		}
	}
}

// A streamed write would need a per-item stamp no wrapper renders, so it fails
// generation instead — the same set Java refuses, for the same reason. A
// request that declares no epoch field is never refused for its shape.
func TestGoStoreEpoch_StreamingShapesAreUnenforceable(t *testing.T) {
	san := goStoreEpochSanitizer()
	const why = "the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render"

	if got := goStoreEpochUnenforceable(goStoreEpochMethod("WriteTask", "WriteTaskRequest", false, false), san, "tasks"); got != "" {
		t.Fatalf("a unary epoch-bearing write is enforceable; got %q", got)
	}
	for _, m := range []*parser.MethodInfo{
		goStoreEpochMethod("StreamWriteTask", "WriteTaskRequest", true, false),
		goStoreEpochMethod("WatchWriteTask", "WriteTaskRequest", false, true),
	} {
		if got := goStoreEpochUnenforceable(m, san, "tasks"); got != why {
			t.Fatalf("%s: want %q, got %q", m.Name, why, got)
		}
	}
	for _, m := range []*parser.MethodInfo{
		goStoreEpochMethod("StreamPlain", "PlainRequest", true, false),
		goStoreEpochMethod("WatchPlain", "PlainRequest", false, true),
	} {
		if got := goStoreEpochUnenforceable(m, san, "tasks"); got != "" {
			t.Fatalf("%s declares no epoch field and must not be refused; got %q", m.Name, got)
		}
	}
}

// WHICH FIELD is read off the shared parser.StoreEpochField, and `= false` means
// unannotated there too.
func TestGoStoreEpochFieldLookup(t *testing.T) {
	san := goStoreEpochSanitizer()
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
