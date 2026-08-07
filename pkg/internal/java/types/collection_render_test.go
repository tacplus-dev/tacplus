// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// WS-1 Java: a Tier-2 SNAPSHOT collection is server-streaming on the wire but
// presents a collection-in/collection-out seam — handler returns List<Item>,
// client returns List<Item>, and the wrapper chunks the List into the stream
// with WS-4 flow control (ServerCallStreamObserver + isReady/setOnReadyHandler).

func javaTier2Method() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "ListItems",
		InputType:       "ListItemsRequest",
		OutputType:      "Item",
		FullInputType:   "catalog.ListItemsRequest",
		FullOutputType:  "catalog.Item",
		ServerStreaming: true,
		AuthDisabled:    true,
		Collection:      &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1000},
		Resolved:        &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 1000, OrderBy: []string{"id"}},
	}
}

func TestJavaTier2_Handler_ReturnsList(t *testing.T) {
	got := generateJavaHandlerMethod(javaTier2Method(), "Catalog.ListItemsRequest", "Catalog.Item")
	if !strings.Contains(got, "java.util.List<Catalog.Item>") {
		t.Fatalf("Tier-2 handler must return a List collection:\n%s", got)
	}
	if strings.Contains(got, "StreamObserver") {
		t.Fatalf("Tier-2 handler must not take a StreamObserver:\n%s", got)
	}
}

func TestJavaTier2_Wrapper_FlowControlledChunking(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaTier2Method(), "Catalog", "", "Catalog.ListItemsRequest", "Catalog.Item",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	for _, want := range []string{
		"java.util.List<Catalog.Item> __items =",
		"io.grpc.stub.ServerCallStreamObserver<Catalog.Item> __sco",
		"__sco.setOnReadyHandler(",
		"__sco.isReady()",
		"__out.onCompleted()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- Tier-3 (cursor / AutoCloseable iterator) ---------------------------------

func javaTier3Method() *parser.MethodInfo {
	m := javaTier2Method()
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}, IdleSecs: 300}
	return m
}

func TestJavaTier3_Handler_ReturnsIterator(t *testing.T) {
	got := generateJavaHandlerMethod(javaTier3Method(), "Catalog.ListItemsRequest", "Catalog.Item")
	if !strings.Contains(got, "java.util.Iterator<Catalog.Item>") {
		t.Fatalf("Tier-3 handler must return a lazy Iterator producer:\n%s", got)
	}
	if strings.Contains(got, "StreamObserver") || strings.Contains(got, "java.util.List<") {
		t.Fatalf("Tier-3 handler must be a lazy Iterator (not StreamObserver / List):\n%s", got)
	}
}

func TestJavaTier3_Wrapper_DrivesIteratorWithFlowControlAndClose(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaTier3Method(), "Catalog", "", "Catalog.ListItemsRequest", "Catalog.Item",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	for _, want := range []string{
		"final java.util.Iterator<Catalog.Item> __it =",
		"io.grpc.stub.ServerCallStreamObserver<Catalog.Item> __sco",
		"__sco.setOnCancelHandler(",
		"__sco.setOnReadyHandler(",
		"((AutoCloseable) __it).close()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-3 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaTier3_Client_ReturnsCloseableIterator(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateJavaGrpcClientMethod(svc, javaTier3Method(), "Catalog.ListItemsRequest", "Catalog.Item", "", "")
	for _, want := range []string{
		"public CloseableIterator<Catalog.Item>",
		"io.grpc.Context.CancellableContext __ctx = io.grpc.Context.current().withCancellation()",
		"__it = stub.",
		"__ctx.cancel(null)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-3 client missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaTier2_Client_DrainsIteratorIntoList(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateJavaGrpcClientMethod(svc, javaTier2Method(), "Catalog.ListItemsRequest", "Catalog.Item", "", "")
	for _, want := range []string{
		"public java.util.List<Catalog.Item>",
		"java.util.Iterator<Catalog.Item> it = stub.",
		"var response = it.next();",
		"items.add(response)",
		"return items;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- WATCH (snapshot→delta envelope + replay ring) ----------------------------

func javaWatchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "Watch",
		InputType:       "WatchReq",
		OutputType:      "WatchEvent",
		FullInputType:   "catalog.WatchReq",
		FullOutputType:  "catalog.WatchEvent",
		ServerStreaming: true,
		AuthDisabled:    true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"id"},
			WatchOneOf: "event", WatchItemType: "catalog.Item", WatchChangeType: "catalog.WatchChange",
			WatchHasFromRevision: true,
		},
	}
}

