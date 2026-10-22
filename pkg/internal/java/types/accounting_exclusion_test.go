// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
)

// accountingTopicFixture is the topic tacplus synthesizes from
// AccountingRecord's spec.record: accountingrecord.AccountingRecordBus.stream.
func accountingTopicFixture(t *testing.T) eventbus.Topic {
	t.Helper()
	ref, err := eventbus.ParseTopicRef("accountingrecord.AccountingRecordBus.stream")
	if err != nil {
		t.Fatalf("parse accounting topic ref: %v", err)
	}
	return eventbus.Topic{Ref: ref, EventTypeFQN: "accountingrecord.AccountingRecord", EventBusLabel: "eventbus"}
}

// Q2: accounting the accounting topic's own publish RPC is an infinite loop —
// handling a PublishToStream emits an accounting event, which publishes another
// AccountingRecord, forever. The exclusion is a correctness blocker, decided in
// decisions/2026-08-25-accounting-sink-q2-q7-decided.md as option (b), and this
// test exists so it cannot be silently deleted later.
func TestAccountingSelfPublishRPCIsExcluded(t *testing.T) {
	topic := accountingTopicFixture(t)

	pkg, svc, rpc := accountingSelfPublishRPC(topic)
	if pkg != "accountingrecord" || svc != "AccountingRecordBusStateService" || rpc != "PublishToStream" {
		t.Fatalf("accounting self-publish RPC resolved to %s.%s/%s, want accountingrecord.AccountingRecordBusStateService/PublishToStream", pkg, svc, rpc)
	}

	in := map[string]service.AccountingInfo{
		"eventbus": {EnumName: "Eventbus", RPCs: map[string]bool{
			"PublishToStream":   true,
			"SubscribeToStream": true,
		}},
	}

	got := excludeAccountingSelfPublish(in, "eventbus", pkg, svc, topic, true)
	if got["eventbus"].RPCs["PublishToStream"] {
		t.Fatalf("accounting topic's own publish RPC must not be accounted: %#v", got["eventbus"].RPCs)
	}
	if !got["eventbus"].RPCs["SubscribeToStream"] {
		t.Fatalf("the exclusion must be narrow — the broker's other RPCs stay accounted: %#v", got["eventbus"].RPCs)
	}
	if !in["eventbus"].RPCs["PublishToStream"] {
		t.Fatalf("exclusion must not mutate the shared accounting map")
	}
}

func TestAccountingExclusionLeavesOtherServicesAlone(t *testing.T) {
	topic := accountingTopicFixture(t)
	in := map[string]service.AccountingInfo{
		"tasks": {EnumName: "Tasks", RPCs: map[string]bool{"CreateTask": true, "PublishToStream": true}},
	}

	// A different proto service that happens to own a same-named RPC keeps it.
	got := excludeAccountingSelfPublish(in, "tasks", "tasks", "TaskActivityBusStateService", topic, true)
	if !got["tasks"].RPCs["PublishToStream"] {
		t.Fatalf("only the ACCOUNTING topic's publish RPC is excluded, not every bus publish: %#v", got["tasks"].RPCs)
	}

	// No accounting record in the proto set (client repos): nothing to exclude.
	got = excludeAccountingSelfPublish(in, "tasks", "accountingrecord", "AccountingRecordBusStateService", eventbus.Topic{}, false)
	if !got["tasks"].RPCs["PublishToStream"] {
		t.Fatalf("with no accounting topic declared the map must pass through unchanged")
	}
}
