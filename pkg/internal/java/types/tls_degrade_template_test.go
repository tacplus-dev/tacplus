// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// PC-L-1 regression: TLS renewal give-up / parse-failure paths must surface
// through the health plane (DEGRADED), not be Error-log-only. Pins the degrade
// seam in the Java SM template: fields, both failure-path calls, and the
// health-wiring subscription that maps reasons to INSTANCE_STATUS_DEGRADED.
func TestJavaSMTemplateTLSDegradeSeam(t *testing.T) {
	tmpl, err := loadTemplate("ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	for _, want := range []string{
		"private final java.util.List<Consumer<String>> tlsDegradeFns",
		"private void reportTLSDegraded(String reason)",
		"private void onTLSDegraded(Consumer<String> fn)",
		`sm.reportTLSDegraded("renewed TLS private key unparseable: " + keyErr.getMessage())`,
		`sm.reportTLSDegraded("TLS renewal stream gave up before first delivery: " + e.getMessage())`,
		"healthReporter.setStatus(InstanceStatus.INSTANCE_STATUS_DEGRADED)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Java SM template missing PC-L-1 degrade seam piece %q", want)
		}
	}
	// The subscription must replay already-recorded reasons to late subscribers.
	if !strings.Contains(tmpl, "for (String r : tlsDegradeSeen)") {
		t.Error("onTLSDegraded must replay already-seen reasons to late subscribers")
	}
}
