# Plan: the `.tac` sugar format and its lossless transpiler

## Background

tacplus defines types and service contracts in ordinary `.proto` files using
proto2 extensions declared in `spec.proto` (`extend google.protobuf.FieldOptions`,
`MessageOptions`, `FileOptions`, …). The upside is that the source is *real proto*:
`protoc` reads it directly, and every parser, IDE, and linter in the proto
ecosystem understands it. The downside is that the annotations are noisy compared
to native keywords:

```proto
service InventoryService {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc GetProduct(GetProductRequest) returns (GetProductResponse);
}

message Config {
  option (spec.grpc_config) = {service_name: "letsencrypt"};
  repeated string hostnames = 4 [(spec.assert_required) = true, (spec.assert_max_size) = 16];
}
```

`.tac` is a **sugar dialect** that adds keywords for the highest-frequency
annotation patterns, so the same source reads:

```tac
s2s InventoryService {
  rpc GetProduct(GetProductRequest) returns (GetProductResponse);
}

config Config {
  repeated string hostnames = 4 [assert_required, assert_max_size = 16];
}
```

A utility shipped inside tacplus converts between the two with **no information
loss in either direction**. It is callable directly from the CLI and is also the
front of the build pipeline: `.tac` input is converted to annotated `.proto`
in-process, then handed to the existing single code-generation path.

### The one architectural fact that shapes the design

tacplus has no proto parser of its own. `NewDescriptorParser`
(`pkg/internal/common/parser/descriptor.go`) shells out to `protoc` over a
directory and consumes the resulting `FileDescriptorSet`. `protoc` cannot read
`.tac`. Therefore the transpiler is not optional polish — it is the **only** way
`.tac` can enter the system, and the only correct integration is: lower `.tac` to
annotated `.proto` *before* descriptors are produced, and leave everything
downstream (protoc, model, all five generators, validate) untouched. There is
exactly one code path; `.tac` is a normalization step in front of it.

`.proto` source must keep working unchanged. `.tac` is additive.

## Design

### `.tac` is a strict superset of `.proto`

Every valid `.proto` is valid `.tac`. The sugar keywords are *optional* alternate
spellings of specific annotation patterns; anything the dialect does not
recognize is already proto and passes through verbatim. This is what makes
losslessness tractable and adoption free: rename `.proto` → `.tac` and it already
compiles; sugar is applied incrementally, never as a rewrite.

Two recognition rules carry the entire format. Both are driven by data, not
hand-maintained keyword lists.

### Rule A — block keywords (the only hand-authored table)

A block keyword replaces a `message`/`service` declaration *and* injects one
recognized `spec` option. Five rows, fixed:

| `.tac` | base construct | injected option | auto-derived fields |
|---|---|---|---|
| `config Name` | `message Name` | `(spec.grpc_config)` | `service_name` ← service identity |
| `state  Name` | `message Name` | `(spec.service_state)` | — |
| `s2s    Name` | `service Name` | `(spec.service_s2s)` | — |
| `c2s    Name` | `service Name` | `(spec.service_c2s)` | — |
| `c2d    Name` | `service Name` | `(spec.service_c2d)` | — |

The literal keyword `service` disappears from `.tac`: you write `s2s X`,
`c2s X`, or `c2d X`. The service identity used to derive `grpc_config.service_name`
is `spec.service.name` if set, otherwise the proto `package` name (confirmed:
`letsencrypt`, `gateway`, `opa` all satisfy `service_name == package`). Derived
values are never authored in `.tac`; they are reconstructed from file context on
lower and dropped on raise.

A block body contains **header statements** that populate the injected option's
sub-fields, followed by ordinary members (fields, rpcs, nested types):

```tac
state LetsEncryptState {
  tag "kernel"                                  // → service_state.tag
  export                                        // → service_state.export = true
  LetsEncryptCertData cert_data = 1 [persistence];
}
```
lowers to
```proto
message LetsEncryptState {
  option (spec.service_state) = {tag: "kernel", export: true};
  LetsEncryptCertData cert_data = 1 [(spec.persistence) = true];
}
```

### Rule B — bare `spec` names

A bare `spec` extension name stands for its qualified option. The set of bare-able
names, and each name's value kind and arity, are read **straight from
`spec.proto`** — never hardcoded. Applied in two positions with one literal
grammar:

- **Field options** inside `[ … ]`:
  - `assert_required`, `persistence` → `(spec.assert_required) = true`, `(spec.persistence) = true`
  - `assert_max_size = 16` → `(spec.assert_max_size) = 16`
  - `assert_numeric = {ge: 1}` → `(spec.assert_numeric) = {ge: 1}`
