// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"strings"
	"testing"
)

// The generated gRPC health endpoint must report readiness, not mere port-open.
// HealthStatusManager defaults the "" service to SERVING, so it is forced
// NOT_SERVING in the constructor and flipped to SERVING by the service_manager
// only after bootstrap completes. Parity with Go/Rust/Python/TS (todo
// REVISIT_REVIEW health check).
func TestJavaGrpcServerHealthStartsNotServingAndExposesSetReady(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/grpcserver/GrpcServer.java.tmpl")
	for _, want := range []string{
		"import io.grpc.health.v1.HealthCheckResponse;",
		"private final HealthStatusManager healthManager;",
		`this.healthManager.setStatus("", HealthCheckResponse.ServingStatus.NOT_SERVING);`,
		"public void setReady(boolean ready)",
		"HealthCheckResponse.ServingStatus.SERVING",
		"healthManager.enterTerminalState();",
		"serverBuilder.addService(healthManager.getHealthService());",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java GrpcServer must readiness-gate the health endpoint; missing %q", want)
		}
	}
	// healthManager must be created exactly once — as a field at construction,
	// not inside listenTLS — otherwise setReady() could race the server thread
	// and leave the endpoint stuck NOT_SERVING.
	if n := strings.Count(rendered, "new HealthStatusManager()"); n != 1 {
		t.Fatalf("Java GrpcServer must construct the HealthStatusManager exactly once (as a field); found %d", n)
	}
}

// The plaintext health probe server must be a completely separate NettyServerBuilder
// that serves ONLY grpc.health.v1 via the shared HealthStatusManager. No TLS, no
// interceptors, no business services. This is the isolation guarantee.
func TestJavaHealthProbeServerIsolation(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/grpcserver/GrpcServer.java.tmpl")

	// The listenHealthProbe method must exist.
	for _, want := range []string{
		"public void listenHealthProbe(int port)",
		"healthManager.getHealthService()",
		"healthProbeServer",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java GrpcServer must have listenHealthProbe with isolated server; missing %q", want)
		}
	}

	// The probe server must NOT have SSL context or interceptors.
	// Find the listenHealthProbe method and verify it does not call sslContext or intercept.
	probeStart := strings.Index(rendered, "public void listenHealthProbe(int port)")
	if probeStart < 0 {
		t.Fatalf("listenHealthProbe method not found")
	}
	// Find the next method boundary (next "public" or "private" after probe start)
	probeBody := rendered[probeStart:]
	probeEnd := len(probeBody)
	for _, marker := range []string{"\n    private void listenTLS", "\n    private SslContext buildSslContext"} {
		idx := strings.Index(probeBody, marker)
		if idx > 0 && idx < probeEnd {
			probeEnd = idx
		}
	}
	probeMethod := probeBody[:probeEnd]
	if strings.Contains(probeMethod, "sslContext") {
		t.Fatalf("listenHealthProbe must NOT configure TLS")
	}
	if strings.Contains(probeMethod, "intercept(") {
		t.Fatalf("listenHealthProbe must NOT add interceptors")
	}
	if strings.Contains(probeMethod, "impl.registerServices") {
		t.Fatalf("listenHealthProbe must NOT register business services")
	}

	// The service_manager must start the probe server in create(), before kernel init.
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	for _, want := range []string{
		"GrpcServer.startHealthProbeServer(",
		"earlyHealthManager",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Java ServiceManager must start health probe server early; missing %q", want)
		}
	}
}

// The service_manager must flip readiness to SERVING after bootstrap (health
// publish + auto-TLS/SMB), for both the business-gRPC and health-only paths.
func TestJavaServiceManagerFlipsHealthReadyAfterBootstrap(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	for _, want := range []string{
		"srv.setReady(true);",
		"healthSrv.setReady(true);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java service_manager must readiness-gate the health endpoint; missing %q", want)
		}
	}
	// Readiness must be advertised only after the serving thread is started.
	listen := strings.Index(rendered, "srv.listen();")
	ready := strings.Index(rendered, "srv.setReady(true);")
	if listen < 0 || ready < 0 || ready < listen {
		t.Fatalf("srv.setReady(true) must come after srv.listen() (listen=%d ready=%d)", listen, ready)
	}
}

