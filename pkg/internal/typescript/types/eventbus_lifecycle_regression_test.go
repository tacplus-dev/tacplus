// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"io/fs"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// Pin tests for the TypeScript event-bus / trace-publisher lifecycle and the
// collection page-token codec:
//
//   - F-4a:  subscriptions must re-resolve the event-bus instance from
//     ServiceHealthState on every reconnect attempt instead of pinning the
//     subscriber to a single address resolved once at startup.
//   - F-18b: subscribers must process events serially (pause/resume) instead of
//     spawning an unawaited async handler per 'data' event.
//   - F-22:  abort listeners on the process-lifetime AbortSignal must be removed
//     on normal resolution, and the trace-publisher drain wait must race stream
//     failure so an erroring stream cannot strand the publisher.
//   - F-25:  page tokens must be validated against a strict unpadded base64url
//     alphabet so TS rejects the same non-canonical tokens Go/Rust reject.

func renderTsServiceManagerForTest(t *testing.T, data tsSmTemplateData) string {
	t.Helper()
	tmplStr, err := loadTemplate("service_manager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("service_manager").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service manager template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service manager template: %v", err)
	}
	return out.String()
}

func renderTsSubscriberForTest(t *testing.T) string {
	t.Helper()
	tmplStr, err := loadTemplate("subscriber.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("subscriber").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse subscriber template: %v", err)
	}
	data := tsSubscriberTemplateData{
		SourcePackage:      "aegis",
		SourceStateRoot:    "AegisState",
		SourceField:        "event_stream",
		FieldPascal:        "EventStream",
		TypeName:           "TopologyEvent",
		TypeSchemaName:     "TopologyEventSchema",
		EnvelopeName:       "AegisStateEventStreamSubscribeRecord",
		EnvelopeSchemaName: "AegisStateEventStreamSubscribeRecordSchema",
		TypeImportPath:     "../../types/aegis/index.js",
		GrpcService:        "eventbus.EventBusTypedService",
		GrpcMethod:         "SubscribeAegisAegisStateEventStream",
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute subscriber template: %v", err)
	}
	return out.String()
}

func tsSmDataWithSubscriptionsAndTraces() tsSmTemplateData {
	return tsSmTemplateData{
		ServiceLabel:           "aegis",
		HasServiceHealthState:  true,
		HealthStateProtoDir:    "services/identity",
		HealthStatePackage:     "identity",
		StateStoreServiceID:    "identity",
		HasSubscriptions:       true,
		HasTracePublisher:      true,
		TracePublisherTopicRef: "trace.TraceState.events",
		TracePublisherTarget:   "customeb",
		TracePublishGrpcPath:   "/customeb.EventBusTypedService/PublishTraceTraceStateEvents",
		Subscriptions: []tsSubscriptionEntry{{
			FieldPascal:       "EventStream",
			FieldCamel:        "eventStream",
			HandlerMethodName: "onEventStream",
			SourcePackage:     "aegis",
			SourceField:       "event_stream",
			TypeName:          "TopologyEvent",
			TypeAlias:         "EventStreamEvent",
			TypeImportPath:    "../../types/aegis/index.js",
			ModuleName:        "aegis_event_stream",
			SubscriberAlias:   "AegisEventStream",
			TargetService:     "eventbus",
		}},
	}
}

// MS-4: the trace publisher must resolve — and mint a token for — the DECLARED
// broker tag, not a hardcoded "eventbus". A non-default broker tag ("customeb"
// here) must appear at both the resolveService and getTokenFor call sites.
func TestTsServiceManager_TracePublisherDialsDeclaredBrokerTag(t *testing.T) {
	rendered := renderTsServiceManagerForTest(t, tsSmDataWithSubscriptionsAndTraces())
	for _, want := range []string{
		"await this.resolveService('customeb', signal);",
		"await this.identity.getTokenFor('customeb');",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("trace publisher must dial the declared broker tag; missing %q", want)
		}
	}
	// The trace-publisher token mint must not fall back to a hardcoded "eventbus".
	// (A subscription block legitimately resolves its own TargetService, so the
	// resolveService negative can't be asserted globally — the getTokenFor site
	// is trace-publisher-exclusive.)
	if strings.Contains(rendered, "getTokenFor('eventbus')") {
		t.Fatalf("trace publisher still hardcodes 'eventbus' for its token instead of the declared broker tag")
	}
}

// F-4a: the subscription block must hand the subscriber a connection provider
// that re-resolves the event-bus instance per attempt — never a single client
// resolved once at startup.
func TestTsServiceManager_SubscriptionReResolvesEventbusPerAttempt(t *testing.T) {
	got := renderTsServiceManagerForTest(t, tsSmDataWithSubscriptionsAndTraces())

	if !strings.Contains(got, "() => this.resolveService('eventbus', subAbort.signal)") {
		t.Fatalf("subscription must pass a per-attempt resolveService provider to the subscriber:\n%s", got)
	}
	if strings.Contains(got, "const subConn = await this.resolveService") {
		t.Fatalf("subscription must not pin a single eventbus address resolved at startup:\n%s", got)
	}
}

// F-4a: the subscriber must invoke the connection provider inside its reconnect
// loop and close provider-owned clients after each attempt.
func TestTsSubscriber_ConnectsInsideReconnectLoop(t *testing.T) {
	got := renderTsSubscriberForTest(t)

	for _, want := range []string{
		"export type ConnectionProvider = () => Promise<grpc.Client>;",
		"conn = await this.connect();",
		"await this.subscribeOnce(conn, handler, signal, opts);",
		"if (conn && this.ownsConnections) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscriber missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "this.conn.makeBidiStreamRequest") {
		t.Fatalf("subscribeOnce must use the per-attempt connection, not a pinned this.conn:\n%s", got)
	}
}

