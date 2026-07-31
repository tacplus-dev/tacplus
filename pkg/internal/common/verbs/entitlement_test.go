// Copyright (c) 2026 Second Star Cavalier LLC

package verbs

import "testing"

func entMethods() []MethodMeta {
	return []MethodMeta{
		{Package: "kv", Service: "Store", RPC: "GetItem", Scopes: []Scope{ScopeAny, ScopeRead}, Capability: ScopeRead},
		{Package: "kv", Service: "Store", RPC: "CreateItem", Scopes: []Scope{ScopeAny, ScopeWrite}, Capability: ScopeWrite},
		{Package: "kv", Service: "Store", RPC: "WatchItems", Scopes: []Scope{ScopeAny, ScopeWatch}, Capability: ScopeWatch},
		{Package: "other", Service: "Svc", RPC: "Ping", Scopes: []Scope{ScopeAny, ScopeRead}, Capability: ScopeRead},
	}
}

func grantKeys(gs []Grant) map[string]bool {
	m := make(map[string]bool, len(gs))
	for _, g := range gs {
		m[g.key()] = true
	}
	return m
}

func TestMethodGrantAndCandidates(t *testing.T) {
	get := entMethods()[0]
	if g := MethodGrant(get); !g.IsExact() || g.RPC != "GetItem" {
		t.Fatalf("MethodGrant = %#v, want exact GetItem", g)
	}
	cand := grantKeys(CandidateGrants(get))
	for _, want := range []Grant{
		{Package: "kv", Service: "Store", RPC: "GetItem"},
		{Package: "kv", Capability: ScopeAny},
		{Package: "kv", Capability: ScopeRead},
	} {
		if !cand[want.key()] {
			t.Errorf("CandidateGrants missing %s", want.key())
		}
	}
}

func TestExpandGrant(t *testing.T) {
	methods := entMethods()

	// READ_ANY is bounded reads only → GetItem, NOT WatchItems (WATCH is a
	// separate grant) and NOT CreateItem (WRITE).
	readExp := grantKeys(ExpandGrant(Grant{Package: "kv", Capability: ScopeRead}, methods))
	if !readExp[(Grant{Package: "kv", Service: "Store", RPC: "GetItem"}).key()] {
		t.Errorf("READ wildcard should imply GetItem, got %v", readExp)
	}
	if readExp[(Grant{Package: "kv", Service: "Store", RPC: "WatchItems"}).key()] {
		t.Errorf("READ wildcard must NOT imply WatchItems (WATCH is a separate grant)")
	}
	if readExp[(Grant{Package: "kv", Service: "Store", RPC: "CreateItem"}).key()] {
		t.Errorf("READ wildcard must NOT imply CreateItem")
	}
	// WATCH_ANY → WatchItems only.
	watchExp := grantKeys(ExpandGrant(Grant{Package: "kv", Capability: ScopeWatch}, methods))
	if !watchExp[(Grant{Package: "kv", Service: "Store", RPC: "WatchItems"}).key()] || len(watchExp) != 1 {
		t.Errorf("WATCH wildcard should imply only WatchItems, got %v", watchExp)
	}

	// Package wildcard ANY → all 3 kv RPCs, none from other package.
	anyExp := ExpandGrant(Grant{Package: "kv", Capability: ScopeAny}, methods)
	if len(anyExp) != 3 {
		t.Errorf("kv ANY should imply 3 RPCs, got %d", len(anyExp))
	}

	// Exact grant → itself.
	exact := ExpandGrant(Grant{Package: "kv", Service: "Store", RPC: "GetItem"}, methods)
	if len(exact) != 1 || exact[0].RPC != "GetItem" {
		t.Errorf("exact expand = %#v, want [GetItem]", exact)
	}
	// Unknown exact → nothing.
	if got := ExpandGrant(Grant{Package: "kv", Service: "Store", RPC: "Nope"}, methods); got != nil {
		t.Errorf("unknown exact should expand to nil, got %v", got)
	}
}

func TestNormalizeAndContainsAll(t *testing.T) {
	methods := entMethods()

	// Dedup: READ_ANY expands to GetItem only; + explicit GetItem → GetItem once.
	norm := NormalizeGrants([]Grant{
		{Package: "kv", Capability: ScopeRead},
		{Package: "kv", Service: "Store", RPC: "GetItem"},
	}, methods)
	if len(norm) != 1 {
		t.Errorf("normalize should dedup to 1 (GetItem), got %d: %v", len(norm), norm)
	}

	// READ ceiling contains GetItem but not CreateItem.
	ceiling := []Grant{{Package: "kv", Capability: ScopeRead}}
	if !ContainsAll([]Grant{{Package: "kv", Service: "Store", RPC: "GetItem"}}, ceiling, methods) {
		t.Errorf("READ ceiling should contain GetItem")
	}
	if ContainsAll([]Grant{{Package: "kv", Service: "Store", RPC: "CreateItem"}}, ceiling, methods) {
		t.Errorf("READ ceiling must NOT contain CreateItem")
	}
	// ANY ceiling contains everything in package.
	anyCeil := []Grant{{Package: "kv", Capability: ScopeAny}}
	if !ContainsAll([]Grant{{Package: "kv", Service: "Store", RPC: "CreateItem"}}, anyCeil, methods) {
		t.Errorf("ANY ceiling should contain CreateItem")
	}
}

func TestBuildPackageGrants(t *testing.T) {
	kv := entMethods()[:3] // the three kv methods
	grants := BuildPackageGrants(kv)
	got := make(map[string]bool)
	for _, g := range grants {
		got[canonicalGrantToken(g)] = true
	}
	// 3 exact + package ANY + READ/WRITE/WATCH capability wildcards.
	for _, want := range []string{
		"KV_STORE_GET_ITEM", "KV_STORE_CREATE_ITEM", "KV_STORE_WATCH_ITEMS",
		"KV_ANY", "KV_READ_ANY", "KV_WRITE_ANY", "KV_WATCH_ANY",
	} {
		if !got[want] {
			t.Errorf("BuildPackageGrants missing %s; got %v", want, got)
		}
	}
	// No PAGE/AGENT wildcard since no kv method admits them.
	if got["KV_PAGE_ANY"] || got["KV_AGENT_ANY"] {
		t.Errorf("BuildPackageGrants must not emit wildcards for absent capabilities")
	}
}

func TestExplainGrant(t *testing.T) {
	methods := entMethods()
	exp := ExplainGrant(Grant{Package: "kv", Capability: ScopeWrite}, methods)
	if len(exp.ImpliedMethods) != 1 || exp.ImpliedMethods[0].RPC != "CreateItem" {
		t.Errorf("WRITE wildcard should imply only CreateItem, got %v", exp.ImpliedMethods)
	}
}

func TestCanonicalGrantToken(t *testing.T) {
	cases := map[string]Grant{
		"KV_STORE_GET_ITEM":                     {Package: "kv", Service: "Store", RPC: "GetItem"},
		"KV_ANY":                                {Package: "kv", Capability: ScopeAny},
		"KV_READ_ANY":                           {Package: "kv", Capability: ScopeRead},
		"OPENAI_OPEN_AI_SERVICE_GET_API_KEY":    {Package: "openai", Service: "OpenAIService", RPC: "GetAPIKey"},
		"OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS": {Package: "openai", Service: "OpenAIService", RPC: "WatchAPIKeys"},
	}
	for want, g := range cases {
		if got := CanonicalGrantToken(g); got != want {
			t.Errorf("CanonicalGrantToken(%#v) = %q, want %q", g, got, want)
		}
	}
}
