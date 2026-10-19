# Stability review: generated runtime under small perturbations

Review for #17. Scope: the generated runtime in Go, Rust and Java (chassis templates and generated wrapper templates under `pkg/templates/`). No generator code was changed.

## Method and evidence rules

- Six reviewers worked independently, one per angle in #17 (timeouts and deadlines; retries and backoff; health, readiness and liveness; connection management and keepalive; load and backpressure; startup and restart ordering). They did not see each other's notes.
- A second pass challenged every finding. Findings that did not survive are listed under "Dropped" below. Disagreements that remain are listed under "Disagreements".
- **CONFIRMED** means the amplification mechanism is fully determined by a precise code path that was read in this review (file:line). No fault-injection test was executed in this review. Each confirmed defect has a filed issue (#31–#38) with the reproduction test to write. The reason a test was not run: the fixtures (a multi-process peer restart, a blocked event bus, a fake state store) were not built in this review.
- **SUSPECTED** means the mechanism depends on runtime timing, on external library behaviour, on deployment manifests, or on the size of the fleet. None of these is claimed as proven here.

No fault-injection test was executed in this review. Fleet-scale claims are SUSPECTED.

## Confirmed (worst first)

Each entry is a code-path defect. Its fleet-scale effect is SUSPECTED unless stated.

1. **Accounting emit can park an RPC completion thread for as long as the event bus is down (Java).** [#34](https://github.com/tacplus-dev/tacplus/issues/34)
   - Trigger: the event bus stops draining the accounting queue (restart, slow publisher, one peer blip).
   - Mechanism: the queue is bounded at 1024 (`pkg/templates/java/types/ServiceManager.java.tmpl:1051`). A full queue makes `EventBusSink.emit` loop on `offer(250ms)` until shutdown (`pkg/templates/java/chassis/am/AccountingManager.java.tmpl:446-447`), and the emit runs on the handler's completion path (`pkg/templates/java/types/servicewrapper.java.tmpl:166-170`). Handler threads park, the executor drains, and every service emits accounting, so one bus outage can stall many services.
   - Languages: Java confirmed on the RPC path. Go (`pkg/templates/go/chassis/am/am.go.tmpl:493-505`) and Rust (`pkg/templates/rust/chassis/am/src/am.rs.tmpl:457`) block at the sink too; their caller paths are not confirmed.

2. **Consumer pool closes an endpoint on the first snapshot that omits it.** [#37](https://github.com/tacplus-dev/tacplus/issues/37)
   - Trigger: one missed or partial health snapshot.
   - Mechanism: Rust (`pkg/templates/rust/chassis/servicemanager/src/connection.rs.tmpl:322-336`), Java (`pkg/templates/java/chassis/serviceconnection/ServiceConnection.java.tmpl:334-344`) and Go (`pkg/templates/go/chassis/serviceconnection/serviceconnection.go.tmpl:279-300`) all drop an endpoint absent from one snapshot. The next snapshot forces a redial and a TLS handshake, and readiness drops at once when the last required endpoint goes. The #2 debounce covers readiness only, not pool membership.
   - Languages: all three.

3. **Unary calls reset the caller's deadline per hop (Rust) and retry with no jitter or budget (all three).** [#38](https://github.com/tacplus-dev/tacplus/issues/38)
   - Trigger: a slight latency increase on a snapshot read that pushes calls past 30 s.
   - Mechanism: the Rust client overwrites the inbound `grpc-timeout` with its fixed timeout (`pkg/templates/rust/types/grpcclient.rs.tmpl:66-70`, `:101-105`), so a callee keeps working after its caller gives up. Retries are unjittered with no budget (Go `grpcclient.go.tmpl:121-132`, Java `grpcclient.java.tmpl:112-117`, Rust `grpcclient.rs.tmpl:80-89`). Work per request can grow with chain depth only if downstream calls are not cancelled with the caller, which was not verified. Go derives its per-call timeout from the handler context (`grpcclient.go.tmpl:144-148`) and is safe here.
   - Languages: Rust confirmed; Java unverified (see Not reviewed); Go safe on the deadline, affected on jitter.

4. **Reconnect and resolve backoff has no jitter in Go, Java and Rust.** [#31](https://github.com/tacplus-dev/tacplus/issues/31)
   - Trigger: a shared event (peer restart, kernel roll) drops many clients at once.
   - Mechanism: the schedule doubles to a 30 s cap and is a pure function of the attempt number (`pkg/templates/go/chassis/serviceconnection/backoff.go.tmpl:19-31`, `pkg/templates/rust/chassis/servicemanager/src/backoff.rs.tmpl:17-25`, `pkg/templates/java/chassis/backoff/Backoff.java.tmpl:23-45`). Clients that lose the same peer attempt at the same instants: t+0.5, +1.5, +3.5, +7.5, +15.5, +31.5, +61.5, then every 30 s. Each resolve attempt is a token fetch, a `GetAllInstances` and a TLS dial, which loads the same dependencies that are recovering.
   - The premise of "flat" 0.5 s to 30 s in #17 is wrong: the schedule doubles. It is unjittered and synchronized.
   - Languages: all three. Jitter exists in Go elsewhere (`pkg/templates/go/types/servicemanager_lib.go.tmpl:80-85`).

5. **Java subscriber re-resolves in a tight loop when a stream closes cleanly.** [#32](https://github.com/tacplus-dev/tacplus/issues/32)
   - Trigger: a server ends a subscription with OK status.
   - Mechanism: `subscribeOnce` returns normally (`pkg/templates/java/types/Subscriber.java.tmpl:262-277`), the caller resets its backoff and falls through with no sleep (`:157-158`), and the loop calls `resolveService` again at RPC speed (`pkg/templates/java/types/ServiceManager.java.tmpl:1392-1394`, `:2199-2226`).
   - Languages: Java only. Go (`pkg/templates/go/types/subscriber.go.tmpl:173-183`) and Rust (`pkg/templates/rust/types/subscriber.rs.tmpl:189-193`) sleep on a clean close.

6. **HTTP GET retries 429 and 5xx without reading Retry-After and without a budget; HTTP and gRPC retry policies disagree.** [#36](https://github.com/tacplus-dev/tacplus/issues/36)
   - Trigger: a peer sheds load with 429 or 503.
   - Mechanism: each retryable GET is sent up to 4 times (`pkg/templates/go/chassis/httpclient/client.go.tmpl:34-42`, `:239-300`; `pkg/templates/java/chassis/httpclient/TypedClient.java.tmpl:116-180`; `pkg/templates/rust/chassis/httpclient/src/retry.rs.tmpl:14-37`). `Retry-After` is never read. gRPC never retries INTERNAL or UNKNOWN, but HTTP retries 5xx.
   - Languages: all three.

7. **Java W2S listener and subscribe iterator buffer messages in unbounded queues.** [#33](https://github.com/tacplus-dev/tacplus/issues/33)
   - Trigger: a slow consumer while the server keeps sending.
   - Mechanism: `pkg/templates/java/types/w2sclient.java.tmpl:445` (unbounded), `:452` and `:462` (`request(1)` unconditional, so the socket is read at network speed); `pkg/templates/java/types/statewrapper.java.tmpl:662`, `:668`, `:678` (unbounded queue, no `request(n)` gating). Heap grows with the backlog.
   - Languages: Java. Go is safe because its handler runs synchronously under gRPC flow control.

8. **Java endpoint latency is a `getState` call, not a round trip, so a slow READY endpoint is never shed.** [#35](https://github.com/tacplus-dev/tacplus/issues/35)
   - Trigger: one endpoint becomes slow while staying READY.
   - Mechanism: the ping times `channel.getState(true)` (`pkg/templates/java/chassis/serviceconnection/ServiceConnection.java.tmpl:896-927`), so latency is microseconds for every endpoint. The 0.3 threshold (`:425`) is never met between endpoints, so routing never moves on latency. Go and Rust rank by real Ping RPC latency, so the three languages disagree on the same input.
   - Languages: Java.

## Suspected (not filed)

Each is a plausible amplification whose trigger, timing or size was not shown in the code.

- **Rust bidi reconnect is a fixed 5 s sleep with no backoff, no jitter, and a token re-mint per attempt** (`pkg/templates/rust/types/grpcclient_bidi.rs.tmpl:159-160`, `:59`). Cross-read downgraded this to hygiene: one attempt per 5 s per stream.
- **Ping failure marks an endpoint unhealthy on one sample, and the fleet herds onto the same lowest-latency survivor** (Rust `endpoint.rs.tmpl:160-166`; Go `servicemanager_lib.go.tmpl:146-150`, `:771-816`; Java `ServiceConnection.java.tmpl:908-927`). Go's switch happens on the 30 s rebalance tick, not immediately. Whether all clients rank endpoints the same way on the same tick was not shown.
- **Routing does not fail over on call failure.** `conn()` returns the active channel whatever its health (`pkg/templates/rust/chassis/servicemanager/src/connection.rs.tmpl:244-256`). Failover does happen on the ping-driven tick. The 90 s figure (three attempts of 30 s) applies to snapshot reads only.
- **Token mint lockout: five consecutive 10 s timeouts give a 300 s retry gap** (`pkg/templates/go/chassis/client_im/manager.go.tmpl:395-420`; Rust `manager.rs.tmpl:411-430`; Java `ClientIdentityManager.java.tmpl:426-455`). Attempts are per caller, not on a shared timer, so this is not synchronized; the impact depends on token TTL against the gap.
- **Java dynamic-pool dial waits 5 s for READY and drops the endpoint on failure** (`pkg/templates/java/chassis/serviceconnection/ServiceConnection.java.tmpl:835`, `:360-367`). Rust allows 15 s (`pkg/templates/rust/chassis/servicemanager/src/endpoint.rs.tmpl:126`, `:141`). Re-dial is gated on health reports.
- **Java state fetch retries UNAVAILABLE with no attempt bound** (`pkg/templates/java/chassis/backoff/Backoff.java.tmpl:47-73`). The fetch's own deadline covers validation retries only (`pkg/templates/java/types/statewrapper.java.tmpl:413-416`). Whether request threads block indefinitely depends on the state stub's per-call deadline, which was not confirmed.
- **Java subscriber's outer backoff is effectively dead; the real gap is the inner backoff** (`pkg/templates/java/types/Subscriber.java.tmpl:137`, `:184-185`), which resets only on a clean EOF and not after a long healthy run that ends in error. Rust resets after 60 s healthy (`pkg/templates/rust/types/service_manager.rs.tmpl:2085-2091`).
- **Watch callbacks run synchronously on the watch receive path** (`pkg/templates/go/types/state_wrapper.go.tmpl:622-638`; `pkg/templates/java/types/statewrapper.java.tmpl:1024-1032`; `pkg/templates/rust/types/statewrapper.rs.tmpl:606-612`). A slow callback stalls the stream. The downstream effect (cursor eviction and relist) was inferred, not traced.
- **Redo-log append fsyncs on the caller's thread under a lock; there is no asynchronous persist** (`pkg/templates/go/chassis/diskstore/redolog.go.tmpl:116-128`; `pkg/templates/rust/chassis/diskstore/src/redolog.rs.tmpl:101-107`; `RedoLog.java.tmpl:123-133`). This is a throughput design choice, not an amplifier by itself. The premise in #17 that it is asynchronous is wrong for all three.
- **Identity managers exit the process after 5 minutes of token failure** (Java `pkg/templates/java/chassis/service_im/IdentityManager.java.tmpl:65-66`, `:286-309`; Rust `pkg/templates/rust/chassis/service_im/src/manager.rs.tmpl:30-31`, `:225-265`). The timers start at outage onset, so pods exit together under a sustained authority outage. This is designed exit-for-restart and matters only for long outages.
- **First health publish treats UNAUTHENTICATED, PERMISSION_DENIED, FAILED_PRECONDITION and INVALID_ARGUMENT as permanent, so boot exits with no retry** (`pkg/templates/go/chassis/healthreporter/healthreporter.go.tmpl:356-363` and `pkg/templates/go/types/service_manager.go.tmpl:1501-1503`; Rust `pkg/templates/rust/chassis/healthreporter/src/lib.rs.tmpl:365-377`; Java `pkg/templates/java/chassis/healthreporter/HealthReporter.java.tmpl:331-337`). Unauthenticated is the stale-token symptom during an authority restart, so this can crash-loop. Whether a kernel returns these codes during a roll is not in this repo.
- **Rust and Java gate startup on the first TLS certificate; Go serves on the bootstrap certificate** (Rust `pkg/templates/rust/types/service_manager.rs.tmpl:886-900`; Java `pkg/templates/java/types/ServiceManager.java.tmpl:385-392`; Go `pkg/templates/go/types/service_manager.go.tmpl:1340-1363`). The Go comment says this is deliberate, so this is an inconsistency, not a proven amplifier.
- **Rust clients do not back off on connect failure and do not adjust keepalive on GOAWAY `too_many_pings`** (`pkg/templates/rust/types/service_manager.rs.tmpl:1079-1080`; `pkg/templates/rust/chassis/servicemanager/src/endpoint.rs.tmpl:123-124`). Go clients self-heal because grpc-go doubles the client ping interval. The tonic reconnect behaviour was read from cache, not verified against the pinned version.
- **Go server keepalive `MinTime` defaults to 30 s, equal to the client ping, so idle subscribers can be given GOAWAY `too_many_pings`.** This is evidence for #28 (see its comment), not a new issue. Borderline, since the repo pins grpc v1.81.1 and the strike logic was read from another version.
- **Go readiness does not count ping failures; Java counts them after about 90 s; Rust after 3 failures or 30 s** (`pkg/templates/go/chassis/serviceconnection/serviceconnection.go.tmpl:110-116`; Java `ServiceConnection.java.tmpl:81`, `:83`, `:947`; Rust `endpoint.rs.tmpl:21-26`). The cadences differ across languages. This is a divergence, and it makes Go absorb a silent peer rather than amplify.
- **Java's stub latency and Go's `ping_interval_ms` are unused.** `pkg/proto/sys/grpcconfig.proto:71-73` declares an interval that no template reads; Go's loop hardcodes 30 s (`pkg/templates/go/types/servicemanager_lib.go.tmpl:57`, `:431`).
- **Dynamic-pool endpoint lists and per-caller state grow without bound** (not reviewed in depth; see Not reviewed).

## #2 / #30 (fleet-plane flaps): producer-side status

The panel did not find the single root cause of the 1–2 minute cadence. What it found is recorded as evidence on #30 (see that issue's comment):

- The arithmetic alone does not produce a 1–2 minute cadence: the reporter publishes every 10 s, and a steady peer keeps its HEALTHY stamp fresh, so a single non-HEALTHY snapshot cannot reach the 30 s staleness threshold.
- Credential-rejection counting is a candidate producer-side cause, and it is wrong in both Go and Rust. Go counts every rejection (`pkg/templates/go/chassis/service_im/service_im.go.tmpl:1701`). Rust's comment says it counts generations, but `note_credential_rejection` bumps `GENERATION` on every call (`pkg/templates/rust/chassis/service_im/src/auth_recovery.rs.tmpl:118`), so it counts every rejection too. Java coalescing was not verified. This can keep a service UNHEALTHY on an idle fleet, but it is not yet shown to be the cause of the cadence.
- The consumer side is debounced in all three languages (#2). The fleet view can still show a non-HEALTHY status for a live peer, which is the open question in #30.

## Disagreements kept in the review

- **Rust credential counting (#30).** The first reviewer said Rust coalesces by generation. The cross-read showed `GENERATION` is bumped on every call, so Rust counts every rejection and its comment is wrong. Verified by reading `auth_recovery.rs.tmpl:100-125`.
- **Go unary retry multiplication.** One reviewer put the cost at 9 attempts at the leaf for a three-hop chain. The cross-read showed Go derives per-call deadlines from the caller's context, so the multiplication holds for Rust and (unverified) Java, not Go.
- **Java deadline propagation.** Whether grpc-java takes the minimum of a stub deadline and an inherited Context deadline was not verified. Java is therefore not stated as affected in #38.
- **Readiness chaining across hops (H-4).** One reviewer described B's published health going NOT_SERVING when C is down. The cross-read found B's fleet status comes only from its own `SetStatus` (`pkg/templates/go/types/service_manager.go.tmpl:1479`, `:1484`), so the chain as described does not exist.

## Dropped after the cross-read

- Go subscription backoff reset per successful resolve (R-2): the Go subscriber caps consecutive failures at 10 and returns to its caller, so the reset happens once per cycle, not every 500 ms.
- Readiness chaining across hops (H-4): see above.
- Liveness restarts from a dependency outage (H-5): the chassis comments describe the gRPC health service as readiness; the restart premise depends on deployment manifests, which are not in this repo.
- Go `Conn()` returning nil (C-5): internal callers check for nil (`pkg/templates/go/types/servicemanager_lib.go.tmpl:603-606`, `:894-897`). Only the public `Conn()` is a footgun, which is low severity.
- The Rust server keepalive gap (#29): no amplifying failure found in source reading (see its comment). Residual missing policy only.
- The Go credential-rejection counting as the cause of the 1–2 minute cadence: a candidate, not a finding of this review (see #30).

## #17 questions answered directly

- **"40 clients lose the same peer, reconnect backoff 0.5 s to 30 s flat."** The premise is wrong: it doubles. It is unjittered, so the clients move in lockstep through a fixed schedule (#31).
- **"Do all generated servers apply `PermitMinTimeSecs`?"** Go's server overrides it from config with a default of 30 s, so it does not apply the 10 s constant when config is set (#28 evidence). Java applies the constant (`pkg/templates/java/chassis/grpcserver/GrpcServer.java.tmpl:294`, `serverBuilder.permitKeepAliveTime({{.KeepalivePermitMinTimeSecs}}, ...)`). Rust applies no enforcement policy; no amplifying failure was found from source (#29).
- **"Is there an ungated liveness signal?"** The chassis exposes only the readiness-gated gRPC health status in all three languages. Whether deployment manifests point liveness at it is not in this repo (see Not reviewed).
- **"What happens when the redo-log disk is slow?"** Append blocks the caller under a lock in all three languages; there is no asynchronous persist (see Suspected).

## Not reviewed

- Fleet runs and fault-injection tests: none were executed. Every confirmed defect's reproduction is written as a test to add (see the filed issues).
- Deployment manifests: whether any liveness probe targets the gRPC health service.
- Java `grpc-java` deadline semantics (min of stub and Context deadlines) and the Java credential-rejection coalescing.
- The Rust server's actual resolved `h2` version (no lockfile in the repo); the Go `grpc` version was read from `go.mod` (v1.81.1), and strike logic was read from a different cached version.
- Python, TypeScript, C# and Swift chassis: out of scope for #17's three languages.
- Diskstore beyond the append path, and the snapshot and roll behaviour (#15, #25).
- The Go endpoint-list growth, the Go and Rust healthreporter in depth, and the grpcserver shutdown fan-out.
- The Rust and Java bidi streams beyond the fixed-sleep and clean-close findings above.
