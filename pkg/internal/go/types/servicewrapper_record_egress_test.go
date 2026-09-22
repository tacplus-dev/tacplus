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
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// The Go (spec.store_namespace)/(spec.store_principal)/(spec.record).private
// egress row filters. Until recordegress_gen.go existed the Go wrapper rendered
// none of them and REFUSED to generate wherever one was owed; these pin the
// filter that replaced that refusal, on every seam, under the same emission
// gate Java and Rust apply.

func recordEgressSanitizer() *egressSanitizer {
	boolAnn := func(name string) map[string]model.AnnotationValue {
		return map[string]model.AnnotationValue{name: {Type: model.AnnotationTypeBool, BoolValue: true}}
	}
	record := func(private bool) map[string]model.AnnotationValue {
		a := map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}}
		if private {
			a["record_private"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
		return a
	}
	rowFields := []model.Field{
		{Name: "namespace", Type: "string", Number: 1, Annotations: boolAnn("store_namespace")},
		{Name: "owner", Type: "string", Number: 2, Annotations: boolAnn("store_principal")},
		{Name: "title", Type: "string", Number: 3},
	}
	return newEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		{Name: "Task", Annotations: record(false), Fields: rowFields},
		{Name: "SecretTask", Annotations: record(true), Fields: rowFields},
		{Name: "TaskFilter", Fields: []model.Field{
			{Name: "namespace", Type: "string", Number: 1, Annotations: boolAnn("store_namespace")},
			{Name: "owner", Type: "string", Number: 2, Annotations: boolAnn("store_principal")},
		}},
		{Name: "ListTasksRequest", Fields: []model.Field{
			{Name: "filter", Type: "TaskFilter", Number: 1, IsMessage: true},
			{Name: "page_token", Type: "string", Number: 2},
			{Name: "page_size", Type: "int32", Number: 3},
		}},
		{Name: "ListTasksResponse", Fields: []model.Field{
			{Name: "tasks", Type: "Task", Number: 1, IsRepeated: true, IsMessage: true},
			{Name: "next_page_token", Type: "string", Number: 2},
		}},
		{Name: "ListSecretsResponse", Fields: []model.Field{
			{Name: "tasks", Type: "SecretTask", Number: 1, IsRepeated: true, IsMessage: true},
		}},
		{Name: "TaskWatchEvent", Fields: []model.Field{
			{Name: "snapshot_item", Type: "Task", Number: 1, IsMessage: true},
			{Name: "delta", Type: "TaskWatchChange", Number: 2, IsMessage: true},
		}},
		{Name: "TaskWatchChange", Fields: []model.Field{
			{Name: "item", Type: "Task", Number: 1, IsMessage: true},
			{Name: "revision", Type: "int64", Number: 2},
		}},
	}}})
}

func recordEgressMethod(name, output string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: name, InputType: "ListTasksRequest", OutputType: output,
		FullInputType: "tasks.ListTasksRequest", FullOutputType: "tasks." + output,
	}
}

// recordEgressService is the three surfaces the emission gate distinguishes.
func recordEgressService(kind string, m *parser.MethodInfo) *parser.ServiceInfo {
	svc := &parser.ServiceInfo{Name: "TaskApi", Methods: []*parser.MethodInfo{m}}
	switch kind {
	case "c2s":
		svc.IsC2S = true
	case "recordplane":
		svc.SynthesizedKind = parser.SynthesizedKindRecordMap
	default:
		svc.IsS2S = true
	}
	return svc
}

func renderRecordEgress(kind string, m *parser.MethodInfo) string {
	return generateGRPCWrapper(recordEgressService(kind, m), m, "tasks", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, recordEgressSanitizer())
}

func wantAll(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("wrapper missing %q\ngot:\n%s", w, got)
		}
	}
}

func wantNone(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(got, w) {
			t.Fatalf("wrapper must not contain %q\ngot:\n%s", w, got)
		}
	}
}

// ---------------------------------------------------------------------------
// The UNARY seam.

func TestGoRecordEgress_UnaryC2SFiltersPrincipalThenNamespace(t *testing.T) {
	got := renderRecordEgress("c2s", recordEgressMethod("ListTasks", "ListTasksResponse"))
	wantAll(t, got,
		`__readPrincipal = caller.Token().GetSubject()`,
		`status.Error(codes.PermissionDenied, "ListTasks returns rows owned by a principal and the caller presented no verified subject")`,
		`if __row.GetOwner() == __readPrincipal {`,
		`__readNamespace = caller.Token().GetNamespace()`,
		`__readNamespaceLinked = caller.Token().GetLinked()`,
		`if __nsAccepted(__row.GetNamespace()) {`,
		`os.Exit(1)`,
		`(spec.store_namespace) backstop fired`,
	)
	// Principal first, then namespace over what survived — the order Java and
	// Rust apply, and the order the abort's meaning depends on.
	if strings.Index(got, "__principalRows0") > strings.Index(got, "__namespaceRows0") {
		t.Fatalf("the principal filter must run before the namespace filter\ngot:\n%s", got)
	}
	// A plain (spec.store_principal) mismatch DROPS the row; only the namespace
	// backstop halts, so the principal filter emits no crash of its own.
	wantNone(t, got, "(spec.record).private backstop fired")
	// The c2s half has nothing to decide at runtime.
	wantNone(t, got, "__nsPinned")
}

