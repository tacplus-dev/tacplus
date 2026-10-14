// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
)

// WS-1 TypeScript: a Tier-2 SNAPSHOT collection is server-streaming on the wire
// but presents a collection-in/collection-out seam — handler returns Item[],
// client returns Item[], and the wrapper chunks the array into the stream with
// WS-4 flow control (call.write() boolean + 'drain' event).

func tsTier2Method() *parser.MethodInfo {
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

func emptyTsAccountingMap() map[string]service.AccountingInfo {
	return map[string]service.AccountingInfo{}
}

func TestTsTier2_Handler_ReturnsArray(t *testing.T) {
	got := generateTsHandlerMethodSignature(tsTier2Method(), "catalog")
	if !strings.Contains(got, "): Promise<") || !strings.Contains(got, "[]>;") {
		t.Fatalf("Tier-2 handler must return an array collection:\n%s", got)
	}
	if strings.Contains(got, "ServerWritableStream") {
		t.Fatalf("Tier-2 handler must not take a writable stream:\n%s", got)
	}
}

func TestTsTier2_Wrapper_FlowControlledChunking(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateTsGRPCWrapperMethod(svc, tsTier2Method(), "catalog", "services", "", emptyTsAccountingMap())
	for _, want := range []string{
		"const items = await this.handler.listItems(call.request)",
		"if (!validatedCall.write(item))",
		"call.once('drain', resolve)",
		"call.end();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- Tier-3 (cursor / async iterator) ----------------------------------------

func tsTier3Method() *parser.MethodInfo {
	m := tsTier2Method()
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, MaxItems: 1000, OrderBy: []string{"id"}, IdleSecs: 300}
	return m
}

func TestTsTier3_Handler_ReturnsAsyncIterable(t *testing.T) {
	got := generateTsHandlerMethodSignature(tsTier3Method(), "catalog")
	if !strings.Contains(got, "): AsyncIterable<") {
		t.Fatalf("Tier-3 handler must return an AsyncIterable producer:\n%s", got)
	}
	if strings.Contains(got, "[]>;") {
		t.Fatalf("Tier-3 handler must not materialize an array (that is Tier-2):\n%s", got)
	}
}

func TestTsTier3_Wrapper_DrivesProducerWithFlowControl(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateTsGRPCWrapperMethod(svc, tsTier3Method(), "catalog", "services", "", emptyTsAccountingMap())
	for _, want := range []string{
		"for await (const item of this.handler.listItems(call.request))",
		"if (!validatedCall.write(item))",
		"call.once('drain', resolve)",
		"call.end();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-3 wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestTsTier3_Client_ReturnsAsyncIterable(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	res, err := generateTsGrpcClientMethod(svc, tsTier3Method(), "catalog")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"Promise<AsyncIterable<",
		"makeServerStreamRequest<",
		"as unknown as AsyncIterable<",
	} {
		if !strings.Contains(res.method, want) {
			t.Fatalf("Tier-3 client missing %q\ngot:\n%s", want, res.method)
		}
	}
}

func TestTsTier2_Client_AccumulatesIntoArray(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	res, err := generateTsGrpcClientMethod(svc, tsTier2Method(), "catalog")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	got := res.method
	for _, want := range []string{
		"): Promise<",
		"makeServerStreamRequest<",
		"stream.on('data'",
		"items.push(item)",
		"resolve(items)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Tier-2 client missing %q\ngot:\n%s", want, got)
		}
	}
}

// --- WATCH (snapshot→delta envelope + replay ring) ----------------------------

func tsWatchMethod() *parser.MethodInfo {
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
		},
	}
}

