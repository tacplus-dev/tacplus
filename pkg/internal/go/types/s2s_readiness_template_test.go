// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// A generated Go service used to flip SERVING (k8s Ready) as soon as bootstrap
// finished, before its s2s channels existed, and its reconnect loops started
// at 2-3s and sat at the 30s cap. Readiness now also requires every required
// s2s channel (declared s2s targets and event-bus subscriptions), and every
// reconnect loop backs off from serviceconnection.ReconnectInitialBackoff.
// Rust twin: s2s_readiness_template_test.go under rust/types.

func goSMTemplate(t *testing.T) string {
	t.Helper()
	src, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	return string(src)
}

func goFnBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("missing %q", signature)
	}
	// The template is unindented, so a nested closing brace also sits at
	// column 0: cut at the next top-level func instead.
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+1+end]
}

func TestGoReadinessWaitsForRequiredS2SChannels(t *testing.T) {
	sm := goSMTemplate(t)
	run := goFnBody(t, sm, "func (sm *ServiceManager) Run(h Handler) error {")

	// Inside the ServiceHealthState branch, bootstrap must hand readiness to
	// driveReadiness instead of flipping SERVING itself.
	healthBranch := run[strings.Index(run, "case err := <-healthReady:"):strings.Index(run, "{{else}}\n// No ServiceHealthState reporter")]
	if strings.Contains(healthBranch, "healthCtl.SetReady(true)") {
		t.Fatalf("Run() must not flip SERVING before the required s2s channels are up")
	}
	for _, want := range []string{
		"healthOK := new(atomic.Bool)",
		"healthOK.Store(status != healthreporter.StatusUnhealthy)",
		"sm.driveReadiness(taskCtx, healthCtl, healthOK, {{if .HasSubscriptions}}readinessStreams{{else}}nil{{end}})",
	} {
		if !strings.Contains(healthBranch, want) {
			t.Fatalf("Run() readiness wiring missing %q", want)
		}
	}

	drive := goFnBody(t, sm, "func (sm *ServiceManager) driveReadiness(")
	for _, want := range []string{
		"serviceconnection.NewRequiredChannels(",
		"{{- if .S2STargetsRequired}}",
		`sm.s2sConnections["{{.}}"],`,
		"ready := healthOK.Load() && !isDown",
		"healthCtl.SetReady(ready)",
		`"readiness: NOT_SERVING, required s2s channel is down"`,
	} {
		if !strings.Contains(drive, want) {
			t.Fatalf("driveReadiness missing %q:\n%s", want, drive)
		}
	}

	// Each subscription marks its channel up after resolve and down after the
	// subscribe call returns.
	subs := run[strings.Index(run, "{{if .HasSubscriptions}}\n// Start event-bus subscriptions"):]
	resolve := strings.Index(subs, `sm.resolveServiceCtx(subCtx, "{{.TargetService}}")`)
	up := strings.Index(subs, "up_{{.HandlerFuncFieldName}}.Store(true)")
	subscribe := strings.Index(subs, "sub.Subscribe(subCtx,")
	down := strings.Index(subs, "up_{{.HandlerFuncFieldName}}.Store(false)")
	if !(resolve >= 0 && resolve < up && up < subscribe && subscribe < down) {
		t.Fatalf("subscription must mark up after resolve and down after subscribe ends (resolve=%d up=%d subscribe=%d down=%d)", resolve, up, subscribe, down)
	}
	if !strings.Contains(run, `{Name: "{{.SourcePackage}}.{{.SourceField}} subscription ({{.TargetService}})", Up: up_{{.HandlerFuncFieldName}}},`) {
		t.Fatalf("subscription up-flags must be handed to readiness as named streams")
	}
}

func TestGoReadinessWithoutHealthStateFlipsOnBootstrap(t *testing.T) {
	run := goFnBody(t, goSMTemplate(t), "func (sm *ServiceManager) Run(h Handler) error {")
	noHealth := run[strings.Index(run, "{{else}}\n// No ServiceHealthState reporter"):]
	noHealth = noHealth[:strings.Index(noHealth, "\n{{end}}")]
	if !strings.Contains(noHealth, "healthCtl.SetReady(true)") {
		t.Fatalf("without ServiceHealthState there are no s2s channels to wait for; bootstrap must flip SERVING:\n%s", noHealth)
	}
	if strings.Contains(noHealth, "driveReadiness") {
		t.Fatalf("driveReadiness must only run with ServiceHealthState")
	}
}

func TestGoS2SReconnectBackoffStartsAtHalfSecond(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/serviceconnection/backoff.go.tmpl")
	if err != nil {
		t.Fatalf("read backoff chassis: %v", err)
	}
	for _, want := range []string{
		"const ReconnectInitialBackoff = 500 * time.Millisecond",
		"const ReconnectMaxBackoff = 30 * time.Second",
		"func NextReconnectBackoff(current time.Duration) time.Duration",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("backoff chassis missing %q", want)
		}
	}
	sm := goSMTemplate(t)
	for _, stale := range []string{"backoff := 2 * time.Second", "const minBackoff = 3 * time.Second", "const minSubBackoff = 2 * time.Second", "backoff *= 2"} {
		if strings.Contains(sm, stale) {
			t.Fatalf("service_manager still carries its own reconnect backoff: %q", stale)
		}
	}
	resolve := goFnBody(t, sm, "func (sm *ServiceManager) resolveServiceCtx(")
	if !strings.Contains(resolve, "backoff := serviceconnection.ReconnectInitialBackoff") {
		t.Fatalf("resolveServiceCtx must start at ReconnectInitialBackoff")
	}
	if n := strings.Count(sm, "backoff = serviceconnection.NextReconnectBackoff(backoff)"); n < 6 {
		t.Fatalf("every reconnect loop must use NextReconnectBackoff; got %d sites", n)
	}
}
