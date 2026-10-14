// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// agentEventSource returns a minimal record message (transformation source) with
// the given record_key annotation (empty string = default "event_id").
func agentEventSource(recordKey string) (parser.MessageDescriptor, *parser.FileDescriptor) {
	anns := map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}}
	if recordKey != "" {
		anns["record_key"] = model.AnnotationValue{StringValue: recordKey}
	}
	msg := parser.MessageDescriptor{
		Name:        "AgentEvent",
		Annotations: anns,
		Fields: []parser.FieldDescriptor{
			{Name: "event_id", Type: "string", TypeKind: "scalar"},
			{Name: "agent_id", Type: "string", TypeKind: "scalar"},
			{Name: "email", Type: "string", TypeKind: "scalar"},
			{Name: "timestamp_ms", Type: "int64", TypeKind: "scalar"},
		},
	}
	fd := &parser.FileDescriptor{Package: "agentsensor", Messages: []parser.MessageDescriptor{msg}}
	return msg, fd
}

func TestTransformationFailsLoudWhenRecordKeyFieldMissing(t *testing.T) {
	msg, fd := agentEventSource("nonexistent_field")
	tp := model.TransformationEntry{
		Name:   "AgentEventRecord",
		Fields: []model.TransformationFieldEntry{{Source: "timestamp_ms", OrderKey: 1}},
	}
	if _, err := generateTransformation(tp, "agentsensor.AgentEvent", msg, fd); err == nil {
		t.Fatal("expected generateTransformation to fail when the record key field is missing")
	}
}

func TestTransformationInjectsRecordKeyWhenAbsent(t *testing.T) {
	msg, fd := agentEventSource("")
	tp := model.TransformationEntry{
		Name: "AgentEventRecord",
		Fields: []model.TransformationFieldEntry{
			{Source: "timestamp_ms", OrderKey: 1},
		},
		Dedup: &model.DedupPolicyEntry{Strategy: model.DedupLatestByVersion, VersionField: "timestamp_ms", KeyFields: []string{"timestamp_ms"}},
	}

	got, err := generateTransformation(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTransformation failed: %v", err)
	}

	for _, want := range []string{
		"string event_id = 2 [",
		`(spec.projection_source) = "event_id"`,
		"(spec.projection_record_key) = true",
		"strategy: DEDUP_LATEST_BY_VERSION",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transformation missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker, got %d:\n%s", n, got)
	}
}

func TestTransformationMarksExistingRecordKeyField(t *testing.T) {
	msg, fd := agentEventSource("")
	tp := model.TransformationEntry{
		Name: "AgentEventRecord",
		Fields: []model.TransformationFieldEntry{
			{Source: "event_id", PartitionKey: true},
			{Source: "timestamp_ms", OrderKey: 1},
		},
	}

	got, err := generateTransformation(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTransformation failed: %v", err)
	}

	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker (no injected duplicate), got %d:\n%s", n, got)
	}
	if strings.Contains(got, "= 3 [") {
		t.Fatalf("unexpected injected field when record key already projected:\n%s", got)
	}
}

// A transformation over a plain (non-record) message is a pure typed conversion:
// there is no record to hydrate back to, so no record key is injected and no
// delivery declaration is consulted.
func TestTransformationOverNonRecordInjectsNoRecordKey(t *testing.T) {
	msg, fd := agentEventSource("")
	delete(msg.Annotations, "record")
	tp := model.TransformationEntry{
		Name:   "AgentEventRecord",
		Fields: []model.TransformationFieldEntry{{Source: "timestamp_ms", OrderKey: 1}},
	}

	got, err := generateTransformation(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTransformation failed: %v", err)
	}
	if strings.Contains(got, "projection_record_key") {
		t.Fatalf("non-record source must not get an injected record key:\n%s", got)
	}
	if !strings.Contains(got, "option (spec.timeseries_type)") {
		t.Fatalf("transformation did not generate its derived type:\n%s", got)
	}
}

// Retention is delivery-side state: a transformation never emits one, even when
// its source record declares one.
func TestTransformationEmitsNoRetention(t *testing.T) {
	msg, fd := agentEventSource("")
	tp := model.TransformationEntry{
		Name:   "AgentEventRecord",
		Fields: []model.TransformationFieldEntry{{Source: "timestamp_ms", OrderKey: 1}},
	}
	got, err := generateTransformation(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTransformation failed: %v", err)
	}
	if strings.Contains(got, "retention:") {
		t.Fatalf("transformation must not emit retention:\n%s", got)
	}
}
