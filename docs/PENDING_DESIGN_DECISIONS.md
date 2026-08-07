# Pending design decisions

Three items that are **not bugs and not oversights**. Each is a modelling or
product choice that needs a maintainer to rule on, and each was deliberately left
alone because picking a direction unilaterally would be the wrong call.

Read this when you want to decide one. Each entry states what exists today, what
is actually undecided, the options with their trade-offs, and a recommendation
you are free to reject.

State verified against the current tree, not carried forward from the ledger —
two of the three turned out to be narrower than previously recorded.

---

## MS-5 — a service's OPA role is inferred from an import

**Where:** `pkg/internal/proto/derive_virtual.go:47-54`

A service is treated as *the OPA server* if its proto imports
`sys/opalib.proto`, and as an *OPA client* otherwise:

```go
// Skip if this service IS the OPA server (imports sys/opalib.proto).
isOpaServer := false
for _, imp := range serviceDesc.Imports {
    if imp == "sys/opalib.proto" { isOpaServer = true; break }
}
```

The core of MS-5 was fixed in `fa4c6d6e` (a hard error replaced a silent path).
What remains is the modelling question, and it is genuinely a question: an import
is a *dependency* statement, and this reads it as a *role* declaration. The two
usually coincide, but nothing enforces that. A service that imports the library
for any other reason silently stops generating an OpaEval client, and the failure
is an absent capability — the shape this codebase produces over and over.

**Options**

1. **Leave it.** Zero churn. The coupling stays implicit and undocumented, and
   the next person to import that proto for an unrelated reason gets a silent
   behaviour change.
2. **Declare the role explicitly** — a `(spec.service).opa_server` bool, with the
   import check kept as a cross-check that hard-errors on disagreement. Costs one
   annotation on one service, and makes a wrong import loud instead of silent.
3. **Derive from the service identity** already in the registry rather than from
   imports at all. Cleanest model, largest change.

**Recommendation:** option 2. It is a few lines, keeps the current behaviour as a
consistency check rather than a guess, and converts the failure mode from "the
client silently disappears" to "generation stops and tells you". Option 3 is
better modelling but not worth the churn for a single service.

---

## NEW-8 — the ANONYMIZE pseudonym key has no provisioning, and no rotation story

**This is the one worth deciding first — the feature does not currently work.**

**Where:** `pkg/proto/sys/grpcconfig.proto:169-175` (`anonymize_hmac_key`,
field 19), consumed by the generated pseudonymize primitives in
`pkg/internal/go/types/sanitize_gen.go:239,255,271,287` and the rust/java
equivalents.

Previously recorded as "a bare parameter with no provisioning seam". That is now
out of date in one direction and understated in another:

- A typed config seam **does** exist. `anonymize_hmac_key` is a per-deployment
  secret carried on grpcconfig, described as the key that makes a value
  pseudonymize to the same surrogate "at any depth, across services and
  restarts".
- **Nothing populates it.** The operator does not set it, and no bootstrap YAML
  sets it. The field is generated, plumbed, and never assigned — the same
  capability-without-a-call-site shape as the credential redactor and the health
  probe defaults.

The practical consequence: the generated primitives fail loud on an empty key
(they panic with "store must supply a per-deployment secret"), so any service
whose records carry ANONYMIZE-capable `(spec.redact)` fields would crash on its
first pseudonymizing egress. It is not a silent data leak — the fail-closed
design holds — but ANONYMIZE cannot be used at all until the key is provisioned.

No service in the platform currently declares an ANONYMIZE-capable
`(spec.redact)` field, which is why nothing is crashing today. The first one to
declare one is the moment this becomes an outage rather than a decision.

What actually needs deciding is not "add a seam" but two policy questions the
seam cannot answer by itself:

1. **Where does the key come from, and how does every service get the SAME one?**
   The cross-service surrogate-stability guarantee is only true if every service
   receives an identical key. That is a distribution problem: operator-generated
   and pushed through config, kernel-minted like other secrets, or supplied by
   the deployer as an opaque input.
2. **Can it rotate?** Rotating changes every surrogate. Previously emitted
   pseudonyms stop linking to newly emitted ones, silently and irreversibly —
   any analysis joining on a surrogate across the rotation boundary is wrong
   without erroring. A key that can never rotate is also a poor answer for a
   long-lived secret.

**Options**

1. **Operator-generated, stored once, never rotated.** Simplest, works, and
   permanently accepts a non-rotatable secret.
2. **Operator-generated with versioned keys** — surrogates carry a key
   generation, consumers can tell "computed under a different key" from "different
   value". Rotation becomes possible and analysis can be honest about the
   boundary. Costs a format change to the surrogate.
3. **Deployer-supplied**, treated like any other external secret, with rotation
   explicitly the deployer's problem and documented as breaking linkage.

**Recommendation:** decide 1 vs 2 now, before anything depends on surrogate
stability, because retrofitting a key generation into an emitted surrogate format
is a data migration. If ANONYMIZE is going to matter, option 2. If it is
speculative, option 1 plus a written statement that rotation is unsupported.

Either way this needs a maintainer: it is a product decision about data linkage,
not a code cleanup.

---

## PAR-9 — Python clients raise bare `RuntimeError` where other languages raise a typed code

**Where:** `pkg/templates/python/types/grpcclient.py.tmpl` (2 sites),
`pkg/templates/python/types/w2sclient.py.tmpl` (7 sites)

Python raises an untyped error for conditions the other languages report with a
structured status:

| condition | python | java | go |
|---|---|---|---|
| token provider missing | `RuntimeError("auth token provider is required")` | `ProtoException(UNAUTHENTICATED, …)` | `status.Error(codes.Unauthenticated, …)` |
| empty token | `RuntimeError("token provider returned an empty token")` | `ProtoException(UNAUTHENTICATED, …)` | — |
| W2S HTTP status | `RuntimeError(f"W2S HTTP status {…}")` | typed | typed |

A Python caller cannot distinguish "not authenticated, get a new token" from any
other failure without matching on message text. Retry and re-auth logic that is
straightforward in go/java is not expressible in Python against these clients.

The reason this was parked rather than fixed: Python clients became a live
surface only after `2a0f82be`, and changing the exception type is a **breaking
change** for any consumer already catching `RuntimeError`.

**Options**

1. **Leave it.** No breakage; Python stays a second-class client surface.
2. **Introduce a typed `ProtoError`** carrying a status code, subclassing
   `RuntimeError` so existing `except RuntimeError` handlers keep working while
   new code can catch precisely. Backward compatible, and the only option that is
   both.
3. **Raise `grpc.RpcError`** with proper codes for full parity with the gRPC
   idiom. Cleanest for consumers who already speak gRPC, hardest for the W2S
   paths, which are HTTP/SSE/WebSocket and not gRPC at all.

**Recommendation:** option 2. Subclassing `RuntimeError` makes it a
non-breaking change, so it can land without coordinating with consumers, and it
covers the W2S transports that option 3 does not fit.

---

## What "decide" means here

None of these is urgent in the sense of a live defect: NEW-8 fails loud, MS-5
affects one service, PAR-9 is an ergonomics gap. But NEW-8's option 1-vs-2 choice
gets harder the longer it waits — once surrogates exist in stored data, changing
their format is a migration rather than a decision.
