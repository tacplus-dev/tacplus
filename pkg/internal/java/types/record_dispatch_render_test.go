// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// <Source>Projections carries the record dispatch, Go's Handle<Source>: one
// typed, synchronous <Projection>Insert per projection, the trace-round noop
// skip, and each extracted row handed to its own callback.
func TestJavaRecordDispatchHandsEachProjectionItsTypedInsert(t *testing.T) {
	tmplStr, err := loadTemplate("extraction_projections.java.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("x").Parse(tmplStr))
	data := javaExtractionTemplateData{
		Package:             "p",
		ClassName:           "SensorEventProjections",
		SourceType:          "SensorEvent",
		SourceFqn:           "p.S.SensorEvent",
		ToProjectionsMethod: "sensorEventToProjections",
		HandleMethod:        "handleSensorEvent",
		HasNoop:             true,
		Projections: []javaExtractionProjection{
			{TypeName: "SensorReading", TypeFqn: "p.S.SensorReading", FieldName: "sensorReading", ExtractMethod: "extractSensorReadingFromSensorEvent"},
			{TypeName: "SensorFault", TypeFqn: "p.S.SensorFault", FieldName: "sensorFault", ExtractMethod: "extractSensorFaultFromSensorEvent"},
		},
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"public interface SensorReadingInsert {\n        void insert(p.S.SensorReading row) throws Exception;\n    }",
		"public interface SensorFaultInsert {\n        void insert(p.S.SensorFault row) throws Exception;\n    }",
		"public static void handleSensorEvent(p.S.SensorEvent event, SensorReadingInsert insertSensorReading, SensorFaultInsert insertSensorFault) throws Exception {",
		"if (event.getNoop()) {\n            return;\n        }",
		"if (insertSensorReading != null) {\n            p.S.SensorReading row = extractSensorReadingFromSensorEvent(event);\n            if (row != null) {\n                insertSensorReading.insert(row);\n            }\n        }",
		"if (insertSensorFault != null) {\n            p.S.SensorFault row = extractSensorFaultFromSensorEvent(event);\n            if (row != null) {\n                insertSensorFault.insert(row);\n            }\n        }",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered class missing %q:\n%s", want, out)
		}
	}

	data.HasNoop = false
	b.Reset()
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "getNoop()") {
		t.Fatal("a source without the injected noop must not be read for it")
	}
}

// The ServiceManager registers one typed insert per projection, however many
// subscriptions deliver its source, and every one of them dispatches through
// <Source>Projections.handle<Source> after the Handler method.
func TestJavaServiceManagerRegistersTypedInsertsPerProjection(t *testing.T) {
	from := map[string]model.AnnotationValue{
		"timeseries_type":         {BoolValue: true},
		"timeseries_table_source": {StringValue: "sensor.SensorEvent"},
	}
	files := []model.ProtoFile{{
		Package: "sensor",
		RelPath: "services/sensor.proto",
		Messages: []model.Message{
			{Name: "SensorEvent", Fields: []model.Field{{Name: "noop", Type: "bool", Number: 201}}},
			{Name: "SensorReading", Annotations: from},
			{Name: "SensorFault", Annotations: from},
		},
	}}
	cfg := &tactype.Config{
		JavaPackageBase: "com.example",
		SelfPackage:     "demo",
		Subscriptions: []tactype.SubscriptionEntry{
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "events", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
			{SourcePackage: "sensor", SourceStateRoot: "SensorState", SourceField: "replay", SourceTypeFqn: "sensor.SensorEvent", SourceProtoDir: "services"},
		},
	}
	data, err := buildJavaSmTemplateData(cfg, nil, nil, "", nil, files)
	if err != nil {
		t.Fatalf("buildJavaSmTemplateData: %v", err)
	}
	if len(data.TimeseriesInserts) != 2 {
		t.Fatalf("TimeseriesInserts = %+v, want SensorReading and SensorFault once each", data.TimeseriesInserts)
	}
	data.HasSubscriptions = true
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", data)

	const projections = "com.example.types.services.sensor.SensorEventProjections"
	for _, want := range []string{
		"private volatile " + projections + ".SensorReadingInsert insertSensorReading;",
		"public void setSensorReadingInsert(" + projections + ".SensorReadingInsert fn) {",
		"public void setSensorFaultInsert(" + projections + ".SensorFaultInsert fn) {",
	} {
		if strings.Count(sm, want) != 1 {
			t.Fatalf("rendered ServiceManager must declare %q exactly once", want)
		}
	}
	call := projections + ".handleSensorEvent(event, insertSensorReading, insertSensorFault);"
	if n := strings.Count(sm, call); n != 2 {
		t.Fatalf("each of the two subscriptions must dispatch through %q, found %d", call, n)
	}
}
