# accounting-identity — wire gate for F10

An accounting record has to name the principal the platform *verified*. Before
F10 the generated Java service wrapper emitted every `RpcCompleted` and
`RpcError` with an empty `identity` map:

```java
em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
```

so the only thing tying a record to a caller was `request_id`, which
`Auth.requestIdFromContext()` reads straight out of the `x-request-id` request
header. A caller picks that value. Pick your own and the audit trail files your
privileged action under it; reuse someone else's and your action reads as
theirs.

The Go chassis already recorded `sub`/`aud`/`iss`/`azp` from the verified token
in context. This gate holds Java to the same record.

## What it does

Generates the `cross-test/consumer-contract` fixture with two extra RPCs on
`ContractAuditApi` — the one fixture service in the generated accounting enum,
whose declared methods are all `RPC_FLAG_PUBLIC_NO_AUTH`. The overlay adds one
`AUTH_ANY_USER` method (the wrapper's `checkAuthenticate` path) and one
`AUTH_ENTITLED_USERS` method (its `checkAuth` path), so both authorization
paths are covered.

It then hosts the real generated wrapper on a real gRPC socket with a real
`AccountingManager.Emitter`, and drives it with the real generated client stub
and real Ed25519-signed tokens. Nothing under test is mocked. The server is
wired the way a deployment wires one: `Authorizer.headerCaptureInterceptor()`
plus `Auth.requestIdInterceptor()`, and the authenticate seam wired exactly as
the generated `ServiceManager` wires it.

## The checks

1. An entitled-user RPC records the verified `sub`, `aud` and `iss`.
2. An `auth_any_user` RPC records them too.
3. `RpcError` — also an audit record — records them too.
4. A second caller writing the *first* caller's `x-request-id` is still
   recorded under their own verified subject.
5. A forged bearer on a verifying RPC is rejected and produces no record at all.
6. On a `PUBLIC_NO_AUTH` RPC, where nothing verifies anything, the record
   carries an EMPTY identity rather than the `sub` an attacker wrote into an
   unsigned bearer.
7. The same RPC with no token at all — case 6 must reveal no more than this.

## Running

```sh
just accounting-identity
```

Options: `--workdir DIR`, `--tacplus BIN`, `--keep`.
