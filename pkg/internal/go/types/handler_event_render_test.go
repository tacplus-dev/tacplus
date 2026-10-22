// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// recordDispatchFiles is one source, sensor.SensorEvent (with the injected
// noop), and two timeseries projections of it in its own package.
func recordDispatchFiles() []model.ProtoFile {
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

// Handle<Source> hands each projection to its own typed insert callback; the
// untyped TimeseriesInsertFunc (table, columns, []interface{}) is gone.
func TestHandleSourceTakesOneTypedInsertPerProjection(t *testing.T) {
	out := t.TempDir()
	if err := generateEventHandlers(recordDispatchFiles(), out); err != nil {
		t.Fatalf("generateEventHandlers: %v", err)
	}
	dir := filepath.Join(out, "golang", "services", "sensor")
	b, err := os.ReadFile(filepath.Join(dir, "extraction_handler_sensor_event.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"func HandleSensorEvent(ctx context.Context, event *SensorEvent, insertSensorReading func(ctx context.Context, row *SensorReading) error, insertSensorFault func(ctx context.Context, row *SensorFault) error) error {",
		"if event.Noop {\n\t\treturn nil\n\t}",
		"if row := ExtractSensorReadingFromSensorEvent(event); row != nil {\n\t\t\tif err := insertSensorReading(ctx, row); err != nil {",
		"if row := ExtractSensorFaultFromSensorEvent(event); row != nil {\n\t\t\tif err := insertSensorFault(ctx, row); err != nil {",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated handler missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "TimeseriesInsertFunc") || strings.Contains(src, "[]interface{}") {
		t.Fatalf("generated handler still carries the untyped row:\n%s", src)
	}
	if _, err := os.Stat(filepath.Join(dir, "extraction_handler_support.go")); !os.IsNotExist(err) {
		t.Fatalf("extraction_handler_support.go (the untyped TimeseriesInsertFunc) must not be generated: %v", err)
	}
}

// The service manager holds one typed insert callback per projection, set
// once however many subscriptions deliver its source, and passes them to
// Handle<Source> in its parameter order from every one of those subscriptions.
func TestGoServiceManagerRegistersTypedInsertsPerProjection(t *testing.T) {
	cfg := &tactype.Config{
		ModuleBasePath: "example.com/demo/model",
		SelfPackage:    "demo",
		Subscriptions: []tactype.SubscriptionEntry{
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "replay", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
		},
	}
	data, err := buildServiceManagerData(cfg, nil, recordDispatchFiles())
	if err != nil {
		t.Fatalf("buildServiceManagerData: %v", err)
	}
	if len(data.TimeseriesInserts) != 2 {
		t.Fatalf("TimeseriesInserts = %+v, want SensorReading and SensorFault once each", data.TimeseriesInserts)
	}

	tmplContent, err := loadTemplate("service_manager.go.tmpl")
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
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	src := string(formatted)

	for _, want := range []string{
		"func (sm *ServiceManager) SetSensorReadingInsertFunc(fn func(ctx context.Context, row *sensor_pb.SensorReading) error) {",
		"func (sm *ServiceManager) SetSensorFaultInsertFunc(fn func(ctx context.Context, row *sensor_pb.SensorFault) error) {",
	} {
		if strings.Count(src, want) != 1 {
			t.Fatalf("rendered service manager must declare %q exactly once:\n%s", want, src)
		}
	}
	call := "return sensor_pb.HandleSensorEvent(ctx, event, sm.insertSensorReadingFunc, sm.insertSensorFaultFunc)"
	if n := strings.Count(src, call); n != 2 {
		t.Fatalf("each of the two subscriptions must dispatch through %q, found %d", call, n)
	}
	if strings.Contains(src, "SetTimeseriesInsertFunc") || strings.Contains(src, "tsInsertFunc") {
		t.Fatalf("rendered service manager keeps the untyped SetTimeseriesInsertFunc")
	}
}
