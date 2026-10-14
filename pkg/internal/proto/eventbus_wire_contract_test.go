// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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

// Carrying the full canonical SubscribeRequest means the synthesized proto now
// declares its own BackpressurePolicy enum, so field 8's type reads
// `eventbus_support.BackpressurePolicy` on one side and `eventbus.BackpressurePolicy`
// on the other. comparableWireTypeOf normalizes that owning-package prefix away.
// It must not normalize anything else: a synthesized field pointing at a
// genuinely different local type is still drift, and still fatal.
func TestEventBusWireContractCatchesDivergentLocalTypeName(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

enum DeliveryPolicy {
  DELIVERY_BUFFER = 0;
}

message SubscribeRequest {
  string consumer_group = 5;
  DeliveryPolicy backpressure = 8;
}
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("a synthesized field typed with a different local enum must fail the wire contract gate")
	}
	for _, want := range []string{
		"EventBus wire contract drift",
		"SubscribeRequest",
		"field 8 type mismatch: synthesized eventbus_support.DeliveryPolicy backpressure, canonical eventbus.BackpressurePolicy backpressure",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure message missing %q, got:\n%s", want, err)
		}
	}
}

// The subset rule still only excuses ABSENT fields. Now that the synthesized
// SubscribeRequest carries every canonical field, a renumbering of one of the
// newly added fields — pause at 8 instead of 7 — is real drift on the wire and
// must fail.
func TestEventBusWireContractCatchesRenumberedSubscribeRequestField(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

message SubscribeRequest {
  string consumer_group = 5;
  bool pause = 8;
}
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("pause renumbered to field 8 must fail the wire contract gate")
	}
	if !strings.Contains(err.Error(), "field 8 type mismatch: synthesized bool pause, canonical eventbus.BackpressurePolicy backpressure") {
		t.Fatalf("unexpected failure message:\n%s", err)
	}
}

// Normalizing the owning-package prefix away is what lets `eventbus_support.
// BackpressurePolicy` compare equal to `eventbus.BackpressurePolicy`. The prefix
// was also the only thing distinguishing a message from an enum of the same
// name, so the comparison carries the type kind explicitly. A synthesized
// `message BackpressurePolicy` at field 8 is length-delimited where the broker
// reads a varint — real drift, and it must still fail.
func TestEventBusWireContractCatchesMessageStandingInForEnum(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

message BackpressurePolicy { int32 policy = 1; }

message SubscribeRequest {
  string consumer_group = 5;
  BackpressurePolicy backpressure = 8;
}
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("a synthesized message standing in for a canonical enum must fail the wire contract gate")
	}
	if !strings.Contains(err.Error(), "field 8 type mismatch") {
		t.Fatalf("unexpected failure message:\n%s", err)
	}
}

// The synthesized proto now declares its own BackpressurePolicy, and an enum's
// value numbering is a wire commitment: the value travels as a bare varint, so a
// synthesized BACKPRESSURE_SAMPLE = 5 makes a subscriber ask the broker for
// something it never meant. compareWireShape cannot see this — both sides'
// field 8 is just "an enum" — so the enum rule has to catch it.
func TestEventBusWireContractCatchesEnumValueSkew(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

enum BackpressurePolicy {
  BACKPRESSURE_BUFFER = 0;
  BACKPRESSURE_DROP_OLD = 1;
  BACKPRESSURE_SAMPLE = 5;
}
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("BACKPRESSURE_SAMPLE renumbered to 5 must fail the wire contract gate")
	}
	for _, want := range []string{
		"BackpressurePolicy",
		"value BACKPRESSURE_SAMPLE = 5 exists only in the synthesized proto; canonical has no value 5",
		"value BACKPRESSURE_SAMPLE = 2 exists only in the canonical proto; the synthesized enum must carry it",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure message missing %q, got:\n%s", want, err)
		}
	}
}

// A renamed enum value at the right number is drift too: generated code in every
// language names the constant, so a subscriber compiled against the synthesized
// name and a broker switching on the canonical one disagree at the source level
// even where the varint happens to line up.
func TestEventBusWireContractCatchesEnumValueRename(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

enum BackpressurePolicy {
  BACKPRESSURE_BUFFER = 0;
  BACKPRESSURE_DROP_OLDEST = 1;
  BACKPRESSURE_SAMPLE = 2;
}
`)

	err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source)
	if err == nil {
		t.Fatal("a renamed enum value must fail the wire contract gate")
	}
	if !strings.Contains(err.Error(), `value 1 name mismatch: synthesized "BACKPRESSURE_DROP_OLDEST", canonical "BACKPRESSURE_DROP_OLD"`) {
		t.Fatalf("unexpected failure message:\n%s", err)
	}
}

// A support proto that declares no BackpressurePolicy at all is not drift — the
// enum rule, like the message rules, only constrains what the synthesized proto
// actually declares.
func TestEventBusWireContractAllowsAbsentEnum(t *testing.T) {
	source := writeCanonicalEventBusTree(t, canonicalEventBusFixture)
	supportDir := writeSupportTree(t, `syntax = "proto3";

package eventbus_support;

import "google/protobuf/timestamp.proto";

message SubscribeRequest {
  string consumer_group = 5;
}
`)

	if err := ValidateEventBusSupportWireContract(supportDir, EventBusSupportProtoPath, source); err != nil {
		t.Fatalf("a support proto that declares no BackpressurePolicy must pass: %v", err)
	}
}
