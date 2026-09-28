// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// WS-1 Python: a Tier-2 SNAPSHOT collection is server-streaming on the wire but
// presents a collection-in/collection-out seam — the handler returns a
// materialized list, the client returns a list (draining the stream), and the
// wrapper chunks the list into the stream (its existing yield loop suffices).

func pyTier2Method() *parser.MethodInfo {
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

func emptyPyAccountingMap() map[string]service.AccountingInfo {
	return map[string]service.AccountingInfo{}
}

func TestPyTier2_Handler_ReturnsList(t *testing.T) {
	got := generatePyHandlerMethodSignature(pyTier2Method(), "catalog")
	if !strings.Contains(got, ") -> list[") {
		t.Fatalf("Tier-2 handler must return a list collection:\n%s", got)
	}
	if strings.Contains(got, "Yields") {
		t.Fatalf("Tier-2 handler must not be documented as a yielding stream:\n%s", got)
	}
}

func TestPyTier2_Client_AccumulatesIntoList(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGrpcClientMethod(svc, pyTier2Method(), "catalog")
	for _, want := range []string{
		") -> list[",
		"async for item in self._stub.ListItems(",
		"items.append(item)",
		"return items",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 client missing %q\ngot:\n%s", want, got)
		}
	}
}

// Tier-3 (cursor) routes to Python's existing lazy server-streaming seam: the
// handler yields a generator and the client returns the async stream iterator
// (GeneratorExit teardown). It must NOT get the Tier-2 list materialization. So
// Tier-3 needs no new Python code — only this lock.
func pyTier3Method() *parser.MethodInfo {
	m := pyTier2Method()
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}, IdleSecs: 300}
	return m
}

func TestPyTier3_RoutesToLazyStreamSeam(t *testing.T) {
	handler := generatePyHandlerMethodSignature(pyTier3Method(), "catalog")
	if !strings.Contains(handler, "Yields") {
		t.Fatalf("Tier-3 handler must be the lazy yielding stream seam:\n%s", handler)
	}
	if strings.Contains(handler, "-> list[") {
		t.Fatalf("Tier-3 must not materialize a list (that is Tier-2):\n%s", handler)
	}
	client := generatePyGrpcClientMethod(&parser.ServiceInfo{Name: "Catalog", IsS2S: true}, pyTier3Method(), "catalog")
	if !strings.Contains(client, "return self._validating_response_iterator(responses, validate_item)") {
		t.Fatalf("Tier-3 client must return a lazy validated async stream iterator:\n%s", client)
	}
	if strings.Contains(client, "async for item") {
		t.Fatalf("Tier-3 client must not accumulate (that is Tier-2):\n%s", client)
	}
}

// The wrapper is tier-agnostic (it iterates the handler result and yields each),
// so a Tier-2 collection still produces a valid server-stream handler.
func TestPyTier2_Wrapper_YieldsItems(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGRPCWrapperMethod(svc, pyTier2Method(), "catalog", "services", "catalog", emptyPyAccountingMap())
	for _, want := range []string{
		"responses = self.handler.list_items(request, context)",
		"for response in responses:",
		"yield response",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- WATCH (snapshot→delta envelope + replay ring) ----------------------------

func pyWatchMethod() *parser.MethodInfo {
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
		},
	}
}

