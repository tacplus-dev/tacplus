# wire-corpus

Cross-language **serialization-equivalence** gate: one message, encoded **once**
by go into a checked-in golden byte corpus, decoded and **value-asserted** by
all five generated languages.

```bash
just -f cross-test/justfile wire-corpus
just -f cross-test/justfile wire-corpus-go
just -f cross-test/justfile wire-corpus-java
just -f cross-test/justfile wire-corpus-python
just -f cross-test/justfile wire-corpus-rust
just -f cross-test/justfile wire-corpus-typescript
```

## Why this exists

Every other cross-test gate answers a different question:

| gate | proves |
| --- | --- |
| `cmd/soundness` | a constraint handler is *referenced* in each emitter (source parity) |
| `current-generation`, `consumer-contract` | generated output *compiles* |
| `runtime-smoke`, `server-runtime` | generated client/server pairs *behave* over the wire |

None of them ever compared a decoded **field value** across languages. Before
this gate, no cross-language value-equivalence test existed anywhere in the
repo: a language whose runtime silently truncated a `uint64` or dropped
`optional` presence would pass every mandatory gate.

## How it works

`cross-test/fixtures/wire_corpus.json` is the single source of truth. Each
vector carries the golden `hex` **and** the `expect` values it decodes to. The
go leg (`decoder/go/main.go`) is the only encoder: with `--emit` it re-encodes
each vector from that vector's own `expect` block, deterministically (sorted map
keys), and rewrites `hex`.

Then five decoders — go, java, python, rust, typescript — each generate the
model from `cross-test/wire-corpus/proto`, decode the **same** blob with their
generated `CorpusRecord` type, and assert every field equals `expect`. Five
decoders against one corpus, not a 5x5 matrix.

The go leg additionally re-encodes `expect` and asserts the bytes are
byte-identical to the checked-in `hex`. That is what pins the corpus: a future
change to go's encoding of these shapes fails here instead of silently rewriting
the standard every other language is measured against.

**If a decoder disagrees with the golden bytes, that is a finding.** Do not edit
an `expect` value to make it agree; `--emit` exists only for a deliberate change
to the corpus itself.

## Covered shapes, and why only these

`CorpusRecord` (`proto/corpus/corpus.proto`) covers exactly the wire shapes this
repo actually uses **and** that actually differ between language runtimes:

| field | vector `full` | vector `sparse` | risk it covers |
| --- | --- | --- | --- |
| `int64 big_negative` | `-2^63` | `2^63-1` | 10-byte sign-extended varint; a 32-bit or float slot loses it |
| `uint64 big_unsigned` | `2^53+1` | `2^64-1` | JS float precision; java has no unsigned long (`Long.toUnsignedString`) |
| `map<string,string> labels` | 3 entries, declared unsorted, one empty value | empty | iteration-order and empty-value stability |
| `optional int32 opt_count` | PRESENT, value `0` | ABSENT | proto3 explicit presence — present-with-zero vs absent is indistinguishable to a runtime that collapses `optional` |
| `bytes blob` | 9 non-UTF-8 bytes | empty | bytes must never round-trip through a string type |

Deliberately **out of scope**, per the approved phase-2 scope note:

- `sint*` / `sfixed*` — **zero** usage anywhere in the repo's protos.
- non-string map keys — maps are exclusively `map<string, X>` (52/52), so
  int/bool map-key ordering risk does not exist here.
- `google.protobuf.Timestamp` precision — timestamps are `int64 timestamp_ms`
  by convention.
- `fixed32`/`fixed64` — only 3 uses repo-wide, and they are fixed-width in every
  runtime.

## Notes

- Local/manual, like `server-runtime` and `w2s-validate`. It is **not** part of
  `just test`, `just soundness`, `just determinism` or `just current-generation`,
  and it is not in CI or `nightly-matrix.sh`: it needs go, maven, cargo, python
  and node concurrently.
- Generated outputs go to a temporary work directory. Use `--keep` plus
  `--workdir` to preserve them.
- The gate proves decode-side value equivalence only. Cross-language *encoding*
  equivalence (each language encodes and go verifies) and the shared
  constraint-class enforcement corpus are still uncovered.