func TestGoRecordEgress_RecordPlaneS2SPinsTenantAtRuntimeOnly(t *testing.T) {
	got := renderRecordEgress("recordplane", recordEgressMethod("ListTasks", "ListTasksResponse"))
	wantAll(t, got,
		`__nsPinned := !strings.HasPrefix(__readNamespace, "/system")`,
		`if !__nsPinned || __nsAccepted(__row.GetNamespace()) {`,
	)
	// The principal seam is a statement about a USER; a peer service is not one,
	// so it stays c2s-only even on the tenant-owned plane.
	wantNone(t, got, "__readPrincipal")
}

func TestGoRecordEgress_PlainS2SEmitsNoFilter(t *testing.T) {
	got := renderRecordEgress("s2s", recordEgressMethod("ListTasks", "ListTasksResponse"))
	wantNone(t, got, "__readPrincipal", "__readNamespace", "backstop fired")
}

// ---------------------------------------------------------------------------
// The (spec.record).private owner pin.

func TestGoRecordEgress_PrivateRowAbortsAndGatesTheDelegatedToken(t *testing.T) {
	got := renderRecordEgress("c2s", recordEgressMethod("ListSecrets", "ListSecretsResponse"))
	wantAll(t, got,
		`switch caller.Token().GetSubType() {`,
		`case "service", "workload":`,
		`__readPrincipal = caller.Token().GetAct()`,
		`status.Error(codes.PermissionDenied, "ListSecrets reads a private record and requires a delegated caller identity (act claim)")`,
		`(spec.record).private backstop fired`,
	)
	// A private mismatch aborts; it must not degrade to the plain filter's
	// silent drop, so the crash is inside the same block the drop is.
	if strings.Index(got, "__principalRows0 = append") > strings.Index(got, "(spec.record).private backstop fired") {
		t.Fatalf("the private abort must follow the row-drop in the same filter\ngot:\n%s", got)
	}
}

// A private record on a surface no user token reaches has no principal for the
// pin to be — the one refusal that is language-independent and still stands.
func TestGoRecordEgress_PrivateOnPlainS2SIsStillAGenerationError(t *testing.T) {
	m := recordEgressMethod("ListSecrets", "ListSecretsResponse")
	info := &parser.ProtoInfo{Package: "tasks", Services: []*parser.ServiceInfo{recordEgressService("s2s", m)}}
	err := generateServiceWrapper(recordEgressWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, recordEgressSanitizer())
	if err == nil {
		t.Fatal("a Go plain-s2s host of (spec.record).private rows generated a wrapper; there is no caller principal to pin to")
	}
	if !strings.Contains(err.Error(), "spec.record).private") || !strings.Contains(err.Error(), "TaskApi.ListSecrets") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The PAGE seam.

func recordEgressPageMethod() *parser.MethodInfo {
	m := recordEgressMethod("ListTasks", "ListTasksResponse")
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"title"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode: "PAGE", OrderBy: []string{"title"}, DefaultPageSize: 50, MaxPageSize: 10000,
		PageItemType: "tasks.Task", PageItemsField: "tasks",
	}
	return m
}

