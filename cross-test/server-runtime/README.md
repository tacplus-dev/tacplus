# server-runtime

Cross-language **runtime** gate for java and rust, in both directions:

- **server half** (LED-2 phase 1): real generated **Java** and **Rust**
  servers, each driven over the wire by the generated **Go** C2S client
- **client half** (LED-2 phase 3): the real generated **Java** C2S client
  driving the **Rust**-hosted server, and the real generated **Rust** C2S
  client driving the **Java**-hosted server

```bash
just -f cross-test/justfile server-runtime          # both halves
just -f cross-test/justfile server-runtime-java     # java-hosted server only
just -f cross-test/justfile server-runtime-rust     # rust-hosted server only
just -f cross-test/justfile server-runtime-clients  # both driving-client legs
just -f cross-test/justfile server-runtime-client-java
just -f cross-test/justfile server-runtime-client-rust
```

## Why this exists

`runtime-smoke` covers a generated **go** server driven by the generated
**python** and **typescript** clients. Before this gate, java and rust generated
code had never crossed a language boundary over the wire in **either**
direction — `consumer-contract` hosts them only as self-generated client+server
pairs inside a single process, and `w2s-validate` hosts the rust W2S dispatch
surface against a rust client.

Phase 1 closed the server half. Phase 3 closes the client half: the java and
rust generated C2S clients are now each driven against a server hosted in
another language.

## Which pairs

One cross-language pairing per driving client, not the full N x N matrix:

| Driving client | Hosted server | Why this pairing |
| --- | --- | --- |
| go (generated) | java (generated) | phase 1 |
| go (generated) | rust (generated) | phase 1 |
| java (generated) | rust (generated) | java <-> rust had no wire evidence in any direction; reuses the server this gate already hosts |
| rust (generated) | java (generated) | same, transposed |

## What is generated and what is fixture

The server fixtures (`fixture/java/Fixture.java`, `fixture/rust/main.rs`)
implement **only** the handler interfaces the generator emits for host services
(`ContractApi.ContractApiHandler` / `ContractApiHandler`) plus the auth seams
the generated wrapper exposes. Everything the assertions actually observe —
request validation, dispatch, response validation, PAGE token encode/decode,
egress sanitization, auth gating — is generated code:

- java: `model/src/main/java/.../s2sm/internal/grpc/servers/contract/contractclient/ContractApi.java`
- rust: `model/s2sm/internal/grpc/servers/contract/contractclient/contract_api.rs`

The drivers are the generated C2S ServiceManagers and do nothing by hand — no
hand-rolled stubs, no hand-built metadata:

- go: `client/go/main.go` — `c2sm.New(...).Grpc().Clients().Contract().ContractApiClient()`
- java: `client/java/Client.java` — `new ServiceManager(channel, ...).grpc().contract().contractApi()`
- rust: `client/rust/main.rs` — `ServiceManager::new(channel, ...).grpc().contractclient().contract_api_client()`

## Coverage (per leg — identical assertions for every driving client)

| Leg | Method | Proves |
| --- | --- | --- |
| unary | `GetPublicContract` (`auth_disabled`) | full request→handler→response round trip with exact field values |
| server stream | `ListEvents` (`auth_any_user`, SNAPSHOT) | streaming egress + the auth_any_user seam, including the bearer token the generated client attaches |
| PAGE | `ListEventsPaged` (`auth_any_user`) | the server encodes a resume cursor and the generated client's auto-pager decodes it and walks two pages |
| fail-closed | `GetContract` (`auth_entitled_users`) | the generated wrapper plus the real generated Authorizer reject before the handler (`UNAUTHENTICATED`, both hosts); the fixture handler returns `HANDLER-REACHED`, so a bypass is loud |

## Fail-closed leg: real generated auth on both sides

Both hosted fixtures run the **real generated `chassis/grpcserver` Authorizer**
on the `authorize` seam, constructed over an explicitly empty verifying-key set:

- java: `Authorizer.create()` (`Fixture.java`)
- rust: `Authorizer::with_seeded_jwks(Vec::new())`, bridged onto the generated
  wrapper's `Authorizer` trait by `FixtureAuthorizer` in `fixture/rust/main.rs`
  the same way the generated ServiceManager's `AuthorizerAdapter` does

So the fail-closed leg exercises generated JWT verification in both languages:
the opaque fixture bearer is not a JWT, fails at token parse, and both hosted
servers reject with **`UNAUTHENTICATED` (16)**. Every driver asserts that exact
code — no "either code" latitude.

The `auth_any_user` seam (`check_at_or_below` / `setAuthenticateFunc`) is the
one place both fixtures still supply their own check: the opaque bearer could
never verify against a real key set, so both short-circuit it identically.

Earlier revisions of this gate saw java answer `UNAUTHENTICATED` and rust
`PERMISSION_DENIED`. That was not a generator asymmetry — rust emits a concrete,
JWT-verifying `Authorizer` too — it was this fixture hand-rolling a stand-in
that refused `authorize` without ever inspecting the token.

## Not covered here

Deliberately deferred:

- the full N x N client/server matrix (python / typescript / swift / csharp as
  driving clients against java- and rust-hosted servers, and java/rust against
  the go-hosted server)
- W2S surfaces against java/rust hosts (`runtime-smoke` covers W2S against the
  hand-written go fixture endpoints only)
- `Watch`, client-streaming and bidi legs

## Notes

- Local/manual, like `w2s-validate`. It is **not** part of `just test` and not
  wired into CI or `nightly-matrix.sh`: it needs the go, maven and cargo
  toolchains concurrently, and `just test` must stay fast.
- Generated outputs go to a temporary work directory. Use `--keep` plus
  `--workdir` to preserve them.
- `--server LANG` and `--client LANG` each narrow the run to that half; with
  neither flag both halves run.
