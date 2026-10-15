// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// rustRecordDispatchFiles is one source, sensor.SensorEvent (with the
// injected noop), and two timeseries projections of it in its own package.
func rustRecordDispatchFiles() []model.ProtoFile {
	from := map[string]model.AnnotationValue{
		"timeseries_type":         {BoolValue: true},
		"timeseries_table_source": {StringValue: "sensor.SensorEvent"},
	}
	return []model.ProtoFile{{
		Package: "sensor",
		RelPath: "services/sensor.proto",
		Messages: []model.Message{
			{Name: "SensorEvent", Fields: []model.Field{
				{Name: "id", Type: "string", Number: 1},
				{Name: "noop", Type: "bool", Number: 201},
			}},
			{Name: "SensorReading", Annotations: from},
			{Name: "SensorFault", Annotations: from},
		},
	}}
}

// handle_<source> is async over the typed <Projection>Row: one
// <Projection>Insert (an Arc'd fn returning a boxed Send future) per
// projection, the trace-round noop skip, and each row awaited.
func TestRustRecordDispatchAwaitsEachProjectionsTypedRow(t *testing.T) {
	out := t.TempDir()
	if err := generateRustExtractionHelpers(rustRecordDispatchFiles(), out); err != nil {
		t.Fatalf("generateRustExtractionHelpers: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "types", "services", "sensor", "extraction_project_sensor_event.rs"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"pub type SensorReadingInsert = std::sync::Arc<\n    dyn Fn(SensorReadingRow) -> std::pin::Pin<Box<dyn std::future::Future<Output = Result<(), Box<dyn std::error::Error + Send + Sync>>> + Send>>",
		"pub type SensorFaultInsert = std::sync::Arc<\n    dyn Fn(SensorFaultRow) ->",
		"pub async fn handle_sensor_event(\n    event: &SensorEvent,\n    insert_sensor_reading: Option<&SensorReadingInsert>,\n    insert_sensor_fault: Option<&SensorFaultInsert>,\n) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {",
		"if event.noop {\n        return Ok(());\n    }",
		"if let Some(insert) = insert_sensor_reading {\n        if let Some(row) = extract_sensor_reading_from_sensor_event(event) {\n            insert(SensorReadingRow::from_proto(&row)).await?;",
		"if let Some(insert) = insert_sensor_fault {\n        if let Some(row) = extract_sensor_fault_from_sensor_event(event) {\n            insert(SensorFaultRow::from_proto(&row)).await?;",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated dispatch missing %q:\n%s", want, src)
		}
	}
}

// The ServiceManager registers one async typed insert per projection, however
// many subscriptions deliver its source, and each subscription awaits
// handle_<source> after the Handler method.
func TestRustServiceManagerRegistersTypedInsertsPerProjection(t *testing.T) {
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
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "replay", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
		},
	}
	data, err := buildSMTemplateData(cfg, nil, nil, rustRecordDispatchFiles())
	if err != nil {
		t.Fatalf("buildSMTemplateData: %v", err)
	}
	if len(data.TimeseriesInserts) != 2 {
		t.Fatalf("TimeseriesInserts = %+v, want SensorReading and SensorFault once each", data.TimeseriesInserts)
	}
	tmplContent, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	src := buf.String()

	const mod = "crate::types::services::sensor"
	for _, want := range []string{
		"insert_sensor_reading: Option<" + mod + "::SensorReadingInsert>,",
		"insert_sensor_reading: None,",
		"pub fn set_sensor_reading_insert<F, Fut>(&mut self, f: F)",
		"F: Fn(" + mod + "::SensorReadingRow) -> Fut + Send + Sync + 'static,",
		"pub fn set_sensor_fault_insert<F, Fut>(&mut self, f: F)",
	} {
		if strings.Count(src, want) != 1 {
			t.Fatalf("rendered ServiceManager must contain %q exactly once", want)
		}
	}
	call := mod + "::handle_sensor_event(&event, sm.insert_sensor_reading.as_ref(), sm.insert_sensor_fault.as_ref()).await"
	if n := strings.Count(src, call); n != 2 {
		t.Fatalf("each of the two subscriptions must await %q, found %d", call, n)
	}
	if !strings.Contains(src, "h.on_events_sensor_sensor_state_events(event.clone(), meta).await?;\n"+strings.Repeat(" ", 28)+call) {
		t.Fatal("the events subscription must await its Handler method before the dispatch")
	}
}
