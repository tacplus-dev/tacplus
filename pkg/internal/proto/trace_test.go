// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInjectTraceFieldsSkippedTypesUseDescriptorPackage(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "services", "iron-maiden")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		t.Fatalf("mkdir proto dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "types.proto"), []byte(`syntax = "proto3";

package ironmaiden;

message LegacyRequest {
  string trace_id = 1;
}
`), 0644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	skipped, err := InjectTraceFields(dir, dir)
	if err != nil {
		t.Fatalf("InjectTraceFields: %v", err)
	}
	if !skipped["ironmaiden.LegacyRequest"] {
		t.Fatalf("skipped trace types = %#v, want descriptor package key", skipped)
	}
	if skipped["types.LegacyRequest"] {
		t.Fatalf("skipped trace types used filename-derived key: %#v", skipped)
	}
}

func TestInjectTraceFieldsIncludesAuthoredEventBusProto(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		t.Fatalf("mkdir proto dir: %v", err)
	}
	protoPath := filepath.Join(protoDir, "eventbus.proto")
	if err := os.WriteFile(protoPath, []byte(`syntax = "proto3";

package eventbus;

service Admin {
  rpc ListTopics(ListTopicsRequest) returns (ListTopicsResponse);
  rpc GetConfig(Config) returns (Config);
  rpc SubscribeToTopics(ListTopicsRequest) returns (stream TopicSubscribeRecord);
}

message ListTopicsRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message ListTopicsResponse {
  repeated string topics = 1;
}

message Config {
  string brokers = 1;
}

message EventBusRecordMeta {
  bytes key = 1;
  string topic = 2;
  int32 partition = 3;
  int64 offset = 4;
}

message TopicSubscribeRecord {
  EventBusRecordMeta meta = 1;
  ListTopicsResponse payload = 2;
}
	`), 0644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	if _, err := InjectTraceFields(dir, dir); err != nil {
		t.Fatalf("InjectTraceFields: %v", err)
	}
	data, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatalf("read proto: %v", err)
	}
	text := string(data)
	assertGeneratedMessageHasTraceFields(t, text, "ListTopicsRequest")
	assertGeneratedMessageHasTraceFields(t, text, "ListTopicsResponse")
	assertGeneratedMessageHasTraceFields(t, text, "Config")
	assertGeneratedMessageHasNoTraceFields(t, text, "EventBusRecordMeta")
	assertGeneratedMessageHasNoTraceFields(t, text, "TopicSubscribeRecord")
}

// MS-9: trace-field injection is suppressed for eventbus wire-value envelopes,
// keyed by NAME (exact "EventBusRecordMeta" or a "SubscribeRecord" SUFFIX). A
// user message must not lose its trace fields to an over-broad match, so pin
// suffix-exactness: only names ENDING in SubscribeRecord (or the exact meta
// name) are excluded — a name that merely contains the token is not.
func TestIsEventBusWireValueMessage_SuffixExact(t *testing.T) {
	excluded := []string{"EventBusRecordMeta", "TopicSubscribeRecord", "SubscribeRecord"}
	for _, n := range excluded {
		if !isEventBusWireValueMessage(n) {
			t.Errorf("%q must be excluded from trace injection (eventbus wire value)", n)
		}
	}
	// User messages that merely CONTAIN the tokens, or are unrelated, must still
	// receive trace fields — the exclusion is a suffix/exact match, not substring.
	included := []string{
		"SubscribeRecordRequest", // contains, does not end with
		"SubscribeRecords",       // plural suffix
		"MySubscribeRecordView",
		"EventBusRecordMetaHolder", // not the exact meta name
		"FooEvent",
		"SubscribeRecordMeta",
	}
	for _, n := range included {
		if isEventBusWireValueMessage(n) {
			t.Errorf("%q is a user message and must NOT be excluded from trace injection (over-broad name match)", n)
		}
	}
}
