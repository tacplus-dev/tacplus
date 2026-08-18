// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func readPythonTemplate(t *testing.T, path string) string {
	t.Helper()
	src, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// F-3: the JWK view watch must be an asyncio coroutine driven with `async for`
// over the grpc.aio channel. The old implementation iterated the aio stream
// synchronously from a threading.Thread — every attempt raised, was swallowed,
// and the JWK cache never filled, so all auth-enabled inbound RPCs were
// rejected while the service reported healthy.
func TestPythonJwkWatchIsAsyncTask(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/chassis/grpcserver/authorizer.py.tmpl")

	for _, banned := range []string{
		"threading.Thread(", // watch must not run on a thread
		"_watch_jwk_view",   // old sync worker
		"time.sleep(1)",     // fixed 1 Hz retry
	} {
		if strings.Contains(tmpl, banned) {
			t.Errorf("authorizer template must not contain %q (JWK watch must be an asyncio task)", banned)
		}
	}
	// Every iteration of the Watch stream must be `async for` (sync iteration
	// over a grpc.aio stream raises immediately).
	if strings.Count(tmpl, "for resp in stub.Watch(") != strings.Count(tmpl, "async for resp in stub.Watch(") {
		t.Errorf("authorizer template must only iterate stub.Watch with `async for`")
	}
	for _, want := range []string{
		"async def watch(self)",
		"async for resp in stub.Watch(",
		"except asyncio.CancelledError:", // cancellation must propagate
		"await asyncio.sleep(backoff)",
		"backoff = min(backoff * 2, max_backoff)", // capped exponential backoff
		"JWK view watch stream ended",             // clean stream end also backs off
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("authorizer template missing %q", want)
		}
	}
	// The constructor must not start the watch (the SM owns the task); the
	// lookup API stays unchanged for callers.
	for _, want := range []string{
		"def get(self, kid: str)",
		"def has_keys(self)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("JwkCache lookup API changed; missing %q", want)
		}
	}
}

func TestPythonAuthorizerNormalizesGeneratedGrantTokens(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/chassis/grpcserver/authorizer.py.tmpl")
	for _, want := range []string{
		"from {{.PythonPackageBase}}.grants import normalize as normalize_grants",
		"for token in normalize_grants(raw):",
		"def method_grant_token(fallback_service: str, full_method: str) -> str:",
		`service_fqn = service_fqn.lstrip("/").rsplit("/", 1)[-1]`,
		"def grant_token_part(value: str) -> str:",
		`value = value.replace("-", "_").replace(".", "_").replace("/", "_")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Python Authorizer must authorize via generated grant normalization; missing %q", want)
		}
	}
}

// SEC-3: a refresh token that carries typ:refresh but no token_use claim must be
// rejected on BOTH the authorize and authenticate paths. The typ fallback must
// therefore appear twice — one path having it while the other fails open is the
// exact hole this pins shut.
func TestPythonAuthorizerRejectsTypRefreshOnBothPaths(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/chassis/grpcserver/authorizer.py.tmpl")
	if n := strings.Count(tmpl, `typ = tw.get_custom_field("typ")`); n < 2 {
		t.Fatalf("Python Authorizer must reject typ:refresh on both authorize and authenticate; found %d typ fallbacks, want >= 2", n)
	}
}

// MS-4: the trace publisher must resolve — and mint a token for — the DECLARED
// broker tag ({{.TracePublisherTarget}}), not a hardcoded "eventbus".
func TestPythonTracePublisherDialsDeclaredBrokerTag(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")
	for _, want := range []string{
		`self._resolve_service("{{.TracePublisherTarget}}")`,
		`self._identity.get_token_for("{{.TracePublisherTarget}}")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("trace publisher must dial the declared broker tag; missing %q", want)
		}
	}
	if strings.Contains(tmpl, `self._resolve_service("eventbus")`) {
		t.Fatalf("trace publisher still hardcodes \"eventbus\" instead of the declared broker tag")
	}
}

// F-3: the ServiceManager owns the JWK watch — scheduled via _create_task at
// startup (so it is cancelled with the rest of the SM tasks at shutdown).
func TestPythonServiceManagerStartsJwkWatchTask(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")

	if !strings.Contains(tmpl, "self._create_task(authorizer.jwk_cache.watch())") {
		t.Fatalf("ServiceManager must schedule authorizer.jwk_cache.watch() as an SM-owned task")
	}
	ctor := strings.Index(tmpl, "authorizer = Authorizer(")
	watch := strings.Index(tmpl, "self._create_task(authorizer.jwk_cache.watch())")
	if ctor < 0 || watch < ctor {
		t.Fatalf("the JWK watch task must be started after the Authorizer is constructed")
	}
}

