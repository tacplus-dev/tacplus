// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
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

	// The sink feeds readiness, and readiness is driven from bootstrap onward
	// by drive_readiness (which also requires the s2s channels to be up) —
	// so the sink must be wired before the driver is started.
	idxSink := strings.Index(sm, "health_reporter.set_status_sink(")
	idxDrive := strings.Index(sm, "self.spawn_owned(self.clone().drive_readiness(")
	if idxSink < 0 || idxDrive < 0 || idxDrive < idxSink {
		t.Fatalf("StatusSink must be wired before readiness is driven (sink=%d drive=%d)", idxSink, idxDrive)
	}
}

// The plaintext health probe server must be a completely separate tonic Server
// that serves ONLY the cloned grpc.health.v1 HealthService. No TLS, no auth
// layer, no business services. The clone shares internal Arc state so
// StatusSink status changes are visible on both listeners.
func TestRustHealthProbeServerIsolation(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	sm := string(src)

	// The health reporter must be stored in the struct and services created from it.
	for _, want := range []string{
		"grpc_health: tonic_health::server::HealthReporter,",
		"HealthService::from_health_reporter(self.grpc_health.clone())",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Rust service_manager must set up health probe server; missing %q", want)
		}
	}

	// The probe server must use a separate tonic::transport::Server — no TLS config.
	// Find the probe server block and verify it does NOT call tls_config().
	probeIdx := strings.Index(sm, `"plaintext health probe server listening"`)
	if probeIdx < 0 {
		t.Fatalf("Rust service_manager must log plaintext health probe server startup")
	}
	// Look backwards for the server builder
	probeBlock := sm[probeIdx-500 : probeIdx+200]
	if strings.Contains(probeBlock, "tls_config(") {
		t.Fatalf("probe server must NOT configure TLS")
	}
	if strings.Contains(probeBlock, "AuthEnforcingLayer") {
		t.Fatalf("probe server must NOT add auth layer")
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

// The health probe listener must bind BEFORE any kernel-dependent bootstrap step.
// The previous implementation created the health service and bound the probe in
// run(), which deadlocked on a cold start.
func TestRustHealthProbeBindsBeforeKernelInit(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	sm := string(src)

	// The health probe binding must appear in new(), BEFORE identity init.
	probeIdx := strings.Index(sm, `"plaintext health probe server listening"`)
	identityIdx := strings.Index(sm, "IdentityManager::new(channel, &cfg)")

	// Find which is in new() vs run()
	newIdx := strings.Index(sm, "pub async fn new()")
	runIdx := strings.Index(sm, "pub async fn run(")

	if probeIdx < 0 {
		t.Fatalf("health probe binding not found")
	}
	if identityIdx < 0 {
		t.Fatalf("identity init not found")
	}
	if probeIdx >= identityIdx {
		t.Fatalf("health probe must bind BEFORE identity init (probe=%d identity=%d) — cold-start deadlock", probeIdx, identityIdx)
	}
	// Probe must be in new(), not run()
	if probeIdx > runIdx {
		t.Fatalf("health probe must bind in new(), not in run() (probe=%d run=%d)", probeIdx, runIdx)
	}
	_ = newIdx
}

// Services that manage their own serving lifecycle (e.g. gateway with custom
// external TLS) never call run(), so the bootstrap SERVING flip never fires.
// The SM must expose a public set_serving() method that drives the SAME
// grpc_health handle backing the 8444 probe — not a second reporter.
func TestRustServiceManagerExposesSetServing(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	sm := string(src)

	// The method must exist with the right signature.
	if !strings.Contains(sm, "pub async fn set_serving(&self, serving: bool)") {
		t.Fatalf("Rust SM must expose pub async fn set_serving(&self, serving: bool)")
	}

	// It must use the same grpc_health field that backs the probe listener,
	// not create a new HealthReporter.
	if !strings.Contains(sm, `self.grpc_health.set_service_status("", status).await;`) {
		t.Fatalf("set_serving must drive self.grpc_health (the shared handle), not a new reporter")
	}

	// The method must NOT appear inside run() — it is an accessor on the struct.
	setServingIdx := strings.Index(sm, "pub async fn set_serving(&self, serving: bool)")
	runIdx := strings.Index(sm, "pub async fn run(")
	if setServingIdx < 0 {
		t.Fatalf("set_serving not found")
	}
	if runIdx >= 0 && setServingIdx > runIdx {
		t.Fatalf("set_serving must be defined before run(), as a standalone accessor (set_serving=%d run=%d)", setServingIdx, runIdx)
	}

	// The method must map true→Serving, false→NotServing.
	// Find the method body and check both variants.
	methodBody := sm[setServingIdx : setServingIdx+500]
	if !strings.Contains(methodBody, "ServingStatus::Serving") {
		t.Fatalf("set_serving(true) must map to ServingStatus::Serving")
	}
	if !strings.Contains(methodBody, "ServingStatus::NotServing") {
		t.Fatalf("set_serving(false) must map to ServingStatus::NotServing")
	}
}
