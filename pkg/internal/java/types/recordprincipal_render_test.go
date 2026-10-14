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

// §4b's guarantee is `principal ∈ entitled_set`. These tests pin the Java end of
// it for the record query plane: which rows a c2s caller may SEE, as opposed to
// which fields egress raw. Before this filter existed any caller holding the
// plane's grant read every principal's rows on every one of the plane's three
// row-bearing shapes (unary sample, page, watch stream).

func recordPrincipalSanitizer() *javaEgressSanitizer {
	rec := func(name, owner string, storePrincipal string) model.Message {
		m := model.Message{
			Name: name,
			Annotations: map[string]model.AnnotationValue{
				"record":       {Type: model.AnnotationTypeBool, BoolValue: true},
				"record_owner": {Type: model.AnnotationTypeString, StringValue: owner},
			},
			Fields: []model.Field{
				{Name: "event_id", Type: "string", Number: 1},
				{Name: owner, Type: "string", Number: 2},
			},
		}
		if storePrincipal != "" {
			m.Fields = append(m.Fields, model.Field{
				Name: storePrincipal, Type: "string", Number: 3,
				Annotations: map[string]model.AnnotationValue{
					"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			})
		}
		return m
	}
	return newJavaEgressSanitizer([]model.ProtoFile{
		{Package: "agentsensor", Messages: []model.Message{
			rec("AgentEvent", "spiffe_id", ""),
			// A record whose declared owner is NOT the human principal: the
			// explicit (spec.store_principal) is what the filter must prefer.
			rec("AccountingRecord", "spiffe_id", "principal_sub"),
			// A record with no resolvable owner at all — generation must refuse.
			{Name: "Orphan", Annotations: map[string]model.AnnotationValue{
				"record": {Type: model.AnnotationTypeBool, BoolValue: true},
			}, Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
			// Not a record: carries no rows, so no filter is owed or refused.
			{Name: "Blob", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
		}},
		{Package: "query", Messages: []model.Message{
			{Name: "SampleAgentEventResponse", Fields: []model.Field{
				{Name: "items", Type: "AgentEvent", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "SampleAccountingRecordResponse", Fields: []model.Field{
				{Name: "items", Type: "AccountingRecord", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "SampleOrphanResponse", Fields: []model.Field{
				{Name: "items", Type: "Orphan", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "SampleBlobResponse", Fields: []model.Field{
				{Name: "items", Type: "Blob", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "AgentEventWatchChange", Fields: []model.Field{
				{Name: "item", Type: "AgentEvent", TypePackage: "agentsensor", IsMessage: true, Number: 2},
				{Name: "revision", Type: "int64", Number: 3},
			}},
			{Name: "AgentEventWatchEvent", Fields: []model.Field{
				{Name: "snapshot_item", Type: "AgentEvent", TypePackage: "agentsensor", IsMessage: true, IsOneOf: true, Number: 1},
				{Name: "snapshot_done", Type: "int64", IsOneOf: true, Number: 2},
				{Name: "delta", Type: "AgentEventWatchChange", IsMessage: true, IsOneOf: true, Number: 3},
			}},
			{Name: "QueryHandle", Fields: []model.Field{{Name: "query_id", Type: "string", Number: 1}}},
		}},
	})
}

func renderRecordPrincipal(m *parser.MethodInfo, isC2S bool) string {
	return generateJavaGRPCWrapperSurface(
		m, "RecordQuery", "query",
		"Query."+m.InputType, "Query."+m.OutputType,
		"Helpers", "Helpers", false, true,
		map[string]service.AccountingInfo{}, "query", "vservices",
		map[string]string{}, proto.SkippedTraceTypes{},
		recordPrincipalSanitizer(), nil, nil, isC2S, false,
	)
}

func sampleMethod(t string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "Get" + t + "Sample", InputType: "QueryHandle", OutputType: "Sample" + t + "Response",
		FullInputType: "query.QueryHandle", FullOutputType: "query.Sample" + t + "Response",
		AuthDisabled: true,
	}
}

func pullMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "ListAgentEvent", InputType: "QueryHandle", OutputType: "PullAgentEventResponse",
		FullInputType: "query.QueryHandle", FullOutputType: "query.PullAgentEventResponse",
		AuthDisabled: true,
		Collection:   &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "PAGE", PageItemType: "agentsensor.AgentEvent",
			PageItemsField: "items", DefaultPageSize: 100, MaxPageSize: 10000, OrderBy: []string{"event_id"},
		},
	}
}

func watchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "WatchAgentEvent", InputType: "QueryHandle", OutputType: "AgentEventWatchEvent",
		FullInputType: "query.QueryHandle", FullOutputType: "query.AgentEventWatchEvent",
		ServerStreaming: true, AuthDisabled: true,
		Collection: &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"event_id"}, WatchOneOf: "event",
			WatchItemType: "agentsensor.AgentEvent", WatchChangeType: "query.AgentEventWatchChange",
		},
	}
}

// The unary sample: rows the caller does not own are removed before the response
// is validated or returned, and the principal is the token's own subject.
func TestRecordPrincipal_UnarySampleFiltersRowsToCaller(t *testing.T) {
	got := renderRecordPrincipal(sampleMethod("AgentEvent"), true)
	for _, want := range []string{
		"final String __rowPrincipal = __pTok == null ? null : __pTok.getSubject();",
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "GetAgentEventSample returns rows owned by a principal and the caller presented no verified subject");`,
		"for (int __pi = __pfb.getItemsCount() - 1; __pi >= 0; __pi--) {",
		"if (!__rowPrincipal.equals(__pfb.getItems(__pi).getSpiffeId())) { __pfb.removeItems(__pi); }",
		"response = __pfb.build();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unary sample wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	filter := strings.Index(got, "__pfb.removeItems(__pi)")
	validate := strings.Index(got, "Helpers.validateSampleAgentEventResponse(response);")
	if filter < 0 || validate < 0 || filter > validate {
		t.Fatalf("the row filter must run before response validation\ngot:\n%s", got)
	}
}

// (spec.store_principal) is the OVERRIDE for a record whose declared owner is
// not the human principal — accounting rows are owned by the emitting service.
// Filtering those on the owner would narrow every caller to nothing.
func TestRecordPrincipal_StorePrincipalBeatsRecordOwner(t *testing.T) {
	got := renderRecordPrincipal(sampleMethod("AccountingRecord"), true)
	if !strings.Contains(got, "__pfb.getItems(__pi).getPrincipalSub()") {
		t.Fatalf("the explicit (spec.store_principal) field must win over (spec.record).owner\ngot:\n%s", got)
	}
	if strings.Contains(got, "__pfb.getItems(__pi).getSpiffeId()") {
		t.Fatalf("the owner field must not be used when (spec.store_principal) is declared\ngot:\n%s", got)
	}
}

// The s2s half of the same plane speaks to a PEER SERVICE, authorized by
// identity through auth_services. Narrowing its reads to rows it published
// itself is not a privacy boundary; it just breaks peer aggregation.
func TestRecordPrincipal_S2SSurfaceIsNotFiltered(t *testing.T) {
	got := renderRecordPrincipal(sampleMethod("AgentEvent"), false)
	if strings.Contains(got, "__rowPrincipal") {
		t.Fatalf("the s2s surface must not carry the per-user row filter\ngot:\n%s", got)
	}
}

// A message that is not a (spec.record) carries no rows, so the filter has
// nothing to say about it and must not appear.
func TestRecordPrincipal_NonRecordResponseIsUntouched(t *testing.T) {
	got := renderRecordPrincipal(sampleMethod("Blob"), true)
	if strings.Contains(got, "__rowPrincipal") {
		t.Fatalf("a non-record response must not be filtered\ngot:\n%s", got)
	}
}

// The page leg. Filtering BEFORE the page is assembled is what keeps
// next_page_token and total_count describing the rows actually shown.
func TestRecordPrincipal_PageFiltersBeforeAssembly(t *testing.T) {
	got := renderRecordPrincipal(pullMethod(), true)
	for _, want := range []string{
		"java.util.List<Agentsensor.AgentEvent> __ownedItems = new java.util.ArrayList<>(__page.items.size());",
		"for (Agentsensor.AgentEvent __prow : __page.items) { if (__rowPrincipal.equals(__prow.getSpiffeId())) { __ownedItems.add(__prow); } }",
		"__builder.addAllItems(__ownedItems);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("page wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	filter := strings.Index(got, "__ownedItems.add(__prow)")
	assemble := strings.Index(got, "__builder.addAllItems(")
	if filter < 0 || assemble < 0 || filter > assemble {
		t.Fatalf("rows must be filtered before the page is assembled\ngot:\n%s", got)
	}
}

// The watch stream. An event carrying a row the caller does not own is never
// written; snapshot_done carries no row and still passes, so a filtered stream
// still ends its snapshot phase.
func TestRecordPrincipal_WatchStreamDropsUnownedEvents(t *testing.T) {
	got := renderRecordPrincipal(watchMethod(), true)
	for _, want := range []string{
		"if (__event.hasSnapshotItem() && !__rowPrincipal.equals(__event.getSnapshotItem().getSpiffeId())) { return; }",
		"if (__event.hasDelta() && __event.getDelta().hasItem() && !__rowPrincipal.equals(__event.getDelta().getItem().getSpiffeId())) { return; }",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("watch wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hasSnapshotDone()") {
		t.Fatalf("snapshot_done carries no row and must not be guarded\ngot:\n%s", got)
	}
	send := strings.Index(got, "__send = (__event) -> {")
	if send < 0 {
		t.Fatalf("no send path rendered\ngot:\n%s", got)
	}
	body := got[send:]
	drop := strings.Index(body, "__event.hasSnapshotItem()")
	sync := strings.Index(body, "synchronized (__readyLock) {")
	if drop < 0 || sync < 0 || drop > sync {
		t.Fatalf("the refusal must be the first thing the send path does, before it ever waits on readiness\ngot:\n%s", got)
	}
}

// Fail closed at GENERATION time. A c2s read over rows the wrapper cannot narrow
// is not emitted unfiltered — that is the state this filter exists to end, and
// it is invisible in a green build.
func TestRecordPrincipal_UnresolvableRecordRefusesGeneration(t *testing.T) {
	san := recordPrincipalSanitizer()
	why := javaRecordPrincipalUnenforceable(sampleMethod("Orphan"), san, "query", true)
	if why == "" {
		t.Fatal("a c2s sample over a record with no resolvable owner must refuse generation")
	}
	if !strings.Contains(why, "spec.store_principal") {
		t.Fatalf("the refusal must name the annotation that fixes it, got %q", why)
	}
	if got := javaRecordPrincipalUnenforceable(sampleMethod("Orphan"), san, "query", false); got != "" {
		t.Fatalf("the s2s surface owes no row filter, got refusal %q", got)
	}
	if got := javaRecordPrincipalUnenforceable(sampleMethod("Blob"), san, "query", true); got != "" {
		t.Fatalf("a non-record response owes no row filter, got refusal %q", got)
	}
	if got := javaRecordPrincipalUnenforceable(sampleMethod("AgentEvent"), san, "query", true); got != "" {
		t.Fatalf("a record declaring an owner is enforceable, got refusal %q", got)
	}
}

// The SIZE-ONLY half. Get<T>Count returns a number computed over the handle's
// predicate and no rows, so no egress filter can narrow it; the predicate is
// pinned at Create<T>Query instead, and every later call on the handle inherits
// it. Stamp-or-refuse, never a silent overwrite.
func recordPrincipalPredicateSanitizer() *javaEgressSanitizer {
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "query", Messages: []model.Message{
		{Name: "AgentEventFilter", Fields: []model.Field{
			{Name: "event_id", Type: "string", IsOptional: true, Number: 1},
			{Name: "spiffe_id", Type: "string", IsOptional: true, Number: 2,
				Annotations: map[string]model.AnnotationValue{
					"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true},
				}},
		}},
		// A record whose principal is not EQ-queryable carries no such criterion.
		{Name: "OrphanFilter", Fields: []model.Field{
			{Name: "id", Type: "string", IsOptional: true, Number: 1},
		}},
		{Name: "OpenAgentEventQueryRequest", Fields: []model.Field{
			{Name: "filter", Type: "AgentEventFilter", IsMessage: true, Number: 1},
			{Name: "standing", Type: "bool", Number: 2},
		}},
		{Name: "OpenOrphanQueryRequest", Fields: []model.Field{
			{Name: "filter", Type: "OrphanFilter", IsMessage: true, Number: 1},
		}},
		{Name: "QueryHandle", Fields: []model.Field{{Name: "query_id", Type: "string", Number: 1}}},
	}}})
}

func renderPredicatePin(input string, isC2S bool) string {
	m := &parser.MethodInfo{
		Name: "CreateAgentEventQuery", InputType: input, OutputType: "QueryHandle",
		FullInputType: "query." + input, FullOutputType: "query.QueryHandle",
		AuthDisabled: true,
	}
	return generateJavaGRPCWrapperSurface(
		m, "RecordQuery", "query", "Query."+input, "Query.QueryHandle",
		"Helpers", "Helpers", false, true,
		map[string]service.AccountingInfo{}, "query", "vservices",
		map[string]string{}, proto.SkippedTraceTypes{},
		recordPrincipalPredicateSanitizer(), nil, nil, isC2S, false,
	)
}

func TestRecordPrincipal_PredicateIsPinnedAtTheEntryPoint(t *testing.T) {
	got := renderPredicatePin("OpenAgentEventQueryRequest", true)
	for _, want := range []string{
		"final String __queryPrincipal = __qTok == null ? null : __qTok.getSubject();",
		"if (!request.getFilter().hasSpiffeId() || request.getFilter().getSpiffeId().isEmpty()) {",
		"request = request.toBuilder().setFilter(request.getFilter().toBuilder().setSpiffeId(__queryPrincipal)).build();",
		"} else if (!__queryPrincipal.equals(request.getFilter().getSpiffeId())) {",
		"ProtoException.INVALID_ARGUMENT",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("create-query wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	validate := strings.Index(got, "Helpers.validateOpenAgentEventQueryRequest(request);")
	pin := strings.Index(got, "__queryPrincipal")
	call := strings.Index(got, "handler.createAgentEventQuery(")
	if !(validate >= 0 && validate < pin && pin < call) {
		t.Fatalf("the pin must run after request validation and before the handler\ngot:\n%s", got)
	}
}

func TestRecordPrincipal_PredicatePinIsC2SOnlyAndOptional(t *testing.T) {
	if got := renderPredicatePin("OpenAgentEventQueryRequest", false); strings.Contains(got, "__queryPrincipal") {
		t.Fatalf("the s2s surface must not pin the predicate to a user principal\ngot:\n%s", got)
	}
	if got := renderPredicatePin("OpenOrphanQueryRequest", true); strings.Contains(got, "__queryPrincipal") {
		t.Fatalf("a predicate with no principal criterion has nothing to pin\ngot:\n%s", got)
	}
}
