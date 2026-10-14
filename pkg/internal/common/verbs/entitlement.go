// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verbs

import (
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// This is the language-agnostic reference implementation of the generated
// entitlement-grant helper (archive/RPC_PATTERNS_AND_VERBS_PLAN.md §5.1). Tacplus generates
// an equivalent helper in every full language plus the grant data in
// registry.json; this Go reference defines the contract those generators must
// match and is exercised by entitlement_test.go.
//
// A Grant is one of three shapes, mirroring the compressed EntitlementGrant
// model that sits alongside the existing per-service <Svc>RPCs enum:
//   - exact:          a single RPC (package+service+rpc)            e.g. anthropic.Anthropic.CreateMessage
//   - package wildcard: every RPC in a package (ScopeAny)            e.g. ANTHROPIC_ANY
//   - capability wildcard: every RPC in a package admitted by a
//     capability (READ/WRITE/WATCH/PAGE/AGENT/<AI-verb>)             e.g. ANTHROPIC_READ_ANY
//
// Wildcards are convenience grants for policy/config/operator/UI input; runtime
// authorizers normalize them to the explicit exact-grant set and authorize by
// intersection with the call's MethodGrant. ANY is never runtime magic.

// MethodMeta is the immutable, generated classification a Grant helper needs for
// one RPC. It mirrors the registry RpcInfo fields: Package, the RPC service name,
// the RPC name, the admitting entitlement scopes (broad→narrow, from
// Classification.ScopesAdmitting), and the primary capability bucket.
type MethodMeta struct {
	Package    string
	Service    string
	RPC        string
	Scopes     []Scope // entitlement_scopes: the grant scopes that admit this method
	Capability Scope   // capability_scope: primary package capability bucket
}

// Grant is a single entitlement grant: an exact RPC, a package wildcard, or a
// capability wildcard. Exactly one shape is populated.
type Grant struct {
	Package    string // always set
	Service    string // set for exact grants
	RPC        string // set for exact grants
	Capability Scope  // set for wildcards; ScopeAny == package-wide "any RPC"
}

// IsExact reports whether g names a single RPC (vs a wildcard).
func (g Grant) IsExact() bool { return g.RPC != "" }

// IsWildcard reports whether g is a package/capability wildcard.
func (g Grant) IsWildcard() bool { return g.RPC == "" && g.Capability != "" }

// key is a stable comparable identity used for dedup/sort/containment.
func (g Grant) key() string {
	if g.IsExact() {
		return "rpc:" + g.Package + "." + g.Service + "." + g.RPC
	}
	return "cap:" + g.Package + "." + string(g.Capability)
}

// MethodGrant returns the exact grant for m — the token a runtime authorizer
// matches against. This is the only grant shape used for runtime authorization.
func MethodGrant(m MethodMeta) Grant {
	return Grant{Package: m.Package, Service: m.Service, RPC: m.RPC}
}

// CandidateGrants returns every grant that would authorize m: the exact grant
// plus each package/capability wildcard implied by m's admitting scopes. Used by
// operator/UI tooling and by ContainsAll before normalization.
func CandidateGrants(m MethodMeta) []Grant {
	out := []Grant{MethodGrant(m)}
	for _, s := range m.Scopes {
		out = append(out, Grant{Package: m.Package, Capability: s})
	}
	return out
}

// ExpandGrant expands a wildcard grant into the exact RPC grants it implies,
// using the generated method metadata. An exact grant returns itself (if it
// matches a known method). A package wildcard (ScopeAny) returns every RPC in the
// package; a capability wildcard returns every RPC in the package whose admitting
// scopes include that capability.
func ExpandGrant(g Grant, methods []MethodMeta) []Grant {
	if g.IsExact() {
		for _, m := range methods {
			if m.Package == g.Package && m.Service == g.Service && m.RPC == g.RPC {
				return []Grant{MethodGrant(m)}
			}
		}
		return nil
	}
	var out []Grant
	for _, m := range methods {
		if m.Package != g.Package {
			continue
		}
		if g.Capability == ScopeAny || scopesContain(m.Scopes, g.Capability) {
			out = append(out, MethodGrant(m))
		}
	}
	return out
}

// NormalizeGrants expands every grant to explicit exact grants, removes
// duplicates, and returns a stable sorted set. This is what a runtime authorizer
// should compute once and then authorize against by intersection.
func NormalizeGrants(grants []Grant, methods []MethodMeta) []Grant {
	seen := make(map[string]Grant)
	for _, g := range grants {
		for _, e := range ExpandGrant(g, methods) {
			seen[e.key()] = e
		}
	}
	out := make([]Grant, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// ContainsAll reports whether the normalized explicit grants of candidate are a
// subset of those of ceiling — i.e. ceiling grants everything candidate does.
// Used to validate cred-pair/tenant ceiling relationships without reflection
// over per-service grant fields.
func ContainsAll(candidate, ceiling []Grant, methods []MethodMeta) bool {
	cap := make(map[string]bool)
	for _, g := range NormalizeGrants(ceiling, methods) {
		cap[g.key()] = true
	}
	for _, g := range NormalizeGrants(candidate, methods) {
		if !cap[g.key()] {
			return false
		}
	}
	return true
}

// GrantExplanation is the display/explain payload for a grant (operator/webapp
// "granting X grants these methods" surfaces).
type GrantExplanation struct {
	Grant          Grant
	Package        string
	Service        string // exact only
	RPC            string // exact only
	Capabilities   []Scope
	ImpliedMethods []Grant // for wildcards: the exact RPC grants implied
}

// ExplainGrant returns a human-facing explanation of a grant, including the exact
// methods a wildcard implies.
func ExplainGrant(g Grant, methods []MethodMeta) GrantExplanation {
	exp := GrantExplanation{Grant: g, Package: g.Package, Service: g.Service, RPC: g.RPC}
	if g.IsWildcard() {
		exp.Capabilities = []Scope{g.Capability}
		exp.ImpliedMethods = ExpandGrant(g, methods)
	} else if g.IsExact() {
		for _, m := range methods {
			if m.Package == g.Package && m.Service == g.Service && m.RPC == g.RPC {
				exp.Capabilities = append([]Scope(nil), m.Scopes...)
				break
			}
		}
	}
	return exp
}

// BuildPackageGrants returns the distinct grant set for one package: every
// exact RPC grant plus every capability wildcard actually present among the
// package's methods (always including the package-wide ANY when the package has
// any method). This is the per-package EntitlementGrant value set that registry
// emission and the per-language generators enumerate. methods must all belong to
// the same package; results are stable-sorted by canonical token.
func BuildPackageGrants(methods []MethodMeta) []Grant {
	seen := make(map[string]Grant)
	caps := make(map[Scope]bool)
	for _, m := range methods {
		g := MethodGrant(m)
		seen[g.key()] = g
		for _, s := range m.Scopes {
			caps[s] = true
		}
	}
	for s := range caps {
		w := Grant{Package: pkgOf(methods), Capability: s}
		seen[w.key()] = w
	}
	out := make([]Grant, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return CanonicalGrantToken(out[i]) < CanonicalGrantToken(out[j]) })
	return out
}

// pkgOf returns the package of the first method (BuildPackageGrants requires a
// single-package input), or "" for an empty set.
func pkgOf(methods []MethodMeta) string {
	if len(methods) == 0 {
		return ""
	}
	return methods[0].Package
}

func scopesContain(scopes []Scope, want Scope) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// CanonicalGrantToken renders a Grant as the flattened upper-snake token used by
// the generated EntitlementGrant enum (e.g. ANTHROPIC_READ_ANY,
// ANTHROPIC_ANTHROPIC_CREATE_MESSAGE). Kept here so the reference and the
// per-language generators agree on the wire form.
func CanonicalGrantToken(g Grant) string {
	pkg := strings.ToUpper(toSnake(g.Package))
	if g.IsExact() {
		return pkg + "_" + strings.ToUpper(toSnake(g.Service)) + "_" + strings.ToUpper(toSnake(g.RPC))
	}
	if g.Capability == ScopeAny {
		return pkg + "_ANY"
	}
	return pkg + "_" + string(g.Capability) + "_ANY"
}

func canonicalGrantToken(g Grant) string {
	return CanonicalGrantToken(g)
}

// toSnake converts a PascalCase/camelCase identifier to snake_case for token rendering.
func toSnake(s string) string {
	return strutil.ToSnakeCase(strings.NewReplacer("-", "_", ".", "_").Replace(s))
}
