// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// Regression: Rust SM init retry loops must log service name, attempt number,
// and elapsed time on each retry and ERROR on give-up, matching the Go parity.
func TestRustSMTemplateStartupObservability(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	// Retry WARN must include service label, attempt, and elapsed.
	for _, want := range []string{
		`"ServiceManager init: identity not ready, retrying"`,
		`"ServiceManager init: no state-store connection, retrying in 2s"`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Rust SM template missing startup retry WARN log %q", want)
		}
	}

	// Give-up ERROR must include service label, attempts, elapsed.
	for _, want := range []string{
		`"ServiceManager startup failed: identity init exhausted retries"`,
		`"ServiceManager startup failed: no state-store connection after max retries"`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Rust SM template missing startup give-up ERROR log %q", want)
		}
	}

	// Elapsed time tracking must exist in the init loop.
	if !strings.Contains(tmpl, "let init_start = std::time::Instant::now()") {
		t.Error("Rust SM template must track init start time for elapsed duration in logs")
	}
}

// Regression: Rust IM initial exchange retry must log attempt count and
// elapsed time on each retry and ERROR on timeout.
func TestRustIMTemplateExchangeRetryObservability(t *testing.T) {
	chassisFS, err := fs.Sub(templates.FS, "rust/chassis/service_im/src")
	if err != nil {
		t.Fatalf("sub filesystem: %v", err)
	}
	data, err := fs.ReadFile(chassisFS, "manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	s := string(data)

	// Must log attempt count on each retry.
	if !strings.Contains(s, "attempt,") && !strings.Contains(s, "attempt =") {
		t.Error("im_initial_exchange_with_retry must log attempt number on each retry")
	}

	// Must log elapsed time.
	if !strings.Contains(s, "elapsed_s") {
		t.Error("im_initial_exchange_with_retry must log elapsed duration on each retry")
	}

	// Must log ERROR on timeout.
	if !strings.Contains(s, "IM: initial token exchange timed out") {
		t.Error("im_initial_exchange_with_retry must log ERROR on timeout")
	}
}
