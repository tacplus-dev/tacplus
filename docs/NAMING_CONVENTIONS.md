# Naming and structure conventions

Proposed convention covering the seven consistency entries CON-2 … CON-8. The
maintainer delegated the choice, so this states the rules rather than the options.

Each rule is followed by what it costs to adopt and whether adopting it changes
**generated output**. That distinction is the important one: internal generator
hygiene can be fixed freely, but anything that changes an emitted identifier, file
name, or persisted name is a consumer-visible break and is listed separately at
the end rather than done quietly.

Verified against the current tree. Several CON entries were partly stale — the
specifics below are what is actually there now.

---

## 1. One case converter per transform, in `common/strutil`

**Rule.** Case conversion lives in `pkg/internal/common/strutil`, named for the
transform it performs: `ToSnakeCase`, `ToPascalCase`, `ToCamelCase`,
`ToUpperSnakeCase`. A generator package may define a local converter **only**
when the target language's own toolchain imposes a different rule, and the
function name must say which language it serves.

**Why.** There are currently eleven case helpers outside `strutil`, and two of
them independently implement upper-snake (a third, `toUpperSnake`, went with
`timeseries_group_counts.go` in the 2026-08-25 timedb boneyard):

| function | file |
|---|---|
| `upperSnake` | `pkg/internal/proto/registry_enums.go:173` |
| `pascalToUpperSnake` | `pkg/internal/proto/configholder.go:696` |
| `pascalCaseWord` | `pkg/runner/runner.go:570` |
| `goCamelCase` | `pkg/internal/go/types/helpers.go:511` |
| `toLowerCamel` | `pkg/internal/go/types/extraction_gen.go:386` |
| `upperSnakeCaseToCamelCase` | `pkg/internal/typescript/types/validation.go:1452` |

Three implementations of one transform is three chances to disagree, and NEW-20
is the record of what disagreement costs: a converter change silently altered a
**persisted** table name, orphaning the existing table with nothing failing.

**Legitimately local**, and to be kept — each encodes a foreign toolchain's rule,
not a general transform:

- `pyGeneratedSnakeName` — python codegen's own naming
- `tsGeneratedSnakeName`, `tsPascalIdentifier`, `tsSnakeIdentifier` — TS codegen
- `methodSnakeNameLower` — prost's digit-acronym handling, which deliberately
  diverges from `ToSnakeCase` (see `tonic_names.go`)

**Cost:** small, mechanical. **Generated output:** must be unchanged — the three
upper-snake variants have to be checked for behavioural equivalence before
merging, not assumed equal. Any divergence found is a real bug, and if one of
them feeds a persisted name it needs the NEW-20 treatment.

## 2. Shared template loading, parameterised by language

**Rule.** One `LoadTemplate(subdir, name string)` in
`pkg/internal/common/tmplutil`. No per-language copy.

**Why.** `tmplutil.go` exists four times — python, typescript, rust, java — and
the copies differ only in the package clause, one comment, and the subdirectory
string passed to `fs.Sub`. It is a parameter, duplicated as a file.

**Cost:** trivial. **Generated output:** none. Do this one first; it is the
cheapest demonstration that the convention holds.

## 3. Generator package clause matches its directory

**Rule.** A generator package under `pkg/internal/<lang>/types` declares
`package types`.

**Why.** `pkg/internal/{python,typescript,rust,go}/types` all declare
`package types`. `pkg/internal/java/types` declares `package java`, across all 54
files in it. Same directory depth, same role, different clause — so java alone
cannot be reached by any convention that says "the types package".

**Cost:** a whole-package rename of 54 files. It turned out cheap to apply
because the package has exactly ONE importer, `pkg/builder/builder.go`, and it
already imports under the explicit alias `javatypes` — so no use site referred to
the package by its default name and nothing outside the directory changed.

**Generated output:** none — this is the generator's own package name, not
anything it emits, so `determinism` and `current-generation` prove it byte for
byte.

java is the only real outlier. An earlier draft of this rule also claimed
`package daemon` under python/typescript and `package foo` under java; that was
wrong — those strings are proto/Go fixture content inside `*_test.go` files, not
package clauses. Counting the first `package` line of each file gives 33/37/61/57
`package types` for python, typescript, rust and go, against java's 54
`package java`.

## 4. `_gen.go` means "emits code"

**Rule.** A generator source file is named `<subject>_gen.go` when its job is to
emit generated code, and `<subject>.go` when it is a helper, model, or registry.
The suffix describes the file's role, not its age.

**Why.** The suffix carries no information today. The clearest case:
`java/types/release.go` and `rust/types/release_gen.go` were structurally
parallel — same function names (`writeRelease`,
`generate<Lang>EntitlementBuilder`, `write<Lang>EntitlementFields`), same role —
under two different names. That one is fixed; java now matches rust.

**Cost:** larger than it looks, and larger than the ledger implied. Beyond the
`release` pair, more than fifteen files under `pkg/internal/proto` write output
without the suffix (`state.go`, `record_map.go`, `configholder.go`,
`persisted_store.go`, `entitlement_gen.go`, …). Applying the rule fully is a
15+-file rename touching most of that package.

**Generated output:** none — file names here are generator-internal.

**Status:** the cross-language `release` drift is fixed. The bulk rename is NOT
done. It is mechanical and safe, but it will conflict with anything else in
flight in `internal/proto`, so it wants a quiet moment and its own commit.

## 5. Every emitted file carries the same DO-NOT-EDIT header

**Rule.** Every generated file begins with the copyright line, `Generated by
tacplus`, and `CODE GENERATED - DO NOT EDIT`, in the target language's comment
syntax.

