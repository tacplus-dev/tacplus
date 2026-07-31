// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func TestTopicSubscribeStreamsMetadataEnvelope(t *testing.T) {
	stateRoot := topicStateRootForTest()
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name, nil)

	for _, want := range []string{
		"rpc SubscribeToEvents(trace.TraceRequest) returns (stream DemoEventBusEventsSubscribeRecord);",
	} {
		if !strings.Contains(result.serviceProto, want) {
			t.Fatalf("topic Subscribe schema missing %q in:\n%s", want, result.serviceProto)
		}
	}
	assertGeneratedMessageHasNoTraceFields(t, result.serviceProto, "DemoEventBusEventsSubscribeRecord")
	if strings.Contains(result.serviceProto, "rpc SubscribeToEvents(trace.TraceRequest) returns (stream DemoEvent);") {
		t.Fatalf("topic Subscribe must stream metadata envelopes, not bare payloads:\n%s", result.serviceProto)
	}
	if !result.needs.eventBusRecordMeta {
		t.Fatalf("topic Subscribe envelope must require EventBusRecordMeta")
	}
	if !result.needs.timestampImport {
		t.Fatalf("EventBusRecordMeta must require google.protobuf.Timestamp import")
	}
}

func TestAppendStateServiceEmitsEventBusRecordMeta(t *testing.T) {
	dir := t.TempDir()
	protoPath := filepath.Join(dir, "demo.proto")
	err := os.WriteFile(protoPath, []byte(`syntax = "proto3";

package demo;

import "sys/spec.proto";

message DemoEvent {}
message DemoEventBus {}
`), 0644)
	if err != nil {
		t.Fatalf("write proto fixture: %v", err)
	}

	stateRoot := topicStateRootForTest()
	err = appendStateServicesForFile(
		dir,
		&parser.FileDescriptor{Package: "demo"},
		[]stateRootEntry{{stateRoot: stateRoot, stateName: stateRoot.Name}},
		"demo.proto",
		&sharedMessageNeeds{},
	)
	if err != nil {
		t.Fatalf("append state service: %v", err)
	}

	data, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatalf("read generated proto: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`import "google/protobuf/timestamp.proto";`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated proto missing %q in:\n%s", want, text)
		}
	}
	assertGeneratedMessageHasNoTraceFields(t, text, "EventBusRecordMeta")
	assertGeneratedMessageHasNoTraceFields(t, text, "DemoEventBusEventsSubscribeRecord")
	for _, want := range []string{
		"message EventBusRecordMeta { bytes key = 1; string topic = 2; int32 partition = 3; int64 offset = 4; google.protobuf.Timestamp timestamp = 5; }",
		"message DemoEventBusEventsSubscribeRecord { EventBusRecordMeta meta = 1; DemoEvent payload = 2; }",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated proto missing compact EventBus wire shape %q in:\n%s", want, text)
		}
	}
	assertMessageContainsFields(t, text, "EventBusRecordMeta",
		"bytes key = 1;",
		"string topic = 2;",
		"int32 partition = 3;",
		"int64 offset = 4;",
		"google.protobuf.Timestamp timestamp = 5;",
	)
	assertGeneratedMessageHasTraceFields(t, text, "MapEmpty")
}

func TestStateServiceSyntheticCollectionMessagesCarryTraceFields(t *testing.T) {
	stateRoot := &parser.MessageDescriptor{
		Name: "DemoState",
		Fields: []parser.FieldDescriptor{{
			Name:       "nodes",
			Type:       "DemoNode",
			TypeKind:   "message",
			IsRepeated: true,
		}},
	}
	stateProto := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name, nil).serviceProto

	for _, messageName := range []string{
		"NodesGetRequest",
		"NodesPutRequest",
		"NodesGetAllRequest",
		"NodesKeysRequest",
		"NodesSizeRequest",
		"NodesClearRequest",
		"NodesValueResponse",
		"NodesEntriesResponse",
		"NodesKeysResponse",
		"NodesSizeResponse",
		"NodesQueryRequest",
		"NodesQueryResponse",
	} {
		assertGeneratedMessageHasTraceFields(t, stateProto, messageName)
	}
}

