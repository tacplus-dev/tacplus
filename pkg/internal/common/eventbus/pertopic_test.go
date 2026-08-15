package eventbus

import "testing"

// Pins the per-topic names against the exact strings the broker binds and the
// generated proto declares. These two are produced by different code generators
// in different repos and languages; a mismatch is invisible until a publish
// returns "Unimplemented: Method not found" at runtime, which is how the split
// documented in control/EVENTBUS-PUBLISH-API-SPLIT.md shipped.
func TestPerTopicNamesMatchTheProtoDeclaration(t *testing.T) {
	ref := TopicRef{Package: "llmsensor", Message: "LlmEventBus", Field: "event_stream"}

	if got, want := PerTopicServiceFullName(ref), "llmsensor.LlmEventBusStateService"; got != want {
		t.Errorf("service = %q, want %q", got, want)
	}
	if got, want := PerTopicPublishFullMethodName(ref), "llmsensor.LlmEventBusStateService/PublishToEventStream"; got != want {
		t.Errorf("publish = %q, want %q", got, want)
	}
	if got, want := PerTopicSubscribeFullMethodName(ref), "llmsensor.LlmEventBusStateService/SubscribeToEventStream"; got != want {
		t.Errorf("subscribe = %q, want %q", got, want)
	}
	if got, want := PerTopicPublishPath(ref), "/llmsensor.LlmEventBusStateService/PublishToEventStream"; got != want {
		t.Errorf("publish path = %q, want %q", got, want)
	}
}

func TestPascalFieldHandlesMultiWordAndSingleWordFields(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"event_stream", "EventStream"},
		{"events", "Events"},
		{"trace_events", "TraceEvents"},
		{"a_b_c", "ABC"},
	} {
		if got := pascalField(tc.in); got != tc.want {
			t.Errorf("pascalField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
