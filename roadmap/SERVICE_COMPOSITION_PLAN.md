# Decentralized Protos & Per-Customer Service Composition — Design Capture

Status: **design capture / exploration.** No implementation. This records an
architecture direction discussed for the platform's proto layout, versioning,
and deployment model. It is deliberately broad; concrete tacplus work items are
called out in [§10](#10-tacplus-work-implied) but are not yet scoped or
scheduled.

Related: this touches the same tacplus machinery as `DATA_ENTITLEMENT_PLAN.md`
(the per-message field-id registry / drift guard is reused here as a per-repo,
per-release-line artifact).

---

## 0. Vocabulary — tenancy is two levels

These are at different layers of the stack and must not be conflated:

- **Customer** — the **deployment unit.** Own instances of kernel and the other
  platform services, own k8s namespace, own composition/build. This is the
  infra + build boundary. (Elsewhere in this doc, "per-customer" = this.)
- **Department** (and finer org units) — **application multitenancy** *inside* a
  single customer's instance. A runtime data-model partition (record `owner` /
  config scope), **not** a separate namespace, composition, or build. This is
  what the existing `config_slice{scope: CONFIG_SLICE_SCOPE_TENANT}` already
  denotes — in-app tenancy, not the deployment unit.

**Everything about protos, versioning, composition, and builds keys on the
_customer_.** Department-level tenancy is orthogonal and rides the existing
record-owner/scope dimension at runtime — it costs no build and no new
namespace. (Note: the codebase says "tenant" for the *department* level; this
doc says "customer" for the deployment unit to avoid the clash.)

---

## 1. Problem

Today all protos live in one monolithic repo (`platform/proto`). Consuming
services (e.g. kernel) clone it and `git reset --hard origin/HEAD`. Two pains:

1. **Ownership is split from the code.** A service's protos live in a different
   repo from the service that implements them. Editing requires touching two
   places; the source of truth is remote from the owner. (This is the class of
   bug that surfaced during the length→size / libs→sys migration — two
   checkouts had to be kept in sync by hand.)
2. **Cleanup cost scales with service count.** Adding or removing a service
   leaves (or requires GC of) entries in one shared repo and one shared registry
   — a merge-conflict magnet.

And a new requirement makes the monolith untenable: **customers will author
their own services** via the webapp (design a service for their domain). The set
of services is no longer a vendor-fixed, release-time fact — it is
customer-specific and changes on the customer's cadence.

---

## 2. Decisions locked

- **Decentralized protos.** Each service repo owns its own `proto/`. A small
  **common** repo holds only shared vocabulary (`sys/*`, `spec/*`, `token/*`,
  the entitlement enum — the things every service imports). Cross-service state
  import happens by importing directly from the *owning* service's repo.
- **Per-repo field-id registry.** Each repo carries its own registry for the
  auto-generated stable ids (redact indicators `50000+N`, entitlement shapes,
  etc.). Field-id stability is a per-message / per-repo concern, so it federates
  naturally — no central registry lock.
- **Kernel is NOT generic.** It keeps statically-generated, typed per-service
  handlers (no descriptor-driven generic record store). Rejected the generic
  alternative deliberately.
- **Per-customer deployment.** Each customer gets their *own* instances of
  kernel and the other platform services, isolated in a **per-customer k8s
  namespace**. A per-customer kernel image is therefore the intended model, not
  sprawl. This is the primary blast-radius control: a bad customer proto or a
  service failure is contained to that customer's namespace. (Department-level
  multitenancy lives *inside* each customer instance — see §0 — and does not add
  namespaces or builds.)
- **Optimize services over time.** The per-customer footprint is accepted now;
  right-sizing / consolidating services is a later optimization, not a blocker.

---

## 3. Two version axes

Self-serve authoring splits the single "platform version" into two independent
axes that **multiply**:

| Axis | Owner | Cadence | Unit |
|------|-------|---------|------|
| **Platform / vendor** | Operations (us) | Release train | `sys`, `common`, `spec`, vendor services (kernel, letsencrypt…) |
| **Composition / application** | Customer | Self-serve (webapp) | The *set* of services in a customer's deployment + their authored protos |

A running deployment is identified by the **pair**:
`platform@1.2.6 + composition@<customer>-vNN`, materialized in that customer's
namespace. The vendor moves the first coordinate (release/patch); the customer
moves the second (author/edit a service). Neither blocks the other.

> The insight driving this: **versioning is now partly an application concern,
> not purely an operations concern.** The composition coordinate belongs to the
> customer.

---

## 4. Pinning rules (the release train)

- **`MAJOR.MINOR` is group-wide.** We cut a release branch across all repos; it
  receives a shared `MAJOR.MINOR` tag. At the cut, **the proto surface is
  final** for that line. Because the whole train shares one `MAJOR.MINOR`,
  everyone imports the same `common@1.2`, `sys@1.2`, etc. — the diamond/transitive
  skew problem is dissolved by construction.
