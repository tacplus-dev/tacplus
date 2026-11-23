// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the generated sign-in config (tacplus#224). NOT generated:
// run.sh copies this file into the generated sys/signin package before
// `go test ./...`.
//
// What it pins: the generated validation of sys/signin.proto refuses a public
// suffix registered as a domain, a SAML IdP and an address-shaped subject, and
// accepts a whole valid configuration; the generated defaults fill the two
// 30-day durations.

package signin

import (
	"strings"
	"testing"
)

func validTenant() *Tenant {
	t := &Tenant{
		Providers: []Provider{Provider_PROVIDER_GOOGLE, Provider_PROVIDER_CUSTOM},
		Idp: &OidcIdp{
			Protocol:               IdpProtocol_IDP_PROTOCOL_OIDC,
			Issuer:                 "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0",
			ClientId:               "a1b2c3",
			MultiTenant:            true,
			IdpTenant:              "9188040d-6c67-4c5b-b112-36a304b66dad",
			SubjectClaim:           "oid",
			SubjectNonReassignable: true,
		},
		Domains:              []*Domain{{Name: "acme.co.uk", IncludeSubdomains: true, State: DomainState_DOMAIN_STATE_VERIFIED, VerifiedAt: 1}},
		JoinMode:             JoinMode_JOIN_MODE_AUTOMATIC,
		MaxProofAgeSeconds:   43200,
		DnsLapseGraceSeconds: 604800,
	}
	TenantDefaults(t)
	return t
}

func TestSignInDefaultsAreThirtyDays(t *testing.T) {
	tn := validTenant()
	if tn.MergeRedirectSeconds != 30*86400 || tn.RegistrationGraceSeconds != 30*86400 {
		t.Fatalf("defaults: merge %d, grace %d", tn.MergeRedirectSeconds, tn.RegistrationGraceSeconds)
	}
}

func TestSignInValidTenantAccepted(t *testing.T) {
	if err := ValidateTenant(validTenant()); err != nil {
		t.Fatalf("a valid tenant was refused: %v", err)
	}
	if err := ValidateDeployment(&Deployment{AccountModel: AccountModel_ACCOUNT_MODEL_SINGLE}); err != nil {
		t.Fatalf("a valid deployment was refused: %v", err)
	}
	if err := ValidateDeployment(&Deployment{}); err == nil {
		t.Fatal("a deployment without an account model was accepted")
	}
}

func TestSignInRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Tenant)
		want   string
	}{
		{"public suffix", func(tn *Tenant) { tn.Domains[0].Name = "co.uk" }, "registrable"},
		{"private-section public suffix", func(tn *Tenant) { tn.Domains[0].Name = "github.io" }, "registrable"},
		{"SAML IdP", func(tn *Tenant) { tn.Idp.Protocol = IdpProtocol_IDP_PROTOCOL_SAML }, "IDP_PROTOCOL_SAML"},
		{"reassignable subject", func(tn *Tenant) { tn.Idp.SubjectNonReassignable = false }, "SubjectNonReassignable"},
		{"multi-tenant IdP without its tenant", func(tn *Tenant) { tn.Idp.IdpTenant = "" }, "IdpTenant"},
		{"http issuer", func(tn *Tenant) { tn.Idp.Issuer = "http://idp.example.com" }, "Issuer"},
		{"no provider", func(tn *Tenant) { tn.Providers = nil }, "Providers"},
		{"no proof age", func(tn *Tenant) { tn.MaxProofAgeSeconds = 0 }, "MaxProofAgeSeconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tn := validTenant()
			tc.mutate(tn)
			err := ValidateTenant(tn)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for another reason: %v", err)
			}
		})
	}
}

func TestSignInAddressShapedSubjectRefused(t *testing.T) {
	ok := &Subject{Issuer: "https://accounts.google.com", Subject: "110169484474386276334"}
	if err := ValidateSubject(ok); err != nil {
		t.Fatalf("an opaque subject was refused: %v", err)
	}
	bad := &Subject{Issuer: "https://accounts.google.com", Subject: "jody@acme.com"}
	if err := ValidateSubject(bad); err == nil || !strings.Contains(err.Error(), "address-shaped") {
		t.Fatalf("an address-shaped subject: %v", err)
	}
}
