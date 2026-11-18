// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func topicStateClientDataForTest(readOnly bool, topic topicBoundary) stateClientData {
	return stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		EventBusSupportPath:   "example.com/project/tacplus/types/generated/eventbus_support",
		ReadOnly:              readOnly,
		Topics:                []topicBoundary{topic},
	}
}

// #454: a state topic's PublishTo<F>/SubscribeTo<F> are bidi streams of the
// topic message → eventbus_support.PublishResponse and eventbus_support.
// SubscribeRequest → <Root><F>SubscribeRecord. The wrapper called them as a
// unary call returning a TopicPublishReceipt nothing declares and as a server
// stream taking a TraceRequest, so the client did not compile.
func TestStateWrapperTopicDrivesBidiStreams(t *testing.T) {
	src := renderStateWrapperForTest(t, topicStateClientDataForTest(false, topicBoundary{
		FieldName: "Events", TypeName: "DemoEvent", GoType: "DemoEvent",
		HasPublish: true, HasSubscribe: true,
	}))
	for _, banned := range []string{"TopicPublishReceipt", "SubscribeToEvents(ctx, &trace_pb.TraceRequest", "PublishToEvents(ctx, msg)", "trace_pb"} {
		if strings.Contains(src, banned) {
			t.Fatalf("topic wrapper still contains %q:\n%s", banned, src)
		}
	}
	for _, want := range []string{
		`eventbus_support_pb "example.com/project/tacplus/types/generated/eventbus_support"`,
		"func (w *EventsWrapper) Publish(ctx context.Context, msg *types.DemoEvent) (*eventbus_support_pb.PublishResponse, error) {",
		"stream, err := w.parent.stateStub.PublishToEvents(ctx)",
		"stream.CloseSend()",
		"func (w *EventsWrapper) Subscribe(ctx context.Context, req *eventbus_support_pb.SubscribeRequest) (types.DemoStateService_SubscribeToEventsClient, error) {",
		"stream, err := w.parent.stateStub.SubscribeToEvents(ctx)",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("topic wrapper missing %q:\n%s", want, src)
		}
	}
}

// A read-only client has no Publish, and a topic RPC the state service does not
// declare gets no method: calling an absent stub method does not compile.
func TestStateWrapperTopicEmitsOnlyDeclaredRPCs(t *testing.T) {
	readOnly := renderStateWrapperForTest(t, topicStateClientDataForTest(true, topicBoundary{
		FieldName: "Events", TypeName: "DemoEvent", GoType: "DemoEvent",
		HasPublish: true, HasSubscribe: true,
	}))
	if strings.Contains(readOnly, "Publish(") {
		t.Fatalf("read-only topic wrapper must not publish:\n%s", readOnly)
	}
	if !strings.Contains(readOnly, "func (w *EventsWrapper) Subscribe(") {
		t.Fatalf("read-only topic wrapper must subscribe:\n%s", readOnly)
	}

	publishOnly := renderStateWrapperForTest(t, topicStateClientDataForTest(false, topicBoundary{
		FieldName: "Events", TypeName: "DemoEvent", GoType: "DemoEvent",
		HasPublish: true,
	}))
	if strings.Contains(publishOnly, "SubscribeToEvents") {
		t.Fatalf("topic without a declared SubscribeTo RPC must not call it:\n%s", publishOnly)
	}
	if !strings.Contains(publishOnly, "PublishToEvents") {
		t.Fatalf("topic with a declared PublishTo RPC must publish:\n%s", publishOnly)
	}

	none := renderStateWrapperForTest(t, topicStateClientDataForTest(false, topicBoundary{
		FieldName: "Events", TypeName: "DemoEvent", GoType: "DemoEvent",
	}))
	if strings.Contains(none, "ToEvents") || strings.Contains(none, "eventbus_support") {
		t.Fatalf("topic with no declared RPC must call none and import no eventbus_support:\n%s", none)
	}
}