- **`PATCH` is per-repo**, starts at `1`, increments independently per repo for
  fixes within the line.
- **A patch MUST be non-surface** (enforced, not by convention): no renumbered/
  removed fields, no type changes, no reused ids, no constraint that rejects
  previously-valid messages. Consumers therefore pin to **`MAJOR.MINOR`** and let
  `PATCH` float forward — a patch rebuild is safe by construction.
  - Open sub-decision: may a patch add a *new optional* field, or is it strictly
    non-surface (docs / loosened constraints / codegen fixes only)? **Lean:
    strictly non-surface**, so "`MAJOR.MINOR` = the contract" is literally true.
    Adding fields → next MINOR cut.
- **Enforcement = breaking-change gate + the per-repo registry.** The registry is
  the append-only id ledger; a breaking-change check (à la `buf breaking
  --against` the `.0` cut of the line) enforces "append-only / non-surface" on
  every patch. These two are the same guardrail.

---

## 5. Why the patch rule matters for customers

A customer composition depends on `platform@1.2` (MINOR-pinned, patch floats). So
shipping kernel/letsencrypt patch `1.2.6`:

- rebuilds each customer's composition against the new base, and
- **cannot break it**, because the surface the customer authored against did not
  move.

Result: vendor patch fixes and customer service edits are fully independent
events. This is the payoff of the §4 patch rule, applied across the
vendor/customer boundary.

A **MINOR bump is different** — it is a new frozen surface and may carry breaking
`sys`/`spec` changes. Moving a customer from `1.2`→`1.3` is a real, assisted
**migration** of their authored services (the webapp designer authors against a
pinned line, e.g. "designing for 1.2"). Patches are free; MINORs have a customer
cost.

---

## 6. Deployment model

- One **k8s namespace per customer**; each holds that customer's own instances
  of kernel and the other platform services. Department-level multitenancy (§0)
  is partitioned *within* that instance at runtime — no extra namespace.
- A deployment = (chosen platform train) × (that customer's composition), built
  and deployed into the customer's namespace.
- Blast radius is the namespace: a failing service, or a malformed/hostile
  customer proto, is contained to one customer. This is *also* the answer to the
  "customer protos are an untrusted build input" concern — a bad compose fails
  that customer's build/namespace and touches nothing shared.

---

## 7. Build / composition pipeline

Two stages:

1. **Vendor stage (release train).** Builds the train's vendor artifacts and the
   vendor↔vendor generated glue against the frozen `MAJOR.MINOR` surface.
   Produced once per release/patch, shared as the base for all customers.
2. **Composition stage (per-customer).** Triggered by (a) a webapp design change,
   or (b) picking up a platform patch. Takes the vendor base at `platform@1.2.x`,
   adds the customer's authored service protos, regenerates the cross-service glue
   for the customer's import-edges (vendor↔customer, customer↔customer — kernel's
   typed handlers for the customer's records included, since kernel is not
   generic), compiles, and deploys into the customer namespace.

Rebuild scope = the customer's composition (their service set + the vendor
services that statically import them). Because kernel is non-generic, adding a
customer service *does* rebuild that customer's kernel — accepted, because it is
a per-customer image in a per-customer namespace anyway.

### 7.1 Build / cache server — copy-on-create, build-on-change (roadmap)

A build/cache server fronts image production and reuse so that onboarding a
customer is normally **not a build**:

- **Copy-on-create.** Creating/onboarding a customer with the default service set
  is a **pure copy** of a prebuilt canonical baseline image for the chosen
  platform train (the vendor base + stock composition). No compile at onboarding
  — instant provisioning.
- **Build-on-change.** The composition stage (step 2 above) runs only when the
  customer actually authors or edits a service. Until then the copied baseline is
  served as-is.
- **Shared pull / layer reuse.** Built images are content-addressed and cached on
  the server; all of a customer's k8s nodes pull the **same** image, and
  base/vendor layers dedup across customers (and across customers on the same
  train who never diverge). Compose cost is paid once per *distinct* composition,
  not per customer.

Net effect: onboarding is O(copy), the common case (customer never edits a
service) never compiles, and node rollout is a normal image pull. This is a
roadmap optimization — not required for correctness, only for onboarding latency
and build/cache economics.

---

## 8. Uniqueness allocation (simplified by per-customer isolation)

Per-repo registries federate field ids. The only things that can't be
decentralized are values that must be unique across a *deployment*: service ids,
`s2s` auth names, record-store namespaces, any global enum space.

**Per-customer namespaces shrink this from global to customer-scoped.** These
values need only be unique within one customer's composition, allocated at that
customer's compose time (e.g. customer-prefixed). Still worth **enumerating the
exact set** that compose-time must allocate — small, but it's the one thing the
per-repo registry cannot own. (Department-level uniqueness is a *data* concern
inside the instance — record `owner`/scope — not a compose-time allocation.)

