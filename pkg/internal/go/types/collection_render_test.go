// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// WS-1: a Tier-2 SNAPSHOT collection method is server-streaming on the wire but
// presents a collection-in/collection-out seam — handler returns []*Item, client
// returns []*Item, and the wrapper chunks the slice into stream sends. These
// render tests pin that contract for the Go emitter.

func tier2Method() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "ListItems",
		InputType:       "ListItemsRequest",
		OutputType:      "Item",
		FullInputType:   "catalog.ListItemsRequest",
		FullOutputType:  "catalog.Item",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1000},
		Resolved:        &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 1000, OrderBy: []string{"id"}},
	}
}

func tier2Service() *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{tier2Method()}}
}

func TestTier2_HandlerInterface_ReturnsCollection(t *testing.T) {
	got := generateGoHandlerInterface(tier2Service(), "catalog")
	want := "ListItems(caller *servicemanager.Caller, req *types.ListItemsRequest) ([]*types.Item, error)"
	if !strings.Contains(got, want) {
		t.Fatalf("handler interface must expose a collection seam.\nwant substring: %s\ngot:\n%s", want, got)
	}
	// It must NOT expose the raw stream seam for this method.
	if strings.Contains(got, "stream grpc.ServerStreamingServer") {
		t.Fatalf("Tier-2 handler must not expose the raw stream seam:\n%s", got)
	}
}

func TestTier2_ServerWrapper_ChunksSliceIntoStream(t *testing.T) {
	got := generateGRPCWrapper(tier2Service(), tier2Method(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)

	// gRPC wire signature stays server-streaming.
	for _, want := range []string{
		"stream grpc.ServerStreamingServer[types.Item]) error",
		"items, err := p.handler.ListItems(caller, req)",
		"sendStream.Send(item)",
		"for _, item := range items",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("server wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestTier2_Client_AccumulatesStreamIntoSlice(t *testing.T) {
	got, err := generateGrpcClientMethod(tier2Service(), tier2Method(), "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"opts ...grpc.CallOption) ([]*types.Item, error)",
		"stream, err := c.client.ListItems(ctx, req, opts...)",
		"item, err := stream.Recv()",
		"items = append(items, item)",
		"return items, nil",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("client method missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- Tier-3 (cursor / iterator) -----------------------------------------------

func tier3Method() *parser.MethodInfo {
	m := tier2Method()
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}, IdleSecs: 300}
	return m
}

func tier3Service() *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{tier3Method()}}
}

func TestTier3_HandlerInterface_ReturnsIterSeq(t *testing.T) {
	got := generateGoHandlerInterface(tier3Service(), "catalog")
	want := "ListItems(caller *servicemanager.Caller, req *types.ListItemsRequest) (iter.Seq2[*types.Item, error], error)"
	if !strings.Contains(got, want) {
		t.Fatalf("Tier-3 handler must expose a pull-producer (iter.Seq2) seam.\nwant: %s\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "stream grpc.ServerStreamingServer") {
		t.Fatalf("Tier-3 handler must not expose the raw stream seam:\n%s", got)
	}
}

func TestTier3_ServerWrapper_DrivesProducer(t *testing.T) {
	got := generateGRPCWrapper(tier3Service(), tier3Method(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"stream grpc.ServerStreamingServer[types.Item]) error",
		"seq, err := p.handler.ListItems(caller, req)",
		"seq(func(item *types.Item, err error) bool {",
		"if ctx.Err() != nil {",
		"sendStream.Send(item)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-3 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestTier3_Client_ReturnsIteratorWithTeardown(t *testing.T) {
	got, err := generateGrpcClientMethod(tier3Service(), tier3Method(), "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"opts ...grpc.CallOption) (iter.Seq2[*types.Item, error], error)",
		"streamCtx, cancel := context.WithCancel(ctx)",
		"defer cancel()",
		"if !yield(item, nil) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-3 client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- WATCH (snapshot→delta envelope + replay ring) ----------------------------

func watchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "Watch",
		InputType:       "WatchReq",
		OutputType:      "WatchEvent",
		FullInputType:   "catalog.WatchReq",
		FullOutputType:  "catalog.WatchEvent",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, IdleSecs: 300, OrderBy: []string{"id"},
			WatchOneOf: "event", WatchItemType: "catalog.Item", WatchChangeType: "catalog.WatchChange",
			// The resolver always populates this (default "revision"); a
			// fixture that omitted it would render c.Get() and not compile.
			WatchRevisionField: "revision",
			// The standard {Method}Request declares from_revision — WATCH
			// validation requires it (validateWatchRequest). The handle-based
			// shape that does not is the record plane's QueryHandle; see
			// TestWatch_ServerWrapper_HandleRequestStartsFromZero.
			WatchHasFromRevision: true,
		},
	}
}

func watchService() *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{watchMethod()}}
}

