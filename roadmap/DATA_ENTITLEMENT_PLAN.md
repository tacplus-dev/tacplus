# Data Entitlement — Implementation Plan

Status: implementation plan (tacplus codegen only). This is the buildable
subset. It supersedes and extends the existing `(spec.redact)` egress-redaction
seam. The broader design discussion lives in `DATA_ENTITLEMENT.md`; **most of
that document is explicitly OUT OF SCOPE here** (see [§10](#10-explicitly-out-of-scope)).

> **Superseded semantics note (2026-07, RED-1 wiring):** this plan's
> "absent decision / nil entitlement = pass-through" model was REVERSED by
> maintainer decision when the wrapper-side egress wiring landed. The shipped
> model is FAIL-CLOSED: `EntitlementLevel` 0 is `UNSPECIFIED` (most-protective
> default — MAY_REDACT redacts, MAY_ANONYMIZE anonymizes), an explicit
> `RAW = 3` (set only from a matching `TokenEntitlements.data_grants` token)
> egresses a field raw, and nil/absent (sub-)entitlements are fully protective.
> See `sys/entitlement.proto` and `docs/RECORD_EVENT_MODEL.md` for the current
> contract; the pass-through references below are historical.

Grounding references (read before Phase 1):
- `pkg/proto/sys/spec.proto` — `redact` (field ext `60082`), `RedactOption`,
  `RedactMode` (`~1491`); `extend google.protobuf.FieldOptions` (`1045`);
  `extend google.protobuf.MessageOptions` (`363`).
- `pkg/internal/go/types/helpers.go` — `generateReleaseHelpers` /
  `generatePseudonymizeHelper` (`538`–`640`).
- `pkg/internal/rust/types/release_gen.go`, `pkg/internal/java/types/release.go`
  — the Rust/Java twins.
- `pkg/internal/typescript/types/validation.go` — `redact` is listed
  informational-only; TS emits no release helper (client-only target).
- `pkg/internal/common/parser/redact_validate.go` — `ValidateRedactAnnotations`,
  `ValidateRedactLanguageSupport`, `redactSupportedLanguages` /
  `redactClientOnlyLanguages`. This is the fail-loud model to mirror.
- `pkg/internal/proto/record_map.go`, `pkg/internal/proto/nested_redact_test.go`
  — `ValidateNoNestedRedact` (nested redaction is rejected TODAY; the new
  entitlement structure recurses — see [§3](#3-generated-entitlement-structure)).
- `pkg/proto/sys/tacplus.proto` — `FieldInfo` (`221`), `TypeInfo` (`273`) —
  registry proto shape.
- `pkg/internal/common/registry/registry.go`, `pkg/internal/proto/registry.go`
  — registry emit.
- `pkg/builder/builder.go` (`150`–`174`) — validator call sites.
- `docs/USER.md` "Entitlements Model" (`755`) — flat `grants` / `TokenEntitlements`.
- `docs/THEORY.md`, `docs/DEVELOPMENT.md`, `cmd/soundness/main.go` — conventions.

---

## 1. Summary and core model

**Data entitlement** is a new tacplus system, parallel to RPC entitlement, that
sanitizes **data (message instances)** on their way out of a service. It is
distinct from RPC-invocation authorization and it **subsumes the existing
`(spec.redact)` / `Release<T>` seam** — the DROP/PSEUDONYMIZE transforms become
the two "hard floor" annotations of the new lattice, and the per-message
`Release<T>` helper is generalized into a `sanitize` helper driven by a
generated entitlement structure.

**Assumption.** The caller is already RPC-authorized and entitled to the data.
This layer never denies or permits — it only redacts/anonymizes what flows out.
Permit/deny stays entirely in RPC auth. There is **no deny/permit level here.**

**Lattice, most-restrictive-wins.** Three levels:

```
pass-through  <  anonymize  <  redact
```

The effective level for any field is `max(annotation-floor, runtime-entitlement)`
under this ordering — the runtime entitlement can only ratchet *tighter*, never
loosen below the annotation floor.

**No principal branching.** One entitlement applies regardless of consumer (LLM
vs human). tacplus never learns who the caller is. Different entitlements per
principal are a leak risk, so the roadmap's requester/executor/recipient
principals are dropped entirely.

**Field-level overrides type(message)-level.** A message-level annotation sets a
default for all eligible fields; a field-level annotation on that field wins.

**Eligibility is annotation-gated.** A field (or message) is transformable
**only if annotated.** An unannotated field is never touched by any entitlement,
whatever the runtime entitlement says. This is the safety property: you cannot
accidentally anonymize/redact a field nobody marked.

**Transform semantics.**
- *Redact* → set the field to its zero/empty value (proto3 scalar → `""`/`0`/
  `false`/empty bytes; message → cleared; repeated → emptied). Because a redacted
  scalar is indistinguishable from a genuine zero, `sanitize` **also returns the
  set of redacted field ids** (registry field ids) so the consumer can tell
  redacted-from-empty. No companion bool is injected into the schema.
- *Anonymize* → keyed **HMAC-SHA256** producing a stable pseudonym per value
  (same key+value → same surrogate). The HMAC **key is caller-supplied** via a
  field on the generated entitlement structure. Valid **only on `string`/`bytes`**;
  generation **rejects** an anonymize annotation on any other type.

**How this subsumes `(spec.redact)`.** Today `REDACT_MODE_DROP` ≈ `is-redacted`
and `REDACT_MODE_PSEUDONYMIZE` ≈ `is-anonymized` — both are unconditional "always
applied when the caller isn't entitled" transforms with a per-field mode. The new
model keeps those as the two `is-*` (hard-floor) annotations and adds two `may-*`
(runtime-decided) annotations. `Release<T>(m, <T>ReleasePolicy, pseudonymKey)` is
replaced by `sanitize(m, entitlement) -> (m', redactedFieldIds)`, where the
entitlement carries per-field levels plus the HMAC key. See [§2](#2-proto-annotation-design)
for the exact annotation-surface decision.

---

## 2. Proto annotation design

### 2.1 Decision: extend `RedactOption`, do not add parallel options

`(spec.redact)` already occupies field ext `60082` and carries a `RedactMode`.
Rather than mint four new field extensions (churny, and it would leave two
overlapping annotation families), we **extend the existing `RedactMode` enum and
`RedactOption` message** so that one annotation expresses all four states across
two transform kinds. Rationale: one annotation surface, one validator, one
registry field; the existing DROP/PSEUDONYMIZE values keep their numbers so
already-authored protos and checked-in `spec_pb` stay valid.

`RedactMode` gains two `may-*` values (existing values unchanged):

```proto
// pkg/proto/sys/spec.proto, ~1512
enum RedactMode {
  REDACT_MODE_UNSPECIFIED    = 0; // invalid — presence of (spec.redact) implies a mode
  REDACT_MODE_DROP           = 1; // HARD FLOOR redact: always cleared          (is-redacted)
  REDACT_MODE_PSEUDONYMIZE   = 2; // HARD FLOOR anonymize: always HMAC'd         (is-anonymized)
  REDACT_MODE_MAY_REDACT     = 3; // ELIGIBLE redact: runtime entitlement decides (may-be-redacted)
  REDACT_MODE_MAY_ANONYMIZE  = 4; // ELIGIBLE anonymize: runtime entitlement decides (may-be-anonymized)
}
```

Mapping to the four decided annotation names (docs/USER.md vocabulary):

| Decided name        | Transform  | Kind        | `RedactMode`                |
|---------------------|------------|-------------|-----------------------------|
| `is-redacted`       | redact     | hard floor  | `REDACT_MODE_DROP`          |
| `may-be-redacted`   | redact     | eligible    | `REDACT_MODE_MAY_REDACT`    |
| `is-anonymized`     | anonymize  | hard floor  | `REDACT_MODE_PSEUDONYMIZE`  |
| `may-be-anonymized` | anonymize  | eligible    | `REDACT_MODE_MAY_ANONYMIZE` |

Mutual exclusivity ("redact" vs "anonymize" kind) is enforced by the enum: a
single field carries exactly one `RedactMode`, so it cannot be both `may-be-redacted`
and `is-redacted`, nor mix redact and anonymize. (You cannot annotate the same
field twice with the same extension.)

### 2.2 Message-level annotation

Add a **message-level** option so a whole message type can carry a default level,
mirroring how `state_eviction_policy` provides a message-level default overridden
per field. New `extend google.protobuf.MessageOptions` entry (next free number
after the `363` block — `60068` is unused; confirm at author time):

```proto
// extend google.protobuf.MessageOptions { ... }
// data_entitlement: message(type)-level default transform for eligible fields of
// this message. A field-level (spec.redact) overrides it. Only meaningful for
// fields that are themselves annotation-eligible (string/bytes for anonymize).
RedactOption data_entitlement = 60068;
```

`RedactOption` itself is unchanged (`mode` field 1); it is simply now usable at
message level too.

### 2.3 Reconciling with `ValidateNoNestedRedact`

Today redaction is confined to top-level fields of a `(spec.record)` message; a
`(spec.redact)` inside a *nested* message declaration is rejected by
`ValidateNoNestedRedact` because the descriptor→model conversion doesn't recurse.
The new entitlement structure **does** recurse into nested message fields
([§3](#3-generated-entitlement-structure)). This plan therefore replaces
`ValidateNoNestedRedact` with a descriptor conversion that recurses into nested
message types so their annotations are visible (Phase 2). Until that lands,
nested annotations remain rejected — the rule is not weakened silently.

Also relax the "record-only" constraint: data entitlement applies to any
message a service returns, not only `(spec.record)` messages. `sanitize` is
generated for any message with at least one eligible/floor annotation.

---

## 3. Generated entitlement structure

For each message `T` with any data-entitlement annotation (its own or reachable
through nested message fields), tacplus generates a parallel **entitlement
structure** `TEntitlement` that mirrors `T`'s fields:

- One `RedactMode`-typed level per annotated field (`pass`/`anonymize`/`redact`,
  represented as the enum's unspecified/anonymize/redact values).
- A message-level default level.
- Nested message fields recurse into the nested type's entitlement (e.g.
  `TEntitlement` holds an `InnerEntitlement` for an `Inner inner` field).
- One caller-supplied **HMAC key** field at the top level.

**Whole-message OR field-by-field, recursively.** A nested message is redactable
**either as a whole (the entire nested message cleared) OR field-by-field
(recursing into the nested entitlement), at every level of nesting.** The
generated entitlement structure **mirrors this recursion**: `TEntitlement` carries
both (a) a `RedactMode` *level for the nested field itself* — set to `redact`, it
clears the whole `Inner inner` in one move — and (b) a nested `InnerEntitlement`
that is consulted when the whole-message level is pass-through, redacting `Inner`'s
own fields individually. This holds recursively: `InnerEntitlement` in turn carries
a whole-vs-field choice for *its* nested messages. `sanitize` evaluates
whole-message first (short-circuit: if the whole nested message is redacted, its
sub-entitlement is not consulted), then field-by-field. Because the entitlement
structure recurses, this replaces the flat top-level-only model that
`ValidateNoNestedRedact` enforced (see [§2.3](#23-reconciling-with-validatenonestedredact)).

`sanitize` computes, per field, `effective = most_restrictive(annotation_floor,
entitlement_level)` and applies it. `pass-through` fields with only a `may-*`
annotation are left untouched when the entitlement says pass; `is-*` fields
ignore any looser entitlement.

### 3.1 Decision (a): entitlement type = generated **proto message** (default)

**Recommend: a generated proto message** (`TEntitlement`), not a language-native
struct. Rationale: it is wire-serializable, so the external policy layer (which
*decides* the entitlement — out of scope for tacplus) can construct it in one
language and ship it over gRPC to the sanitizing service; it participates in the
existing registry/schema tooling; and it gives one canonical shape across all
languages for free instead of five hand-kept struct definitions. Alternative
(rejected as default): a language-native struct per language — lighter to emit
but not portable across the process boundary and five surfaces to keep at parity.

The generated `.proto` lives alongside the generated types for the package; the
level fields reuse `RedactMode`; the key is `bytes hmac_key`.

### 3.2 Decision (b): nested key = **one top-level key propagates down** (default)

**Recommend: a single `hmac_key` on the top-level entitlement, propagated to all
nested entitlements** during `sanitize`. Rationale: a value that appears at two
nesting depths pseudonymizes to the same surrogate (join-preserving, which is the
whole point of keyed HMAC), and the caller has one scope knob. Alternative
(rejected as default): a key per nesting level — offers finer scoping but breaks
cross-level joins by default and multiplies caller bookkeeping. If a service ever
needs per-level scoping it can be added later as an optional per-nested override
without changing the default.

---

## 4. Helper API and language parity

### 4.1 Collision-free naming

Existing helpers use a `<Verb><T>` / `release_<t>` convention. Data-entitlement
helpers use a **`tacplusSanitize`-rooted, per-type-suffixed** pattern so they can
never collide with a user field/message named `Sanitize`, and singleton vs
collection are distinct names:

| Concept              | Go                          | Rust                          | Java                              |
|----------------------|-----------------------------|-------------------------------|-----------------------------------|
| singleton            | `SanitizeT`                 | `sanitize_t`                  | `sanitizeT`                       |
| collection           | `SanitizeTCollection`       | `sanitize_t_collection`       | `sanitizeTCollection`             |
| entitlement type     | `TEntitlement`              | `TEntitlement`                | `TEntitlement`                    |
| HMAC primitive       | `tacplusAnonymize` (pkg)    | `tacplus_anonymize` (file)    | `tacplusAnonymize` (once/file)    |

`T` is the message name; the private `tacplus`-prefixed HMAC primitive is emitted
once per package/file exactly as `tacplusPseudonymize` is today, renamed for the
generalized semantics. This inherits the existing collision-free approach in
`helpers.go` (private `tacplus*` primitive + public per-type transform).

### 4.2 Signatures

Singleton (per message `T`), returning the sanitized copy and the set of redacted
field ids:

```go
// Go
func SanitizeT(m *T, ent *TEntitlement) (*T, map[int32]struct{})
```
```rust
// Rust
pub fn sanitize_t(m: &T, ent: &TEntitlement) -> (T, std::collections::HashSet<i32>)
```
```java
// Java — SanitizeResult<T> { T value; java.util.Set<Integer> redactedFieldIds; }
public static SanitizeResult<T> sanitizeT(T m, TEntitlement ent)
```

Collection variant — takes a collection and **one** entitlement, applies it
element-wise:

```go
func SanitizeTCollection(items []*T, ent *TEntitlement) ([]*T, map[int32]struct{})
```

(Union the per-element redacted-id sets, or return per-element sets — see
[§9](#9-open-questions). Default: a union set keyed by field id, since field ids
are stable across elements.)

Helpers are **pure functions**. How the entitlement is decided/populated is
explicitly out of scope for tacplus.

### 4.2.1 Null-safety requirement (codegen requirement)

The generated `sanitize` **must null-guard every nested entitlement
sub-structure.** A null/absent nested entitlement (`ent.inner == null` /
`None` / unset) means **pass-through for that entire subtree** — never a
null-pointer dereference / NPE / `unwrap` panic. Concretely, per full-service
language: Go must nil-check the nested `*InnerEntitlement` before recursing (a nil
nested entitlement leaves `Inner` untouched); Rust must treat `Option<InnerEntitlement>`
`None` as pass-through (no `unwrap`); Java must null-check the nested field before
calling `sanitizeInner` (no NPE). The same guard applies to a null nested *message*
on the value side (`m.inner == null` → nothing to sanitize, no crash). This is a
**hard codegen requirement**, not a caller responsibility — a partially-populated
entitlement (deep tree, only some nested levels supplied) is a normal, supported
input, consistent with open question §9.6 (absent decision = pass-through).

**Required test case (all three full-service languages):** call `sanitize` with an
entitlement whose nested sub-structure is null/absent (and separately, a value
whose nested message is null) and assert it returns the value with that subtree
untouched and **does not panic/NPE**. This test is mandatory in Phase 5's
per-language render/behavioral suite.

### 4.3 Target languages and parity note

Mirror the existing `redactSupportedLanguages` / `redactClientOnlyLanguages`
split:

- **Full-service (emit `sanitize`):** Go, Rust, Java. These host the egress
  surface; fail-loud if the annotation is present but a language's `sanitize`
  generator is missing (same posture as `ValidateRedactLanguageSupport`).
- **Client-only (no `sanitize`):** TypeScript, Python, Swift, C#. They cannot
  host a store egress and only consume already-sanitized data, so the annotation
  is a no-op — exactly as `(spec.redact)` is today. TS lists it informational in
  `validation.go`.

**Parity rule (repo law):** the `tacplusAnonymize` HMAC primitive is a lib
primitive; if it lands for one full-service language it must land for all three
in the same change. Client-only languages get the informational-skip entry in the
same change.

---

## 5. Registry additions and drift guard

### 5.1 Schema additions

The registry (`FieldInfo` in `pkg/proto/sys/tacplus.proto`, `TypeInfo` for
message-level default) records, **per message**, the eligible/floor field ids
and their mode. Add to `FieldInfo` (next free field number; `FieldInfo` is at
`221`):

```proto
// data-entitlement mode for this field from (spec.redact) / message default:
// one of DROP / PSEUDONYMIZE / MAY_REDACT / MAY_ANONYMIZE. UNSPECIFIED = not
// annotated (never transformed).
spec.RedactMode data_entitlement_mode = 31;
```

The proto field number (the wire tag) is the **field id** used in the redacted-id
set and the drift guard — it is already recoverable during descriptor conversion.
Add a message-level default to `TypeInfo` (`data_entitlement_default`, next free
number after the `273` block).

The registry emitters in `pkg/internal/common/registry/` populate these from the
parsed model. This is a genuinely new capability: today the registry does **not**
record redact metadata at all (grep confirms no `redact` in the registry
package), so this is additive.

### 5.2 Drift guard — HARD CRASH at generation

A dedicated check compares the registry's recorded data-entitlement field ids
against the live proto and **hard-crashes generation** on any drift. Treated as
the same class of drift:

1. A recorded field id no longer exists in the proto.
2. A recorded field's **mode changed** (`may` ↔ `is`, or `redact` ↔ `anonymize`).
3. An anonymize-mode field is **no longer `string`/`bytes`**.

No silent auto-heal on removal — the operator must re-add the field or update the
registry deliberately. This mirrors the existing fail-loud posture (the soundness
gate and the checked-in `spec_pb` regen guard).

**Hook point.** In `pkg/builder/builder.go`, immediately after the existing
`ValidateRedactAnnotations` call (`159`) and before language emit, add
`parser.ValidateDataEntitlementRegistryDrift(allParsedFiles, loadedRegistry)`.
Error message shape (matching `redact_validate.go`'s multi-violation style):

```
data entitlement registry drift (registry out of sync with proto):
  - T (field id 7 "prompt"): recorded in registry as MAY_ANONYMIZE but the field
    no longer exists — re-add the field or update the registry
  - T (field id 3 "email"): mode changed proto=MAY_REDACT registry=MAY_ANONYMIZE
    — update the registry
  - T (field id 9 "count"): registry marks it anonymize but its type is int64
    (anonymize requires string/bytes)
```

---

## 6. Generation-time validations (all fail loud)

Extend `ValidateRedactAnnotations` (rename to `ValidateDataEntitlementAnnotations`,
keeping the old name as a thin alias so callers don't churn) to enforce:

1. **Anonymize on non-string/bytes → reject.** `REDACT_MODE_PSEUDONYMIZE` and
   `REDACT_MODE_MAY_ANONYMIZE` require `string` or `bytes` (widen today's
   string-only PSEUDONYMIZE check to also allow `bytes`, per the decided spec).
2. **Mutual exclusivity.** Enforced structurally by the single-`RedactMode`-per-field
   design; still assert the field carries exactly one recognized mode and reject
   `REDACT_MODE_UNSPECIFIED`/empty `{}` (already done today).
3. **Registry drift** ([§5.2](#52-drift-guard--hard-crash-at-generation)).
4. **Language support.** Extend `ValidateDataEntitlementLanguageSupport` (from
   `ValidateRedactLanguageSupport`): a full-service target lacking a `sanitize`
   generator fails loud rather than emitting an unsanitized egress surface.
5. **Nested visibility.** Once nested recursion lands, a nested annotation is
   valid; until then keep the `ValidateNoNestedRedact` rejection.

All emit every violation at once (the `strings.Join(errs, ...)` pattern).

---

## 7. Phased task breakdown

Each phase is independently testable with `go test ./pkg/...` (generator render
tests) before any downstream language builds.

### Phase 1 — proto surface + parser + validation (no emit yet)
- `pkg/proto/sys/spec.proto`: add `REDACT_MODE_MAY_REDACT`/`REDACT_MODE_MAY_ANONYMIZE`;
  add message-level `data_entitlement` (`MessageOptions` ext). Regenerate + commit
  checked-in `spec_pb` (per the spec_pb regen guard).
- `pkg/internal/common/parser/redact_validate.go`: rename to data-entitlement,
  widen anonymize to `string`/`bytes`, accept the two new modes, accept message-level
  default. Keep back-compat alias.
- Tests: table-driven parser tests for each mode × field type (accept/reject).

### Phase 2 — descriptor conversion + nested recursion
- `pkg/internal/common/parser/descriptor.go`: recurse into nested message types so
  their annotations reach the model; replace `ValidateNoNestedRedact` with the
  recursion (or gate it behind the entitlement feature).
- Tests: nested-annotation fixture now *accepted*; update `nested_redact_test.go`.

### Phase 3 — registry schema + emit + drift guard
- `pkg/proto/sys/tacplus.proto`: `FieldInfo.data_entitlement_mode`,
  `TypeInfo.data_entitlement_default`.
- `pkg/internal/common/registry/registry.go` (+ `pkg/internal/proto/registry.go`):
  populate the new fields.
- New `ValidateDataEntitlementRegistryDrift`; wire into `pkg/builder/builder.go`
  after `159`.
- Tests: registry round-trip test; drift-guard tests for the three drift classes
  (each expects a hard crash naming the field).

### Phase 4 — entitlement structure generation (proto message)
- Generate `TEntitlement` proto per annotated message, recursing into nested
  messages, with per-field `RedactMode` levels + `bytes hmac_key`.
- Tests: golden/render test of the generated entitlement `.proto` for a fixture
  with nested + repeated + string/bytes fields.

### Phase 5 — `sanitize` helpers, per language (Go → Rust → Java)
- Go: generalize `generateReleaseHelpers` → `SanitizeT`/`SanitizeTCollection`;
  rename `generatePseudonymizeHelper` → `tacplusAnonymize`; return the redacted-id
  set (`pkg/internal/go/types/helpers.go`).
- Rust: same in `pkg/internal/rust/types/release_gen.go`.
- Java: same in `pkg/internal/java/types/release.go` (`SanitizeResult<T>` wrapper).
- Client-only: TS/Python/Swift/C# — add the informational-skip entry
  (`pkg/internal/typescript/types/validation.go` list, and peers). No emit.
- Parity: the three full-service emitters land together (repo parity law).
- Tests: per-language render/regression tests (mirroring `release_gen_test.go`,
  `release_test.go`), **including the mandatory null-nested-entitlement /
  null-nested-message pass-through test ([§4.2.1](#421-null-safety-requirement-codegen-requirement))
  asserting no NPE/panic.**

### Phase 6 — soundness + docs
- `cmd/soundness/main.go`: add a data-entitlement wiring probe (each full-service
  language emits `SanitizeT`; registry records modes) to the semantic-surface
  probes.
- `docs/USER.md` "Entitlements Model": document the data-entitlement lattice and
  the four annotations; `docs/DEVELOPMENT.md`: note the new validator + drift guard.

---

## 8. Test strategy

- **Generator tests (local, `go test ./pkg/...`):** render/regression tests per
  language emitter (Go/Rust/Java) asserting the exact generated `sanitize` +
  `TEntitlement` text for fixtures covering: string/bytes anonymize, scalar/
  message/repeated redact, nested recursion, message-level default overridden by
  field, `is-*` floor vs looser entitlement. Validator tests assert every reject
  path fails loud with a field-naming message. Drift-guard tests assert a hard
  crash for each of the three drift classes.
- **Chassis behavioral tests (generated-service builds):** for the Go/Rust/Java
  generated code, exercise `SanitizeT` at runtime: same key+value → same
  surrogate; redact zeroes and reports the field id; `is-*` floor cannot be
  loosened; collection variant applies element-wise; **a null/absent nested
  entitlement (and a null nested message) is pass-through and never NPEs/panics
  ([§4.2.1](#421-null-safety-requirement-codegen-requirement)).** These run where
  the generated service is built.
- **No local non-Go builds.** Java/Rust/Python/TS **compile is CI**, per the
  repo's no-local-build convention — the local gate is `go test ./pkg/...` +
  `cmd/soundness`. Do not `mvn`/`cargo`/`tsc` locally.

---

## 9. Open questions / decisions still needed

1. **Collection redacted-id return.** Union set keyed by field id (default) vs
   per-element sets? Union loses which element was redacted; per-element is
   heavier. Confirm the default is acceptable.
2. **`bytes` anonymize surrogate encoding.** For a `bytes` field, does the HMAC
   surrogate stay `bytes` (raw mac) or a hex/`pseud:`-prefixed string coerced back
   to `bytes`? Recommend raw mac bytes (no prefix) for `bytes`, keep the
   `pseud:`-prefixed hex for `string`.
3. **Message-level default on nested messages.** Does a parent's message-level
   default cascade into nested messages, or is it per-message only? Recommend
   per-message only (nested messages carry their own default) to keep the lattice
   local and predictable.
4. **Registry field-number allocation.** Confirm `FieldInfo` field `31` and the
   `MessageOptions`/`TypeInfo` numbers proposed here are actually free at author
   time (reserved ranges shift).
5. **Empty HMAC key.** Today `tacplusPseudonymize` panics on an empty key. Keep
   panic (misconfiguration) or make `sanitize` return an error? Recommend keep
   panic — an empty key is a programming error, and `sanitize` is otherwise
   infallible.
6. **`may-*` with no runtime entitlement supplied.** If the caller passes a
   `TEntitlement` with no level for a `may-*` field, default to pass-through or to
   the transform? Recommend pass-through (the annotation only makes the field
   *eligible*; absence of a decision = no transform), consistent with "entitlement
   ratchets tighter."

---

## 9a. Amendments during implementation (authoritative — supersede the above)

These decisions were made with the maintainer while building Phases 2–5 and
override the corresponding earlier text:

1. **"nested" = message-TYPED field recursion, not nested declarations.** All
   messages in this repo are top-level; a message-typed field references a
   top-level message. Data entitlement recurses through those references
   (whole-message OR field-by-field). Nested type *declarations* stay rejected by
   `ValidateNoNestedRedact` ("hoist to top level") — the repo uses no anonymous /
   nested types.
2. **Redacted indicator = an injected companion bool on the message, not a
   returned id-set.** For every `(spec.redact)` field, tacplus injects a
   `<field>_redacted` bool into the record's OWN proto (before native
   compilation, like `projection_record_key`), so it appears in every language's
   generated type. `sanitize`/`Release` sets it true when it redacts the paired
   field. This replaces §1's "returns the set of redacted field ids."
3. **Injected field number = `RedactIndicatorFieldBase (50000) + source field
   number`.** Stable (tied to the source number), high, collision-free, removed
   with the field. The band `>= 50000` is RESERVED:
   `ValidateReservedFieldNumbers` rejects any author field number in it.
4. **The registry drift guard (§5.2) is dropped.** `FieldInfo`/`TypeInfo` are
   emitted-only, never loaded back, so there is nothing to drift against; the
   invariants it would check are already enforced at parse time, and bool↔field
   sync is guaranteed by regeneration. The registry *schema* additions
   (`FieldInfo.data_entitlement_mode` #31, `TypeInfo.data_entitlement_default`
   #25) and emit are kept.
5. **`bytes` anonymize is supported** via a `tacplusPseudonymizeBytes` primitive
   (raw 128-bit HMAC, no prefix), alongside string's `pseud:`-hex form.

### Future feature (captured, not yet planned): format-preserving anonymization

Anonymize a constrained field to a value that still satisfies its constraint — an
email field → a syntactically-valid email, an IPv4 → a valid IPv4, a UUID → a
valid UUID. The field's format is already known (the `assert_*` annotations /
registry `ConstraintInfos`). The hard part is cross-language determinism: each
format's keyed surrogate generator must emit byte-identical output in
Go/Rust/Java (a per-format golden wire-vector, like the existing `pseud:` vector),
or pseudonymized records stop joining across a mixed-language deployment. Scope as
a separate feature after the core sanitize path lands.

---

## 10. Explicitly OUT OF SCOPE

The heavy machinery in `DATA_ENTITLEMENT.md` is **cut** from this plan:

- No async nonce / release-job flow; no data-returning-RPC async surface.
- No sealed candidate storage; no release processors; no release envelopes.
- No Python-in-JVM capability shell.
- No tenant cells / two-plane control-vs-data split.
- No finish-phase LLM leak check.
- No principals modeling (requester/executor/recipient/tenant/data-subject).
- No filter/generalize/suppress/aggregate/noise transforms — only `pass-through`,
  `anonymize` (keyed HMAC), and `redact` (zero + reported field id).
- The **policy/decision** of which entitlement to apply is **external**, not
  tacplus. tacplus provides the mechanism (annotations, entitlement structure,
  pure `sanitize` helpers, registry, fail-loud generation); policy lives in the
  service/platform.

Consistent with tacplus philosophy: mechanism in tacplus / policy external;
all-language parity; fail-loud; deterministic generation.
