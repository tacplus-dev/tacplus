// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
)

func TestExplicitPublisherEntryUsesCanonicalPath(t *testing.T) {
	topic := eventbus.Topic{
		Ref: eventbus.TopicRef{
			Raw:     "telemetry.TraceEventBus.trace_events",
			Package: "telemetry",
			Message: "TraceEventBus",
			Field:   "trace_events",
		},
		EventTypeFQN: "trace.TraceEvent",
		RPCStem:      "TelemetryTraceEventBusTraceEvents",
	}

	entry := publisherEntryForTopic(
		topic,
		"telemetry_trace_event_bus_trace_events",
		topic.RPCStem,
		"crate::types::sys::trace::TraceEvent",
	)

	if entry.FnName != "create_telemetry_trace_event_bus_trace_events_publisher" {
		t.Fatalf("unexpected helper name %q", entry.FnName)
	}
	if entry.EventTypeFQN != "trace.TraceEvent" {
		t.Fatalf("EventTypeFQN = %q, want trace.TraceEvent", entry.EventTypeFQN)
	}
	// Per-topic: the path the generated proto declares. The flat
	// "/eventbus.EventBusTypedService/Publish<Stem>" that used to be asserted
	// here is declared in no proto in the tree, so only a client hardcoding it
	// could reach the broker. See control/EVENTBUS-PUBLISH-API-SPLIT.md.
	if entry.PublishPath != "/telemetry.TraceEventBusStateService/PublishToTraceEvents" {
		t.Fatalf("unexpected canonical publish path %q", entry.PublishPath)
	}
}

func TestPublisherEntryUsesDeclaredBrokerTag(t *testing.T) {
	topic := eventbus.Topic{
		Ref: eventbus.TopicRef{
			Raw:     "telemetry.TraceEventBus.trace_events",
			Package: "telemetry",
			Message: "TraceEventBus",
			Field:   "trace_events",
		},
		EventBusLabel: "events",
		EventTypeFQN:  "trace.TraceEvent",
		RPCStem:       "TelemetryTraceEventBusTraceEvents",
	}

	entry := publisherEntryForTopic(
		topic,
		"telemetry_trace_event_bus_trace_events",
		topic.RPCStem,
		"crate::types::sys::trace::TraceEvent",
	)

	if entry.TargetService != "events" {
		t.Fatalf("TargetService = %q, want events", entry.TargetService)
	}
	// The declared broker tag no longer appears in the METHOD name, and should
	// not: the per-topic service is declared in the topic's own package
	// regardless of which broker hosts it, and the proto is what makes the name
	// reachable. The tag still selects the broker, which is what TargetService
	// above asserts — that is the routing decision, not the method name.
	if entry.PublishPath != "/telemetry.TraceEventBusStateService/PublishToTraceEvents" {
		t.Fatalf("unexpected canonical publish path %q", entry.PublishPath)
	}
}
