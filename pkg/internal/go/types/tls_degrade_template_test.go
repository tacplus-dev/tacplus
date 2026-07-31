// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"strings"
	"testing"
)

// PC-L-1 regression: TLS renewal give-up / parse-failure paths must surface
// through the health plane (DEGRADED), not be Error-log-only. Pins the degrade
// seam in the SM template: struct fields, both failure-path calls, and the
// health-wiring subscription that maps reasons to StatusDegraded.
func TestGoSMTemplateTLSDegradeSeam(t *testing.T) {
	data, err := os.ReadFile("../../../templates/go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"tlsDegradeFns  []func(reason string)",
		"func (sm *ServiceManager) reportTLSDegraded(reason string)",
		"func (sm *ServiceManager) onTLSDegraded(fn func(reason string))",
		`sm.reportTLSDegraded("renewed TLS private key unparseable: " + err.Error())`,
		`sm.reportTLSDegraded("TLS renewal stream gave up before first delivery: " + err.Error())`,
		"hr.SetStatus(healthreporter.StatusDegraded)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SM template missing PC-L-1 degrade seam piece %q", want)
		}
	}
	// The subscription must come AFTER reporter creation and replay-safe
	// registration must exist (onTLSDegraded replays tlsDegradeSeen).
	if !strings.Contains(s, "for _, r := range sm.tlsDegradeSeen") {
		t.Error("onTLSDegraded must replay already-seen reasons to late subscribers")
	}
}
