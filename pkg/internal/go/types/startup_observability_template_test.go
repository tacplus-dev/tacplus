// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"strings"
	"testing"
)

// Regression: a service that blocks during startup and dies with only "context
// canceled" is unacceptable observability. The SM template must:
//   - Bootstrap a production zap logger before init retries so zap.L() is not
//     a no-op during the startup path.
//   - Log WARN on each retry attempt with service name, attempt number, elapsed
//     time, and the error.
//   - Log ERROR on give-up (max retries or context cancellation) naming the
//     unmet dependency, attempt count, and elapsed time.
func TestGoSMTemplateStartupObservability(t *testing.T) {
	data, err := os.ReadFile("../../../templates/go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	s := string(data)

	// Bootstrap logger must be installed before the init retry loop.
	if !strings.Contains(s, "zap.ReplaceGlobals(prod)") {
		t.Error("SM template must bootstrap a production zap logger in New() so init retry warnings are visible")
	}

	// Retry WARN must include service label, attempt, elapsed, and error.
	for _, want := range []string{
		`"ServiceManager init: identity not ready, retrying"`,
		`"ServiceManager init: servicemanager not ready, retrying"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SM template missing startup retry WARN log %q", want)
		}
	}

	// Give-up ERROR must include service label, attempts, elapsed.
	for _, want := range []string{
		`"ServiceManager startup failed: identity init exhausted retries"`,
		`"ServiceManager startup failed: servicemanager init exhausted retries"`,
		`"ServiceManager startup aborted: identity init incomplete"`,
		`"ServiceManager startup aborted: servicemanager init incomplete"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SM template missing startup give-up ERROR log %q", want)
		}
	}

	// Elapsed time tracking must exist in the init loop.
	if !strings.Contains(s, "initStart := time.Now()") {
		t.Error("SM template must track init start time for elapsed duration in logs")
	}
}

// Regression: imInitialExchangeWithRetry must log attempt count and elapsed
// time on each retry and on timeout, not just the error and delay.
func TestGoIMTemplateExchangeRetryObservability(t *testing.T) {
	data, err := os.ReadFile("../../../templates/go/chassis/service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	s := string(data)

	// Must track attempt count.
	if !strings.Contains(s, `zap.Int("attempt", attempt)`) {
		t.Error("imInitialExchangeWithRetry must log attempt number on each retry")
	}

	// Must log elapsed time.
	if !strings.Contains(s, `zap.Duration("elapsed", elapsed)`) {
		t.Error("imInitialExchangeWithRetry must log elapsed duration on each retry")
	}

	// Must log ERROR on timeout with attempt count.
	if !strings.Contains(s, `"IdentityManager: initial token exchange timed out"`) {
		t.Error("imInitialExchangeWithRetry must log ERROR on timeout")
	}

	// Must log ERROR on context cancellation.
	if !strings.Contains(s, `"IdentityManager: initial token exchange aborted"`) {
		t.Error("imInitialExchangeWithRetry must log ERROR on context cancellation")
	}
}
