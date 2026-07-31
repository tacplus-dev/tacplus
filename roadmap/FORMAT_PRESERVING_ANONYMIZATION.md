# Format-Preserving Anonymization — Design Plan

Status: design plan (not yet implemented). Extends the data-entitlement ANONYMIZE
transform so a constrained field anonymizes to a value that STILL SATISFIES its
format constraint — an email → a syntactically-valid email, an IPv4 → a valid
IPv4 — instead of the current `pseud:<hex>` token that fails downstream format
validators and is useless as realistic test/demo data.

## 1. What exists today

`ANONYMIZE` (from `PSEUDONYMIZE` / `MAY_ANONYMIZE`) emits, per string field,
`tacplusPseudonymize(value, key)` → `"pseud:" + hex(HMAC-SHA256(key, value)[:16])`
(bytes get the raw mac). Deterministic and join-preserving, but NOT format-valid.

The field's format is already known: `assert_email`, `assert_ipaddr_type`
(`IPV4`/`IPV6`), `assert_fqdn`, `assert_url` (and more) are parsed onto
`field.Annotations` and reach the sanitize generator via
`EntitlementField.Field.Annotations`. So the anonymize path can branch on format.

## 2. Core model

- **Keyed + deterministic.** Every surrogate is a pure function of
  `HMAC-SHA256(key, value)`. Same (value, key) → same surrogate at any depth and in
  any language (joins preserved), unreversible without the key. This reuses the
  existing cross-language HMAC whose bytes are already pinned by the `pseud:` wire
  vector test.
- **Format-valid by construction.** Each format has a generator that shapes the
  mac bytes into a valid instance of that format.
- **Fully synthetic, no source leakage (default).** The surrogate is derived only
  from the mac, so no fragment of the original value survives (see §4 for the
  email-domain decision).
- **Fallback unchanged.** A string/bytes field with no recognized format
  annotation keeps `tacplusPseudonymize` (the `pseud:` token).

## 3. Where it plugs in

Only the leaf ANONYMIZE emitters change — `writeGoAnonymize` / `rustAnonymize` /
`javaAnonymize`. For a string field with an ANONYMIZE effective level, pick the
primitive by the field's format annotation:

| Field annotation | Primitive | Fallback if absent |
|---|---|---|
| `assert_email` | `tacplusPseudonymizeEmail` | — |
| `assert_ipaddr_type = IPV4` | `tacplusPseudonymizeIpv4` | — |
| `assert_ipaddr_type = IPV6` | `tacplusPseudonymizeIpv6` | — |
| `assert_fqdn` | `tacplusPseudonymizeFqdn` | — |
| `assert_url` | `tacplusPseudonymizeUrl` | (v2) |
| none | `tacplusPseudonymize` | (today's `pseud:` token) |

Each primitive is a once-per-file generated helper, gated on use (like the
existing string/bytes helpers), and shares the HMAC core.

## 4. Per-format surrogate algorithm (v1)

Let `m = HMAC-SHA256(key, value)` (32 bytes). All hex is lowercase.

- **email** → `u<hex(m[0:6])>@h<hex(m[6:10])>.example`
  Fully synthetic, RFC-valid shape, `.example` reserved TLD (never routable).
  **Decision needed (§6.1):** preserve the original domain instead
  (`u<hex>@<original-domain>`) for analytics? Default: **do NOT** — treat the
  domain as potentially identifying.
- **ipv4** → `10.<m[0]>.<m[1]>.<m[2]>` — RFC-1918 private range, always valid,
  never a real public host. (Avoids 0.0.0.0/broadcast edge cases of raw octets.)
- **ipv6** → `2001:db8:<4 hex groups from m[0:8]>` within the RFC-3849
  documentation prefix `2001:db8::/32` — always valid, never routable.
- **fqdn** → `h<hex(m[0:8])>.example` — valid hostname, reserved TLD.
- **bytes / no-format string** → unchanged (`tacplusPseudonymizeBytes` /
  `tacplusPseudonymize`).

Every mapping is length-bounded and uses only `[0-9a-f]` + structural chars, so
the output re-passes the same `assert_*` validator.

## 5. Cross-language determinism (the hard part)

Each primitive must emit BYTE-IDENTICAL output in Go/Rust/Java, or anonymized
records stop joining across a mixed-language deployment. Enforcement mirrors the
existing `pseud:` vector:

- One **golden wire-vector test per format**, computed offline, asserted in all
  three languages' generator tests (the string the primitive must produce for a
  fixed (key, value)).
- A `cmd/soundness` probe that each format primitive is emitted in all
  full-service languages (extends the existing `data-entitlement Sanitize<T>`
  probe).
- A cross-test (consumer-contract) that anonymizes one field of each format and
  asserts the surrogate re-passes that format's validator AND equals the golden
  vector — real end-to-end evidence in the built toolchain.

## 6. Open questions

1. **Email domain preservation.** Fully synthetic domain (default, most private)
   vs. preserve the original domain (better analytics, but the domain may be
   identifying)? Recommend fully synthetic; could add an opt-in later.
2. **IP range choice.** Private (`10/8`, `2001:db8::/32` docs) as above, vs. a
   caller-configurable range. Recommend fixed ranges for v1.
3. **v1 format set.** email / ipv4 / ipv6 / fqdn. Defer url, uuid (no
   `assert_uuid` today), mac, semver to v2.
4. **Length fit.** For a field with `assert_max_size`, ensure the surrogate fits
   (all v1 surrogates are short and bounded; add a guard if a tight max_size is
   set).

## 7. Phased tasks

1. **Go primitives + emitter branch** — `tacplusPseudonymize{Email,Ipv4,Ipv6,Fqdn}`
   + format dispatch in `writeGoAnonymize`; golden-vector unit tests.
2. **Rust + Java parity** — same primitives + dispatch; golden vectors match Go.
3. **Soundness probe + cross-test** — per-format end-to-end evidence.
4. **Docs** — USER.md ANONYMIZE section notes format preservation.