func TestTsWatch_Handler_ReturnsWatchSource(t *testing.T) {
	got := generateTsHandlerMethodSignature(tsWatchMethod(), "catalog")
	want := "watch(req: types.WatchReq): Promise<WatchSource<types.Item, types.WatchChange>>;"
	if !strings.Contains(got, want) {
		t.Fatalf("WATCH handler must return WatchSource.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestTsWatch_Wrapper_EnvelopeRingAndFlowControl(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateTsGRPCWrapperMethod(svc, tsWatchMethod(), "catalog", "services", "", emptyTsAccountingMap())
	for _, want := range []string{
		"await this.handler.watch(call.request)",
		"create(types.WatchEventSchema, { event: { case: 'snapshotItem', value: item } })",
		"create(types.WatchEventSchema, { event: { case: 'snapshotDone', value: BigInt(source.revision) } })",
		"this.watchWatchHub.watch(source.scope",
		"create(types.WatchEventSchema, { event: { case: 'delta', value: change } })",
		"call.once('drain', resolve)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("WATCH wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestTsWatch_Client_ReturnsAsyncIterableEnvelope(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	res, err := generateTsGrpcClientMethod(svc, tsWatchMethod(), "catalog")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"Promise<AsyncIterable<types.WatchEvent>>",
		"makeServerStreamRequest<",
		"as unknown as AsyncIterable<types.WatchEvent>",
	} {
		if !strings.Contains(res.method, want) {
			t.Fatalf("WATCH client missing %q\ngot:\n%s", want, res.method)
		}
	}
}

// --- PAGE (unary keyset window: explicit page + auto-pager) -------------------

func tsPageMethod() *parser.MethodInfo {
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

func TestTsPage_Handler_ReturnsPage(t *testing.T) {
	got := generateTsHandlerMethodSignature(tsPageMethod(), "catalog")
	want := "listEvents(req: types.ListEventsRequest, page: PageParams): Promise<Page<types.Event>>;"
	if !strings.Contains(got, want) {
		t.Fatalf("PAGE handler must take PageParams and return Page.\nwant: %s\ngot:\n%s", want, got)
	}
}

func TestTsPage_Wrapper_DecodesBoundsEncodes(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	got := generateTsGRPCWrapperMethod(svc, tsPageMethod(), "catalog", "services", "", emptyTsAccountingMap())
	for _, want := range []string{
		"decodePageToken(String((call.request as any).pageToken",
		"effectivePageSize(Number((call.request as any).pageSize",
		"this.handler.listEvents(call.request, { limit, after })",
		"create(types.ListEventsPageSchema, {",
		"items: page.items,",
		"nextPageToken: encodePageToken(page.nextAfter),",
		"totalCount: page.total >= 0 ? BigInt(page.total) : 0n,",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestTsPage_Client_ExplicitPageAndAutoPager(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog", AuthDisabled: true}
	res, err := generateTsGrpcClientMethod(svc, tsPageMethod(), "catalog")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"async listEventsPage(request: types.ListEventsRequest): Promise<{ items: types.Event[]; nextPageToken: string; total: number }>",
		"items: ((response as any).items ?? []) as types.Event[]",
		"total: Number((response as any).totalCount ?? -1)",
		"async *listEvents(request: types.ListEventsRequest): AsyncIterable<types.Event>",
		"await this.listEventsPage(cur)",
		"cur.pageToken = nextPageToken;",
	} {
		if !strings.Contains(res.method, want) {
			t.Fatalf("PAGE client missing %q\ngot:\n%s", want, res.method)
		}
	}
}

// NEW-35 (TS leg of PC-W-2): auth on client-streaming/bidi is STREAM-OPEN
// scoped — exactly one metadata attachment when the stream opens, and NO
// per-send auth call inside the send loop. The removed pattern awaited
// this.metadata(true) per outbound message: the minted token was never sent
// (gRPC metadata is fixed at stream open) and a provider hiccup destroyed a
// legitimate stream. Mirrors the java bidi pin.
func TestTsStreamingClients_AuthIsStreamOpenScoped(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog"}
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
		res, err := generateTsGrpcClientMethod(svc, tc.method, "catalog")
		if err != nil {
			t.Fatalf("%s: client gen error: %v", tc.name, err)
		}
		// Client-streaming attaches exactly one metadata at stream open; the
		// managed bidi handle authenticates per RECONNECT ATTEMPT inside its
		// lifecycle template, so its method body has none. Neither may auth
		// inside the send loop.
		switch tc.name {
		case "client-streaming":
			if got := strings.Count(res.method, "this.metadata("); got != 1 {
				t.Fatalf("%s: auth must attach exactly one metadata at stream open, found %d this.metadata( calls:\n%s", tc.name, got, res.method)
			}
		case "bidi":
			// The managed bidi handle authenticates per RECONNECT attempt inside
			// grpcclient_bidi.ts.tmpl (via tokenProvider); its method body must
			// carry no auth at all (run-19 hardening: the send side is the
			// handle's sendFn, not a for-await loop, so the loop check alone was
			// vacuous for this shape).
			if strings.Contains(res.method, "this.metadata(") {
				t.Fatalf("%s: bidi method body must carry no auth (per-reconnect auth lives in the lifecycle template):\n%s", tc.name, res.method)
			}
		}
		sendLoop := strings.Index(res.method, "for await (const request of")
		if sendLoop >= 0 && strings.Contains(res.method[sendLoop:], "this.metadata(") {
			t.Fatalf("%s: send loop must not perform per-send auth (stream-open scoped):\n%s", tc.name, res.method)
		}
	}
}

// Template-level hardening (run-20 nit): the bidi lifecycle template's send
// path must carry no per-send auth — its ONLY auth is the per-reconnect-
// attempt token mint in the connect path.
func TestTsBidiTemplateSendPathCarriesNoAuth(t *testing.T) {
	tmplStr, err := loadTemplate("grpcclient_bidi.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate grpcclient_bidi.ts.tmpl: %v", err)
	}
	// NOTE: this only catches a `this.metadata()` call in the send path. The
	// stronger pin on the same contract — which also catches a direct
	// tokenProvider() preflight — is TestTypeScriptBidiSendDoesNotPreflightToken
	// in grpcclient_bidi_regression_test.go.
	if idx := strings.Index(tmplStr, "sendFn"); idx >= 0 && strings.Contains(tmplStr[idx:], "this.metadata(") {
		t.Fatalf("bidi sendFn path must not carry per-send auth (stream-open scoped)")
	}
}
