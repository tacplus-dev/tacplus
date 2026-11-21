// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// #313: a stored value never keeps a trace. The state client's singleton set
// and collection put_<f> clear the injected trace_id on the caller's owned
// value before sending it to the state store, and only for a type that
// carries it. Go/Java twins: TestGoStateClientClearsInjectedTraceBeforeStoring,
// TestJavaStateClientClearsInjectedTraceBeforeStoring.
func TestRustStateClientClearsInjectedTraceBeforeStoring(t *testing.T) {
	data := func(clear bool) rustStateWrapperData {
		return rustStateWrapperData{
			Header:                 "// generated for test",
			StateRootName:          "DemoState",
			TypesCratePath:         "services::demo",
			PackageName:            "demo",
			StateServiceName:       "DemoStateService",
			StateServiceModule:     "demo_state_service_client",
			StateServiceClientType: "DemoStateServiceClient",
			Singletons: []rustSingletonBoundary{{
				FieldName: "config", FieldPascal: "Config", TypeName: "Config", DefaultsFn: "config_defaults", ClearTrace: clear,
			}},
			Collections: []rustCollectionBoundary{{
				FieldName: "players", FieldPascal: "Players", TypeName: "Player", KeyFieldName: "player_id", MaxEntries: 10, ClearTrace: clear,
			}},
		}
	}
	const clear = "        // A stored value never keeps a trace (#313). `value` is the caller's\n" +
		"        // owned copy, so clearing it here never touches the caller's message.\n" +
		"        let mut value = value;\n" +
		"        value.trace_id = String::new();\n"

	traced := renderRustStateWrapperForTest(t, data(true))
	for _, want := range []string{
		"pub async fn set(&self, value: Config) -> Result<Config, tonic::Status> {\n" + clear +
			"        let req = self.inner.authorized_request(value).await?;",
		"pub async fn put_players_traced(&self, key: String, value: Player, trace_id: &str) -> Result<(), tonic::Status> {\n" + clear +
			"        let req = self.authorized_request(PlayersPutRequest {",
	} {
		if !strings.Contains(traced, want) {
			t.Fatalf("traced state client missing the clear before the store write %q:\n%s", want, traced)
		}
	}
	// One for set, one for the inner put_players; the outer put_players only
	// delegates.
	if got := strings.Count(traced, clear); got != 2 {
		t.Fatalf("want the clear on set and the inner put_players only, got %d", got)
	}

	// Paired control: a type without the injected trace generates as before.
	plain := renderRustStateWrapperForTest(t, data(false))
	// The requests carry their own trace_id (#327); the VALUE is never touched.
	if strings.Contains(plain, "value.trace_id") || strings.Contains(plain, "#313") {
		t.Fatalf("untraced state client must not clear a trace:\n%s", plain)
	}
}

// #313: a store-derived subscription is the record store's write path. Its
// dispatch clears the event's trace_id before the handler (and so before the
// record dispatch). A declared subscription keeps the trace.
func TestRustStoreIngestSubscriptionClearsTraceBeforeHandler(t *testing.T) {
	dir := t.TempDir()
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	cfg := &tactype.Config{
		CrateBasePath: "demo-model", SelfPackage: "demo", FlatProtoDir: dir, AnnotatedProtoDir: dir,
		Subscriptions: []tactype.SubscriptionEntry{
			{SourcePackage: "sensor", SourceStateRoot: "SensorBus", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services", StoreIngest: true, SourceCarriesTrace: true},
			{SourcePackage: "alerts", SourceStateRoot: "AlertBus", SourceField: "alerts", SourceTypeFqn: "alerts.AlertEvent", SourceProtoDir: "services", SourceCarriesTrace: true},
			{SourcePackage: "audit", SourceStateRoot: "AuditBus", SourceField: "entries", SourceTypeFqn: "audit.AuditEntry", SourceProtoDir: "services", StoreIngest: true},
		},
	}
	data, err := buildSMTemplateData(cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildSMTemplateData: %v", err)
	}
	clears := map[string]bool{}
	for _, s := range data.Subscriptions {
		clears[s.TypeName] = s.ClearStoredTrace
	}
	if !clears["SensorEvent"] || clears["AlertEvent"] || clears["AuditEntry"] {
		t.Fatalf("ClearStoredTrace = %v, want only the traced store-ingest SensorEvent", clears)
	}

	const clear = "// A stored value never keeps a trace (#313).\n" +
		"                            let mut event = event;\n" +
		"                            event.trace_id = String::new();\n"
	for _, dispatchFn := range []string{"", "crate::types::services::sensor::handle_sensor_event"} {
		d := data
		d.HasSubscriptions = true
		d.Subscriptions = append([]subscriptionEntry(nil), data.Subscriptions...)
		for i := range d.Subscriptions {
			d.Subscriptions[i].DispatchFn = dispatchFn
		}
		rendered := renderRustServiceManagerForTest(t, d)
		if got := strings.Count(rendered, clear); got != 1 {
			t.Fatalf("dispatchFn=%q: want exactly one cleared dispatch, got %d:\n%s", dispatchFn, got, rendered)
		}
		at := strings.Index(rendered, clear)
		handler := strings.Index(rendered[at:], "h.on_")
		if handler < 0 || !strings.Contains(rendered[at+handler:at+handler+80], "sensor_bus_events") {
			t.Fatalf("dispatchFn=%q: the clear must precede the SensorEvent handler:\n%s", dispatchFn, rendered[at:])
		}
	}
}
