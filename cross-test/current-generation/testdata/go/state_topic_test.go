// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the event-bus backpressure fixture's Go leg. NOT generated:
// run.sh copies this file into the generated AgentState state package before
// `go test ./...`, because the StateClient's stub field is package-internal.
//
// What it pins (#454): a (spec.topic) boundary of a service_state root generated
// a Go state client that did not compile. Its Publish called PublishTo<Field> as a
// unary call returning a TopicPublishReceipt nothing declares, and its Subscribe
// called SubscribeTo<Field> as a server stream taking a TraceRequest; the state
// service declares both as bidi streams. No gate generated a state topic in Go.
// AgentState.events (cross-test/proto-eventbus-backpressure) is that topic, and
// this test drives Publish and Subscribe over a real gRPC connection to a server
// registered on the generated AgentStateService.

package agent_state

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	eventbus_support_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/generated/eventbus_support"
	types "gitlab.com/example/dummy/agentsensor/tacplus/model/types/services/agentsensor"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// topicServer is a broker for the one topic: it acks each published event with
// its position and records it, and streams back what a subscriber asks for.
type topicServer struct {
	types.UnimplementedAgentStateServiceServer
	published  chan *types.AgentEvent
	requests   chan *eventbus_support_pb.SubscribeRequest
	records    []*types.AgentStateEventsSubscribeRecord
	publishErr error
	subDone    chan error
}

func (s *topicServer) PublishToEvents(stream grpc.BidiStreamingServer[types.AgentEvent, eventbus_support_pb.PublishResponse]) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	for offset := int64(41); ; offset++ {
		event, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		s.published <- event
		if err := stream.Send(&eventbus_support_pb.PublishResponse{Partition: 3, Offset: offset}); err != nil {
			return err
		}
	}
}

func (s *topicServer) SubscribeToEvents(stream grpc.BidiStreamingServer[eventbus_support_pb.SubscribeRequest, types.AgentStateEventsSubscribeRecord]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	s.requests <- first
	for _, rec := range s.records {
		if err := stream.Send(rec); err != nil {
			return err
		}
	}
	// The request side stays open for flow control until the client cancels.
	for {
		req, err := stream.Recv()
		if err != nil {
			s.subDone <- stream.Context().Err()
			return err
		}
		s.requests <- req
	}
}

func newTopicClient(t *testing.T, srv *topicServer) *EventsWrapper {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	types.RegisterAgentStateServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	c := &StateClient{stateStub: types.NewAgentStateServiceClient(conn)}
	c.wrapperEvents = &EventsWrapper{parent: c}
	return c.Events()
}

func newTopicServer() *topicServer {
	return &topicServer{
		published: make(chan *types.AgentEvent, 4),
		requests:  make(chan *eventbus_support_pb.SubscribeRequest, 4),
		subDone:   make(chan error, 1),
	}
}

func TestStateTopicPublishReturnsBrokerAck(t *testing.T) {
	srv := newTopicServer()
	events := newTopicClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ack, err := events.Publish(ctx, &types.AgentEvent{Id: "evt-1", Body: []byte("payload"), TraceId: "trace-454"})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if ack.GetPartition() != 3 || ack.GetOffset() != 41 {
		t.Fatalf("Publish ack = partition %d offset %d, want 3/41", ack.GetPartition(), ack.GetOffset())
	}
	got := <-srv.published
	if got.GetId() != "evt-1" || string(got.GetBody()) != "payload" {
		t.Fatalf("broker received %v, want evt-1/payload", got)
	}
	// The caller's trace rides on the published event itself.
	if got.GetTraceId() != "trace-454" {
		t.Fatalf("broker received trace_id %q, want trace-454", got.GetTraceId())
	}

	// Each Publish is its own call: a second one is acked from a fresh stream.
	ack, err = events.Publish(ctx, &types.AgentEvent{Id: "evt-2"})
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if ack.GetOffset() != 41 {
		t.Fatalf("second Publish ack offset %d, want 41 (one event per stream)", ack.GetOffset())
	}
	if got := <-srv.published; got.GetId() != "evt-2" {
		t.Fatalf("broker received %q, want evt-2", got.GetId())
	}
}

func TestStateTopicPublishReturnsBrokerStatus(t *testing.T) {
	srv := newTopicServer()
	srv.publishErr = status.Error(codes.PermissionDenied, "not a producer")
	events := newTopicClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := events.Publish(ctx, &types.AgentEvent{Id: "evt-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Publish error = %v, want the broker's PermissionDenied", err)
	}
}

func TestStateTopicSubscribeStreamsRecords(t *testing.T) {
	srv := newTopicServer()
	srv.records = []*types.AgentStateEventsSubscribeRecord{
		{Meta: &types.EventBusRecordMeta{Topic: "events", Offset: 7}, Payload: &types.AgentEvent{Id: "evt-7"}},
		{Meta: &types.EventBusRecordMeta{Topic: "events", Offset: 8}, Payload: &types.AgentEvent{Id: "evt-8"}},
	}
	events := newTopicClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subCtx, unsubscribe := context.WithCancel(ctx)

	stream, err := events.Subscribe(subCtx, &eventbus_support_pb.SubscribeRequest{ConsumerGroup: "agents"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if req := <-srv.requests; req.GetConsumerGroup() != "agents" {
		t.Fatalf("broker received consumer group %q, want agents", req.GetConsumerGroup())
	}
	for _, want := range []struct {
		id     string
		offset int64
	}{{"evt-7", 7}, {"evt-8", 8}} {
		rec, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if rec.GetPayload().GetId() != want.id || rec.GetMeta().GetOffset() != want.offset {
			t.Fatalf("record = %s@%d, want %s@%d", rec.GetPayload().GetId(), rec.GetMeta().GetOffset(), want.id, want.offset)
		}
	}

	// The request side carries later flow control.
	if err := stream.Send(&eventbus_support_pb.SubscribeRequest{Pause: true}); err != nil {
		t.Fatalf("Send pause: %v", err)
	}
	if req := <-srv.requests; !req.GetPause() {
		t.Fatalf("broker received %v, want a pause", req)
	}

	// Cancelling the context ends the subscription on the broker's side.
	unsubscribe()
	select {
	case err := <-srv.subDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("broker subscription ended with %v, want context.Canceled", err)
		}
	case <-ctx.Done():
		t.Fatal("broker subscription did not end after the client cancelled")
	}
}

// A nil request subscribes with the defaults.
func TestStateTopicSubscribeNilRequest(t *testing.T) {
	srv := newTopicServer()
	events := newTopicClient(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := events.Subscribe(ctx, nil); err != nil {
		t.Fatalf("Subscribe(nil): %v", err)
	}
	if req := <-srv.requests; req.GetConsumerGroup() != "" || req.GetPause() {
		t.Fatalf("broker received %v, want a default SubscribeRequest", req)
	}
}
