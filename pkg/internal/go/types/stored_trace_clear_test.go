// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// #313: a stored value never keeps a trace. The state client's Set (singleton)
// and Put<F> (collection) send the state store a COPY of the caller's value
// with the injected trace_id cleared, and only for a type that carries it.
func TestGoStateClientClearsInjectedTraceBeforeStoring(t *testing.T) {
	data := func(clear bool) stateClientData {
		return stateClientData{
			PackageName:           "demo",
			ClientTypeName:        "StateClient",
			StateServiceName:      "DemoStateService",
			StateRootName:         "DemoState",
			TypesImportPath:       "example.com/project/tacplus/types/services/demo",
			ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
			TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
			Singletons:            []singletonBoundary{{FieldName: "Config", TypeName: "Config", GoType: "Config", ClearTrace: clear}},
			Collections: []collectionBoundary{{
				FieldName: "Players", TypeName: "Player", GoType: "Player",
				KeyFieldName: "PlayerId", KeyGoType: "string", ClearTrace: clear,
			}},
		}
	}

	traced := renderStateWrapperForTest(t, data(true))
	if _, err := format.Source([]byte(traced)); err != nil {
		t.Fatalf("traced state wrapper is not valid Go: %v\n%s", err, traced)
	}
	for _, want := range []string{
		"\t// A stored value never keeps a trace (#313).\n\tif v != nil {\n\t\tv = proto.Clone(v).(*types.Config)\n\t\tv.TraceId = \"\"\n\t}\n\treturn w.parent.stateStub.SetConfig(ctx, v)",
		"\t// A stored value never keeps a trace (#313).\n\tif value != nil {\n\t\tvalue = proto.Clone(value).(*types.Player)\n\t\tvalue.TraceId = \"\"\n\t}\n\tresp, err := c.stateStub.PutPlayers(",
	} {
		if !strings.Contains(traced, want) {
			t.Fatalf("traced state wrapper missing the copy-and-clear before the store write %q:\n%s", want, traced)
		}
	}

	// Paired control: a type without the injected trace generates as before.
	plain := renderStateWrapperForTest(t, data(false))
	for _, banned := range []string{"#313", "TraceId = \"\""} {
		if strings.Contains(plain, banned) {
			t.Fatalf("untraced state wrapper must not clear a trace (%q):\n%s", banned, plain)
		}
	}
	if !strings.Contains(plain, "\t\treturn nil, err\n\t}\n\treturn w.parent.stateStub.SetConfig(ctx, v)") {
		t.Fatalf("untraced singleton Set changed shape:\n%s", plain)
	}
}

// #313: a store-derived subscription is the record store's write path. Its
// dispatch clears the event's trace_id after the trace-event publish (which
// reads it) and before the handler. A declared subscription keeps the trace.
func TestGoStoreIngestSubscriptionClearsTraceBeforeHandler(t *testing.T) {
	cfg := &tactype.Config{
		ModuleBasePath: "example.com/demo/model",
		SelfPackage:    "demo",
		Subscriptions: []tactype.SubscriptionEntry{
			{SourcePackage: "sensor", SourceStateRoot: "SensorBus", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", StoreIngest: true, SourceCarriesTrace: true},
			{SourcePackage: "alerts", SourceStateRoot: "AlertBus", SourceField: "alerts", SourceTypeFqn: "alerts.AlertEvent", SourceCarriesTrace: true},
			{SourcePackage: "audit", SourceStateRoot: "AuditBus", SourceField: "entries", SourceTypeFqn: "audit.AuditEntry", StoreIngest: true},
		},
	}
	data, err := buildServiceManagerData(cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildServiceManagerData: %v", err)
	}
	clears := map[string]bool{}
	for _, s := range data.Subscriptions {
		clears[s.TypeName] = s.ClearStoredTrace
	}
	if !clears["SensorEvent"] || clears["AlertEvent"] || clears["AuditEntry"] {
		t.Fatalf("ClearStoredTrace = %v, want only the traced store-ingest SensorEvent", clears)
	}

	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := format.Source(out.Bytes()); err != nil {
		t.Fatalf("rendered service manager is not valid Go: %v", err)
	}
	rendered := out.String()
	const clear = "// A stored value never keeps a trace (#313).\nevent.TraceId = \"\"\n"
	if got := strings.Count(rendered, clear); got != 1 {
		t.Fatalf("want exactly one cleared dispatch (the traced store-ingest one), got %d:\n%s", got, rendered)
	}
	// Order inside the SensorEvent dispatch: publish (reads the trace), clear,
	// then the handler.
	dispatch := strings.Index(rendered, "sub.Subscribe(subCtx, func(ctx context.Context, event *sensor_pb.SensorEvent, meta *eb_pb.RecordMeta) error {")
	if dispatch < 0 {
		t.Fatalf("SensorEvent dispatch not rendered:\n%s", rendered)
	}
	block := rendered[dispatch:]
	block = block[:strings.Index(block, "WithEarliest()")]
	publish := strings.Index(block, "TraceId: event.GetTraceId(),")
	at := strings.Index(block, clear)
	handler := strings.Index(block, "Func(ctx, event, meta)")
	if publish < 0 || at < 0 || handler < 0 || !(publish < at && at < handler) {
		t.Fatalf("the clear must sit after the trace-event publish and before the handler (publish=%d clear=%d handler=%d):\n%s", publish, at, handler, block)
	}
}
