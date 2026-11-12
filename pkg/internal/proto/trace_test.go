// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
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

// #327: field 200 is on every message of the sys contract, as on a user's own.
// The exceptions are sys/spec.proto's option schemas (never on the wire) and
// trace.TraceEvent, which carries the trace it reports as trace_id = 1. A sys
// message that declares trace_id = 200 itself (sys/tokenrequest.proto,
// trace.TraceRequest) carries it like an injected one, so a store clears it.
func TestInjectTraceFieldsCoversSysContract(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	skipped, err := InjectTraceFields(dir, dir)
	if err != nil {
		t.Fatalf("InjectTraceFields: %v", err)
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	checked := 0
	for relPath, fileDesc := range dp.GetAllFiles() {
		if !IsSysProto(relPath) {
			continue
		}
		for _, msg := range fileDesc.Messages {
			fqn := fileDesc.Package + "." + msg.Name
			wantTrace := !IsTraceExemptSysMessage(relPath, msg.Name)
			trace, noop := false, false
			for _, f := range msg.Fields {
				if IsTraceField(f.Name, int(f.Number)) {
					if IsNoopField(f.Name, int(f.Number)) {
						noop = true
					} else {
						trace = true
					}
				}
			}
			if wantTrace != trace || wantTrace != noop {
				t.Errorf("%s (%s): trace_id = 200 %v, noop = 201 %v; want both %v", fqn, relPath, trace, noop, wantTrace)
			}
			if got := CarriesInjectedTrace(dp, fqn, skipped); got != wantTrace {
				t.Errorf("CarriesInjectedTrace(%q) = %v, want %v", fqn, got, wantTrace)
			}
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("checked %d sys messages; the sys protos were not all parsed", checked)
	}
	for _, exempt := range []struct{ relPath, msg string }{
		{"sys/spec.proto", "StringConst"},
		{"sys/trace.proto", "TraceEvent"},
	} {
		if !IsTraceExemptSysMessage(exempt.relPath, exempt.msg) {
			t.Errorf("%s %s must be exempt", exempt.relPath, exempt.msg)
		}
	}
}
