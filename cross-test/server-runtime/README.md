# server-runtime

Cross-language **server** runtime gate: real generated **Java** and **Rust**
servers, each driven over the wire by the generated **Go** C2S client.

```bash
just -f cross-test/justfile server-runtime
just -f cross-test/justfile server-runtime-java
just -f cross-test/justfile server-runtime-rust
```

## Why this exists

`runtime-smoke` covers the transpose: a generated **go** server driven by the
generated **python** and **typescript** clients. Before this gate, java and rust
generated servers had never been driven by another language's generated client
anywhere in the repo — `consumer-contract` hosts them only as self-generated
client+server pairs inside a single process, and `w2s-validate` hosts the rust
W2S dispatch surface against a rust client. Cross-language wire evidence for
java- and rust-hosted servers did not exist.

## What is generated and what is fixture

The fixtures (`fixture/java/Fixture.java`, `fixture/rust/main.rs`) implement
**only** the handler interfaces the generator emits for host services
(`ContractApi.ContractApiHandler` / `ContractApiHandler`) plus the auth seams
the generated wrapper exposes. Everything the assertions actually observe —
request validation, dispatch, response validation, PAGE token encode/decode,
egress sanitization, auth gating — is generated code:

- java: `model/src/main/java/.../s2sm/internal/grpc/servers/contract/contractclient/ContractApi.java`
- rust: `model/s2sm/internal/grpc/servers/contract/contractclient/contract_api.rs`

The driving client (`client/main.go`) is the generated go C2S ServiceManager
(`c2sm.New(...).Grpc().Clients().Contract().ContractApiClient()`); it dials a
real TCP port and does nothing by hand.

## Coverage (per hosted server)

| Leg | Method | Proves |
| --- | --- | --- |
| unary | `GetPublicContract` (`auth_disabled`) | full request→handler→response round trip with exact field values |
| server stream | `ListEvents` (`auth_any_user`, SNAPSHOT) | streaming egress + the auth_any_user seam |
| PAGE | `ListEventsPaged` (`auth_any_user`) | the server encodes a resume cursor and decodes it back — the client auto-pager walks two pages |
| fail-closed | `GetContract` (`auth_entitled_users`) | the generated wrapper rejects before the handler; the fixture handler returns `HANDLER-REACHED`, so a bypass is loud |

## Not covered here

Deliberately deferred to a later phase — this gate is server coverage only:

- the full N x N client/server matrix (python / typescript / swift / csharp as
  driving clients against java- and rust-hosted servers, and java/rust as
  driving clients)
- W2S surfaces against java/rust hosts (`runtime-smoke` covers W2S against the
  hand-written go fixture endpoints only)
- the shared cross-language constraint-class corpus
- `Watch`, client-streaming and bidi legs

## Notes

- Local/manual, like `w2s-validate`. It is **not** part of `just test` and not
  wired into CI or `nightly-matrix.sh`: it needs the go, maven and cargo
  toolchains concurrently, and `just test` must stay fast.
- Generated outputs go to a temporary work directory. Use `--keep` plus
  `--workdir` to preserve them.
