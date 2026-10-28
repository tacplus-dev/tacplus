// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// storeFixture writes a proto set into a temp dir alongside the sys/ protos and
// returns a parser over it.
func storeFixture(t *testing.T, files map[string]string) *parser.DescriptorParser {
	t.Helper()
	dir := virtualFixture(t, files)
	p, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return p
}

func derive(t *testing.T, p *parser.DescriptorParser, selfPackage string) ([]boundaries.SubscriptionData, error) {
	t.Helper()
	var fds []*parser.FileDescriptor
	for _, fd := range p.GetAllFiles() {
		fds = append(fds, fd)
	}
	registry, err := eventbus.BuildTopicRegistry(fds)
	if err != nil {
		t.Fatalf("BuildTopicRegistry: %v", err)
	}
	return boundaries.DeriveStoreSubscriptionsFromRegistry(p, selfPackage, registry)
}

const timeseriesStoreProto = `syntax = "proto3";
package probestore;

import "sys/spec.proto";

option (spec.service) = {name: "probestore"};
option (spec.timeseries_store) = {};

message TimeRange {
  int64 from_ms = 1;
  int64 to_ms = 2;
}
`

// A derived row type hosted by the store, whose source has NO EventBus topic.
const sourcelessRowProto = `syntax = "proto3";
package probesrc;

import "sys/spec.proto";

message ProbeEvent {
  string probe_id = 1;
  int64 timestamp_ms = 2;
}

message ProbeRow {
  option (spec.timeseries_type) = {source: "probesrc.ProbeEvent"};

  string probe_id = 1 [
    (spec.timeseries_column) = {order_key: 1},
    (spec.projection_source) = "probe_id"
  ];
  int64 timestamp_ms = 2 [
    (spec.timeseries_column) = {order_key: 2},
    (spec.projection_source) = "timestamp_ms"
  ];
}
`

// The same set, plus the delivery declaration: ProbeEvent is a record (so a topic
// is synthesized) published on a state root's topic field.
const deliveredRowProto = `syntax = "proto3";
package probesrc;

import "sys/spec.proto";

message ProbeEvent {
  option (spec.record) = {
    key: "probe_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["*"]}
  };

  string probe_id = 1;
  string spiffe_id = 2;
  int64 timestamp_ms = 3;
}

message ProbeState {
  option (spec.service_state) = {name: "ProbeState", label: ["probesrc"], export: true};
  option (spec.service_event) = {label: "eventbus", export: true};

  repeated ProbeEvent events = 1 [(spec.topic) = true];
}

message ProbeRow {
  option (spec.timeseries_type) = {source: "probesrc.ProbeEvent"};

  string probe_id = 1 [
    (spec.timeseries_column) = {order_key: 1},
    (spec.projection_source) = "probe_id"
  ];
  int64 timestamp_ms = 2 [
    (spec.timeseries_column) = {order_key: 2},
    (spec.projection_source) = "timestamp_ms"
  ];
}
`

// A minimal EventBus broker: BuildTopicRegistry requires a loaded gRPC service
// whose identity matches a topic's declared broker tag.
const eventBusBrokerProto = `syntax = "proto3";
package eventbus;

import "sys/spec.proto";

option (spec.service) = {name: "eventbus"};

message EventBusPing {}

service EventBusBroker {
  option (spec.service_s2s) = {};

  rpc Ping(EventBusPing) returns (EventBusPing) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`

// PAIRED-CONTROL VALIDATOR (RED). A store hosts rows derived from a source type
// that no EventBus topic publishes: the generated store subscribes to nothing and
// never ingests a row. This used to be a silent `continue` — generation succeeded
// and shipped a store that was dead on arrival. It must now fail loudly.
func TestDeriveStoreSubscriptionsRejectsSourceWithNoTopic(t *testing.T) {
	p := storeFixture(t, map[string]string{
		"services/probestore.proto": timeseriesStoreProto,
		"services/probesrc.proto":   sourcelessRowProto,
	})
	_, err := derive(t, p, "probestore")
	if err == nil {
		t.Fatal("a store source with no EventBus topic must fail generation, got nil error")
	}
	if !strings.Contains(err.Error(), "probesrc.ProbeEvent") {
		t.Fatalf("error must name the unresolvable source:\n%v", err)
	}
	if !strings.Contains(err.Error(), "never ingest") {
		t.Fatalf("error must say the store would never ingest:\n%v", err)
	}
}

// PAIRED-CONTROL VALIDATOR (GREEN). The same store and the same derived row type,
// with delivery declared: the source is a record, so a topic is synthesized and
// the store's subscription resolves. Generation succeeds.
func TestDeriveStoreSubscriptionsAcceptsDeliveredSource(t *testing.T) {
	p := storeFixture(t, map[string]string{
		"services/probestore.proto": timeseriesStoreProto,
		"services/probesrc.proto":   deliveredRowProto,
		"services/eventbus.proto":   eventBusBrokerProto,
	})
	subs, err := derive(t, p, "probestore")
	if err != nil {
		t.Fatalf("a delivered source must generate cleanly: %v", err)
	}
	if len(subs) == 0 {
		t.Fatal("expected a derived store subscription for the delivered source")
	}
}

// (spec.record_store).no_bus_ingest (#163, D2-A): a record store that takes its
// rows only through its generated write surface derives no EventBus
// subscription, even for a record whose type no topic publishes — which, on a
// store that does ingest, is the dead-store failure below it. Paired control:
// the same set without the flag still fails that way.
func TestRecordStoreNoBusIngestDerivesNoSubscription(t *testing.T) {
	const record = `syntax = "proto3";
package xidrows;

import "sys/spec.proto";

message XidRow {
  option (spec.record) = {key: "xid", s2s: {auth_services: ["*"]}};
  string xid = 1;
}
`
	store := func(flag string) string {
		return `syntax = "proto3";
package xidstore;

import "sys/spec.proto";

option (spec.service) = {name: "xidstore"};
option (spec.record_store) = {` + flag + `};

message Ping {}
`
	}
	p := storeFixture(t, map[string]string{
		"services/xidstore.proto": store("no_bus_ingest: true"),
		"vservices/xidrows.proto": record,
	})
	subs, err := derive(t, p, "xidstore")
	if err != nil {
		t.Fatalf("a no_bus_ingest store must derive cleanly, got: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("a no_bus_ingest store must derive no subscription, got %d: %+v", len(subs), subs)
	}

	p = storeFixture(t, map[string]string{
		"services/xidstore.proto": store(""),
		"vservices/xidrows.proto": record,
	})
	if subs, err := derive(t, p, "xidstore"); err == nil && len(subs) == 0 {
		t.Fatal("paired control: a bus-ingesting store must derive a subscription for the record (or fail), not nothing")
	}
}
