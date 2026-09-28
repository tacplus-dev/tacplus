// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

func TestEnsureEventBusSupportProtoWritesCanonicalMessages(t *testing.T) {
	dir := t.TempDir()

	relPath, err := EnsureEventBusSupportProto(dir, EventBusSupportProtoOptions{
		GoPackage: "example.com/project/types/generated/eventbus_support",
	})
	if err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if relPath != EventBusSupportProtoPath {
		t.Fatalf("relPath = %q, want %q", relPath, EventBusSupportProtoPath)
	}

	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(EventBusSupportProtoPath)))
	if err != nil {
		t.Fatalf("read support proto: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`package eventbus_support;`,
		`option go_package = "example.com/project/types/generated/eventbus_support";`,
		`message RecordMeta { bytes key = 1; string topic = 2; int32 partition = 3; int64 offset = 4; google.protobuf.Timestamp timestamp = 5; }`,
		`message SubscribeRequest {`,
		`int64 offset = 1;`,
		`bool latest = 3;`,
		`bool earliest = 4;`,
		`string consumer_group = 5;`,
		`message PublishResponse { int32 partition = 1; int64 offset = 2; }`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("support proto missing %q in:\n%s", want, text)
		}
	}
}

// A stale support proto on disk — the shape that shipped before babc6a0b — must
// be overwritten by the next generation, not preserved. The file is pure codegen
// output, so an exists-check gate would let a wrong wire shape survive every
// regeneration in a checked-in service tree.
func TestEnsureEventBusSupportProtoOverwritesStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash(EventBusSupportProtoPath))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := `syntax = "proto3";

package eventbus_support;

message PublishResponse { bool success = 1; string error = 2; int64 offset = 3; }
`
	if err := os.WriteFile(path, []byte(stale), 0644); err != nil {
		t.Fatalf("seed stale support proto: %v", err)
	}

	opts := EventBusSupportProtoOptions{GoPackage: "example.com/project/types/generated/eventbus_support"}
	if _, err := EnsureEventBusSupportProto(dir, opts); err != nil {
		t.Fatalf("EnsureEventBusSupportProto (first): %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read support proto: %v", err)
	}
	if strings.Contains(string(first), "string error = 2;") {
		t.Fatalf("stale PublishResponse survived regeneration:\n%s", first)
	}
	if !strings.Contains(string(first), "message PublishResponse { int32 partition = 1; int64 offset = 2; }") {
		t.Fatalf("support proto does not carry the canonical PublishResponse:\n%s", first)
	}

	// A second generation over the freshly written file must be a no-op change:
	// always-rewrite has to stay deterministic.
	if _, err := EnsureEventBusSupportProto(dir, opts); err != nil {
		t.Fatalf("EnsureEventBusSupportProto (second): %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read support proto: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("regeneration is not byte-identical:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestEventBusSupportProtoIsInfra(t *testing.T) {
	if !IsEventBusSupportProto(EventBusSupportProtoPath) {
		t.Fatalf("%s must be recognized as EventBus support proto", EventBusSupportProtoPath)
	}
	if !IsInfraProto(EventBusSupportProtoPath) {
		t.Fatalf("%s must be treated as infrastructure", EventBusSupportProtoPath)
	}
	if !IsInfraProtoFile(EventBusSupportProtoPath, &parser.FileDescriptor{Package: "eventbus_support"}) {
		t.Fatalf("support-only %s must be treated as infrastructure", EventBusSupportProtoPath)
	}
}

func TestAuthoredEventBusProtoIsNotInfraFile(t *testing.T) {
	fd := &parser.FileDescriptor{
		Package:  "eventbus",
		Services: []parser.ServiceDescriptor{{Name: "Admin"}},
		Messages: []parser.MessageDescriptor{{
			Name: "EventbusState",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "EventbusState"},
			},
		}},
	}

	if IsInfraProtoFile(EventBusSupportProtoPath, fd) {
		t.Fatalf("authored %s with services/state must not be treated as infrastructure", EventBusSupportProtoPath)
	}
}

func TestNeedsEventBusSupportProtoDetectsTopics(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	protoText := `syntax = "proto3";

package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo"};

message DemoEvent {
  string id = 1;
}

message DemoState {
  option (spec.service_event) = {label: "eventbus" export: true};

  repeated DemoEvent events = 1 [(spec.topic) = true];
}
`
	if err := os.WriteFile(filepath.Join(dir, "services", "demo.proto"), []byte(protoText), 0644); err != nil {
		t.Fatalf("write demo proto: %v", err)
	}
	if !NeedsEventBusSupportProto(dir) {
		t.Fatal("NeedsEventBusSupportProto returned false for an EventBus topic")
	}
}

// The synthesized SubscribeRequest used to be a strict subset of the canonical
// one: it carried only offset/latest/earliest/consumer_group, so a generated
// subscriber could not express seek-by-timestamp, replay speed, pause,
// backpressure, sampling or partition pinning even though the broker serves all
// of them. The wire contract gate allows a subset by design (SubscribeRequest is
// CLIENT→SERVER), so nothing caught it. This pins the full canonical shape.
func TestEnsureEventBusSupportProtoCarriesFullSubscribeRequest(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureEventBusSupportProto(dir, EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(EventBusSupportProtoPath)))
	if err != nil {
		t.Fatalf("read support proto: %v", err)
	}
	text := string(data)

	// Field numbers are the contract; they must match
	// platform/proto/services/eventbus.proto exactly.
	for _, want := range []string{
		"enum BackpressurePolicy {",
		"BACKPRESSURE_BUFFER = 0;",
		"BACKPRESSURE_DROP_OLD = 1;",
		"BACKPRESSURE_SAMPLE = 2;",
		"google.protobuf.Timestamp timestamp = 2;",
		"double speed_multiplier = 6;",
		"bool pause = 7;",
		"BackpressurePolicy backpressure = 8;",
		"int32 sample_interval = 9;",
		"repeated int32 partitions = 10;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("support proto missing %q in:\n%s", want, text)
		}
	}

	// timestamp = 2 belongs INSIDE the start_from oneof, next to offset/latest/
	// earliest — outside it, seeking by timestamp would not clear a prior seek.
	oneof := text[strings.Index(text, "oneof start_from {"):]
	oneof = oneof[:strings.Index(oneof, "}")]
	if !strings.Contains(oneof, "google.protobuf.Timestamp timestamp = 2;") {
		t.Fatalf("timestamp = 2 must be a start_from oneof member, got oneof:\n%s", oneof)
	}
	for _, notInOneof := range []string{"speed_multiplier", "pause", "backpressure", "sample_interval", "partitions"} {
		if strings.Contains(oneof, notInOneof) {
			t.Fatalf("%s must not be inside the start_from oneof, got oneof:\n%s", notInOneof, oneof)
		}
	}
}
