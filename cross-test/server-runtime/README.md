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
| fail-closed | `GetContract` (`auth_entitled_users`) | the generated wrapper rejects before the handler; the fixture handler returns `HANDLER-REACHED`, so a bypass is loud |

## Known cross-language difference (not a fixture papering-over)

The fail-closed leg is rejected with **`UNAUTHENTICATED` (16)** by the
java-hosted server and **`PERMISSION_DENIED` (7)** by the rust-hosted server, so
every driver accepts either code. The cause is a structural asymmetry in the
generated auth seam, not in the drivers:

- java emits a concrete `chassis/grpcserver/Authorizer` with JWT verification
  built in (`Authorizer.create()`), which rejects the fixture's opaque bearer
  token at token verification → `UNAUTHENTICATED`
- rust emits an `Authorizer` **trait** only, with no default implementation, so
  the fixture must supply one; it refuses `authorize` outright →
  `PERMISSION_DENIED`

This predates phase 3 (it was already visible in the phase 1 go-driven legs) and
is recorded here rather than smoothed over.

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
