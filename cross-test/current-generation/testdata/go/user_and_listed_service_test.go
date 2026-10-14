// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress fixture. NOT generated: run.sh copies this
// file into the generated chassis/grpcserver package before `go test ./...`. It
// lives IN that package to reuse the generated authorizer_behavior_test.go
// helpers (newTestKey, jwkForKey, signedToken, bearerCtx, authorizerWithKeys),
// and in the fixture because the user leg needs the fixture's REAL generated
// grant catalog — the Authorizer drops any grant the catalog does not know.
//
// What it pins: one c2s rpc callable by entitled USERS and by the SERVICE
// identities its (spec.rpc).auth_services lists, on the same surface.
// egressvault.EgressVaultApi/CreateVaultNote declares
//   {principal: RPC_PRINCIPAL_USER, flags: [RPC_FLAG_AUTH_ENTITLED_USERS],
//    auth_services: ["notary"]}
// and run.sh asserts the generated wrapper authorizes it with fullMethod and
// registers exactly the SetMethodAuth line this test repeats. Then, through the
// real Authorizer over real signed tokens:
//   - a user token holding the rpc's grant passes,
//   - a tenant-owned "notary" service token passes, and the verified token it
//     returns is what the wrapper builds Caller from — the caller's SPIFFE
//     subject and its tenant namespace, never a client-chosen value,
//   - a service not listed is refused,
//   - a user token without the grant is still refused.

package grpcserver

import (
	"testing"

	tokenlib "gitlab.com/example/dummy/egressvault/tacplus/model/chassis/token"
	entitlementspb "gitlab.com/example/dummy/egressvault/tacplus/model/types/generated/entitlements"
)

const (
	// The full method the generated EgressVaultApi wrapper passes to authorize.
	createVaultNoteFullMethod = "/services/egressvault.EgressVaultApi/CreateVaultNote"
	// The generated exact grant for the rpc (grants catalog token).
	createVaultNoteGrant = "EGRESSVAULT_EGRESS_VAULT_API_CREATE_VAULT_NOTE"
	notaryID             = "spiffe://example.com/tenants/acme/services/notary"
	strangerID           = "spiffe://example.com/tenants/acme/services/stranger"
)

func userAndListedServiceAuthorizer(t *testing.T) (*authorizer, func(func(*tokenlib.TokenWrapper)) string) {
	t.Helper()
	key := newTestKey(t)
	a := authorizerWithKeys(t, jwkForKey(t, key))
	// Exactly the line the generated registerMethodAuth emits for this rpc.
	a.SetMethodAuth("egressvault.EgressVaultApi/CreateVaultNote", []string{"notary"})
	if got := qualifiedMethodKey(createVaultNoteFullMethod); got != "egressvault.EgressVaultApi/CreateVaultNote" {
		t.Fatalf("qualifiedMethodKey(%q) = %q", createVaultNoteFullMethod, got)
	}
	return a, func(customize func(*tokenlib.TokenWrapper)) string { return signedToken(t, key, customize) }
}

func userWithGrants(grants ...string) func(*tokenlib.TokenWrapper) {
	return func(tok *tokenlib.TokenWrapper) {
		tok.SetSubType("user").
			SetSubject("someone@example.com").
			SetServiceEntitlements(&entitlementspb.TokenEntitlements{Grants: grants})
	}
}

func TestUserAndListedService_EntitledUserPasses(t *testing.T) {
	a, sign := userAndListedServiceAuthorizer(t)
	if _, err := a.Authorize(bearerCtx(sign(userWithGrants(createVaultNoteGrant))), createVaultNoteFullMethod, nil); err != nil {
		t.Fatalf("a user holding the rpc's grant must pass: %v", err)
	}
}

func TestUserAndListedService_ListedServicePassesWithItsOwnIdentity(t *testing.T) {
	a, sign := userAndListedServiceAuthorizer(t)
	verified, err := a.Authorize(bearerCtx(sign(serviceToken(notaryID))), createVaultNoteFullMethod, nil)
	if err != nil {
		t.Fatalf("a service listed in auth_services must pass: %v", err)
	}
	if got := verified.GetSubject(); got != notaryID {
		t.Fatalf("subject = %q, want %q", got, notaryID)
	}
	if got := verified.GetNamespace(); got != "/tenants/acme" {
		t.Fatalf("namespace = %q, want /tenants/acme", got)
	}
	if got, err := verified.ServicePath(); err != nil || got != "notary" {
		t.Fatalf("service path = %q (%v), want notary", got, err)
	}
}

func TestUserAndListedService_UnlistedServiceIsRefused(t *testing.T) {
	a, sign := userAndListedServiceAuthorizer(t)
	if _, err := a.Authorize(bearerCtx(sign(serviceToken(strangerID))), createVaultNoteFullMethod, nil); err == nil {
		t.Fatal("a service not in auth_services must be refused")
	}
}

func TestUserAndListedService_UnentitledUserIsRefused(t *testing.T) {
	a, sign := userAndListedServiceAuthorizer(t)
	if _, err := a.Authorize(bearerCtx(sign(userWithGrants())), createVaultNoteFullMethod, nil); err == nil {
		t.Fatal("auth_services must not open the rpc to users without its grant")
	}
}
