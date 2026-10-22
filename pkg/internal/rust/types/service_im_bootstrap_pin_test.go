// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// H1 / SEC-1 (rust parity of the go/java TestServiceIMBootstrapPinsTrustDomain):
// the rust SpiffeServerCertVerifier must ALWAYS pin the peer's SPIFFE trust
// domain. NEW-30 removed the `chain_only` escape hatch that skipped the URI SAN
// check for bootstrap peers — CA-chain-only trust is fail-open (any CA-issued
// cert could impersonate the token authority). Rust was the one language whose
// safety net was removed without a test; this guards against re-introducing it.
func TestRustSpiffeVerifierHasNoChainOnlyEscapeHatch(t *testing.T) {
	rendered := loadRustLibTemplate(t, "rust/chassis/servicemanager/src/spiffe.rs.tmpl")

	// The escape hatch must be gone entirely — field, constructor state, and the
	// chain_only() method that returned a SAN-skipping verifier.
	if strings.Contains(rendered, "chain_only") {
		t.Fatalf("spiffe.rs.tmpl still references chain_only — the fail-open bootstrap escape hatch must stay removed (NEW-30):\n%s", rendered)
	}

	// Fail-closed: the SPIFFE SAN check must run unconditionally in the
	// ServerCertVerifier path, no longer guarded by `if !self.chain_only`.
	if want := "self.check_spiffe_san(end_entity)?;"; !strings.Contains(rendered, want) {
		t.Fatalf("spiffe.rs.tmpl missing unconditional SPIFFE SAN pin %q:\n%s", want, rendered)
	}
}