- **Header statements** inside a block, populating the injected option message's fields.

Literal grammar (identical in both positions):

| form | meaning | requires (from schema) |
|---|---|---|
| `name` | `name = true` | bool field |
| `name v` | `name = v` | scalar field |
| `name [a, b]` | `name = [a, b]` | `repeated` field |
| `name {…}` | `name = {…}` | message field |

**Arity is schema-driven, never name-driven.** Whether a value is a scalar or a
bracket list comes from the field's type in `spec.proto` (`repeated` ⇒ `[ ]`,
singular ⇒ bare), and the value itself is opaque to tacplus. (Tags are a routing
label tacplus does not interpret. Note: `StateAnnotation.tag` is currently
*singular* `string`; under the schema-driven rule it lowers as `tag "kernel"`. If
multiple tags are wanted, widen the field to `repeated string tag` in `spec.proto`
and `tag ["kernel", "edge"]` falls out automatically — do not special-case the
transpiler. See Open Questions.)

Standard proto options (`[deprecated = true]`, `default`, …) are not `spec`
extensions, are not in the registry, and pass through untouched.

### Everything else passes through

`message`, `enum`, `oneof`, `map`, `reserved`, `import`, `package`, file-level
options, nested types, comments, and blank lines are all first-class `.tac` and
emit verbatim. Block keywords are an *optional overlay* applied only to
constructs that carry one of the five recognized options. This is required, not a
convenience: a real file
([`cross-test/proto/services2/inventory.proto`](../cross-test/proto/services2/inventory.proto))
is mostly plain request/response messages and enums with no message-level
annotation; if every construct needed a block keyword the format could not
represent — let alone round-trip — a single real file. The bare-option rule still
applies inside plain `message` blocks, so plain DTOs get the readable
`[assert_required, assert_min_size = 8]` spelling too.

### Losslessness

The transpiler is built as a pair of inverse functions over a single
trivia-preserving tree (see Phase 1). Each block keyword ↔ (construct + injected
option) is a bijection; each bare name ↔ qualified option is a bijection; derived
values are reconstructed from context. Therefore:

```
Raise(Lower(x)) == x   for all .tac x
Lower(Raise(y)) == y   for all .proto y
```

Both are enforced as property tests over the `cross-test` corpus (Phase 1).

When raise meets an annotated construct whose option *has no* block keyword, or a
block that carries an extra unsugared option (e.g. a `state` that also sets
`state_consistency`), it folds what it recognizes and leaves the remainder as a
verbatim `option (spec.…)` line inside the block — legal because `.tac` is a
superset. No construct is ever un-representable.

### Two spellings, one canonical form

Because the superset admits both `state X { … }` and `message X { option
(spec.service_state) … }`, the same construct has two spellings. This is not
ambiguity — it is what `fmt`/`lint` resolve: `lower` accepts both, `fmt`/`raise`
canonicalize to the sugar form, and `lint` flags the verbose spelling. Linting is
a first-class deliverable, not an afterthought.

## Components

| Component | Responsibility |
|---|---|
| `pkg/tac` (new) | The dialect: lexer, trivia-preserving CST, `Lower`, `Raise`, `Lint`, `Fmt`. No I/O, no protoc. |
| spec registry | Loads the embedded `spec.proto`, parses its `extend` blocks and option messages, yields {name, scope, value kind, arity, sub-fields}. Drives Rule B. Zero drift by construction. |
| block table | The 5-row Rule-A mapping, plus the `service_name` derivation. The only hand-authored mapping. |
| protosrc shim | Normalizes a resolved source tree so downstream sees only `.proto` (Phase 2). |
| CLI | `lower`/`raise`/`fmt`/`lint` subcommands; `.tac` transparently accepted by `build`/`validate` (Phase 3). |

### Parser approach (no regex)