**Why.** The go tree carries 24 pre-rename headers, and swift/csharp templates
carry no DO-NOT-EDIT line at all. A generated file without that marker is a file
someone will eventually hand-edit, and the edit will vanish at the next
generation with no warning.

**Cost:** a header pass across templates. **Generated output:** yes — the header
text of emitted files changes. Harmless to consumers (comments only) but it will
show up as a large diff in every consumer's generated tree, so it should land in
its own commit, announced.

## 6. Package-name transforms are documented and collision-checked

**Rule.** Any transform from a canonical proto package to a target-language
module name is documented in this file, and must be injective. If two distinct
canonical packages can fold to one module name, generation fails rather than
silently merging them.

**Why.** `swiftIdentifier` / `csharpIdentifier` split on every non-alphanumeric
character and PascalCase-join the parts, so all separators are erased:

    foo.bar -> FooBar    foo_bar -> FooBar    foo-bar -> FooBar    foobar -> Foobar

Two names differing only in separators therefore produce one identifier, and the
symbols merge silently — capability loss with no error, the recurring shape.

The ledger recorded this as *module* folding colliding *packages*. That is not
what the code does, and the correction matters for anyone implementing the guard:
`swiftModuleName`/`csharpModuleName` are applied ONCE, to the package base
(`generator.go:91` and `:94`). The transform underneath them, `swiftIdentifier` /
`csharpIdentifier`, is what gets applied per name — to service names, message
names, method names and path segments. **The collision risk is on identifiers,
not on packages**, so a guard placed on module names would sit where two names
can never meet and would never fire.

**Measured, so the next person has a baseline** — no collision exists today:

| population | count | collisions |
|---|---|---|
| proto packages across the platform | 74 | 0 |
| message + service names | 1215 | 0 |

**Cost:** a collision check over emitted identifiers, per scope. **Generated
output:** none today, since nothing currently collides.

**Status:** NOT implemented. The hazard is real but entirely latent, and the
fix must guard identifiers within a scope rather than package names. Recorded
with its measurement rather than implemented in a hurry against a premise that
turned out to be wrong.

## 7. One annotation vocabulary in `spec.proto`

**Rule.** Constraint annotations live in the `assert_*` family. An annotation that
constrains a field is named `assert_<constraint>`. Option numbers are allocated
from a documented range per category, and the ranges live in a comment block at
the top of `spec.proto`.

**Why.** `required_if_false`, `required_if_true` and `required_if_eq`
(`spec.proto:1070,1075,1103`) constrain fields but sit outside the `assert_`
family, so neither a reader nor a tool can enumerate "the constraints" by name.
There is no documented option-number scheme. (The two parallel graph annotation
systems this rule also cited are gone — graph store support was removed
2026-08-25.)

**Cost:** renaming a proto option is a **breaking change** for every consumer
proto that uses it. See the end of this file.

## 8. A registry field name means one thing

**Rule.** A name in `tacplus.proto` has exactly one meaning across the schema.
Where two concepts genuinely differ, they get different names, even if that makes
one of them longer.

**Why.** `graph_type` used to mean two different things in `tacplus.proto`: the
state-graph boundary kind on `BoundaryInfo`, and the graph-store node type name on
`TypeInfo`. The second was removed with graph store support (2026-08-25), so only
the boundary-kind meaning survives — the collision is resolved by deletion, not by
a rename. `EventPublish` and `WatchInfo` are near-twins whose difference is not
recoverable from their names. String-typed enums use mixed casing.

**Cost:** renaming a registry field changes the emitted registry. See below.

## 9. Comments describe the mechanism, not one caller

**Rule.** A comment on a general mechanism names the mechanism. Reach for a
specific service name only when the text is genuinely about that service.

**Why.** "stratum" appears in six comments describing generic behaviour, which
tells a reader the mechanism is stratum-specific when it is not.

**Cost:** trivial. **Generated output:** none.

---

## Safe to adopt now

Rules 1, 2, 3, 4 and 9 are generator-internal. They change no emitted byte, and
`determinism` plus `current-generation` prove that: if generated output shifts,
the byte-identical assertions fail. Each should land in its own commit.

Order by cost, cheapest first — rule 2, then 9, then 1, then 4, then 3. Rule 3
is last because it is a 54-file package rename that will conflict with anything
else in flight; do it when nothing else is open in `internal/java`.

**Adopted so far:** rule 2 (shared template loader) and rule 1's upper-snake case
— `upperSnake`, `toUpperSnake` and `pascalToUpperSnake` were three identical
copies of one transform in a single package, now `strutil.ToUpperSnakeCase`. They
were checked for divergence before merging; they agreed.

Rule 9 is adopted, and rule 4's cross-language `release` drift is fixed.

Rule 3 is adopted: `pkg/internal/java/types` now declares `package types` like
its four siblings.

**Not yet adopted:** the rest of rule 1 (the remaining scattered helpers) and
rule 4's bulk `_gen.go` rename. Both are safe; neither is urgent.

Rule 6 is safe **unless** a collision already exists, which is worth finding out
either way.

## Needs an explicit go-ahead — these break consumers

Not done as part of adopting this convention, because each changes something
outside this repository:

- **Rule 5** (headers) — changes the first lines of every generated file. Cosmetic
  but produces a very large diff in every consumer tree.
- **Rule 7** (`required_if_*` → `assert_required_if_*`) — renaming a proto option
  breaks every consumer proto that uses it. Needs a deprecation window: accept
  both names, warn on the old one, remove later.
- **Rule 8** (registry field names) — changes the emitted registry, which
  consumers and the LLM-facing registry read.

The pattern for all three is the same: accept both spellings, warn on the old,
remove after a stated period. That is a maintainer's call on timing, not mine.
