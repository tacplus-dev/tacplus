// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"sort"
	"strings"
	"testing"
)

func policyByName(t *testing.T, policies []SecurityPolicy, name string) SecurityPolicy {
	t.Helper()
	for _, p := range policies {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no security policy named %q in %+v", name, policies)
	return SecurityPolicy{}
}

func assertStrings(t *testing.T, got, want []string, what string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// A state root's read policy is its owner plus the services that declared a
// state_import of it — not "*". consumer.proto imports owner.OwnerState.current,
// so consumer reads OwnerState; nothing imports ConsumerState, so only consumer
// reads it.
func TestComputeSecurityPoliciesROIsOwnerPlusDeclaredImporters(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)

	policies, err := ComputeSecurityPolicies(dir)
	if err != nil {
		t.Fatalf("ComputeSecurityPolicies: %v", err)
	}

	owner := policyByName(t, policies, "OwnerState")
	assertStrings(t, owner.RO, []string{"service:owner", "service:consumer"}, "OwnerState ro")
	assertStrings(t, owner.RW, []string{"service:owner"}, "OwnerState rw")

	consumer := policyByName(t, policies, "ConsumerState")
	assertStrings(t, consumer.RO, []string{"service:consumer"}, "ConsumerState ro")
}

// The RO set published to codegen-config.json and the auth_services set gating
// the generated state service are the same resolution, so parity holds.
func TestValidateStatePolicyAuthParityAcceptsComputedPolicies(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)

	policies, err := ComputeSecurityPolicies(dir)
	if err != nil {
		t.Fatalf("ComputeSecurityPolicies: %v", err)
	}
	if err := ValidateStatePolicyAuthParity(dir, policies); err != nil {
		t.Fatalf("ValidateStatePolicyAuthParity: %v", err)
	}
}

// Forcing the two planes apart — as a future edit to one derivation and not the
// other would — must fail generation rather than drift silently.
func TestValidateStatePolicyAuthParityRejectsDivergentRO(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)

	policies, err := ComputeSecurityPolicies(dir)
	if err != nil {
		t.Fatalf("ComputeSecurityPolicies: %v", err)
	}

	widened := append([]SecurityPolicy(nil), policies...)
	for i := range widened {
		if widened[i].Name == "OwnerState" {
			widened[i].RO = []string{"*"}
		}
	}
	err = ValidateStatePolicyAuthParity(dir, widened)
	if err == nil {
		t.Fatal("ValidateStatePolicyAuthParity accepted a widened RO set, want error")
	}
	if !strings.Contains(err.Error(), "owner.OwnerState") {
		t.Fatalf("error does not name the diverging root: %v", err)
	}

	// A narrowed RO that drops a declared importer must fail the same way.
	narrowed := append([]SecurityPolicy(nil), policies...)
	for i := range narrowed {
		if narrowed[i].Name == "OwnerState" {
			narrowed[i].RO = []string{"service:owner"}
		}
	}
	if err := ValidateStatePolicyAuthParity(dir, narrowed); err == nil {
		t.Fatal("ValidateStatePolicyAuthParity accepted an RO set missing a declared importer, want error")
	}
}