Per [`docs/DEVELOPMENT.md`](../docs/DEVELOPMENT.md) ("do not parse proto text with
ad hoc string matching"), the transpiler is **CST-based**, not regex- or
string-replace-based. One concrete syntax tree represents both dialects:

- Parse `.tac` → CST. The same parser parses `.proto` (a subset), so sugar nodes
  simply do not appear for proto input.
- `Lower` = CST → proto text (expand sugar nodes to canonical option/construct text).
- `Raise` = CST → tac text (fold recognized option/construct nodes to sugar).
- `Fmt`   = parse → emit same dialect, canonicalized.

The CST retains comments, blank lines, and member order so the non-sugar
remainder round-trips byte-for-byte. One parser, one tree, two emitters — which
is what makes `Lower`/`Raise` structurally inverse rather than inverse by
coincidence.

## Phase 1 — Transpiler core, standalone

**Goal:** `pkg/tac` with `Lower`/`Raise`/`Lint`/`Fmt` and the spec registry,
fully tested in isolation. No pipeline wiring, no CLI.

In scope:
1. Lexer + trivia-preserving CST for the proto grammar plus the five block
   keywords and the bare-option positions.
2. Spec registry extracted from the embedded `spec.proto` (names, scopes, value
   kinds, arities, sub-fields).
3. The 5-row block table and the `service_name` derivation.
4. `Lower`, `Raise`, `Fmt`, `Lint`.
5. Tests:
   - Unit tests for each rule (block kw, header stmt, bare field option, derived
     value, passthrough, mixed/unsugared-option fold).
   - **Round-trip property tests**: for every `.proto` under `cross-test`,
     `Lower(Raise(p)) == p`; and the descriptor set produced from `Lower(Raise(p))`
     is byte-identical to that produced from `p` (semantic-equivalence backstop,
     independent of textual formatting).

Acceptance: `go test ./pkg/tac/...` green; round-trip holds over the full
`cross-test` proto corpus.

## Phase 2 — Pipeline wiring (single path preserved)

**Goal:** `build` and `validate` accept `.tac` transparently, with no second
code-generation path and no `.proto` written to the user's tree.

In scope:
1. Normalize at source resolution (`pkg/protosrc`): when resolving an input tree,
   lower every `.tac` to `.proto` text. Choose one of:
   - emit lowered `.proto` into the existing ephemeral staging tree the
     generators already build (`copyProtoFiles` →`tempProtoDir`), or
   - hand lowered content in-memory to the descriptor parser.
   Normalizing once at `protosrc` (rather than in each of the five per-language
   `copyProtoFiles`) means all consumers — every generator, `validate`, and the
   standalone `ParseProtoFile` — see only `.proto`.
2. Mixed `.tac`/`.proto` trees: out of scope by decision (no mixed trees, no
   cross-dialect imports). Reject or document accordingly.
3. Error mapping: a lower/parse failure on `.tac` reports the `.tac` source
   location, not the synthesized `.proto`.

Acceptance: a `.tac` project and the equivalent `.proto` project produce
byte-identical generated output for Go, Java, and Rust; `validate` accepts `.tac`.

## Phase 3 — CLI surface and linting

**Goal:** direct conversion and quality tooling.

In scope:
1. `tacplus lower <file.tac>` / `tacplus raise <file.proto>` — one-shot conversion
   to stdout for inspection and migration.
2. `tacplus fmt` — canonicalize `.tac` (fold to sugar, normalize layout).
3. `tacplus lint` — flag: bare names that are not registered `spec` extensions;
   verbose spellings that have a sugar form; block keywords whose derived value
   contradicts file context; arity mismatches against the schema.

Acceptance: lint catches each class on a fixture; `fmt` is idempotent.

## Phase 4 — Docs and adoption

Document the dialect in [`docs/USER.md`](../docs/USER.md) (the five keywords, the
bare-option rule, "`.tac` is a superset"), note the transpiler internals in
[`docs/DEVELOPMENT.md`](../docs/DEVELOPMENT.md), and ship a short migration note
(`raise` an existing `.proto` tree, eyeball the diff, commit `.tac`). Optional:
convert one consumer service as a worked example.

## Open questions

1. **`StateAnnotation.tag` arity.** Singular today; the `state` example wants
   multiple. Widen to `repeated string tag` in `spec.proto` (and update
   `descriptor.go` parsing + existing `.proto`), or keep singular and lower as
   `tag "kernel"`? The transpiler stays schema-driven either way.
2. **Bare-option scope.** v1 assumes the bare-option rule applies in field
   options and block header statements only. Other option scopes (file options,
   enum-value options like `json_return_value`) stay fully qualified unless we
   choose to extend the rule.
3. **CST library vs hand-written.** Build the trivia-preserving parser in `pkg/tac`
   directly, or extend an existing proto AST (e.g. `bufbuild/protocompile/ast`,
   which preserves comments) with the sugar productions? Hand-written keeps the
   sugar grammar first-class; extending reuses a hardened proto grammar at the
   cost of forking it.
4. **Staging vs in-memory (Phase 2).** Lowered `.proto` into the existing
   `tempProtoDir`, or fully in-memory to the descriptor parser? Staging reuses the
   current flow with minimal change; in-memory most literally satisfies "no
   requirement to write `.proto` to the filesystem."
