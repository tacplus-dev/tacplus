// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// The generated gRPC health endpoint must report readiness, not mere port-open.
// It starts NOT_SERVING and is flipped to SERVING by the service_manager only
// after bootstrap completes, so k8s readiness probes reflect actual readiness.
// This mirrors the Python/TS _ready gate and is enforced for parity across
// Go/Java/Rust (todo REVISIT_REVIEW health check).
func TestGoGrpcServerHealthStartsNotServingAndExposesSetReady(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/grpcserver/grpcserver.go.tmpl")
	if err != nil {
		t.Fatalf("read grpcserver template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		`hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)`,
		"func (ps *GrpcServer) SetReady(ready bool)",
		"healthpb.RegisterHealthServer(grpcServer, ps.healthServer)",
		"ps.healthServer.Shutdown()",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go grpcserver must readiness-gate the health endpoint; missing %q", want)
		}
	}
	// The Listen path must register the shared (NOT_SERVING) health server, not a
	// freshly-created one that is statically set SERVING.
	if strings.Contains(rendered, "RegisterHealthServer(grpcServer, healthServer)") {
		t.Fatalf("Go grpcserver must register the shared ps.healthServer, not a static-SERVING local")
	}
}

// The plaintext health probe server must be a completely separate grpc.Server
// that serves ONLY grpc.health.v1. It shares the same healthServer instance
// (so StatusSink flips both endpoints) but has no TLS, no interceptors, and
// no business services. This is the isolation guarantee.
func TestGoHealthProbeServerIsolation(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/grpcserver/grpcserver.go.tmpl")
	if err != nil {
		t.Fatalf("read grpcserver template: %v", err)
	}
	rendered := string(src)

	// The ListenHealthProbe standalone function and method must exist.
	for _, want := range []string{
		"func ListenHealthProbe(ctx context.Context, port int32, hs *health.Server) error",
		"func (ps *GrpcServer) ListenHealthProbe(ctx context.Context, port int32) error",
		"probeServer := grpc.NewServer()",
		"healthpb.RegisterHealthServer(probeServer, hs)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go grpcserver must have ListenHealthProbe with isolated server; missing %q", want)
		}
	}

	// The probe server must NOT have TLS credentials, interceptors, or service
	// registration. Verify it creates a bare grpc.NewServer() — no ServerOption
	// arguments beyond the empty call.
	probeIdx := strings.Index(rendered, "probeServer := grpc.NewServer()")
	if probeIdx < 0 {
		t.Fatalf("probe server must use bare grpc.NewServer()")
	}

	// Must NOT register any ServiceImplementation on the probe server.
	if strings.Contains(rendered, "ps.impl.RegisterServices(probeServer)") {
		t.Fatalf("probe server must NOT register business services")
	}

	// The service_manager must wire the probe server in New() before kernel-dependent init.
	smSrc, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	sm := string(smSrc)
	// The health probe must bind in New(), before the kernel-dependent identity init loop.
	for _, want := range []string{
		"grpcserver.NewHealthServer()",
		"grpcserver.ListenHealthProbe(ctx, port, healthServer)",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Go service_manager must start health probe server; missing %q", want)
		}
	}
}

// The health probe listener must bind BEFORE any kernel-dependent bootstrap step.
// The previous implementation bound the probe in Run() (after identity/state-client
// init in New()), which deadlocked on a cold start: kernel gated on its own 8444
// probe, so nothing bootstrapped. This test asserts ordering in the template.
func TestGoHealthProbeBindsBeforeKernelInit(t *testing.T) {
	src, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	sm := string(src)

	// The health probe binding must appear BEFORE the identity init retry loop.
	probeIdx := strings.Index(sm, "grpcserver.ListenHealthProbe(ctx, port, healthServer)")
	identityIdx := strings.Index(sm, "service_im.New(ctx, cfg)")
	if probeIdx < 0 {
		t.Fatalf("health probe binding not found in New()")
	}
	if identityIdx < 0 {
		t.Fatalf("identity init not found in New()")
	}
	if probeIdx >= identityIdx {
		t.Fatalf("health probe must bind BEFORE identity init (probe=%d identity=%d) — cold-start deadlock", probeIdx, identityIdx)
	}

	// The probe must also appear before servicemanager init and state-client init.
	smInitIdx := strings.Index(sm, "servicemanager.NewServiceManager(ctx, cfg, identity)")
	if smInitIdx >= 0 && probeIdx >= smInitIdx {
		t.Fatalf("health probe must bind BEFORE servicemanager init (probe=%d sm=%d)", probeIdx, smInitIdx)
	}

	// The health probe must NOT appear in Run() — it must be in New().
	runIdx := strings.Index(sm, "func (sm *ServiceManager) Run(h Handler) error {")
	if runIdx >= 0 && probeIdx > runIdx {
		t.Fatalf("health probe must bind in New(), not in Run()")
	}
}

// The service_manager must flip readiness to SERVING only after the serving
// goroutine has started and bootstrap (incl. first health publish) is done.
func TestGoServiceManagerFlipsHealthReadyAfterBootstrap(t *testing.T) {
	src, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"var healthCtl *grpcserver.GrpcServer",
		"healthCtl.SetReady(true)",
		"grpcserver.NewHealthOnly(",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go service_manager must readiness-gate the health endpoint; missing %q", want)
		}
	}
	// Readiness must be advertised only after the server starts listening.
	listen := strings.Index(rendered, "srv.Listen(taskCtx)")
	ready := strings.Index(rendered, "healthCtl.SetReady(true)")
	if listen < 0 || ready < 0 || ready < listen {
		t.Fatalf("SetReady(true) must come after the serving goroutine starts (listen=%d ready=%d)", listen, ready)
	}
}