// F-4a: the event-bus target must be re-resolved via ServiceHealthState on
// EVERY reconnect attempt (channel_provider), not resolved once at startup.
func TestPythonServiceManagerSubscriptionsReResolveTarget(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")

	if strings.Contains(tmpl, "sub_conn_{{.VarName}} = await self._resolve_service") {
		t.Fatalf("subscription target must not be resolved once at startup")
	}
	for _, want := range []string{
		"channel_provider=_resolve_{{.VarName}},",
		`return await self._resolve_service("{{.TargetService}}")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("subscription must re-resolve the target per attempt; missing %q", want)
		}
	}
}

// F-4a: the subscriber invokes the channel provider inside the reconnect loop
// (one fresh channel per attempt, closed before the next attempt).
func TestPythonSubscriberResolvesChannelPerAttempt(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/subscriber.py.tmpl")

	loop := strings.Index(tmpl, "while True:")
	if loop < 0 {
		t.Fatalf("subscriber missing reconnect loop")
	}
	body := tmpl[loop:]
	for _, want := range []string{
		"channel = await self._channel_provider()",
		"await channel.close()",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("subscriber reconnect loop missing %q", want)
		}
	}
}

func TestPythonTagsRuntimeTemplateMatchesKubernetesSemantics(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/chassis/tags/__init__.py.tmpl")
	for _, want := range []string{
		"def contains_all(have: tags_pb2.TagSet | None, want: tags_pb2.TagSet | None) -> bool:",
		"def matches(selector: tags_pb2.TagSelector | None, tags: tags_pb2.TagSet | None) -> bool:",
		"_OP_IN = 1",
		"_OP_NOT_IN = 2",
		"return not exists or value not in req.values",
		"_OP_EXISTS = 3",
		"_OP_DOES_NOT_EXIST = 4",
		"def validate_key(key: str) -> None:",
		"def validate_selector(selector: tags_pb2.TagSelector | None) -> None:",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Python tags runtime template missing %q", want)
		}
	}
}

// F-4b: UNAUTHENTICATED / PERMISSION_DENIED on the subscription stream are
// retryable with capped backoff (transient for S2S: token rotation, authorizer
// cache lag) instead of silently killing the subscription task forever.
func TestPythonSubscriberRetriesAuthErrors(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/subscriber.py.tmpl")

	handler := strings.Index(tmpl, "except grpc.aio.AioRpcError")
	if handler < 0 {
		t.Fatalf("subscriber missing AioRpcError handler")
	}
	// Up to the next except clause: no status codes may terminate the loop.
	block := tmpl[handler:]
	if next := strings.Index(block[1:], "except "); next > 0 {
		block = block[:next+1]
	}
	for _, banned := range []string{"PERMISSION_DENIED", "UNAUTHENTICATED", "raise"} {
		if strings.Contains(block, banned) {
			t.Errorf("AioRpcError handler must retry all stream errors, found %q:\n%s", banned, block)
		}
	}
	for _, want := range []string{
		"await asyncio.sleep(backoff)",
		"backoff = min(backoff * 2, max_backoff)",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("AioRpcError handler missing capped backoff %q:\n%s", want, block)
		}
	}
}

// F-4b: the ServiceManager must observe subscription task failures when they
// happen (done callback logging), not only at shutdown.
func TestPythonServiceManagerLogsSubscriptionTaskFailure(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")

	for _, want := range []string{
		"def _log_subscription_failure(",
		"subscription task failed",
		`add_done_callback(`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("ServiceManager must log subscription task failures eagerly; missing %q", want)
		}
	}
}

// F-4c: the Subscribe call must carry an audience-scoped bearer token (like
// Go/TS), minted fresh per reconnect attempt via the identity manager.
func TestPythonSubscriberAttachesBearerToken(t *testing.T) {
	subTmpl := readPythonTemplate(t, "python/types/subscriber.py.tmpl")
	smTmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")

	once := strings.Index(subTmpl, "async def _subscribe_once(")
	if once < 0 {
		t.Fatalf("subscriber missing _subscribe_once")
	}
	body := subTmpl[once:]
	for _, want := range []string{
		"token = await self._token_provider()",
		`metadata = (("authorization", f"Bearer {token}"),)`,
		"multi_callable(request_iter(), metadata=metadata)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("subscriber must attach a per-attempt bearer token; missing %q", want)
		}
	}
	for _, want := range []string{
		"token_provider=_token_{{.VarName}},",
		`return await self._identity.get_token_for("{{.TargetService}}")`,
	} {
		if !strings.Contains(smTmpl, want) {
			t.Errorf("ServiceManager must wire an audience-scoped token provider; missing %q", want)
		}
	}
}

// F-16: a handler failure on the FIRST auto-TLS cert delivery is fatal —
// startup raises instead of hanging with readiness never signaled. Renewal
// failures stay log-and-continue (cross-language policy).
func TestPythonFirstTlsCertHandlerFailureIsFatal(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/service_manager.py.tmpl")

	for _, want := range []string{
		"class _FirstTlsCertDeliveryError(RuntimeError):",
		"raise _FirstTlsCertDeliveryError(",
		"Handler new_tls_cert failed on renewal",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("auto-TLS first-delivery failure must be fatal; missing %q", want)
		}
	}
	// The retry loop must re-raise the fatal error BEFORE the generic
	// Exception handler that retries with backoff.
	loop := strings.Index(tmpl, "async def _tls_cert_stream_loop(")
	if loop < 0 {
		t.Fatalf("ServiceManager missing _tls_cert_stream_loop")
	}
	body := tmpl[loop:]
	fatal := strings.Index(body, "except _FirstTlsCertDeliveryError:")
	generic := strings.Index(body, "except Exception as e:")
	if fatal < 0 || generic < 0 || fatal > generic {
		t.Fatalf("_tls_cert_stream_loop must re-raise _FirstTlsCertDeliveryError before the generic retry handler")
	}
	// The old log-only path on first delivery must be gone.
	if strings.Contains(tmpl, `logger.error("Handler new_tls_cert failed: %s", e)`) {
		t.Fatalf("first-delivery handler failure must not be log-and-continue")
	}
}

// F-25: page-token decode is strict no-padding base64url — '=' padding and
// non-alphabet characters are rejected before decode, matching Go/Rust/TS.
func TestPythonPageTokenDecodeIsStrict(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/chassis/collection/__init__.py.tmpl")

	for _, want := range []string{
		"_PAGE_TOKEN_ALPHABET",
		"if not all(c in _PAGE_TOKEN_ALPHABET for c in token) or len(token) % 4 == 1:",
		"raise BadPageToken()",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("decode_page_token must reject non-alphabet/padded tokens before decode; missing %q", want)
		}
	}
}

// F-25 behavioral: the collection runtime is plain Python (no template
// directives) — execute decode_page_token under the system interpreter and
// verify padded/tampered tokens raise BadPageToken while canonical tokens
// round-trip. Skips when no python3 is on PATH.
func TestPythonPageTokenDecodeBehavior(t *testing.T) {
	src, err := templates.FS.ReadFile("python/chassis/collection/__init__.py.tmpl")
	if err != nil {
		t.Fatalf("read collection template: %v", err)
	}
	if strings.Contains(string(src), "{{") {
		t.Fatalf("collection template gained template directives; update this test to render it first")
	}

	var py string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python interpreter found; skipping behavioral check")
	}

	dir := t.TempDir()
	mod := filepath.Join(dir, "collection_runtime.py")
	if err := os.WriteFile(mod, src, 0o644); err != nil {
		t.Fatalf("write module: %v", err)
	}

	script := `
import importlib.util
import sys, types

# Stub the grpc dependency: only grpc.StatusCode attributes are touched at
# import time (class-level grpc_code assignments).
class _AnyAttr:
    def __getattr__(self, name):
        return name
grpc_stub = types.ModuleType("grpc")
grpc_stub.StatusCode = _AnyAttr()
sys.modules["grpc"] = grpc_stub

spec = importlib.util.spec_from_file_location("collection_runtime", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
sys.modules["collection_runtime"] = mod
spec.loader.exec_module(mod)

Cursor = mod.Cursor
BadPageToken = mod.BadPageToken
encode = mod.encode_page_token
decode = mod.decode_page_token

# Empty token: first page.
assert decode("") is None

# Canonical round-trip.
tok = encode(Cursor(keys=["alpha", "b/2"]))
assert "=" not in tok, tok
got = decode(tok)
assert got is not None and list(got.keys) == ["alpha", "b/2"], got

# Padded variant of a valid token must be rejected (Go/Rust parity).
padded = tok + "=" * (-len(tok) % 4 or 4)
try:
    decode(padded)
    raise SystemExit("padded token accepted")
except BadPageToken:
    pass

# Non-alphabet characters (standard-base64 '+', '/') must be rejected.
for bad in (tok[:-1] + "+", tok[:-1] + "/", tok + "!"):
    try:
        decode(bad)
        raise SystemExit("non-alphabet token accepted: %r" % bad)
    except BadPageToken:
        pass

# A base64url string of length % 4 == 1 is never canonical.
try:
    decode("AAAAA")
    raise SystemExit("len%4==1 token accepted")
except BadPageToken:
    pass

print("ok")
`
	out, err := exec.Command(py, "-c", script, mod).CombinedOutput()
	if err != nil {
		t.Fatalf("page-token behavioral check failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// PC-C-5 pin: the rendered subscriber must decode the metadata-bearing
// Subscribe envelope and surface the REAL offset/timestamp/key — never the
// bare payload with a synthesized zero RecordMeta. The envelope class must
// resolve at IMPORT time (fail-fast on a missing envelope, NEW-26), timestamp
// is epoch millis (java parity, NEW-27), and meta-only records are skipped
// (NEW-28). Renders with real data and ast-parses the result so an unset
// EnvelopeName (which renders broken python) fails here, not at runtime.
func TestPythonSubscriberDecodesEnvelopeAndSurfacesRecordMeta(t *testing.T) {
	tmplStr := readPythonTemplate(t, "python/types/subscriber.py.tmpl")
	tmpl, err := template.New("subscriber").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse subscriber template: %v", err)
	}
	data := pySubscriberTemplateData{
		SourcePackage:        "aegis",
		SourceStateRoot:      "AegisState",
		SourceField:          "event_stream",
		FieldPascal:          "EventStream",
		TypeName:             "TopologyEvent",
		TypeImportFrom:       "svc_tacplus.types.vservices.aegis",
		TypePbModule:         "aegis_pb2",
		ExceptionsImportFrom: pyExceptionsImportFrom("svc_tacplus."),
		EnvelopeName:         "AegisStateEventStreamSubscribeRecord",
		GrpcService:          "eventbus.EventBusTypedService",
		GrpcMethod:           "SubscribeAegisAegisStateEventStream",
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute subscriber template: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"_ENVELOPE = event_pb.AegisStateEventStreamSubscribeRecord",
		"response_deserializer=_ENVELOPE.FromString,",
		`if not record.HasField("payload"):`,
		"ts_millis = meta.timestamp.seconds * 1000 + meta.timestamp.nanos // 1_000_000",
		"RecordMeta(offset=meta.offset, timestamp=ts_millis, key=meta.key),",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscriber must decode the Subscribe envelope and surface real RecordMeta, missing %q\ngot:\n%s", want, got)
		}
	}
	for _, forbid := range []string{
		"RecordMeta(offset=0, timestamp=0",  // the old zero-meta synthesis
		"event_pb.TopologyEvent.FromString", // the old bare-payload decode
	} {
		if strings.Contains(got, forbid) {
			t.Fatalf("subscriber must not contain %q (bare-payload decode / zero meta)\ngot:\n%s", forbid, got)
		}
	}

	// Syntax-validate the render so a broken expansion (e.g. `event_pb.` from an
	// unset EnvelopeName) fails here rather than at consumer runtime.
	var py string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python on PATH; string assertions only")
	}
	dir := t.TempDir()
	mod := filepath.Join(dir, "subscriber_render.py")
	if err := os.WriteFile(mod, []byte(got), 0o644); err != nil {
		t.Fatalf("write render: %v", err)
	}
	script := `import ast, sys
ast.parse(open(sys.argv[1]).read())
print("ok")
`
	out2, err := exec.Command(py, "-c", script, mod).CombinedOutput()
	if err != nil {
		t.Fatalf("rendered subscriber is not valid python: %v\n%s", err, out2)
	}
}