func TestWatch_HandlerInterface_ReturnsWatchSource(t *testing.T) {
	got := generateGoHandlerInterface(watchService(), "catalog")
	want := "Watch(caller *servicemanager.Caller, req *types.WatchReq) (*collection.WatchSource[types.Item, types.WatchChange], error)"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH handler must return a WatchSource.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestWatch_ServerWrapper_EnvelopeAndRing(t *testing.T) {
	got := generateGRPCWrapper(watchService(), watchMethod(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"collection.NewHub[types.WatchChange](1024, 300)",
		"req.GetFromRevision()",
		"resumeRev = src.Revision",
		"&types.WatchEvent{Event: &types.WatchEvent_SnapshotItem{SnapshotItem: item}}",
		"&types.WatchEvent{Event: &types.WatchEvent_SnapshotDone{SnapshotDone: src.Revision}}",
		"&types.WatchEvent{Event: &types.WatchEvent_Delta{Delta: change}}",
		"response validation failed",
		".Watch(src.Scope, resumeRev, src.Changes, func(c *types.WatchChange) int64 { return c.GetRevision() })",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// A WATCH whose request is a HANDLE — the record query plane's QueryHandle —
// carries no from_revision, so the wrapper must start from 0 (snapshot + live)
// rather than read a field that does not exist. Java renders 0L from the same
// resolved flag; Go rendered req.GetFromRevision() unconditionally, which did
// not compile the first time a Go service hosted the plane.
func TestWatch_ServerWrapper_HandleRequestStartsFromZero(t *testing.T) {
	method := watchMethod()
	method.Resolved.WatchHasFromRevision = false
	got := generateGRPCWrapper(watchService(), method, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	if !strings.Contains(got, "fromRev := int64(0)") {
		t.Fatalf("handle-based WATCH must resume from 0\ngot:\n%s", got)
	}
	if strings.Contains(got, "req.GetFromRevision()") {
		t.Fatalf("handle-based WATCH must not read from_revision off a request that declares none\ngot:\n%s", got)
	}
}

func TestWatch_Client_IteratorOverEnvelope(t *testing.T) {
	got, err := generateGrpcClientMethod(watchService(), watchMethod(), "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"opts ...grpc.CallOption) (iter.Seq2[*types.WatchEvent, error], error)",
		"stream.Recv()",
		"if !yield(item, nil) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- PAGE (unary keyset window: explicit page + auto-pager) -------------------

func pageMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "ListEvents",
		InputType:      "ListEventsRequest",
		OutputType:     "ListEventsPage",
		FullInputType:  "catalog.ListEventsRequest",
		FullOutputType: "catalog.ListEventsPage",
		Collection:     &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50},
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"},
			DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "catalog.Event", PageItemsField: "items", PageHasTotal: true,
		},
	}
}

func pageService() *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{pageMethod()}}
}

