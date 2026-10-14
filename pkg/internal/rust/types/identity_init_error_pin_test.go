// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// NEW-21 leg (c), rust parity. Found while closing the go/java legs: the rust
// ServiceManager carried the same dead empty-trust-domain tolerance on the
// health-identity path. new() already fails loudly when it builds the SPIFFE
// verifier (SpiffeServerCertVerifier::new rejects an empty trust domain,
// pinned by chassis/servicemanager/src/spiffe.rs.tmpl), so run() cannot be
// reached with one — and publishing an empty instance identity into the fleet
// health plane instead of failing is the fail-open shape.
func TestRustServiceManagerHasNoEmptyTrustDomainTolerance(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust/types/service_manager.rs.tmpl: %v", err)
	}
	rendered := string(src)

	if !strings.Contains(rendered, `let spiffe_id = spiffe::service_spiffe_id(&self.cfg.spiffe_trust_domain, &self.cfg.location_path, "{{.ServiceLabel}}")`) {
		t.Fatal("rust health identity must be built unconditionally from the configured trust domain")
	}
	if strings.Contains(rendered, "let spiffe_id = if self.cfg.spiffe_trust_domain.is_empty() {") {
		t.Fatal("rust health identity must not tolerate an empty trust domain: publishing an empty " +
			"instance identity instead of failing is the fail-open shape (NEW-21)")
	}
}

// NEW-21 leg (b), rust parity. The rust classifier was pure message-substring
// matching; it now downcasts the typed error the identity chassis returns when a
// build declares no token exchanger. `service_im` must export it, and the
// no-exchanger minter stubs must return it rather than a bare string.
func TestRustServiceManagerClassifiesMissingTokenMinterByType(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"rust/types/service_manager.rs.tmpl", "err.downcast_ref::<service_im::TokenMinterNotConfigured>().is_some()"},
		{"rust/chassis/service_im/src/manager.rs.tmpl", "pub struct TokenMinterNotConfigured;"},
		{"rust/chassis/service_im/src/manager.rs.tmpl", "impl std::error::Error for TokenMinterNotConfigured {}"},
		{"rust/chassis/service_im/src/manager.rs.tmpl", "return Err(Box::new(TokenMinterNotConfigured));"},
		{"rust/chassis/service_im/src/lib.rs.tmpl", "pub use manager::{IdentityManager, TokenMinterNotConfigured};"},
		{"rust/chassis/service_im/src/token_minter.rs.tmpl", "Err(Box::new(crate::manager::TokenMinterNotConfigured))"},
		{"rust/chassis/service_im/src/cloud_minter.rs.tmpl", "Err(Box::new(crate::manager::TokenMinterNotConfigured))"},
	} {
		src, err := templates.FS.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if !strings.Contains(string(src), tc.want) {
			t.Fatalf("%s must carry the typed missing-minter seam; missing %q", tc.file, tc.want)
		}
	}

	sm, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust/types/service_manager.rs.tmpl: %v", err)
	}
	if strings.Contains(string(sm), `contains("service token minter is not configured")`) {
		t.Fatal("rust ServiceManager must not classify a permanent identity-init failure by message " +
			"substring: the chassis message is not a contract (NEW-21 leg b)")
	}
}
