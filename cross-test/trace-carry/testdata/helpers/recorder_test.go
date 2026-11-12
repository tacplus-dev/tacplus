// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's Go recorder (tacplus#327), copied into each
// generated package the leg drives (the run script rewrites its package
// clause). It stands in for the gRPC connection under the generated client, so
// every request a helper builds is seen as it leaves, with no server.
package recorder

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// sent is one request as it left: the RPC's method name and its trace_id.
type sent struct {
	method  string
	traceID string
}

type recordingConn struct {
	mu   sync.Mutex
	sent []sent
}

// traceOf reads field 200, which every request a helper builds carries.
func traceOf(t *testing.T, method string, msg any) string {
	t.Helper()
	m, ok := msg.(proto.Message)
	if !ok {
		t.Fatalf("%s: request %T is not a proto message", method, msg)
	}
	fd := m.ProtoReflect().Descriptor().Fields().ByNumber(200)
	if fd == nil || fd.Name() != "trace_id" {
		t.Fatalf("%s: request %s has no trace_id = 200", method, m.ProtoReflect().Descriptor().FullName())
	}
	return m.ProtoReflect().Get(fd).String()
}

func (c *recordingConn) record(t *testing.T, method string, msg any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, sent{method: method[strings.LastIndex(method, "/")+1:], traceID: traceOf(t, method, msg)})
}

// take returns what was sent since the last take.
func (c *recordingConn) take() []sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.sent
	c.sent = nil
	return out
}

type recordingT struct {
	*recordingConn
	t *testing.T
}

func (c recordingT) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	c.record(c.t, method, args)
	// A query handle the reader goes on to page and release.
	if m, ok := reply.(proto.Message); ok {
		if fd := m.ProtoReflect().Descriptor().Fields().ByName("query_id"); fd != nil && fd.Kind() == protoreflect.StringKind {
			m.ProtoReflect().Set(fd, protoreflect.ValueOfString("q-1"))
		}
	}
	return nil
}

func (c recordingT) NewStream(ctx context.Context, _ *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	return &recordingStream{conn: c, method: method, ctx: ctx}, nil
}

type recordingStream struct {
	conn   recordingT
	method string
	ctx    context.Context
}

func (s *recordingStream) Header() (metadata.MD, error) { return nil, nil }
func (s *recordingStream) Trailer() metadata.MD         { return nil }
func (s *recordingStream) CloseSend() error             { return nil }
func (s *recordingStream) Context() context.Context     { return s.ctx }
func (s *recordingStream) SendMsg(m any) error {
	s.conn.record(s.conn.t, s.method, m)
	return nil
}
func (s *recordingStream) RecvMsg(any) error { return io.EOF }

// helperCase is one helper: its traced form called with a trace, its untraced
// form, and the RPCs each must send.
type helperCase struct {
	name      string
	traced    func(ctx context.Context, traceID string) error
	untraced  func(ctx context.Context) error
	wantCalls []string
}

// runHelperCases checks every case: each RPC the traced form sends carries the
// trace, and today's form sends the same RPCs with no trace.
func runHelperCases(t *testing.T, conn *recordingConn, leg string, cases []helperCase) {
	t.Helper()
	ctx := context.Background()
	check := func(c helperCase, form, want string) {
		got := conn.take()
		if len(got) != len(c.wantCalls) {
			t.Errorf("%s (%s): sent %v, want the RPCs %v", c.name, form, got, c.wantCalls)
			return
		}
		for i, s := range got {
			if s.method != c.wantCalls[i] {
				t.Errorf("%s (%s): RPC %d is %s, want %s", c.name, form, i, s.method, c.wantCalls[i])
			}
			if s.traceID != want {
				t.Errorf("%s (%s): %s carried trace_id %q, want %q", c.name, form, s.method, s.traceID, want)
			}
		}
	}
	for _, c := range cases {
		trace := "t-" + c.name
		if err := c.traced(ctx, trace); err != nil {
			t.Errorf("%s (traced): %v", c.name, err)
		}
		check(c, "traced", trace)
		if err := c.untraced(ctx); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		check(c, "untraced", "")
	}
	t.Logf("trace-helpers go %s: %d cases", leg, len(cases))
}