func TestGeneratedServicesKeepSynthesizedMarkerAsLeadingComment(t *testing.T) {
	stateRoot := &parser.MessageDescriptor{
		Name: "DemoConfigState",
		Fields: []parser.FieldDescriptor{{
			Name:     "config",
			Type:     "Config",
			TypeKind: "message",
		}},
	}
	stateProto := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name, nil).serviceProto
	assertGeneratedServiceMarkerLeadsService(t, stateProto, StateServiceName(stateRoot.Name))

	storeProto := buildStoreService("DemoStore", "id", "")
	assertGeneratedServiceMarkerLeadsService(t, storeProto, StoreServiceName("DemoStore"))
}

func TestStateImportEmitsReadOnlyStateServiceBoundary(t *testing.T) {
	stateRoot := &parser.MessageDescriptor{
		Name: "ConsumerState",
		Fields: []parser.FieldDescriptor{{
			Name:     "imported_current",
			Type:     "owner.OwnerRecord",
			TypeKind: "message",
			Annotations: map[string]model.AnnotationValue{
				"state_import": {Type: model.AnnotationTypeString, StringValue: "owner.OwnerState.current"},
			},
		}},
	}
	stateProto := buildStateService(&parser.FileDescriptor{Package: "consumer"}, stateRoot, stateRoot.Name, nil).serviceProto

	for _, want := range []string{
		"rpc GetImportedCurrent(trace.TraceRequest) returns (owner.OwnerRecord);",
		"rpc WatchImportedCurrent(trace.TraceRequest) returns (stream owner.OwnerRecord);",
	} {
		if !strings.Contains(stateProto, want) {
			t.Fatalf("state_import read-only RPC missing %q in:\n%s", want, stateProto)
		}
	}
	for _, bad := range []string{
		"rpc SetImportedCurrent",
		"rpc PutImportedCurrent",
		"rpc DeleteImportedCurrent",
		"rpc RemoveImportedCurrent",
		"message ImportedCurrentPutRequest",
	} {
		if strings.Contains(stateProto, bad) {
			t.Fatalf("state_import must not emit mutable boundary surface %q in:\n%s", bad, stateProto)
		}
	}
}

func assertGeneratedServiceMarkerLeadsService(t *testing.T, protoText, serviceName string) {
	t.Helper()
	want := "// tacplus:generated-service\nservice " + serviceName + " {"
	if !strings.Contains(protoText, want) {
		t.Fatalf("generated service marker must directly lead %s; want substring %q in:\n%s", serviceName, want, protoText)
	}
	bad := "// tacplus:generated-service\n\nservice " + serviceName + " {"
	if strings.Contains(protoText, bad) {
		t.Fatalf("generated service marker must not be detached by a blank line for %s", serviceName)
	}
}

func assertGeneratedMessageHasTraceFields(t *testing.T, protoText, messageName string) {
	t.Helper()
	body := generatedMessageBody(t, protoText, messageName)
	for _, want := range []string{
		"string trace_id = 200;",
		"bool noop = 201;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated message %s missing %q in:\n%s", messageName, want, body)
		}
	}
}

func assertGeneratedMessageHasNoTraceFields(t *testing.T, protoText, messageName string) {
	t.Helper()
	body := generatedMessageBody(t, protoText, messageName)
	for _, bad := range []string{
		"string trace_id = 200;",
		"bool noop = 201;",
	} {
		if strings.Contains(body, bad) {
			t.Fatalf("generated message %s unexpectedly contains %q in:\n%s", messageName, bad, body)
		}
	}
}

func assertMessageContainsFields(t *testing.T, protoText, messageName string, fields ...string) {
	t.Helper()
	body := generatedMessageBody(t, protoText, messageName)
	for _, want := range fields {
		if !strings.Contains(body, want) {
			t.Fatalf("generated message %s missing %q in:\n%s", messageName, want, body)
		}
	}
}

func generatedMessageBody(t *testing.T, protoText, messageName string) string {
	t.Helper()
	start := strings.Index(protoText, "message "+messageName)
	end := findMessageClosingBrace(protoText, messageName)
	if start < 0 || end < 0 || end < start {
		t.Fatalf("generated proto missing message %s in:\n%s", messageName, protoText)
	}
	return protoText[start:end]
}

func topicStateRootForTest() *parser.MessageDescriptor {
	return &parser.MessageDescriptor{
		Name: "DemoEventBus",
		Fields: []parser.FieldDescriptor{{
			Name:       "events",
			Type:       "DemoEvent",
			TypeKind:   "message",
			IsRepeated: true,
			Annotations: map[string]model.AnnotationValue{
				"topic": {Type: model.AnnotationTypeBool, BoolValue: true},
			},
		}},
	}
}