func TestGoRecordEgress_PageFiltersBeforeTheResponseIsAssembled(t *testing.T) {
	got := renderRecordEgress("c2s", recordEgressPageMethod())
	wantAll(t, got,
		`__ownedItems := page.Items[:0:0]`,
		`__tenantItems := page.Items[:0:0]`,
		`page.Items = __tenantItems`,
	)
	// Filtering before assembly is what keeps next_page_token and total_count
	// describing the rows the caller is actually shown.
	if strings.Index(got, "page.Items = __tenantItems") > strings.Index(got, "collection.EncodePageToken") {
		t.Fatalf("the page filter must run before the next cursor is encoded\ngot:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// The WATCH seam.

func recordEgressWatchMethod() *parser.MethodInfo {
	m := recordEgressMethod("WatchTasks", "TaskWatchEvent")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "WATCH"}
	m.Resolved = &collection.Resolved{
		Mode: "WATCH", WatchOneOf: "event", WatchItemType: "tasks.Task",
		WatchChangeType: "tasks.TaskWatchChange", ReplayDepth: 64, IdleSecs: 300,
		WatchRevisionField: "revision",
	}
	return m
}

func TestGoRecordEgress_WatchGuardsBothFrames(t *testing.T) {
	got := renderRecordEgress("c2s", recordEgressWatchMethod())
	wantAll(t, got,
		// snapshot_item IS the row.
		"if item.GetOwner() != __readPrincipal {",
		"if !__nsAccepted(item.GetNamespace()) {",
		// the delta WRAPS the row, and an unset envelope field is nil.
		"if __row := change.GetItem(); __row != nil {",
		"if __row.GetOwner() != __readPrincipal {",
		"if !__nsAccepted(__row.GetNamespace()) {",
		// a principal mismatch skips the frame; a tenant mismatch halts.
		"continue",
		"(spec.store_namespace) backstop fired",
	)
	// The resolves bind once, ahead of the producer loop.
	if strings.Index(got, "__readNamespace = caller.Token().GetNamespace()") > strings.Index(got, "p.handler.WatchTasks(") {
		t.Fatalf("the watch resolves must bind before the handler opens the stream\ngot:\n%s", got)
	}
}

// A delta that carries pinned rows in a repeated field has no shape the
// per-frame guard can walk — refused, exactly as Rust refuses it.
func TestGoRecordEgress_WatchUnwalkableDeltaIsAGenerationError(t *testing.T) {
	san := recordEgressSanitizer()
	msg := san.msgs["tasks\x00TaskWatchChange"]
	msg.Fields[0].IsRepeated = true
	m := recordEgressWatchMethod()
	if err := goStreamEgressRefusal("TaskApi", m, san, "tasks"); err == nil {
		t.Fatal("a WATCH whose delta carries pinned rows in a repeated field generated a guard it cannot walk")
	}
}

// ---------------------------------------------------------------------------
// Streaming shapes with no per-item hook.

func TestGoRecordEgress_SnapshotStreamOverPinnedRowsIsAGenerationError(t *testing.T) {
	m := recordEgressMethod("StreamTasks", "Task")
	m.ServerStreaming = true
	err := goStreamEgressRefusal("TaskApi", m, recordEgressSanitizer(), "tasks")
	if err == nil {
		t.Fatal("a plain server stream over pinned rows generated with no egress filter")
	}
	if !strings.Contains(err.Error(), "streaming path") {
		t.Fatalf("refusal does not name the shape it refused on: %v", err)
	}
}

func TestGoRecordEgress_UnpinnedStreamStillGenerates(t *testing.T) {
	m := recordEgressMethod("StreamFilters", "TaskFilter")
	m.ServerStreaming = true
	if err := goStreamEgressRefusal("TaskApi", m, recordEgressSanitizer(), "tasks"); err != nil {
		t.Fatalf("a stream over a non-record message was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The query predicate.

func TestGoRecordEgress_PredicatePinsAreStampOrRefuse(t *testing.T) {
	got := renderRecordEgress("c2s", recordEgressMethod("ListTasks", "ListTasksResponse"))
	wantAll(t, got,
		`__queryPrincipal = caller.Token().GetSubject()`,
		`__pred.Owner = __queryPrincipal`,
		`status.Error(codes.InvalidArgument, "filter.owner names a principal other than the caller; the query plane is per-principal and the predicate is not the caller's to choose")`,
		`__pred.Namespace = __queryNamespace`,
		`} else if !__queryNsAccepted(__pred.GetNamespace()) {`,
		`status.Error(codes.InvalidArgument, "filter.namespace names a tenant the caller neither owns nor is linked to; the tenant boundary is not the predicate's to choose")`,
	)
	// The pin must precede the handler, or the store answers the unpinned query
	// and the aborting egress backstop becomes client-triggerable.
	if strings.Index(got, "__pred.Namespace = __queryNamespace") > strings.Index(got, "p.handler.ListTasks(") {
		t.Fatalf("the predicate pin must precede the handler call\ngot:\n%s", got)
	}
}

func TestGoRecordEgress_RecordPlanePredicateLeavesSystemPeersUnpinned(t *testing.T) {
	got := renderRecordEgress("recordplane", recordEgressMethod("ListTasks", "ListTasksResponse"))
	wantAll(t, got, `if !strings.HasPrefix(__queryNamespace, "/system") {`)
	wantNone(t, got, "__queryPrincipal")
}

// recordEgressWrapperSvc is the whole-wrapper harness, for the generation
// refusals that only surface through generateServiceWrapper.
func recordEgressWrapperSvc(t *testing.T) ServiceInfo {
	t.Helper()
	return ServiceInfo{
		ProtoFile:   "services/tasks.proto",
		PackageName: "tasks",
		DirName:     "tasks",
		ServiceName: "TaskApi",
		GrpcName:    "tasks",
		ParentDir:   "services",
		Config:      &tactype.Config{OutputDir: t.TempDir()},
	}
}
