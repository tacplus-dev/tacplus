// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// A generated Rust service used to flip SERVING (k8s Ready) as soon as
// bootstrap finished, before its s2s channels existed. After an eventbus
// restart scotty logged "Failed to create channel to service, retrying ...
// retry_in 30s" for 1-2 minutes while its pod was already Ready. Readiness now
// also requires every required s2s channel, and reconnects back off from 0.5s
// instead of starting at 2-3s and sitting at the 30s cap.

func renderS2SReadinessSM(t *testing.T, s2sTargetsRequired bool) string {
	t.Helper()
	return renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:          "scotty",
		StateStoreServiceID:   "statestore",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasServiceHealthState: true,
		HasS2SClients:         true,
		S2STargetsRequired:    s2sTargetsRequired,
		TargetServices: []TargetServiceInfo{
			{ServiceID: "identity", AccessorName: "identity"},
			{ServiceID: "statestore", AccessorName: "statestore"},
		},
		HasSubscriptions: true,
		Subscriptions: []subscriptionEntry{{
			FieldSnake:        "event_stream",
			HandlerMethodName: "on_event_stream",
			SubscriberMod:     "aegis_event_stream",
			SourcePackage:     "aegis",
			TypeName:          "TopologyEvent",
			TargetService:     "eventbus",
		}},
	})
}

func fnBody(t *testing.T, rendered, signature string) string {
	t.Helper()
	start := strings.Index(rendered, signature)
	if start < 0 {
		t.Fatalf("missing %q", signature)
	}
	end := strings.Index(rendered[start:], "\n    }\n")
	if end < 0 {
		t.Fatalf("no end for %q", signature)
	}
	return rendered[start : start+end]
}

func TestRustReadinessWaitsForRequiredS2SChannels(t *testing.T) {
	rendered := renderS2SReadinessSM(t, true)

	// Bootstrap no longer flips SERVING unconditionally; drive_readiness does.
	runBody := rendered[strings.Index(rendered, "pub async fn run("):]
	if strings.Contains(runBody, `grpc_health.set_service_status("", tonic_health::ServingStatus::Serving).await;`) {
		t.Fatalf("run() must not flip SERVING before the required s2s channels are up")
	}
	for _, want := range []string{
		"health_ok.store(status != InstanceStatus::Unhealthy, std::sync::atomic::Ordering::Relaxed);",
		"self.spawn_owned(self.clone().drive_readiness(",
		"sub_up,",
	} {
		if !strings.Contains(runBody, want) {
			t.Fatalf("run() readiness wiring missing %q", want)
		}
	}

	drive := fnBody(t, rendered, "async fn drive_readiness(")
	for _, want := range []string{
		"servicemanager::RequiredChannels::new(",
		"self.identity_conn.clone(),",
		"self.state_store_conn.clone(),",
		"let down = required.first_down().await;",
		"let ready = health_ok.load(std::sync::atomic::Ordering::Relaxed) && down.is_none();",
		"tonic_health::ServingStatus::NotServing",
		"tonic_health::ServingStatus::Serving",
		`grpc_health.set_service_status("", status).await;`,
	} {
		if !strings.Contains(drive, want) {
			t.Fatalf("drive_readiness missing %q:\n%s", want, drive)
		}
	}

	// The subscription channel reports up only between resolve and the end of
	// the subscribe call.
	loop := rendered[strings.Index(rendered, `sub_up.push(("aegis.event_stream subscription (eventbus)".to_string(), up.clone()));`):]
	resolveIdx := strings.Index(loop, `sub_sm.resolve_service("eventbus")`)
	upIdx := strings.Index(loop, "up.store(true, std::sync::atomic::Ordering::Relaxed);")
	subscribeIdx := strings.Index(loop, "subscriber.subscribe(handler_fn.clone())")
	downIdx := strings.Index(loop, "up.store(false, std::sync::atomic::Ordering::Relaxed);")
	if resolveIdx < 0 || upIdx < 0 || subscribeIdx < 0 || downIdx < 0 ||
		!(resolveIdx < upIdx && upIdx < subscribeIdx && subscribeIdx < downIdx) {
		t.Fatalf("subscription must mark up after resolve and down after subscribe ends (resolve=%d up=%d subscribe=%d down=%d)",
			resolveIdx, upIdx, subscribeIdx, downIdx)
	}
}

// Under s2s_client_all every gRPC service is a target; gating readiness on all
// of them would keep a pod unready whenever any service anywhere is down.
func TestRustReadinessIgnoresS2SClientAllTargets(t *testing.T) {
	drive := fnBody(t, renderS2SReadinessSM(t, false), "async fn drive_readiness(")
	if strings.Contains(drive, "_conn.clone()") {
		t.Fatalf("s2s_client_all targets must not gate readiness:\n%s", drive)
	}
}

// Without ServiceHealthState there is no health reporter and no s2s plane;
// bootstrap still flips SERVING directly.
func TestRustReadinessWithoutHealthStateFlipsOnBootstrap(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel: "plain",
		HasGrpc:      true,
		HasTlsGrpc:   true,
	})
	if !strings.Contains(rendered, `grpc_health.set_service_status("", tonic_health::ServingStatus::Serving).await;`) {
		t.Fatalf("bootstrap SERVING flip missing when there is no ServiceHealthState")
	}
	if strings.Contains(rendered, "async fn drive_readiness(") || strings.Contains(rendered, ".drive_readiness(") {
		t.Fatalf("drive_readiness must only exist with ServiceHealthState")
	}
}

func TestRustS2SReconnectBackoffStartsAtHalfSecond(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/chassis/servicemanager/src/backoff.rs.tmpl")
	if err != nil {
		t.Fatalf("read backoff template: %v", err)
	}
	for _, want := range []string{
		"pub const RECONNECT_INITIAL_BACKOFF: Duration = Duration::from_millis(500);",
		"pub const RECONNECT_MAX_BACKOFF: Duration = Duration::from_secs(30);",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("backoff chassis missing %q", want)
		}
	}

	rendered := renderS2SReadinessSM(t, true)
	resolve := fnBody(t, rendered, "pub async fn resolve_service(")
	if !strings.Contains(resolve, "let mut backoff = servicemanager::RECONNECT_INITIAL_BACKOFF;") {
		t.Fatalf("resolve_service must start at RECONNECT_INITIAL_BACKOFF:\n%s", resolve)
	}
	if n := strings.Count(resolve, "backoff = servicemanager::next_reconnect_backoff(backoff);"); n != 4 {
		t.Fatalf("every resolve_service retry must use next_reconnect_backoff; got %d sites", n)
	}
	if strings.Contains(resolve, "from_secs(2)") {
		t.Fatalf("resolve_service must not start at 2s any more")
	}
}
