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
		`healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)`,
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

	// The ListenHealthProbe method must exist and create a separate grpc.Server.
	for _, want := range []string{
		"func (ps *GrpcServer) ListenHealthProbe(ctx context.Context, port int32) error",
		"probeServer := grpc.NewServer()",
		"healthpb.RegisterHealthServer(probeServer, ps.healthServer)",
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

	// The service_manager must wire the probe server alongside the TLS server.
	smSrc, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	sm := string(smSrc)
	for _, want := range []string{
		"ListenHealthProbe(taskCtx, port)",
		"sm.cfg.GetHealthProbePort()",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Go service_manager must start health probe server; missing %q", want)
		}
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