func TestJavaWatch_Handler_ReturnsWatchSource(t *testing.T) {
	got := generateJavaHandlerMethod(javaWatchMethod(), "Catalog.WatchReq", "Catalog.WatchEvent")
	want := "CollectionRuntime.WatchSource<Catalog.Item, Catalog.WatchChange> watch(Caller caller, Catalog.WatchReq request)"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH handler must return WatchSource.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestJavaWatch_Wrapper_EnvelopeRingAndFlowControl(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaWatchMethod(), "Catalog", "", "Catalog.WatchReq", "Catalog.WatchEvent",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	for _, want := range []string{
		"CollectionRuntime.WatchSource<Catalog.Item, Catalog.WatchChange> __source = handler.watch(caller, request)",
		"watchWatchHub.watch(",
		"request.getFromRevision()",
		"setSnapshotItem(__source.snapshot.next())",
		"setSnapshotDone(__source.revision)",
		"setDelta(__changes.next())",
		"__sco.setOnReadyHandler(",
		"Thread __watchThread = new Thread(",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// A handle-based watch request (e.g. RecordMap's QueryHandle) has no
// from_revision field, so the wrapper must start from 0 rather than call the
// nonexistent request.getFromRevision().
func TestJavaWatch_Wrapper_NoFromRevisionField(t *testing.T) {
	m := javaWatchMethod()
	m.Resolved.WatchHasFromRevision = false
	got := generateJavaGRPCWrapper(
		m, "Catalog", "", "Catalog.WatchReq", "Catalog.WatchEvent",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	if strings.Contains(got, "request.getFromRevision()") {
		t.Fatalf("handle-based watch must not call request.getFromRevision()\ngot:\n%s", got)
	}
	if !strings.Contains(got, "final long __fromRevision = 0L;") {
		t.Fatalf("handle-based watch must start from revision 0\ngot:\n%s", got)
	}
}

func TestJavaWatch_Client_ReturnsCloseableIterator(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateJavaGrpcClientMethod(svc, javaWatchMethod(), "Catalog.WatchReq", "Catalog.WatchEvent", "", "")
	for _, want := range []string{
		"public CloseableIterator<Catalog.WatchEvent>",
		"WATCH collection RPC",
		"io.grpc.Context.CancellableContext __ctx = io.grpc.Context.current().withCancellation()",
		"__ctx.cancel(null)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- PAGE (unary keyset window: explicit page + auto-pager) -------------------

func javaPageMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "ListEvents",
		InputType:      "ListEventsRequest",
		OutputType:     "ListEventsPage",
		FullInputType:  "catalog.ListEventsRequest",
		FullOutputType: "catalog.ListEventsPage",
		AuthDisabled:   true,
		Collection:     &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50},
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"},
			DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "catalog.Event", PageItemsField: "items", PageHasTotal: true,
		},
	}
}

func TestJavaPage_Handler_ReturnsPage(t *testing.T) {
	got := generateJavaHandlerMethod(javaPageMethod(), "Catalog.ListEventsRequest", "Catalog.ListEventsPage")
	want := "CollectionRuntime.Page<Catalog.Event> listEvents(Caller caller, Catalog.ListEventsRequest request, CollectionRuntime.PageParams page)"
	if !strings.Contains(got, want) {
		t.Fatalf("PAGE handler must take PageParams and return Page.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestJavaPage_Wrapper_DecodesBoundsEncodes(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaPageMethod(), "Catalog", "", "Catalog.ListEventsRequest", "Catalog.ListEventsPage",
		"", "", false, true,
		map[string]service.AccountingInfo{}, "catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	for _, want := range []string{
		"CollectionRuntime.decodePageToken(request.getPageToken())",
		"CollectionRuntime.effectivePageSize(request.getPageSize(), 50L, 10000L)",
		"handler.listEvents(caller, request, new CollectionRuntime.PageParams(__limit, __after))",
		"Catalog.ListEventsPage.Builder __builder = Catalog.ListEventsPage.newBuilder()",
		"__builder.addAllItems(__page.items)",
		"__builder.setNextPageToken(CollectionRuntime.encodePageToken(__page.nextAfter))",
		"if (__page.total >= 0) {",
		"__builder.setTotalCount(__page.total)",
		"Catalog.ListEventsPage response;",
		"response = __builder.build()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Catalog.ListEventsPage response = __builder.build()") {
		t.Fatalf("PAGE wrapper must assign the response variable instead of redeclaring it\ngot:\n%s", got)
	}
}

func TestJavaPage_WrapperAccounting_DoesNotRedeclareResponse(t *testing.T) {
	got := generateJavaGRPCWrapper(
		javaPageMethod(), "Catalog", "catalog", "Catalog.ListEventsRequest", "Catalog.ListEventsPage",
		"", "", false, true,
		map[string]service.AccountingInfo{
			"catalog": {RPCs: map[string]bool{"ListEvents": true}},
		},
		"catalog", "services", map[string]string{}, proto.SkippedTraceTypes{}, nil, nil)
	if !strings.Contains(got, "Catalog.ListEventsPage response;\n            try {") {
		t.Fatalf("PAGE accounting wrapper must declare response before the handler try block\ngot:\n%s", got)
	}
	if strings.Contains(got, "Catalog.ListEventsPage response = __builder.build()") {
		t.Fatalf("PAGE accounting wrapper must not redeclare response inside the handler try block\ngot:\n%s", got)
	}
}

func TestJavaPage_Client_ExplicitPageAndAutoPager(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateJavaGrpcClientMethod(svc, javaPageMethod(), "Catalog.ListEventsRequest", "Catalog.ListEventsPage", "", "")
	for _, want := range []string{
		"public CollectionRuntime.PageResult<Catalog.Event> listEventsPage(Catalog.ListEventsRequest request)",
		"new CollectionRuntime.PageResult<>(response.getItemsList(), response.getNextPageToken(), response.getTotalCount())",
		"public java.util.Iterator<Catalog.Event> listEvents(Catalog.ListEventsRequest request)",
		"request.toBuilder().setPageToken(__token).build()",
		"CollectionRuntime.PageResult<Catalog.Event> __p = listEventsPage(__req)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE client missing %q\ngot:\n%s", want, got)
		}
	}
}
