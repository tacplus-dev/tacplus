// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
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
