// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
)

func TestExplicitPublisherEntryUsesCanonicalPath(t *testing.T) {
	topic := eventbus.Topic{
		Ref: eventbus.TopicRef{
			Raw:     "claudesensor.TraceEventBus.trace_events",
			Package: "claudesensor",
			Message: "TraceEventBus",
			Field:   "trace_events",
		},
		EventTypeFQN: "trace.TraceEvent",
		RPCStem:      "ClaudesensorTraceEventBusTraceEvents",
	}

	entry := publisherEntryForTopic(
		topic,
		"claudesensor_trace_event_bus_trace_events",
		topic.RPCStem,
		"crate::types::sys::trace::TraceEvent",
	)

	if entry.FnName != "create_claudesensor_trace_event_bus_trace_events_publisher" {
		t.Fatalf("unexpected helper name %q", entry.FnName)
	}
	if entry.EventTypeFQN != "trace.TraceEvent" {
		t.Fatalf("EventTypeFQN = %q, want trace.TraceEvent", entry.EventTypeFQN)
	}
	if entry.PublishPath != "/eventbus.EventBusTypedService/PublishClaudesensorTraceEventBusTraceEvents" {
		t.Fatalf("unexpected canonical publish path %q", entry.PublishPath)
	}
}

func TestPublisherEntryUsesDeclaredBrokerTag(t *testing.T) {
	topic := eventbus.Topic{
		Ref: eventbus.TopicRef{
			Raw:     "claudesensor.TraceEventBus.trace_events",
			Package: "claudesensor",
			Message: "TraceEventBus",
			Field:   "trace_events",
		},
		EventBusTag:  "events",
		EventTypeFQN: "trace.TraceEvent",
		RPCStem:      "ClaudesensorTraceEventBusTraceEvents",
	}

	entry := publisherEntryForTopic(
		topic,
		"claudesensor_trace_event_bus_trace_events",
		topic.RPCStem,
		"crate::types::sys::trace::TraceEvent",
	)

	if entry.TargetService != "events" {
		t.Fatalf("TargetService = %q, want events", entry.TargetService)
	}
	if entry.PublishPath != "/events.EventBusTypedService/PublishClaudesensorTraceEventBusTraceEvents" {
		t.Fatalf("unexpected canonical publish path %q", entry.PublishPath)
	}
}