### 8.1 Proto-package uniqueness — two-tier naming (design)

Proto packages are one global flat namespace, so service packages must never
collide — with each other, or with tacplus's generated/framework packages.
Background: the framework was squatting bare names (the EventBus support proto
took `package eventbus`, colliding with the eventbus *service*). Step one was to
move **generated** protos into their own namespace (`generated/…` with distinct
packages — done for the EventBus support proto). Step two is the service side:

Disambiguation is a **namespace segment (directory)**, not a suffix on the
service name — the service name stays clean, and a parent segment separates the
tiers:

- **Core (vendor) services** — a curated, controlled set (kernel, eventbus,
  letsencrypt, …) under a reserved **`csvc/`** segment: `…/csvc/kernel` →
  package `…csvc.kernel`. No hashing needed; the set is known and the segment
  keeps core out of both the framework and user namespaces.
- **User-defined services** — under a **per-customer namespace segment**
  (`<userpkg>/`, unique per customer — a hash / tenant id): `…/<userpkg>/kernel`
  → package `…<userpkg>.kernel`. Assigned during the **proto rewrite** so the
  author writes any readable name and tacplus guarantees uniqueness. The unique
  namespace also **extends into the container name** so the deployment artifact
  is uniquely named.
- **No collision across tiers or customers**, even for identical service names:
  core `csvc.kernel`, user-A `<userpkgA>.kernel`, user-B `<userpkgB>.kernel` are
  all distinct packages. The service name (`kernel`) and all bare references
  (`service_name`, broker tag, discovery id) stay clean; the segment is the
  collision-guard.

**Path derivation — gRPC vs REST split.** The unique package (hash/`csvc`) must
not leak into user-facing URLs. So the two path surfaces decouple:

- **gRPC path** = `{package}.{service}/{method}` — uses the package **as-is**
  (hash/`csvc`). Machine-facing (S2S, generated clients), so an opaque hash costs
  nothing. Entitlement-grant identity keys off this (the full package), *not* the
  REST path.
- **REST path** = strip only the **common org prefix** off the full package,
  keeping the tier/customer segment → `{root}/csvc/kernel/{service}/{method}` or
  `{root}/{userpkg}/kernel/{service}/{method}`. User-facing and clean, and the
  retained segment means a user `kernel` and the core `kernel` get **distinct**
  REST paths with no rename. This still **decouples REST from the gRPC identity**
  (today they're the same — `descriptor.go:705`, "T10"): REST drops only the org
  prefix; gRPC/entitlements keep the full raw package.
- **No extra compose-time check needed for REST names.** Because the segment
  (`csvc` / `<userpkg>`) is retained in the REST path and each `<userpkg>` is
  unique, distinct services never map to the same REST path. Uniqueness reduces
  to "`<userpkg>` segments are unique" (guaranteed at rewrite) — the earlier
  per-display-name validation is unnecessary.

Rejected: forcing a uniform `svc` suffix on *every* service — noisy, and it only
guards service-vs-framework, never service-vs-service (which the user-tier hash
is specifically for). Enforcement (validation / hard-fail on a mis-named core
service, hash assignment for user services) lives in tacplus's rewrite +
validation passes; not yet scoped.

---

## 9. Open questions

1. **Patch legality** (§4): strictly non-surface, or new-optional-fields allowed?
2. **The global-uniqueness set** (§8): exact list of values compose-time must
   allocate per customer.
3. **Dependency expression**: manifest/lockfile format a consumer uses to pin
   imports (`deps: { agent-sensor: "1.2", common: "1.2" }`), and how patch-float
   is resolved at build.
4. **MINOR migration UX** (§5): how the webapp assists moving a customer's authored
   services across a breaking `MAJOR.MINOR` bump.
5. **Common-repo contents**: confirm the boundary of what is truly shared
   vocabulary vs. what should live in a producing service's repo.
6. **Migration from today's monolith**: how `platform/proto` is decomposed into
   per-service repos + a `common` repo without a flag-day.

---

## 10. tacplus work implied

Not scoped — captured so it isn't lost:

- **Multi-source resolver.** `tacplus build` must resolve N proto sources from a
  manifest/lockfile (fetch each repo at the latest patch of its pinned
  `MAJOR.MINOR`), lay them out so `import "agent-sensor/foo.proto"` resolves, and
  dedup `common`. Prior art: buf modules / BSR.
- **Per-repo, per-line field-id registry.** Reuse the DATA_ENTITLEMENT drift
  guard, scoped per repo and baselined per release line.
- **Breaking-change gate.** Enforce non-surface patches against the `.0` baseline
  of the line.
- **Compose-time uniqueness allocation.** Assign the §8 global-scoped values
  within a customer composition.
- **Customer-proto validation / isolation** in the composition stage (lint +
  breaking + registry checks before anything shared is touched).
