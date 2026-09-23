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

// WS-1 Rust: a Tier-2 SNAPSHOT collection is server-streaming on the wire but
// presents a collection-in/collection-out seam — handler returns Vec<Item>,
// client returns Vec<Item>, and the wrapper chunks the Vec into the stream.

func rustTier2Method() *parser.MethodInfo {
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

func TestRustTier2_Handler_ReturnsVec(t *testing.T) {
	got := generateRustHandlerMethod(rustTier2Method(), "catalog", map[string]string{})
	if !strings.Contains(got, "-> Result<Vec<") {
		t.Fatalf("Tier-2 handler must return a Vec collection:\n%s", got)
	}
	if strings.Contains(got, "tonic::Response<std::pin::Pin") {
		t.Fatalf("Tier-2 handler must not expose the boxed-stream seam:\n%s", got)
	}
}

func TestRustTier2_Wrapper_ChunksVecIntoStream(t *testing.T) {
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustTier2Method(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"let items =",
		"tokio_stream::iter(items.into_iter()",
		"Box::pin(stream) as Self::ListItemsStream",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// Tier-3 (cursor) routes to Rust's existing lazy server-streaming seam: the
// handler returns a boxed Stream producer and the client returns
// tonic::Streaming (a Stream whose Drop cancels). It must NOT get the Tier-2 Vec
// materialization. So Tier-3 needs no new Rust code — only this lock.
func rustTier3Method() *parser.MethodInfo {
	m := rustTier2Method()
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}, IdleSecs: 300}
	return m
}

func TestRustTier3_RoutesToLazyStreamSeam(t *testing.T) {
	handler := generateRustHandlerMethod(rustTier3Method(), "catalog", map[string]string{})
	if !strings.Contains(handler, "tonic::Response<std::pin::Pin<Box<dyn tokio_stream::Stream") {
		t.Fatalf("Tier-3 handler must be the lazy boxed-stream producer:\n%s", handler)
	}
	if strings.Contains(handler, "-> Result<Vec<") {
		t.Fatalf("Tier-3 must not materialize a Vec (that is Tier-2):\n%s", handler)
	}
	res, err := generateRustGrpcClientMethod(&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, rustTier3Method(), "catalog", map[string]string{})
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if !strings.Contains(res.method, "tonic::Streaming<") {
		t.Fatalf("Tier-3 client must return tonic::Streaming (a Stream; Drop cancels):\n%s", res.method)
	}
}

func TestRustTier2_Client_DrainsStreamIntoVec(t *testing.T) {
	res, err := generateRustGrpcClientMethod(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, rustTier2Method(), "catalog", map[string]string{})
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	got := res.method
	for _, want := range []string{
		"-> Result<Vec<",
		"stream.message().await?",
		"items.push(item)",
		"Ok(items)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- WATCH (snapshot→delta envelope + replay ring) ----------------------------

func rustWatchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "Watch",
		InputType:       "WatchReq",
		OutputType:      "WatchEvent",
		FullInputType:   "catalog.WatchReq",
		FullOutputType:  "catalog.WatchEvent",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"id"},
			WatchOneOf: "event", WatchItemType: "catalog.Item", WatchChangeType: "catalog.WatchChange",
			// The resolver always populates this (default "revision"); a
			// fixture that omitted it would render `change.` and not compile.
			WatchRevisionField: "revision",
			// The standard {Method}Request declares from_revision — WATCH
			// validation requires it. The handle-based shape that does not is
			// the record plane's QueryHandle; see
			// TestRustWatch_Wrapper_HandleRequestStartsFromZero.
			WatchHasFromRevision: true,
		},
	}
}

func TestRustWatch_Handler_ReturnsWatchSource(t *testing.T) {
	got := generateRustHandlerMethod(rustWatchMethod(), "catalog", map[string]string{})
	want := "Result<collection::WatchSource<Item, WatchChange>, tonic::Status>"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH handler must return WatchSource.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestRustWatch_Wrapper_EnvelopeAndRing(t *testing.T) {
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustWatchMethod(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"collection::WatchSource<Item, WatchChange>",
		"self.watch_watch_hub.watch(",
		"req_msg.from_revision",
		"watch_event::Event::SnapshotItem(item)",
		"watch_event::Event::SnapshotDone(source_revision)",
		"watch_event::Event::Delta(change)",
		"tokio::sync::mpsc::channel(32)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustWatch_Client_ReturnsTonicStreamingEnvelope(t *testing.T) {
	res, err := generateRustGrpcClientMethod(&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, rustWatchMethod(), "catalog", map[string]string{})
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"WATCH collection over server streaming",
		"Result<tonic::Response<tonic::Streaming<WatchEvent>>, tonic::Status>",
		"client.watch(req).await",
	} {
		if !strings.Contains(res.method, want) {
			t.Fatalf("WATCH client missing %q\ngot:\n%s", want, res.method)
		}
	}
}

// The wrapper's oneof enum path must be derived from the oneof's actual name
// (prost renders `oneof frame` as `pub enum Frame`), not hardcoded to "Event".
// A hardcoded "Event" generates cleanly (validateWatchEnvelope only checks the
// three variant names, not the oneof's own name) and then fails to compile.
func TestRustWatch_Wrapper_OneofEnumNameFollowsTheOneofsOwnName(t *testing.T) {
	m := rustWatchMethod()
	m.Resolved.WatchOneOf = "frame"
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"watch_event::Frame::SnapshotItem(item)",
		"watch_event::Frame::SnapshotDone(source_revision)",
		"watch_event::Frame::Delta(change)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper with oneof `frame` missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "::Event::") {
		t.Fatalf("WATCH wrapper must not hardcode the oneof enum as Event when the oneof is named `frame`:\n%s", got)
	}
}

