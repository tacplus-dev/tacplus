// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalEventBusFixture mirrors the wire-bearing messages of the canonical
// platform/proto/services/eventbus.proto, annotations included. It is the shape
// the Java broker actually serves.
const canonicalEventBusFixture = `syntax = "proto3";

package eventbus;

import "sys/spec.proto";
import "google/protobuf/timestamp.proto";

enum BackpressurePolicy {
  BACKPRESSURE_BUFFER = 0;
  BACKPRESSURE_DROP_OLD = 1;
  BACKPRESSURE_SAMPLE = 2;
}

message SubscribeRequest {
  oneof start_from {
    int64 offset = 1 [(spec.assert_ge) = 0];
    google.protobuf.Timestamp timestamp = 2;
    bool latest = 3;
    bool earliest = 4;
  }
  string consumer_group = 5 [(spec.assert_max_size) = 256];
  double speed_multiplier = 6;
  bool pause = 7;
  BackpressurePolicy backpressure = 8;
  int32 sample_interval = 9;
  repeated int32 partitions = 10 [(spec.assert_max_size) = 256];
}

message PublishResponse {
  int32 partition = 1 [(spec.assert_ge) = 0];
  int64 offset = 2 [(spec.assert_ge) = 0];
}

message RecordMeta {
  bytes key = 1;
  string topic = 2 [(spec.assert_required) = true, (spec.assert_max_size) = 256];
  int32 partition = 3 [(spec.assert_ge) = 0];
  int64 offset = 4 [(spec.assert_ge) = 0];
  google.protobuf.Timestamp timestamp = 5;
}
`

// writeCanonicalEventBusTree lays out a source tree carrying the sys protos and
// the canonical eventbus proto, the way proto-base reaches a real build.
func writeCanonicalEventBusTree(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services", "eventbus.proto"), []byte(text), 0644); err != nil {
		t.Fatalf("write canonical eventbus proto: %v", err)
	}
	return dir
}

// writeSupportTree writes a support proto exactly where generation puts it.
func writeSupportTree(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash(EventBusSupportProtoPath))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir generated: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatalf("write support proto: %v", err)
	}
	return dir
}

// TestEventBusWireContractAcceptsSynthesizedProto is the positive case: the
// support proto tacplus actually emits must agree with canonical.
func TestEventBusWireContractAcceptsSynthesizedProto(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := t.TempDir()
	relPath, err := EnsureEventBusSupportProto(supportDir, EventBusSupportProtoOptions{})
	if err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := ValidateEventBusSupportWireContract(supportDir, relPath, source); err != nil {
		t.Fatalf("synthesized support proto must match canonical eventbus.proto: %v", err)
	}
}

// TestEventBusWireContractCatchesPublishResponseDrift reintroduces the exact
// shape that shipped to production and looped a live publisher forever:
// PublishResponse field 2 as `string error` against canonical `int64 offset`.
func TestEventBusWireContractCatchesPublishResponseDrift(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

message PublishResponse { bool success = 1; string error = 2; int64 offset = 3; }
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("the pre-babc6a0b PublishResponse shape must fail the wire contract gate")
	}
	for _, want := range []string{
		"EventBus wire contract drift",
		"PublishResponse",
		"field 1 type mismatch: synthesized bool success, canonical int32 partition",
		"field 2 type mismatch: synthesized string error, canonical int64 offset",
		"field 3 (int64 offset) exists only in the synthesized proto",
		"reconnects forever",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure message missing %q, got:\n%s", want, err)
		}
	}
}

// TestEventBusWireContractCatchesMissingCanonicalField guards the other
// direction of an exact-match message: a canonical field the client drops.
func TestEventBusWireContractCatchesMissingCanonicalField(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

message RecordMeta { bytes key = 1; string topic = 2; int32 partition = 3; int64 offset = 4; }
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("dropping canonical RecordMeta.timestamp must fail the wire contract gate")
	}
	if !strings.Contains(err.Error(), "field 5 (google.protobuf.Timestamp timestamp) exists only in the canonical proto") {
		t.Fatalf("failure message missing the dropped canonical field, got:\n%s", err)
	}
}

