// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// PC-L-1 regression: TLS renewal give-up / parse-failure paths must surface
// through the health plane (DEGRADED), not be Error-log-only. Pins the degrade
// seam in the Rust SM template: struct fields, both failure-path calls, and the
// health-wiring subscription that maps reasons to InstanceStatus::Degraded.
func TestRustSMTemplateTLSDegradeSeam(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	for _, want := range []string{
		"fns: Vec<Box<dyn Fn(&str) + Send + Sync>>",
		"fn report_tls_degraded(",
		"fn on_tls_degraded(",
		`report_tls_degraded(tls_degrade, &format!("renewed TLS private key unparseable: {}", e))`,
		`report_tls_degraded(&tls_degrade, &format!("TLS renewal stream gave up before first delivery: {}", cause))`,
		"hr.set_status(InstanceStatus::Degraded)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Rust SM template missing PC-L-1 degrade seam piece %q", want)
		}
	}
	// The subscription must replay already-recorded reasons to late subscribers.
	if !strings.Contains(tmpl, "for r in &guard.seen") {
		t.Error("on_tls_degraded must replay already-seen reasons to late subscribers")
	}
}
