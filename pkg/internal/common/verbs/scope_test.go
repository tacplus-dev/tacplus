// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verbs

import (
	"sort"
	"testing"
)

func scopeSet(c Classification) map[Scope]bool {
	m := map[Scope]bool{}
	for _, s := range c.ScopesAdmitting() {
		m[s] = true
	}
	return m
}

func mustClassify(t *testing.T, m MethodShape) Classification {
	t.Helper()
	c, err := Classify(m)
	if err != nil {
		t.Fatalf("Classify(%q): %v", m.Name, err)
	}
	return c
}

func TestScopesAdmitting(t *testing.T) {
	cases := []struct {
		shape MethodShape
		want  []Scope
	}{
		{MethodShape{Name: "GetUser"}, []Scope{ScopeAny, ScopeRead}},
		{MethodShape{Name: "ListEvents", CollectionMode: "SNAPSHOT"}, []Scope{ScopeAny, ScopeRead}},
		// PAGE/WATCH are their own grant classes, NOT also admitted by READ.
		{MethodShape{Name: "ListEvents", CollectionMode: "PAGE"}, []Scope{ScopeAny, ScopePage}},
		{MethodShape{Name: "WatchSessions", ServerStreaming: true, CollectionMode: "WATCH"}, []Scope{ScopeAny, ScopeWatch}},
		{MethodShape{Name: "CreateTenant"}, []Scope{ScopeAny, ScopeWrite}},
		{MethodShape{Name: "MintToken"}, []Scope{ScopeAny, ScopeWrite}},
		{MethodShape{Name: "Ping"}, []Scope{ScopeAny, ScopeRead}},
		// AGENT verbs are their own class, NOT also admitted by READ/WRITE.
		{MethodShape{Name: "GenerateCompletion"}, []Scope{ScopeAny, ScopeAgent, "GENERATE"}},
		{MethodShape{Name: "GuardPrompt"}, []Scope{ScopeAny, ScopeAgent, "GUARD"}},
		{MethodShape{Name: "EvaluatePolicy"}, []Scope{ScopeAny, ScopeAgent, "EVALUATE"}},
	}
	for _, c := range cases {
		cl := mustClassify(t, c.shape)
		got := scopeSet(cl)
		if len(got) != len(c.want) {
			t.Fatalf("%q scopes = %v, want %v", c.shape.Name, sortScopes(cl.ScopesAdmitting()), c.want)
		}
		for _, w := range c.want {
			if !got[w] {
				t.Fatalf("%q scopes missing %q (got %v)", c.shape.Name, w, sortScopes(cl.ScopesAdmitting()))
			}
		}
	}
}

func TestAdmits(t *testing.T) {
	guard := mustClassify(t, MethodShape{Name: "GuardPrompt"})
	// AGENT is its own grant class: a Guard call is admitted by ANY/AGENT/GUARD,
	// NOT by READ (AI ops require an AI grant — conservative default).
	if !guard.Admits(ScopeAgent) || !guard.Admits("GUARD") || !guard.Admits(ScopeAny) {
		t.Fatalf("Guard must be admitted by ANY/AGENT/GUARD")
	}
	if guard.Admits(ScopeRead) || guard.Admits("GENERATE") || guard.Admits(ScopeWrite) || guard.Admits(ScopePage) {
		t.Fatalf("Guard must NOT be admitted by READ/GENERATE/WRITE/PAGE")
	}

	// A WRITE create is not admitted by a READ grant.
	create := mustClassify(t, MethodShape{Name: "CreateTenant"})
	if create.Admits(ScopeRead) || create.Admits(ScopeWatch) {
		t.Fatalf("Create must not be admitted by READ/WATCH")
	}
	if !create.Admits(ScopeWrite) {
		t.Fatalf("Create must be admitted by WRITE")
	}
}

func TestCapabilityScope(t *testing.T) {
	cases := []struct {
		shape MethodShape
		want  Scope
	}{
		{MethodShape{Name: "GetUser"}, ScopeRead},
		{MethodShape{Name: "ListEvents", CollectionMode: "PAGE"}, ScopePage},
		{MethodShape{Name: "WatchSessions", ServerStreaming: true, CollectionMode: "WATCH"}, ScopeWatch},
		{MethodShape{Name: "CreateTenant"}, ScopeWrite},
		{MethodShape{Name: "GuardPrompt"}, ScopeAgent},
	}
	for _, c := range cases {
		got := mustClassify(t, c.shape).CapabilityScope()
		if got != c.want {
			t.Fatalf("%q capability = %q, want %q", c.shape.Name, got, c.want)
		}
	}
}

func TestRetryPolicy(t *testing.T) {
	cases := []struct {
		shape MethodShape
		want  RetryPolicy
	}{
		{MethodShape{Name: "GetUser"}, RetryReadOnly},
		{MethodShape{Name: "ListEvents", CollectionMode: "PAGE"}, RetryReadOnly},
		{MethodShape{Name: "WatchSessions", ServerStreaming: true, CollectionMode: "WATCH"}, RetryWatchReconnect},
		{MethodShape{Name: "CreateTenant"}, RetryIdempotencyRequired},
		{MethodShape{Name: "SendBlobs", ClientStreaming: true}, RetryNoGeneratedAutoRetry},
		{MethodShape{Name: "AssistChat", ClientStreaming: true, ServerStreaming: true}, RetryNoGeneratedAutoRetry},
	}
	for _, c := range cases {
		cl := mustClassify(t, c.shape)
		got := cl.RetryPolicy(c.shape.ClientStreaming, c.shape.ServerStreaming)
		if got != c.want {
			t.Fatalf("%q retry policy = %q, want %q", c.shape.Name, got, c.want)
		}
	}
}

func TestIsValidScope(t *testing.T) {
	for _, s := range []Scope{ScopeAny, ScopeRead, ScopeWrite, ScopeWatch, ScopePage, ScopeAgent, "GENERATE", "EMBED", "GUARD", "EVALUATE", "RANK", "CLASSIFY", "ASSIST"} {
		if !IsValidScope(s) {
			t.Fatalf("%q must be a valid scope", s)
		}
	}
	for _, s := range []Scope{"FROB", "GET", "CREATE", "MINT", "", "read"} {
		if IsValidScope(s) {
			t.Fatalf("%q must NOT be a valid scope (only base scopes + AI-verb scopes)", s)
		}
	}
}

func sortScopes(s []Scope) []Scope {
	out := append([]Scope{}, s...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
