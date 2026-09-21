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

// The Java twin of principal_private_render_test.go's Rust suite: a
// (spec.record).private row is a STRONGER guarantee than a plain
// (spec.store_principal) field. Reaching a foreign owner's row here means the
// store's own query already leaked it — the process halts instead of
// silently filtering, and a service/workload caller with no delegated `act`
// claim is refused outright rather than compared against its own identity.
func recordPrincipalPrivateSanitizer() *javaEgressSanitizer {
	return newJavaEgressSanitizer([]model.ProtoFile{
		{Package: "agentsensor", Messages: []model.Message{
			{
				Name: "PrivateEvent",
				Annotations: map[string]model.AnnotationValue{
					"record":         {Type: model.AnnotationTypeBool, BoolValue: true},
					"record_owner":   {Type: model.AnnotationTypeString, StringValue: "spiffe_id"},
					"record_private": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
				Fields: []model.Field{
					{Name: "event_id", Type: "string", Number: 1},
					{Name: "spiffe_id", Type: "string", Number: 2, Annotations: map[string]model.AnnotationValue{
						"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true},
					}},
				},
			},
		}},
		{Package: "query", Messages: []model.Message{
			{Name: "SamplePrivateEventResponse", Fields: []model.Field{
				{Name: "items", Type: "PrivateEvent", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "PrivateEventWatchChange", Fields: []model.Field{
				{Name: "item", Type: "PrivateEvent", TypePackage: "agentsensor", IsMessage: true, Number: 2},
				{Name: "revision", Type: "int64", Number: 3},
			}},
			{Name: "PrivateEventWatchEvent", Fields: []model.Field{
				{Name: "snapshot_item", Type: "PrivateEvent", TypePackage: "agentsensor", IsMessage: true, IsOneOf: true, Number: 1},
				{Name: "snapshot_done", Type: "int64", IsOneOf: true, Number: 2},
				{Name: "delta", Type: "PrivateEventWatchChange", IsMessage: true, IsOneOf: true, Number: 3},
			}},
			{Name: "QueryHandle", Fields: []model.Field{{Name: "query_id", Type: "string", Number: 1}}},
		}},
	})
}

func renderRecordPrincipalPrivate(m *parser.MethodInfo, isC2S bool) string {
	return generateJavaGRPCWrapperSurface(
		m, "RecordQuery", "query",
		"Query."+m.InputType, "Query."+m.OutputType,
		"Helpers", "Helpers", false, true,
		map[string]service.AccountingInfo{}, "query", "vservices",
		map[string]string{}, proto.SkippedTraceTypes{},
		recordPrincipalPrivateSanitizer(), nil, nil, isC2S, false,
	)
}

func privateSampleMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "GetPrivateEventSample", InputType: "QueryHandle", OutputType: "SamplePrivateEventResponse",
		FullInputType: "query.QueryHandle", FullOutputType: "query.SamplePrivateEventResponse",
		AuthDisabled: true,
	}
}

func privatePullMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "ListPrivateEvent", InputType: "QueryHandle", OutputType: "PullPrivateEventResponse",
		FullInputType: "query.QueryHandle", FullOutputType: "query.PullPrivateEventResponse",
		AuthDisabled: true,
		Collection:   &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "PAGE", PageItemType: "agentsensor.PrivateEvent",
			PageItemsField: "items", DefaultPageSize: 100, MaxPageSize: 10000, OrderBy: []string{"event_id"},
		},
	}
}

func privateWatchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "WatchPrivateEvent", InputType: "QueryHandle", OutputType: "PrivateEventWatchEvent",
		FullInputType: "query.QueryHandle", FullOutputType: "query.PrivateEventWatchEvent",
		ServerStreaming: true, AuthDisabled: true,
		Collection: &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"event_id"}, WatchOneOf: "event",
			WatchItemType: "agentsensor.PrivateEvent", WatchChangeType: "query.PrivateEventWatchChange",
		},
	}
}

// The unary sample over a private record: a mismatch removes the row AND
// halts the JVM, and the resolve pins the delegate (act) for a service/
// workload caller instead of its own subject.
func TestRecordPrincipalPrivate_UnarySampleAbortsOnMismatch(t *testing.T) {
	got := renderRecordPrincipalPrivate(privateSampleMethod(), true)
	for _, want := range []string{
		`if ("service".equals(__pSubType) || "workload".equals(__pSubType)) {`,
		"__rowPrincipal = __pTok.getAct();",
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "GetPrivateEventSample reads a private record and requires a delegated caller identity (act claim)");`,
		"if (!__rowPrincipal.equals(__pfb.getItems(__pi).getSpiffeId())) {",
		"__pfb.removeItems(__pi);",
		"Runtime.getRuntime().halt(1);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("private unary sample wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// CONTRAST: the plain (non-private) unary sample must keep its exact current
// shape — no permission_denied refusal, no halt.
func TestRecordPrincipal_PlainUnarySampleUnaffectedByPrivateSupport(t *testing.T) {
	got := renderRecordPrincipal(sampleMethod("AgentEvent"), true)
	if strings.Contains(got, "permission_denied") || strings.Contains(got, "getAct()") {
		t.Fatalf("a plain store_principal record must not gain the private delegate resolve\ngot:\n%s", got)
	}
	if strings.Contains(got, "Runtime.getRuntime().halt(1)") {
		t.Fatalf("a plain store_principal record must not gain the private abort\ngot:\n%s", got)
	}
}

// The page leg over a private record: a mismatch aborts instead of being
// silently excluded from the page.
func TestRecordPrincipalPrivate_PageAbortsOnMismatch(t *testing.T) {
	got := renderRecordPrincipalPrivate(privatePullMethod(), true)
	for _, want := range []string{
		"if (__rowPrincipal.equals(__prow.getSpiffeId())) { __ownedItems.add(__prow); continue; }",
		"Runtime.getRuntime().halt(1);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("private page wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// The watch stream over a private record: a mismatched event halts the JVM
// instead of being dropped with `return;`.
func TestRecordPrincipalPrivate_WatchAbortsOnMismatch(t *testing.T) {
	got := renderRecordPrincipalPrivate(privateWatchMethod(), true)
	if !strings.Contains(got, "Runtime.getRuntime().halt(1);") {
		t.Fatalf("private watch wrapper must halt on a mismatched event\ngot:\n%s", got)
	}
	if strings.Contains(got, "!__rowPrincipal.equals(__event.getSnapshotItem().getSpiffeId())) { return; }") {
		t.Fatalf("a private record's watch guard must not silently return past a mismatch\ngot:\n%s", got)
	}
}
