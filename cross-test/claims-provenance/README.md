# claims-provenance

Wire-level gate for claims **provenance** in the generated Java chassis (audit
finding F11).

Identity claims must come from a token the `Authorizer` **verified**. A base64
decode of the bearer header proves only that the caller can write base64, and on
an RPC that never verifies anything — `auth_disabled`, `RPC_FLAG_PUBLIC_NO_AUTH`,
`RPC_FLAG_SELF_AUTHENTICATES` — it is pure attacker input. The RED-1 egress
sanitizer reads exactly those claims (`service_entitlements.data_grants`) to
decide how much of a record to reveal, so an unsigned token used to unlock the
unsanitized view.

## What it does

1. Copies the `consumer-contract` proto fixture tree and applies
   `proto-overlay.proto`, which adds one method to `ContractApi`: a no-auth unary
   read returning `ContractEvent`, whose `payload` field is
   `(spec.redact) MAY_REDACT`. The canonical fixture has no such method and this
   gate does not change it. (Anonymous *collection* reads are refused by the
   generator at every streaming tier, so the overlay is a Tier-1 unary read.)
2. Generates Java with a tacplus built from this checkout, compiles it, and runs
   `fixture/ClaimsProvenanceTest.java`: the real generated `ContractApi` wrapper
   on a real gRPC socket, driven by the real generated `ContractApiGrpc` stub
   with real Ed25519-signed tokens. Nothing under test is mocked.

The server wiring mirrors production: the chassis `GrpcServer` installs
`Authorizer.interceptor()`, and `platform/services/kernel`'s `ClusterServer`
additionally installs `Auth.requestIdInterceptor()`. Both are installed here.

## The four checks

- a forged (unsigned) bearer asserting the data grant gets the **redacted** view
  on the no-auth RPC — this is the check that fails on the pre-fix generator;
- the same RPC with no token at all stays redacted (the fail-closed baseline the
  forged case must not beat);
- the forged bearer is rejected `UNAUTHENTICATED` on an RPC that does verify, so
  the first check cannot be passing because verification is broken everywhere;
- a real signed token carrying the grant still egresses the payload raw — the
  fix changes where claims come from, not what an entitled caller sees.

## Running

    just claims-provenance

Needs the go and java (maven) toolchains. Not part of `just test`; a stale
generator binary would silently invalidate the demonstration, so `run.sh` builds
one from this checkout unless `--tacplus` names one.
