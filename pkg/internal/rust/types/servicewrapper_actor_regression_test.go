// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// Item 6 Piece A: the `act` (actor) claim resolution. rustActorResolve
// mirrors rustPrincipalResolve but is Option-returning — an actor-less
// (ordinary, non-delegated) call is NOT an error, unlike a principal-less
// call, which the RPC's (spec.principal) requirement treats as
// UNAUTHENTICATED. These tests confirm that shape difference and that the
// (spec.store_principal) read-filter path never gains a dependency on the
// actor claim.

// Positive: when rustActorResolve renders, it reads Token::actor() (the
// generated accessor added alongside CLAIM_ACT in the Rust token template)
// and produces an Option<String> local — no unwrap, no ok_or_else, no
// tonic::Status error path. This is the "stamped" side of the case: the
// local is populated whenever the caller's token carries `act`.
func TestRustActorResolveBindsOptionFromActorClaim(t *testing.T) {
	rendered := rustActorResolve("__actor", "SomeMethod", "        ")

	if !strings.Contains(rendered, "let __actor = caller.as_ref()") {
		t.Fatalf("rustActorResolve did not bind the expected local:\n%s", rendered)
	}
	if !strings.Contains(rendered, ".and_then(|c| c.token().actor())") {
		t.Fatalf("rustActorResolve did not read Token::actor():\n%s", rendered)
	}
	// Must not end in an unwrap/error path — the trailing token is `;`, not a
	// `?` after `.ok_or_else(...)` the way rustPrincipalResolve's is.
	if strings.Contains(rendered, "ok_or_else") || strings.Contains(rendered, "unauthenticated") {
		t.Fatalf("rustActorResolve must never hard-error on an absent actor:\n%s", rendered)
	}
	if !strings.HasSuffix(strings.TrimRight(rendered, "\n"), ".map(|s| s.to_string());") {
		t.Fatalf("rustActorResolve must bind an Option<String>, not unwrap it:\n%s", rendered)
	}
}

// Absent: rustActorResolve's rendered shape is identical whether or not the
// token in fact carries `act` at runtime — that's the whole point of an
// Option local instead of a stamp-or-refuse block. This test pins that the
// generator itself never branches on presence (there is no "else stamp
// empty" path to omit); the None case is handled entirely by the standard
// library Option chain at runtime, not by codegen branching.
func TestRustActorResolveHasNoBranchOnAbsence(t *testing.T) {
	rendered := rustActorResolve("__actor", "SomeMethod", "        ")
	if strings.Contains(rendered, "if ") || strings.Contains(rendered, "else") {
		t.Fatalf("rustActorResolve should be a single Option-chain expression, not a branch:\n%s", rendered)
	}
}

// Negative space: rustResponsePrincipalFilterBlock is the (spec.store_principal)
// read filter — "the actor does not appear in the read filter" (the
// acceptance sentence's own words) must be true BY CONSTRUCTION, not by
// convention. No code path building this function's output may reference
// actor()/__actor.
func TestRustResponsePrincipalFilterBlockNeverReferencesActor(t *testing.T) {
	fields := []rustPrincipalListField{
		{Field: "tasks", ItemField: "created_by"},
		{Field: "widgets", ItemField: "owner"},
	}
	rendered := rustResponsePrincipalFilterBlock(fields, "ListTasks")

	for _, forbidden := range []string{"actor", "__actor", "act()", "CLAIM_ACT"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("rustResponsePrincipalFilterBlock output must never mention %q (the actor must never appear in the read filter):\n%s", forbidden, rendered)
		}
	}
	// Sanity: the function still does its one real job — filters by the
	// caller's resolved *subject*, never the actor.
	if !strings.Contains(rendered, "__read_principal") {
		t.Fatalf("rustResponsePrincipalFilterBlock lost its principal-filter binding:\n%s", rendered)
	}
	for _, f := range fields {
		want := "resp." + f.Field + ".retain(|__row| __row." + f.ItemField + " == __read_principal);"
		if !strings.Contains(rendered, want) {
			t.Fatalf("rustResponsePrincipalFilterBlock missing expected retain call %q:\n%s", want, rendered)
		}
	}
}

// Also pin: rustPrincipalResolve (unchanged) is still the hard-error subject
// resolution used by the store_principal filter and the (spec.principal)
// stamp-or-refuse block — confirms the two resolvers really do differ in
// exactly the one way this feature requires (error-on-absence vs. Option).
func TestRustPrincipalResolveStillHardErrorsOnAbsence(t *testing.T) {
	rendered := rustPrincipalResolve("__principal", "SomeMethod", "        ")
	if !strings.Contains(rendered, "ok_or_else") || !strings.Contains(rendered, "unauthenticated") {
		t.Fatalf("rustPrincipalResolve must still hard-error on an absent principal:\n%s", rendered)
	}
}

// The accounting identity map (Authorizer::identity_claims, the one real,
// live per-RPC place the Rust wrapper already extracts a fixed claim set
// off the caller's verified token — sub/aud/iss/azp) must also surface
// `act`, mirroring azp exactly, so an accounted RPC's audit record carries
// the delegated-mint actor when the caller's token has one.
func TestRustAuthorizerIdentityClaimsIncludesAct(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/grpcserver/src/authorizer.rs.tmpl")

	fnStart := strings.Index(tmpl, "pub fn identity_claims")
	if fnStart < 0 {
		t.Fatal("authorizer.rs.tmpl: identity_claims function not found")
	}
	fnEnd := strings.Index(tmpl[fnStart:], "\n    }")
	if fnEnd < 0 {
		t.Fatal("authorizer.rs.tmpl: could not find end of identity_claims function")
	}
	body := tmpl[fnStart : fnStart+fnEnd]

	if !strings.Contains(body, `("act", tw.actor())`) {
		t.Fatalf("identity_claims must resolve \"act\" from tw.actor(), mirroring azp:\n%s", body)
	}
	if !strings.Contains(body, `("azp", tw.azp())`) {
		t.Fatalf("test fixture assumption broken: azp entry not found in identity_claims:\n%s", body)
	}
}