func TestPage_HandlerInterface_ReturnsPage(t *testing.T) {
	got := generateGoHandlerInterface(pageService(), "catalog")
	want := "ListEvents(ctx context.Context, caller *servicemanager.Caller, req *types.ListEventsRequest, page collection.PageParams) (collection.Page[*types.Event], error)"
	if !strings.Contains(got, want) {
		t.Fatalf("PAGE handler must take PageParams and return a Page.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestPage_ServerWrapper_DecodesBoundsEncodes(t *testing.T) {
	got := generateGRPCWrapper(pageService(), pageMethod(), "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"collection.DecodePageToken(req.GetPageToken())",
		"collection.EffectivePageSize(int(req.GetPageSize()), 50, 10000)",
		"collection.PageParams{Limit: limit, After: after}",
		"collection.EncodePageToken(page.NextAfter)",
		"page.Items,",
		"NextPageToken: nextPageToken,",
		"if page.Total >= 0 {",
		"resp.TotalCount = page.Total",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestPage_Client_ExplicitPageAndAutoPager(t *testing.T) {
	got, err := generateGrpcClientMethod(pageService(), pageMethod(), "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"ListEventsPage(ctx context.Context, req *types.ListEventsRequest, opts ...grpc.CallOption) ([]*types.Event, string, int64, error)",
		"return resp.GetItems(), resp.GetNextPageToken(), resp.GetTotalCount(), nil",
		"ListEvents(ctx context.Context, req *types.ListEventsRequest, opts ...grpc.CallOption) iter.Seq2[*types.Event, error]",
		"cur := proto.Clone(req).(*types.ListEventsRequest)",
		"items, next, _, err := c.ListEventsPage(ctx, cur, opts...)",
		"cur.PageToken = next",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE client missing %q\ngot:\n%s", want, got)
		}
	}
}

// PAGE total_count is optional: when the response declares no total_count field,
// the wrapper omits the assignment and the explicit client returns -1 (unknown).
func TestPage_NoTotalCount(t *testing.T) {
	m := pageMethod()
	m.Resolved.PageHasTotal = false
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	wrapper := generateGRPCWrapper(svc, m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	if strings.Contains(wrapper, "resp.TotalCount") {
		t.Fatalf("wrapper must not set total_count when the field is absent:\n%s", wrapper)
	}
	client, err := generateGrpcClientMethod(svc, m, "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if !strings.Contains(client, "resp.GetNextPageToken(), -1, nil") {
		t.Fatalf("explicit page client must return -1 total when absent:\n%s", client)
	}
}

// A server-streaming method WITHOUT a method_collection annotation keeps the raw
// stream seam — the Tier-2 path must not capture ordinary streams.
func TestNonCollectionServerStream_KeepsStreamSeam(t *testing.T) {
	plain := &parser.MethodInfo{
		Name: "Watch", InputType: "WatchReq", OutputType: "Event",
		FullInputType: "catalog.WatchReq", FullOutputType: "catalog.Event", ServerStreaming: true,
	}
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{plain}}
	got := generateGoHandlerInterface(svc, "catalog")
	if !strings.Contains(got, "stream grpc.ServerStreamingServer[types.Event]) error") {
		t.Fatalf("plain server-stream must keep the stream seam:\n%s", got)
	}
}

// PC-C-4: the idle-timeout backstop is a WATCH mechanism (the replay Ring/Hub),
// not a Tier-3 SNAPSHOT-cursor one — documented in docs/API.md "Idle-timeout
// backstop — scope". Even when a cursor's trust tier carries IdleSecs, the
// generated cursor drive does NOT wire a Hub or an idle timer: it is a
// synchronous pull torn down by exhaustion / client cancel / ctx. Pin that
// scope so a future change does not silently add (or claim) a cursor idle timer.
func TestTier3_NoIdleBackstopReliesOnContext(t *testing.T) {
	m := tier3Method() // Resolved.IdleSecs = 300
	if m.Resolved.IdleSecs == 0 {
		t.Fatal("fixture precondition: Tier-3 method should carry a non-zero IdleSecs")
	}
	got := generateGRPCWrapper(tier3Service(), m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)

	// Teardown is context-driven.
	if !strings.Contains(got, "if ctx.Err() != nil {") {
		t.Fatalf("Tier-3 cursor must tear down via context:\n%s", got)
	}
	// It must NOT wire the WATCH idle mechanism.
	for _, forbidden := range []string{"NewHub", "NewRing", "ErrIdleTimeout", "idleTimeout", "idle timeout"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("Tier-3 cursor must NOT wire the WATCH idle backstop, found %q\n%s", forbidden, got)
		}
	}
}

// A delta that names its own sequence renders that accessor, not the default.
// This is (spec.method_collection).revision_field reaching the ring: without
// it, a row-shaped delta such as scottypersonal.ChatEvent (which carries `seq`,
// not `revision`) generates a wrapper that does not compile.
func TestWatch_ServerWrapper_HonorsDeclaredRevisionField(t *testing.T) {
	m := watchMethod()
	m.Resolved.WatchRevisionField = "seq"
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	got := generateGRPCWrapper(svc, m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	want := "func(c *types.WatchChange) int64 { return c.GetSeq() }"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH ring must key off the declared revision field.\nwant: %s\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "c.GetRevision()") {
		t.Fatalf("the default accessor must not survive a declared revision_field:\n%s", got)
	}
}

// A UNARY method must never render the Tier-2 collection seam, whatever tier
// Resolved carries. The Go emitter used to ask a private isTier2SnapshotCollection
// that omitted the ServerStreaming guard the canonical
// parser.MethodInfo.IsTier2SnapshotCollection() carries and every other backend
// uses, so a unary SNAPSHOT that resolved to Tier-2 generated a server-streaming
// handler seam and wrapper for a unary RPC — code that does not compile. The
// validator refuses that shape upstream ("declare this method
// server-streaming"); this pins the emitter's own guard behind it.
func TestUnarySnapshot_NeverRendersTier2Seam(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "ListLinks", InputType: "ListLinksRequest", OutputType: "ListLinksResponse",
		FullInputType: "catalog.ListLinksRequest", FullOutputType: "catalog.ListLinksResponse",
		ServerStreaming: false,
		Collection:      &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 100},
		Resolved:        &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 100, OrderBy: []string{"id"}},
	}
	if m.IsTier2SnapshotCollection() {
		t.Fatal("fixture precondition: a unary method must not report as a Tier-2 collection")
	}
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}

	got := generateGoHandlerInterface(svc, "catalog")
	want := "ListLinks(ctx context.Context, caller *servicemanager.Caller, req *types.ListLinksRequest) (*types.ListLinksResponse, error)"
	if !strings.Contains(got, want) {
		t.Fatalf("unary SNAPSHOT must keep the unary handler seam.\nwant substring: %s\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "([]*types.ListLinksResponse, error)") {
		t.Fatalf("unary SNAPSHOT must not render the Tier-2 collection seam:\n%s", got)
	}

	wrapper := generateGRPCWrapper(svc, m, "catalog", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
	if strings.Contains(wrapper, "grpc.ServerStreamingServer") {
		t.Fatalf("unary SNAPSHOT wrapper must not take a server stream:\n%s", wrapper)
	}

	client, err := generateGrpcClientMethod(svc, m, "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if strings.Contains(client, "[]*types.ListLinksResponse") {
		t.Fatalf("unary SNAPSHOT client must not accumulate a stream into a slice:\n%s", client)
	}
}
