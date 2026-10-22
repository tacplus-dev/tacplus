// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// A generated Java service used to flip SERVING (k8s Ready) as soon as
// bootstrap finished, before its s2s channels existed, and its reconnect loops
// started at 2-3s and sat at the 30s cap. Readiness now also requires every
// required s2s channel (declared s2s targets and event-bus subscriptions), and
// every reconnect loop backs off from ReconnectBackoff.INITIAL_MS.
// Go and Rust twins: s2s_readiness_template_test.go under go/types and rust/types.

func renderJavaReadinessSM(t *testing.T, healthState, s2sTargetsRequired bool) string {
	t.Helper()
	return renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:           "com.example",
		ChassisBase:           "com.example.chassis",
		TypesBase:             "com.example.types",
		SmPrefix:              "s2sm",
		ServiceLabel:          "scotty",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasGrpcS2SClients:     true,
		HasServiceHealthState: healthState,
		StateStoreServiceID:   "statestore",
		S2STargetsRequired:    s2sTargetsRequired,
		TargetServices: []javaTargetServiceEntry{
			{ServiceID: "identity", ParamName: "identityChannel", FieldName: "identityConn"},
			{ServiceID: "statestore", ParamName: "statestoreChannel", FieldName: "statestoreConn"},
		},
		HasSubscriptions: true,
		Subscriptions: []javaSubscriptionEntry{{
			FieldPascal: "EventStream", FieldCamel: "eventStream",
			HandlerMethodName: "onEventStream",
			SourcePackage:     "aegis", SourceField: "event_stream",
			TypeName: "TopologyEvent", TypeImport: "com.example.types.aegis.Aegis",
			TypeOuterClass: "Aegis", ClassName: "AegisEventStreamSubscriber",
			TargetService: "eventbus",
		}},
	})
}

func TestJavaReadinessWaitsForRequiredS2SChannels(t *testing.T) {
	sm := renderJavaReadinessSM(t, true, true)
	if strings.Contains(sm, "setReady(true)") {
		t.Fatalf("run() must not flip SERVING before the required s2s channels are up")
	}
	for _, want := range []string{
		"AtomicBoolean healthOk = new AtomicBoolean(true);",
		"healthOk.set(effectiveStatus != InstanceStatus.INSTANCE_STATUS_UNHEALTHY)",
		"GrpcServer readyCtl = srv;",
		`startManagedThread("readiness", true,`,
		"driveReadiness(readyCtl, healthOk, readinessStreams)",
		"AtomicBoolean up_onEventStream = new AtomicBoolean(false);",
		`new RequiredChannels.RequiredStream("aegis.event_stream subscription (eventbus)", up_onEventStream)`,
		"subscriber.setUpFlag(up_onEventStream);",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("run() readiness wiring missing %q", want)
		}
	}
	drive := sm[strings.Index(sm, "private void driveReadiness("):]
	drive = drive[:strings.Index(drive, "\n    }\n")]
	for _, want := range []string{
		"new RequiredChannels(java.util.List.of(",
		"identityConn",
		"stateStoreConn",
		"boolean ready = healthOk.get() && down.isEmpty();",
		"readyCtl.setReady(ready);",
		`"readiness: NOT_SERVING, required s2s channel is down: "`,
	} {
		if !strings.Contains(drive, want) {
			t.Fatalf("driveReadiness missing %q:\n%s", want, drive)
		}
	}
}

func TestJavaReadinessIgnoresS2SClientAllTargets(t *testing.T) {
	sm := renderJavaReadinessSM(t, true, false)
	drive := sm[strings.Index(sm, "private void driveReadiness("):]
	drive = drive[:strings.Index(drive, "\n    }\n")]
	if strings.Contains(drive, "identityConn") || strings.Contains(drive, "stateStoreConn") {
		t.Fatalf("s2s_client_all targets must not gate readiness:\n%s", drive)
	}
}

func TestJavaReadinessWithoutHealthStateFlipsOnBootstrap(t *testing.T) {
	sm := renderJavaReadinessSM(t, false, true)
	if !strings.Contains(sm, "srv.setReady(true);") {
		t.Fatalf("without ServiceHealthState there are no s2s channels to wait for; bootstrap must flip SERVING")
	}
	if strings.Contains(sm, "driveReadiness(readyCtl") {
		t.Fatalf("driveReadiness must only run with ServiceHealthState")
	}
}

func TestJavaS2SReconnectBackoffStartsAtHalfSecond(t *testing.T) {
	sm := renderJavaReadinessSM(t, true, true)
	for _, stale := range []string{"long backoff = 2000;", "long backoff = 3000;", "Thread.sleep(2000)", "Math.min(backoff * 2, maxBackoff)"} {
		if strings.Contains(sm, stale) {
			t.Fatalf("ServiceManager still carries its own reconnect backoff: %q", stale)
		}
	}
	resolve := sm[strings.Index(sm, "public io.grpc.ManagedChannel resolveService("):]
	resolve = resolve[:strings.Index(resolve, "\n    }\n")]
	if !strings.Contains(resolve, "long backoff = ReconnectBackoff.INITIAL_MS;") || !strings.Contains(resolve, "backoff = ReconnectBackoff.next(backoff);") {
		t.Fatalf("resolveService must use the shared backoff:\n%s", resolve)
	}
	if !strings.Contains(sm, "subBackoff = ReconnectBackoff.next(subBackoff);") {
		t.Fatalf("subscription outer loop must use the shared backoff")
	}
}
