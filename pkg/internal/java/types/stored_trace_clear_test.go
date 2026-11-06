// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// #313: a stored value never keeps a trace. put<F> sends the state store a copy
// of the caller's (immutable) value with the injected trace_id cleared, and only
// for an item type that carries it. Go/Rust twins:
// TestGoStateClientClearsInjectedTraceBeforeStoring,
// TestRustStateClientClearsInjectedTraceBeforeStoring.
func TestJavaStateClientClearsInjectedTraceBeforeStoring(t *testing.T) {
	data := func(clear bool) javaStateWrapperData {
		return javaStateWrapperData{
			Header:           "// generated for test",
			Package:          "com.example.tacplus.s2sm.internal.state.services.demo.demostate",
			OuterClassName:   "Demo",
			StateRootName:    "DemoState",
			StateServiceName: "DemoStateService",
			GrpcServiceFQN:   "com.example.tacplus.types.services.demo.DemoStateServiceGrpc",
			TraceFQN:         "com.example.tacplus.types.sys.trace.Trace",
			Collections: []javaCollectionBoundary{{
				FieldName: "players", FieldPascal: "Players", TypeName: "Player", JavaType: "Demo.Player",
				KeyFieldName: "playerId", KeyFieldPascal: "PlayerId", MaxEntries: 10, ClearTrace: clear,
			}},
		}
	}

	traced := renderJavaStateWrapperForTest(t, data(true))
	want := "                        .setKey(key)\n" +
		"                        // A stored value never keeps a trace (#313).\n" +
		"                        .setValue(value.toBuilder().clearTraceId().build())\n" +
		"                        .build();\n" +
		"        stateStub.putPlayers(req);"
	if !strings.Contains(traced, want) {
		t.Fatalf("traced putPlayers must send a trace-cleared copy:\n%s", traced)
	}

	// Paired control: a type without the injected trace generates as before.
	plain := renderJavaStateWrapperForTest(t, data(false))
	if strings.Contains(plain, "clearTraceId") || strings.Contains(plain, "#313") {
		t.Fatalf("untraced state client must not clear a trace:\n%s", plain)
	}
	if !strings.Contains(plain, "                        .setKey(key)\n                        .setValue(value)\n                        .build();") {
		t.Fatalf("untraced putPlayers changed shape:\n%s", plain)
	}
}

// #313: a store-derived subscription is the record store's write path (a record
// has no write RPC). Its dispatch clears the event's trace_id after the
// store_epoch stamp and before the handler. A declared subscription keeps it.
func TestJavaStoreIngestSubscriptionClearsTraceBeforeHandler(t *testing.T) {
	cfg := &tactype.Config{
		JavaPackageBase: "com.example",
		SelfPackage:     "demo",
		Subscriptions: []tactype.SubscriptionEntry{
			{SourcePackage: "sensor", SourceStateRoot: "SensorBus", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", StoreIngest: true, SourceCarriesTrace: true, SourceStoreEpochField: "store_epoch_ms"},
			{SourcePackage: "alerts", SourceStateRoot: "AlertBus", SourceField: "alerts", SourceTypeFqn: "alerts.AlertEvent", SourceCarriesTrace: true},
			{SourcePackage: "audit", SourceStateRoot: "AuditBus", SourceField: "entries", SourceTypeFqn: "audit.AuditEntry", StoreIngest: true},
		},
	}
	data, err := buildJavaSmTemplateData(cfg, nil, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("buildJavaSmTemplateData: %v", err)
	}
	clears := map[string]bool{}
	for _, s := range data.Subscriptions {
		clears[s.TypeName] = s.ClearStoredTrace
	}
	if !clears["SensorEvent"] || clears["AlertEvent"] || clears["AuditEntry"] {
		t.Fatalf("ClearStoredTrace = %v, want only the traced store-ingest SensorEvent", clears)
	}

	rendered := renderJavaSm(t, data.Subscriptions)
	const clear = "                            // A stored value never keeps a trace (#313).\n" +
		"                            event = event.toBuilder().clearTraceId().build();\n"
	if got := strings.Count(rendered, clear); got != 1 {
		t.Fatalf("want exactly one cleared dispatch (the traced store-ingest one), got %d:\n%s", got, rendered)
	}
	at := strings.Index(rendered, clear)
	stamp := strings.Index(rendered, "event = event.toBuilder().setStoreEpochMs(System.currentTimeMillis()).build();")
	handler := strings.Index(rendered[at:], "handler.on")
	if stamp < 0 || stamp > at || handler < 0 || !strings.Contains(rendered[at:at+handler+80], "SensorBusEvents") {
		t.Fatalf("the clear must follow the store_epoch stamp and precede the SensorEvent handler:\n%s", rendered)
	}
}