// T15: the generated Java HealthReporter must NOT start its publish loop at
// construction (the Rust race fixed in e7f540a). The constructor only creates
// the scheduler; publishing begins on explicit start(), which the
// service_manager calls after setStatus(HEALTHY). The default status is a
// defined STARTING, never an undefined value.
// The HealthReporter must expose a StatusSink callback that bridges effective
// health status to the gRPC health endpoint. The ServiceManager wires this
// after bootstrap so UNHEALTHY flips the endpoint to NOT_SERVING and recovery
// flips it back to SERVING.
func TestJavaHealthReporterStatusSinkBridgesHealthToGrpc(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/healthreporter/HealthReporter.java.tmpl")

	// HealthReporter must define the StatusSink interface.
	for _, want := range []string{
		"public interface StatusSink",
		"void onStatus(InstanceStatus status);",
		"private volatile StatusSink statusSink;",
		"public void setStatusSink(StatusSink sink)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java HealthReporter must define StatusSink; missing %q", want)
		}
	}

	// The sink must be invoked in publish() after computing effectiveStatus,
	// and must be skipped during DRAINING.
	for _, want := range []string{
		"sink.onStatus(effectiveStatus);",
		"INSTANCE_STATUS_DRAINING",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java HealthReporter publish must call StatusSink; missing %q", want)
		}
	}

	// The ServiceManager must wire the StatusSink to flip the gRPC health endpoint.
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	for _, want := range []string{
		"healthReporter.setStatusSink(",
		"INSTANCE_STATUS_UNHEALTHY",
		"setReady(serving);",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Java ServiceManager must wire StatusSink to gRPC health; missing %q", want)
		}
	}

	// The sink wiring must come AFTER the initial setReady(true) bootstrap.
	idxBootstrap := strings.Index(sm, "srv.setReady(true);")
	idxSink := strings.Index(sm, "healthReporter.setStatusSink(")
	if idxBootstrap < 0 || idxSink < 0 || idxSink < idxBootstrap {
		t.Fatalf("StatusSink must be wired after bootstrap setReady(true) (bootstrap=%d sink=%d)", idxBootstrap, idxSink)
	}
}

// The health probe listener must bind BEFORE any kernel-dependent bootstrap step.
// The previous implementation bound the probe in run() (after identity/state-client
// init in create()), which deadlocked on a cold start.
func TestJavaHealthProbeBindsBeforeKernelInit(t *testing.T) {
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	// The health probe binding must appear BEFORE the identity init retry loop.
	probeIdx := strings.Index(sm, "GrpcServer.startHealthProbeServer(")
	identityIdx := strings.Index(sm, "IdentityManager.create(cfg)")
	if probeIdx < 0 {
		t.Fatalf("health probe binding not found in create()")
	}
	if identityIdx < 0 {
		t.Fatalf("identity init not found in create()")
	}
	if probeIdx >= identityIdx {
		t.Fatalf("health probe must bind BEFORE identity init (probe=%d identity=%d) — cold-start deadlock", probeIdx, identityIdx)
	}

	// The health probe must NOT appear in run() — it must be in create().
	runIdx := strings.Index(sm, "public void run(Handler handler)")
	if runIdx >= 0 && probeIdx > runIdx {
		t.Fatalf("health probe must bind in create(), not in run()")
	}
}

func TestJavaHealthReporterDoesNotPublishAtConstruction(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/healthreporter/HealthReporter.java.tmpl")

	if !strings.Contains(rendered,
		"private volatile InstanceStatus status = InstanceStatus.INSTANCE_STATUS_STARTING;") {
		t.Fatalf("Java HealthReporter must default to a defined STARTING status, not undefined")
	}

	ctorEnd := strings.Index(rendered, "public void setStatus(")
	if ctorEnd < 0 {
		t.Fatalf("could not locate end of HealthReporter constructor (setStatus method)")
	}
	for _, publishCall := range []string{"scheduler.execute(", "scheduleAtFixedRate(", "publish("} {
		idx := strings.Index(rendered, publishCall)
		if idx >= 0 && idx < ctorEnd {
			t.Fatalf("Java HealthReporter must not start publishing at construction; %q appears before start() (idx=%d ctorEnd=%d)", publishCall, idx, ctorEnd)
		}
	}
	// And the publish loop must be submitted from start().
	if start := strings.Index(rendered, "public void start(CountDownLatch firstPublishReady, int maxFirstPublishAttempts)"); start < 0 {
		t.Fatalf("Java HealthReporter must start the publish loop via an explicit start()")
	}
}