// TestEventBusWireContractAllowsSubscribeRequestSubset documents the one
// intentional gap: generated subscribers expose fewer capabilities than the
// broker accepts. That is wire-safe on a client→server message, so it passes —
// but renumbering one of the fields they DO send is not.
func TestEventBusWireContractAllowsSubscribeRequestSubset(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)

	ok := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

message SubscribeRequest {
  oneof start_from {
    int64 offset = 1;
    bool latest = 3;
    bool earliest = 4;
  }
  string consumer_group = 5;
}
`)
	if err := ValidateEventBusSupportWireContract(ok, EventBusSupportProtoPath, source); err != nil {
		t.Fatalf("a strict subset of canonical SubscribeRequest must pass: %v", err)
	}

	bad := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

message SubscribeRequest {
  oneof start_from {
    int64 offset = 1;
    bool latest = 3;
    bool earliest = 4;
  }
  string consumer_group = 6;
}
`)
	err := ValidateEventBusSupportWireContract(bad, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("renumbering consumer_group must fail the wire contract gate")
	}
	if !strings.Contains(err.Error(), "field 6 type mismatch: synthesized string consumer_group, canonical double speed_multiplier") {
		t.Fatalf("failure message missing the renumbered field, got:\n%s", err)
	}
}

// TestEventBusWireContractSkipsBrokerPresenceStub covers the identification of
// "canonical": package eventbus is the broker's SERVICE identity, so a proto
// that only stands in for the broker (cross-test/consumer-contract's
// contracteventbus.proto) declares it while defining no wire message at all.
// Such a stub is not the wire contract and must not fail generation.
func TestEventBusWireContractSkipsBrokerPresenceStub(t *testing.T) {
	source := writeCanonicalEventBusTree(t, `syntax = "proto3";

package eventbus;

import "sys/spec.proto";

option (spec.service) = {name: "eventbus"};

message EventBusPing {}

service EventBusBroker {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc Ping(EventBusPing) returns (EventBusPing);
}
`)
	supportDir := t.TempDir()
	relPath, err := EnsureEventBusSupportProto(supportDir, EventBusSupportProtoOptions{})
	if err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := ValidateEventBusSupportWireContract(supportDir, relPath, source); err != nil {
		t.Fatalf("a broker presence stub declares no wire contract and must not fail the gate, got: %v", err)
	}
}

// TestEventBusWireContractCatchesDriftAlongsideStub is the other half: a tree
// carrying BOTH a presence stub and the real canonical proto must still catch
// drift. Skipping the stub must not skip the file that does declare the shape.
func TestEventBusWireContractCatchesDriftAlongsideStub(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	if err := os.WriteFile(filepath.Join(source, "services", "contracteventbus.proto"), []byte(`syntax = "proto3";

package eventbus;

message EventBusPing {}
`), 0644); err != nil {
		t.Fatalf("write presence stub: %v", err)
	}

	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

message PublishResponse { bool success = 1; string error = 2; int64 offset = 3; }
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("PublishResponse drift must still fail when a presence stub shares the eventbus package")
	}
	if !strings.Contains(err.Error(), "field 2 type mismatch: synthesized string error, canonical int64 offset") {
		t.Fatalf("failure message missing the drifted field, got:\n%s", err)
	}
	if strings.Contains(err.Error(), "contracteventbus.proto") {
		t.Fatalf("the presence stub must not be reported as canonical, got:\n%s", err)
	}
}

// TestEventBusWireContractSkipsTreeWithoutCanonicalProto keeps the gate quiet
// for source trees that carry no canonical eventbus proto (tacplus's own
// cross-test fixtures, for instance) rather than failing them.
func TestEventBusWireContractSkipsTreeWithoutCanonicalProto(t *testing.T) {
	source := t.TempDir()
	if err := CopySysProtos(source); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	supportDir := t.TempDir()
	relPath, err := EnsureEventBusSupportProto(supportDir, EventBusSupportProtoOptions{})
	if err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := ValidateEventBusSupportWireContract(supportDir, relPath, source); err != nil {
		t.Fatalf("a tree with no canonical eventbus proto must skip the check, got: %v", err)
	}
}