// --- PAGE (unary keyset window: explicit page + auto-pager) -------------------

func rustPageMethod() *parser.MethodInfo {
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

func TestRustPage_Handler_ReturnsPage(t *testing.T) {
	got := generateRustHandlerMethod(rustPageMethod(), "catalog", map[string]string{})
	want := "async fn list_events(&self, caller: Option<&servicemanager::auth::Caller>, req: ListEventsRequest, page: collection::PageParams) -> Result<collection::Page<Event>, tonic::Status>;"
	if !strings.Contains(got, want) {
		t.Fatalf("PAGE handler must take PageParams and return Page.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestRustPage_Wrapper_DecodesBoundsEncodes(t *testing.T) {
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		rustPageMethod(), "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	for _, want := range []string{
		"collection::decode_page_token(&req.page_token)?",
		"collection::effective_page_size(req.page_size as i64, 50, 10000)",
		"self.handler.list_events(caller.as_ref(), req, collection::PageParams { limit, after }).await?",
		"resp.items = page.items;",
		"resp.next_page_token = collection::encode_page_token(page.next_after.as_ref());",
		"if page.total >= 0 {",
		"resp.total_count = page.total;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustPage_Client_ExplicitPageAndAutoPager(t *testing.T) {
	res, err := generateRustGrpcClientMethod(&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, rustPageMethod(), "catalog", map[string]string{})
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"pub async fn list_events_page(",
		"Result<(Vec<Event>, String, i64), tonic::Status>",
		"Ok((resp.items, resp.next_page_token, total))",
		"impl futures::Stream<Item = Result<Event, tonic::Status>> + '_",
		"futures::stream::try_unfold(",
		"let (items, next, _total) = self.list_events_page(page_req).await?;",
	} {
		if !strings.Contains(res.method, want) {
			t.Fatalf("PAGE client missing %q\ngot:\n%s", want, res.method)
		}
	}
}

func TestRustPage_ClientTemplateDoesNotShadowModelChannel(t *testing.T) {
	tmpl, err := loadTemplate("grpcclient.rs.tmpl")
	if err != nil {
		t.Fatalf("load grpcclient template: %v", err)
	}
	if strings.Contains(tmpl, "use tonic::transport::Channel;") {
		t.Fatalf("grpc client template must not import tonic::transport::Channel; PAGE items can be model messages named Channel")
	}

	method := rustPageMethod()
	method.Resolved.PageItemType = "catalog.Channel"
	res, err := generateRustGrpcClientMethod(&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, method, "catalog", map[string]string{})
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if !strings.Contains(res.method, "Result<(Vec<Channel>, String, i64), tonic::Status>") {
		t.Fatalf("PAGE client should keep same-package item type Channel without transport shadowing:\n%s", res.method)
	}
}

// A WATCH whose request is a HANDLE — the record query plane's QueryHandle —
// carries no from_revision, so the wrapper must start from 0 (snapshot + live)
// rather than read a field that does not exist. Java renders 0L and Go int64(0)
// from the same resolved flag; Rust read the field unconditionally, which did
// not compile the first time the plane's server was built for a Rust host.
func TestRustWatch_Wrapper_HandleRequestStartsFromZero(t *testing.T) {
	m := rustWatchMethod()
	m.Resolved.WatchHasFromRevision = false
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	if !strings.Contains(got, "let from_revision = 0i64;") {
		t.Fatalf("handle-based WATCH must resume from 0\ngot:\n%s", got)
	}
	if strings.Contains(got, "req_msg.from_revision") {
		t.Fatalf("handle-based WATCH must not read from_revision off a request that declares none\ngot:\n%s", got)
	}
}

// A delta that names its own sequence renders that field, not the default.
// See the Go twin for why this matters (a row-shaped delta such as
// scottypersonal.ChatEvent carries `seq`, not `revision`).
func TestRustWatch_Wrapper_HonorsDeclaredRevisionField(t *testing.T) {
	m := rustWatchMethod()
	m.Resolved.WatchRevisionField = "seq"
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	if !strings.Contains(got, "|change: &WatchChange| change.seq,") {
		t.Fatalf("WATCH ring must key off the declared revision field:\n%s", got)
	}
	if strings.Contains(got, "change.revision,") {
		t.Fatalf("the default field must not survive a declared revision_field:\n%s", got)
	}
}