// F-18b: events must be processed serially and in order — the stream is paused
// while the handler runs and resumed afterwards. An unawaited `async` 'data'
// callback spawns unbounded concurrent handlers and loses ordering.
func TestTsSubscriber_SerializesEventProcessing(t *testing.T) {
	got := renderTsSubscriberForTest(t)

	if strings.Contains(got, "stream.on('data', async") {
		t.Fatalf("'data' handler must not be an unawaited async callback (unbounded concurrency):\n%s", got)
	}
	for _, want := range []string{
		"stream.pause();",
		"stream.resume();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscriber must pause/resume the stream around the handler, missing %q\ngot:\n%s", want, got)
		}
	}
	// Handler rejection keeps teardown/reconnect semantics.
	if !strings.Contains(got, "stream.cancel();") {
		t.Fatalf("handler rejection must still cancel the stream:\n%s", got)
	}
}

// PC-C-5 pin: the subscriber must decode the metadata-bearing Subscribe
// envelope (field 1 = EventBusRecordMeta, field 2 = payload) and surface the
// REAL offset/timestamp/key — never the bare payload with a synthesized empty
// RecordMeta. timestamp is epoch millis (java parity, NEW-27); meta-only
// records are skipped, not synthesized (NEW-28).
func TestTsSubscriber_DecodesEnvelopeAndSurfacesRecordMeta(t *testing.T) {
	got := renderTsSubscriberForTest(t)

	for _, want := range []string{
		"fromBinary(AegisStateEventStreamSubscribeRecordSchema, data)",
		"AegisStateEventStreamSubscribeRecordSchema } from",
		"offset: record.meta?.offset ?? 0n,",
		"ts.seconds * 1000n + BigInt(Math.floor(ts.nanos / 1_000_000))",
		"key: record.meta?.key ?? new Uint8Array(0),",
		"if (!record.payload) {",
		"await handler(record.payload, meta);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscriber must decode the Subscribe envelope and surface real RecordMeta, missing %q\ngot:\n%s", want, got)
		}
	}
	for _, forbid := range []string{
		"emptyRecordMeta",                       // the old zero-meta synthesis
		"fromBinary(TopologyEventSchema, data)", // the old bare-payload decode
		"fromBinary(, data)",                    // an unset EnvelopeSchemaName renders broken TS
	} {
		if strings.Contains(got, forbid) {
			t.Fatalf("subscriber must not contain %q (bare-payload decode / zero meta)\ngot:\n%s", forbid, got)
		}
	}
}

// F-22a: helpers that wait on the process-lifetime AbortSignal must detach
// their 'abort' listeners when they resolve normally; otherwise listeners
// accumulate without bound under steady traffic.
func TestTsServiceManager_AbortListenersRemovedOnNormalResolution(t *testing.T) {
	got := renderTsServiceManagerForTest(t, tsSmDataWithSubscriptionsAndTraces())

	// sleep(): the timer path must remove the abort listener before resolving.
	if !strings.Contains(got, "signal.removeEventListener('abort', onAbort);") {
		t.Fatalf("sleep()/drain wait must remove the abort listener on normal resolution:\n%s", got)
	}
	// take(): a single publisher-lifetime abort hook, not one listener per call.
	if !strings.Contains(got, "waiter = { resolve, reject };") {
		t.Fatalf("trace publisher take() must park waiters without per-call abort listeners:\n%s", got)
	}
	if strings.Contains(got, "this.abortController.signal.addEventListener('abort', onAbort, { once: true });") {
		t.Fatalf("take() must not register a fresh abort listener per call:\n%s", got)
	}
}

// F-22b: the backpressure drain wait must also wake on stream error/end/close —
// otherwise a stream that dies mid-drain strands the publisher forever and all
// subsequent traces are silently dropped.
func TestTsServiceManager_TraceDrainWaitRacesStreamFailure(t *testing.T) {
	got := renderTsServiceManagerForTest(t, tsSmDataWithSubscriptionsAndTraces())

	for _, want := range []string{
		"stream.on('close', () => reject(new Error('trace publish stream closed')));",
		"const onDrain = () => resolve();",
		"stream.removeListener('drain', onDrain);",
		"closed,",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("trace drain wait missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "new Promise<void>((resolve) => stream.once('drain', resolve)),") {
		t.Fatalf("drain wait must not await 'drain' without racing stream failure:\n%s", got)
	}
}

// F-25: decodePageToken must reject non-canonical tokens (padding, whitespace,
// invalid length) that Node's lenient base64url decoder would otherwise accept,
// matching the strict Go/Rust decoders.
func TestTsCollection_PageTokenDecodeIsStrict(t *testing.T) {
	raw, err := fs.ReadFile(templates.FS, "typescript/chassis/collection/index.ts.tmpl")
	if err != nil {
		t.Fatalf("read collection template: %v", err)
	}
	got := string(raw)

	for _, want := range []string{
		"const BASE64URL_STRICT = /^[A-Za-z0-9_-]+$/;",
		"if (!BASE64URL_STRICT.test(token) || token.length % 4 === 1) {",
		"if (decoded.length !== Math.floor((token.length * 3) / 4)) {",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("decodePageToken missing strict validation %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "raw = Buffer.from(token, 'base64url').toString('utf8');") {
		t.Fatalf("decodePageToken must not decode leniently without validation:\n%s", got)
	}
}
