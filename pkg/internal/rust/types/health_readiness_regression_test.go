// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// The generated gRPC health endpoint must report readiness, not mere port-open.
// On the business-gRPC path it starts NOT_SERVING and is flipped to SERVING only
// after bootstrap completes (first health publish + auto-TLS/SMB), just before
// serving. Parity with Go/Java/Python/TS (todo REVISIT_REVIEW health check).
// The HealthReporter must expose a StatusSink callback that bridges effective
// health status to the gRPC health endpoint. UNHEALTHY flips to NOT_SERVING,
// recovery flips back to SERVING. The sink is not called during DRAINING.
func TestRustHealthReporterStatusSinkBridgesHealthToGrpc(t *testing.T) {
	hrSrc, err := templates.FS.ReadFile("rust/chassis/healthreporter/src/lib.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust healthreporter template: %v", err)
	}
	hr := string(hrSrc)

	for _, want := range []string{
		"pub type StatusSink = Arc<dyn Fn(InstanceStatus) + Send + Sync>;",
		"status_sink: Option<StatusSink>,",
		"pub fn set_status_sink(&self, sink: StatusSink)",
		"sink(status);",
		"InstanceStatus::Draining",
	} {
		if !strings.Contains(hr, want) {
			t.Fatalf("Rust HealthReporter must support StatusSink; missing %q", want)
		}
	}

	// The service_manager must wire the StatusSink to the gRPC health endpoint.
	smSrc, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	sm := string(smSrc)
	for _, want := range []string{
		"health_reporter.set_status_sink(",
		"InstanceStatus::Unhealthy",
		"ServingStatus::NotServing",
		"ServingStatus::Serving",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Rust service_manager must wire StatusSink to gRPC health; missing %q", want)
		}
	}

	// The sink wiring must come AFTER the bootstrap SERVING flip.
	idxBootstrap := strings.Index(sm, `grpc_health.set_service_status("", tonic_health::ServingStatus::Serving).await;`)
	idxSink := strings.Index(sm, "health_reporter.set_status_sink(")
	if idxBootstrap < 0 || idxSink < 0 || idxSink < idxBootstrap {
		t.Fatalf("StatusSink must be wired after bootstrap SERVING flip (bootstrap=%d sink=%d)", idxBootstrap, idxSink)
	}
}

func TestRustServiceManagerHealthStartsNotServingUntilBootstrap(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	rendered := string(src)

	notServing := `grpc_health.set_service_status("", tonic_health::ServingStatus::NotServing).await;`
	if !strings.Contains(rendered, notServing) {
		t.Fatalf("Rust health endpoint must start NOT_SERVING; missing %q", notServing)
	}

	// The NOT_SERVING init must precede the SERVING flip, which must precede serve.
	idxNotServing := strings.Index(rendered, notServing)
	idxServing := strings.Index(rendered, `grpc_health.set_service_status("", tonic_health::ServingStatus::Serving).await;`)
	idxServe := strings.Index(rendered, "serve_with_incoming_shutdown(incoming")
	if idxNotServing < 0 || idxServing < 0 || idxServe < 0 {
		t.Fatalf("Rust health readiness markers missing (notServing=%d serving=%d serve=%d)", idxNotServing, idxServing, idxServe)
	}
	if !(idxNotServing < idxServing && idxServing < idxServe) {
		t.Fatalf("Rust health must go NOT_SERVING -> SERVING -> serve (notServing=%d serving=%d serve=%d)", idxNotServing, idxServing, idxServe)
	}
}