func TestPyWatch_Handler_ReturnsWatchSource(t *testing.T) {
	got := generatePyHandlerMethodSignature(pyWatchMethod(), "catalog")
	want := "def watch(self, req: pb.WatchReq, context) -> WatchSource[pb.Item, pb.WatchChange]:"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH handler must return WatchSource.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestPyWatch_Wrapper_EnvelopeAndRing(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGRPCWrapperMethod(svc, pyWatchMethod(), "catalog", "services", "catalog", emptyPyAccountingMap())
	for _, want := range []string{
		"source = self.handler.watch(request, context)",
		"resume_revision = source.revision",
		"pb.WatchEvent(snapshot_item=item)",
		"pb.WatchEvent(snapshot_done=source.revision)",
		"self._watch_watch_hub.watch(source.scope, resume_revision, source.changes",
		"pb.WatchEvent(delta=change)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestPyWatch_Client_ReturnsAsyncStream(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGrpcClientMethod(svc, pyWatchMethod(), "catalog")
	for _, want := range []string{
		"Call Catalog.Watch (WATCH collection over server streaming).",
		"responses = self._stub.Watch(request, metadata=metadata)",
		"return self._validating_response_iterator(responses, validate_watch_event)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- PAGE (unary keyset window: explicit page + auto-pager) -------------------

func pyPageMethod() *parser.MethodInfo {
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

func TestPyPage_Handler_ReturnsPage(t *testing.T) {
	got := generatePyHandlerMethodSignature(pyPageMethod(), "catalog")
	want := "def list_events(self, req: pb.ListEventsRequest, page: PageParams, context) -> Page[pb.Event]:"
	if !strings.Contains(got, want) {
		t.Fatalf("PAGE handler must take PageParams and return Page.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestPyPage_Wrapper_DecodesBoundsEncodes(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGRPCWrapperMethod(svc, pyPageMethod(), "catalog", "services", "catalog", emptyPyAccountingMap())
	for _, want := range []string{
		`after = decode_page_token(getattr(request, "page_token", ""))`,
		`limit = effective_page_size(getattr(request, "page_size", 0), 50, 10000)`,
		"page = self.handler.list_events(request, PageParams(limit=limit, after=after), context)",
		"resp = pb.ListEventsPage()",
		"resp.items.extend(page.items)",
		"resp.next_page_token = encode_page_token(page.next_after)",
		"if page.total >= 0:",
		"resp.total_count = page.total",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestPyPage_Client_ExplicitPageAndAutoPager(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	got := generatePyGrpcClientMethod(svc, pyPageMethod(), "catalog")
	for _, want := range []string{
		"async def list_events_page(self, request: pb.ListEventsRequest) -> tuple[list[pb.Event], str, int]:",
		"return (list(response.items), response.next_page_token, int(getattr(response, 'total_count', 0)))",
		"async def list_events(self, request: pb.ListEventsRequest):",
		"items, next_page_token, _ = await self.list_events_page(cur)",
		"cur.page_token = next_page_token",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE client missing %q\ngot:\n%s", want, got)
		}
	}
}

// NEW-36 (python leg of PC-W-2): auth on client-streaming/bidi is STREAM-OPEN
// scoped — one metadata attachment when the stream opens, and NO per-item auth
// inside the request iterator. The removed pattern awaited
// self._metadata(auth_required=True) per outbound message: the minted token
// was never sent (gRPC metadata is fixed at stream open) and a provider hiccup
// destroyed a legitimate stream. Mirrors the TS/java pins.
func TestPyStreamingClients_AuthIsStreamOpenScoped(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", IsS2S: true}
	for _, tc := range []struct {
		name   string
		method *parser.MethodInfo
	}{
		{"client-streaming", &parser.MethodInfo{
			Name:            "SendItems",
			InputType:       "Item",
			OutputType:      "SendItemsResponse",
			FullInputType:   "catalog.Item",
			FullOutputType:  "catalog.SendItemsResponse",
			ClientStreaming: true,
		}},
		{"bidi", &parser.MethodInfo{
			Name:            "ChatItems",
			InputType:       "Item",
			OutputType:      "Item",
			FullInputType:   "catalog.Item",
			FullOutputType:  "catalog.Item",
			ClientStreaming: true,
			ServerStreaming: true,
		}},
	} {
		got := generatePyGrpcClientMethod(svc, tc.method, "catalog")
		// The single legitimate auth call is the stream-open
		// `self._metadata(auth_required=...)`; the request ITERATOR must not
		// carry auth (per-item minting).
		if strings.Contains(got, "_validating_request_iterator(request_iterator") &&
			strings.Contains(got[strings.Index(got, "_validating_request_iterator"):], "auth_required") {
			t.Fatalf("%s: request iterator must not carry per-item auth (stream-open scoped):\n%s", tc.name, got)
		}
		if c := strings.Count(got, "self._metadata("); c != 1 {
			t.Fatalf("%s: auth must attach exactly one metadata at stream open, found %d self._metadata( calls:\n%s", tc.name, c, got)
		}
	}

	// Template-level hardening (run-20 nit): the iterator HELPER itself must
	// carry no auth — an unconditional call inside the helper would evade the
	// per-method render checks above.
	tmpl, err := templates.FS.ReadFile("python/types/grpcclient.py.tmpl")
	if err != nil {
		t.Fatalf("read grpcclient template: %v", err)
	}
	helper := string(tmpl)
	if idx := strings.Index(helper, "_validating_request_iterator"); idx >= 0 {
		body := helper[idx:]
		if end := strings.Index(body, "async def _validating_response_iterator"); end > 0 {
			body = body[:end]
		}
		if strings.Contains(body, "self._metadata(") {
			t.Fatalf("_validating_request_iterator helper must not carry auth (stream-open scoped):\n%s", body)
		}
	}
}
