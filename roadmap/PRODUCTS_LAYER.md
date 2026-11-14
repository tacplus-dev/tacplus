# Product layer: `/products/<name>` (design, #475)

Status: **approved** (Jody, 2026-11-09, on #475), with the answers in [Decisions](#decisions). Not implemented yet:
the implementation follows the [order below](#implementation-order), after #258 lands. Decision: tacplus-dev/roadmap
`decisions/products-layer-2026-11-08.md` (Jody, 2026-11-08). Builds on #258 (S2S never crosses tenants, landing) and
#284 (the token's `tid` claim, landed).

The decision settles *what*: `/system` is tacplus.dev's platform only; a fleet's product code runs at the trust-domain
path `/products/<name>`, between `/system` and the tenants; five S2S edges fixed at build time; tenant data there only as
a custodian keyed by the token's tid. This document settles *how*, in the order the issue asks, and ends with
Jody's answers to the questions it asked ([Decisions](#decisions)).

## The three layers, one table

Every principal is in exactly one layer, read from its verified token, never from a request:

| Layer | Service subject | Namespace claim | `tid` claim |
|---|---|---|---|
| platform | `spiffe://<td>/system/…/services/<svc>` | `/system` or below | none |
| product | `spiffe://<td>/products/<name>/services/<svc>` | `/products/<name>` | none |
| tenant | `spiffe://<td>/tenants/<tid>/services/<svc>` | `/tenants/<tenant-name>` | `<tid>` |

S2S edges (caller → target), the decision's table with the two edges it implies but does not list:

| Caller ↓ / target → | `/system` | `/products/<p>` | `/products/<q>` (q ≠ p) | tenant `<t>` |
|---|---|---|---|---|
| `/system` | allowed | **refused** | **refused** | allowed, not a pattern (#258) |
| `/products/<p>` | allowed | allowed *(same product, implied)* | **refused** | **refused** |
| tenant `<t>` | allowed | allowed | allowed | own tenant only (#258) |

The rules are about **service principals**. User traffic (C2S, including a service acting on a user's behalf with a
delegated token whose subject is the user) is unchanged: its door is entitlements, as today.

## 1. Declaration

**Proposal: the proto declares it, the fleet build checks it.** One new field on the existing service annotation:

```protobuf
option (spec.service) = {name: "billing", product: "ledger"};
```

`(spec.service).product` (`string`, field 10 of `ServiceAnnotation`) says *whose code this is*: the service runs at
`/products/ledger`, once per fleet, like a platform service (one shared instance set, never per tenant). Why the proto
and not `tacplus.yaml` or `fleet.yaml`:

- Everything the edges depend on is already in the proto (`(spec.grpc_s2s_client)`, `(spec.record_read)`,
  `state_import`, `tenant_owned`), so the compiler can refuse a forbidden edge in the service's **own** build, not first
  in a fleet. `tacplus.yaml` holds generation facts that do not belong in a proto (THEORY.md); a layer is identity.
- The generated code must know its layer to enforce it (section 3); a proto fact reaches every generated language the
  same way `tenant_owned` does.
- A fleet cannot move a service between layers by configuration. Moving one is a proto change under review, which is the
  same trust boundary as `tenant_owned` and `accepted_audiences`.

`fleet.yaml` changes nothing: a product service is listed under `services:` like any other. The fleet build derives the
fleet's products from the merged protos and prints them in `fleet.lock` (`products: [ledger]`, each service's
`product:`), so a lock diff shows a product appearing.

**Name.** `[a-z][a-z0-9-]{0,30}[a-z0-9]`, and refused when it has the shape of a tid (exactly 8 of `[0-9a-z]`): the
operator's namespace for it is `tac-<deployment>-<name>`, the same shape as a tenant's `tac-<deployment>-<tid>`, so a
product named like a tid would collide with a tenant's namespace. `system`, `tenants`, `products` and `tacplus` are
reserved.

**Checks** (a new `parser.ValidateProductLayer`, run by `tacplus build` in `pkg/builder` and by the whole-fleet checks in
`pkg/fleet/fleet.go` `validateFleet`):

1. `product` and `tenant_owned` are exclusive (and so `pinned_tenant`). A sys contract (`pkg/proto/sys`) never declares
   `product`: a tacplus release ships no product code.
2. Every edge a service declares is checked against the table above. An edge is any of: `(spec.grpc_s2s_client)`
   (including a hosted label/trait surface, resolved to its host), `(spec.record_read)` (the S2S half it implies),
   a `state_import` / file-level `state_imports` root, and an `(spec.service_s2s)` caller list
   (`auth_services`, `auth_services_from_fleet`). A refused edge fails generation, naming the caller, the target and the
   rule (`product "ledger" → tenant-owned "crew": a product never calls a tenant (products-layer-2026-11-08)`).
3. `all_grpc_s2s_clients` on a product service generates clients for `/system` services and its own product's only; on a
   `/system` service it leaves product services out. It never generates a client for a refused edge.
4. The fleet build checks the same rules over the merged protos (a service's own build only sees its `depends`), and
   that a product service's registry entry agrees (section 4).

## 2. Identity

**Kernel (an issue on tacplus-dev/kernel).** For a service the operator deploys as a product's, the kernel mints a
service token and SVID with subject `spiffe://<td>/products/<name>/services/<svc>` and namespace claim
`/products/<name>`, and **no `tid` claim**. `aud` is unchanged (the target service's name, plus what it hosts), so
audiences need no product segment: who the caller is, is the subject and namespace; whether it may call is the door
below. The kernel refuses to mint a product token for a name of tid shape or a reserved name. The kernel learns a
service's layer by the same path it learns `tenant_owned` today (`MintServiceToken`'s Kubernetes attestation; the
kernel issue settles the details): a workload the operator deployed as product `<name>`, in `tac-<deployment>-<name>`,
gets that identity, and nothing else does.

**Operator (an issue on tacplus-dev/operator).** One namespace per product, `tac-<deployment>-<name>`, holding every
service of that product; created when the fleet's first service of the product is deployed, deleted with the last. Its
services read `(spec.service).product` from the merged protos, as they read `tenant_owned`. The rules stay about
principals: the namespace is where the pods run.

tacplus itself generates nothing new for identity: the token wrappers already expose subject, namespace and `tid`
(`GetTid` / `getTid` / `tid()`, #284). It adds one generated helper per language, `CallerLayer` (Go), `Caller.layer()`
(Java, Rust), returning `SYSTEM`, `PRODUCT(name)`, `TENANT(tid)` or an error, parsed from the **namespace claim** and
cross-checked against the subject for a service principal (both must name the same layer, or `PERMISSION_DENIED`). Every
door below uses that one helper, so the three languages cannot drift on parsing. Whether a caller is a service or a user
principal is #258's test, the token's `sub_type` (`service` or `workload` is a service principal), reused as is; main does not
tell them apart at the door until #258 lands.

**The SPIFFE grammar learns the root.** Today `/products/…` does not parse in any generated language: the namespace roots
are only `system`, `tenants` and `users`, and a service root only `system` or `tenants` (Go `spiffe.go.tmpl`
`NormalizeNamespace`/`classify`, Java `Spiffe.java.tmpl`, Rust `spiffe.rs.tmpl`; the compiler's own
`pkg/internal/common/spiffe/grammar.go` reserved segments). `products` becomes a third service root, with
`/products/<name>/services/<svc>[/workloads/…]` and the name rule above, and the shared vectors in
`cross-test/fixtures/spiffe_vectors.json` gain its cases, so the three parsers agree by test.

**Every "not `/system`, so a tenant" assumption becomes explicit.** These generated checks read a caller as tenant-held
when its namespace is not under `/system`, and would misread a product caller:

| Where (Go; Java and Rust mirror each) | Today | Product caller |
|---|---|---|
| record write pin, `recordwrite_gen.go` | stamp/refuse unless `/system` | refused (a product never writes a tenant's records: product → tenant) |
| record-plane S2S read pin, `recordegress_gen.go` / `recordnamespace_gen.go` / `namespace_gen.go` | pin unless `/system` | refused, same reason |
| record C2S reader, `record_c2s.go.tmpl` | the caller's own tenant | unchanged: C2S users only |
| MCP session tenant, `mcp_dispatch_gen.go` | tenant from the token | refused, as `/system` is (no session in a tenant) |
| tenant-pinned config read, `tenantpin_gen.go` | `/system` all, `/tenants/<t>` own, else refused | refused (decision 4 below) |
| member port identity, `MemberPort.java.tmpl` | `tenantOwned ? own : "/system"` | the product's own namespace |

Each becomes a three-way test on `CallerLayer`, never a prefix test on `/system`, and each gets a refusing test for a
product caller. The implementation greps the generators for every `"/system"` and `"/tenants/"` literal (the list above
is from that grep on 7b690e0b) so none is missed.

## 3. Enforcement at every door

All in the generated Go, Java and Rust; no handler code involved. Every refusal is `PERMISSION_DENIED` before the
handler runs.

**The server door.** Each generated server already reads its own identity at startup (`SetHostIdentity` /
`set_host_identity` / `setHostIdentity`, #202) and refuses to start without it. The door gains the product rows:

| Instance | Service principal admitted | Change |
|---|---|---|
| tenant `<t>` | own tenant, `/system` | refuse `/products/*` explicitly, `RPC_FLAG_ANY_TENANT` included (it opens to users only, #258) |
| `/products/<p>` (new) | any tenant, own product `<p>` | refuse `/system` and every other product |
| `/system` | any | refuse nothing new: product → `/system` is allowed |

Today (Go: `checkHostTenant` in `pkg/templates/go/chassis/grpcserver/authorizer.go.tmpl`, mirrored in `java/…/Authorizer.java.tmpl` and
`rust/…/authorizer.rs.tmpl`) a
tenant-owned instance admits a caller at-or-above its tenant or at `/system` or below, so a `/products/*` service is
already refused by default, but **any** caller passes on an `RPC_FLAG_ANY_TENANT` rpc. #258 narrows that flag to user
principals for other tenants; the product row extends the same narrowing to product principals, and gets its own test so
it does not rest on #258's by accident. A shared (`/system`) instance has no layer door today; a product instance gets
one, set from its own identity at startup in the same `SetHostNamespace` / `SetHostIdentity` path.

Watches and subscriptions are opened by the subscriber (a product watching kernel state or a topic is product →
`/system`), so refusing `/system` callers at a product's door breaks no delivery path the docs describe; the
implementation confirms it against the eventbus and state host, and stops for a ruling if a platform service turns out to
dial a subscriber.

A product instance refuses to start if its own namespace claim is not `/products/<name>` for the `product` its proto
declares. User principals pass the layer door as today and meet entitlements.

**The S2S client.** The generated client checks before sending, with the caller's own token (the same place #258
point 2 stamps a tenant caller's tid): a product caller refuses a tenant-owned target and another product's service; a
`/system` caller refuses a product's service. Build-time checks (section 1) mean a generated client for a refused edge
does not exist; this runtime check covers a client generated with one identity and deployed with another, and is what
the cross-tests drive.

**Custodian rows keyed by the token's tid.** A new field annotation, the same shape as `(spec.principal)` and
`(spec.namespace)` today:

```protobuf
message RecordUsageRequest {
  string tenant = 1 [(spec.caller_tid) = true];  // bound to the caller's token's tid, never the client's value
  int64 units = 2;
}
```

The generated wrapper sets the field to the caller's `tid` claim before the handler runs (an empty value is filled; a
non-empty value that differs is refused, like the MCP carrier); a caller with no `tid` claim
(`/system`, a product) is refused. So a handler that keys its storage by that field reaches only the caller's tenant's
rows. It is allowed on any rpc, and required, as a check, on a product service: every rpc of a product service that
names a tenant at all must name it through `caller_tid` (a plain `string tenant` field on a product service's request
fails generation). Users of a tenant calling a product service directly (C2S) carry the `tid` claim too, so the same
binding covers them.

**Deletion with the tenant.** A product service that declares any `caller_tid` field gets a generated, required handler
method, `OnTenantDeleted(tid)`, driven by a watch of the kernel's tenant records (product → `/system`, an allowed edge).
The generated runner calls it once per deleted tenant and retries until it returns OK; the implementer rule (USER.md) is
that it deletes every row that tid owns. Proposed for tenant-deletion-2026-10-27's table (Jody's to add): "a product's
custodian data: deleted by the product, on the kernel's deletion event."

**Cross-tests,** like #258, one leg per cell of the edge table above, in each server language (Go, Java, Rust), in a new
`cross-test/proto-productlayer`: a product instance, a tenant-owned instance and a `/system` instance; tokens minted
with each layer's subject, namespace and `tid` (the door's per-language unit cases go beside the tenant door's: the generated
`authorizer_behavior_test.go` and `AuthorizerBehaviorTest.java`, and Rust's tenant-door tests). Each refused cell is
asserted twice: at the door (a raw call with the caller's token) and at the client (the generated client refuses before
sending, and the server sees nothing). Plus: `caller_tid` refuses a forged tenant and a token without `tid`; and
`OnTenantDeleted` runs.

## 4. Registry

The registry already carries `visibility: public|private` per service (`tacplus registry register --visibility`; in
`registry.yaml` only, never `registry.json`, and today a note that nothing enforces), and a
fleet owner's private registry (`scope: private`, ids 1024-2047) exists for services no other fleet needs. A product
service is registered like any other; the proposal adds one field:

- `product: <name>` on a registry service entry. `tacplus registry register --product <name>`. The fleet build checks
  it equals the proto's `(spec.service).product`, so the registry, the proto and the lock agree on whose code it is.
- A product service in **tacplus-dev/registry** (the global one) must be `visibility: private` unless the product is
  open source; a private product's services normally live in the owner's private registry. No new visibility value.

## 5. Reads across tenants: nobody, in this release

The custodian rule means a product service's own code sees every tenant's rows (it stores them); the question is
whether anyone may ask it for a view **across** tenants, e.g. a product owner's billing view over per-tenant usage.

- **A. Nobody, in this release.** No rpc of a product service may return data of a tenant other than the caller's.
  Billing waits for its own design. Smallest, and nothing to undo.
- **B. The product's owner tenant.** `fleet.yaml` names, per product, the tenant that owns it
  (`products: [{name: ledger, owner_tenant: <tid>}]`). An rpc flagged `RPC_FLAG_ACROSS_TENANTS` on a product service
  admits only **users** of that tenant with an entitlement to it (C2S, so run-time policy within build-time
  entitlements, the same model as everything else), and its `caller_tid` binding is lifted on that rpc only. Every call
  is audited with the owner user's identity. No service principal ever reads across tenants.
- **C. `/system` operators.** Platform administrators read across tenants. Mixes the platform with product data, which
  the decision exists to prevent.

**Decided (Jody, 2026-11-09): A now.** B is its follow-up, a PROPOSALS.md entry, so the layer ships without settling
who a product's owner is. C is declined.

## Decisions

Jody, 2026-11-09, on #475 ("Approve 475"): the design is approved as proposed, with these answers.

1. **Declaration in the proto,** `(spec.service).product`, with no `fleet.yaml` change (section 1).
2. **Product names** may not have a tid's shape (8 of `[0-9a-z]`). The namespace stays `tac-<deployment>-<name>`.
3. **Within a product,** service → service of the same product is allowed.
4. **Tenant-config slices: no** in this release. A product's per-tenant settings go through `caller_tid` rpcs, and a
   product caller's tenant-pinned config read is refused.
5. **Reads across tenants: A** now (nobody). **B** (the product's owner tenant, users only, entitlement-gated, audited)
   is a PROPOSALS.md entry.
6. **Split:** once this design lands, the kernel and operator parts become issues on tacplus-dev/kernel and
   tacplus-dev/operator. #475's last criterion (an example product service in a fleet, called by a tenant service)
   waits on them.

## Implementation order

1. `(spec.service).product`, `(spec.caller_tid)`, `ValidateProductLayer` in the build and the fleet build; registry
   `product:`; `product` in `registry.json`'s service info. Tests first, per check.
2. The SPIFFE grammar's `products` root and its shared vectors; `CallerLayer`; the door rows and every former
   "not `/system`" check, Go, Java, Rust; the S2S client check; `caller_tid` binding; `OnTenantDeleted`.
3. `cross-test/proto-productlayer`, every cell, all three languages; in `just gates`.
4. USER.md (a "Product layer" section beside the tenant section), API.md, STATE.md.
5. Kernel and operator issues; the example product service (`example/`) and a fleet that runs it once they land.

Depends on #258 landing (the door it extends).
