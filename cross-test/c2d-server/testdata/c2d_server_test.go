// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for the generated Go standalone C2D host. It is an EXTERNAL
// test of the generated s2sm package (package sm_test), so it reaches the
// daemon only the way a cluster-less daemon's main does: it implements the
// generated Handler and calls the exported sm.ServeC2DStandalone, with no
// config, no identity and no state store anywhere in the process. Calls go
// over the real Unix socket, through the generated c2dm client.
//
// What each assertion would catch:
//   - a valid call succeeds with NO token and NO authorizer wired: a wrapper
//     that did not skip token auth for c2d fails closed ("authorizer not
//     configured") instead;
//   - an invalid request sent on the raw stub (the c2dm client validates
//     before sending, so it would never reach the server) is InvalidArgument
//     and never reaches the handler: the standalone host serves the generated
//     wrapper, not the bare handler;
//   - a handler response that breaks its constraints is Internal: the full
//     handler pipeline runs, not just the request half;
//   - the socket file is a socket, mode 0600, and replaced a stale file left
//     at the path: the access boundary and the stale-socket removal;
//   - cancelling the context ends ServeC2DStandalone with nil, the socket file
//     is gone, and Handler.Shutdown ran exactly once, after the drain.

package sm_test

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	c2dm "example.com/c2dserver/model/c2dm"
	"example.com/c2dserver/model/chassis/servicemanager"
	sm "example.com/c2dserver/model/s2sm"
	c2dserver_fe "example.com/c2dserver/model/s2sm/grpc/servers/svc/c2dserver"
	types "example.com/c2dserver/model/types/svc/c2dserver"
)

const socketPath = "/tmp/tacplus-c2d-server-check/daemon.sock"

type recordingDaemon struct {
	unaryHits     atomic.Int32
	streamHits    atomic.Int32
	shutdownCalls atomic.Int32
}

// The generated Handler: the c2d getter and Shutdown, nothing cluster-bound.
var _ sm.Handler = (*recordingDaemon)(nil)

func (d *recordingDaemon) LocalDaemonGrpcImpl() c2dserver_fe.LocalDaemonHandler { return d }

func (d *recordingDaemon) Shutdown(context.Context) { d.shutdownCalls.Add(1) }

func (d *recordingDaemon) GetLocal(_ context.Context, caller *servicemanager.Caller, req *types.LocalRequest) (*types.LocalResponse, error) {
	d.unaryHits.Add(1)
	if caller != nil {
		return nil, status.Error(codes.Internal, "c2d handler got a caller identity; no token was verified")
	}
	if req.GetItemId() == "bad-response" {
		return &types.LocalResponse{}, nil // result_id is assert_required
	}
	return &types.LocalResponse{ResultId: "result-" + req.GetItemId()}, nil
}

func (d *recordingDaemon) GenerateLocalEvents(_ *servicemanager.Caller, req *types.LocalRequest, stream grpc.ServerStreamingServer[types.LocalEvent]) error {
	d.streamHits.Add(1)
	for i := int32(0); i < req.GetCount(); i++ {
		if err := stream.Send(&types.LocalEvent{ItemId: req.GetItemId(), Seq: i}); err != nil {
			return err
		}
	}
	return nil
}

func TestGeneratedC2DStandaloneHostOverSocket(t *testing.T) {
	// A stale file at the socket path, as a crashed daemon leaves behind.
	if err := os.MkdirAll("/tmp/tacplus-c2d-server-check", 0o755); err != nil {
		t.Fatal(err)
	}
	// Clear whatever an aborted earlier run left (possibly a live-looking socket
	// file, which WriteFile cannot open), then plant a stale regular file.
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A daemon passes the generated constant for the declared socket_path.
	if sm.C2DSocketPath != socketPath {
		t.Fatalf("sm.C2DSocketPath = %q, want the declared socket_path %q", sm.C2DSocketPath, socketPath)
	}
	daemon := &recordingDaemon{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- sm.ServeC2DStandalone(ctx, sm.C2DSocketPath, daemon) }()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	// Ready once the stale file has been replaced by a socket AND the socket is
	// locked down: the chassis listens first and chmods after, so the mode is
	// only final a moment after the file appears. A host that never restricts
	// it fails here at the deadline.
	for {
		info, err := os.Stat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0o600 {
			break
		}
		select {
		case err := <-serveErr:
			t.Fatalf("standalone c2d host exited before listening: %v", err)
		case <-dialCtx.Done():
			t.Fatalf("no owner-only (0600) c2d socket at %s (last stat: %v, %v); the socket ACL is the c2d auth boundary", socketPath, info, err)
		case <-time.After(20 * time.Millisecond):
		}
	}

	client, err := c2dm.Dial(dialCtx, socketPath)
	if err != nil {
		t.Fatalf("dial c2d socket: %v", err)
	}
	defer client.Close()
	daemonClient := client.Grpc().Clients().Svc().LocalDaemonClient()

	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()

	// Valid unary call: no token anywhere, and it must still be served.
	resp, err := daemonClient.GetLocal(callCtx, &types.LocalRequest{ItemId: "abc"})
	if err != nil {
		t.Fatalf("valid c2d call without a token failed (token auth not skipped?): %v", err)
	}
	if resp.GetResultId() != "result-abc" {
		t.Fatalf("result_id = %q, want result-abc", resp.GetResultId())
	}
	if got := daemon.unaryHits.Load(); got != 1 {
		t.Fatalf("handler hits after valid call = %d, want 1", got)
	}

	// Invalid request on the raw stub: the wrapper must reject it before the
	// handler (item_id has assert_min_size 3).
	raw := types.NewLocalDaemonClient(client.Conn())
	_, err = raw.GetLocal(callCtx, &types.LocalRequest{ItemId: "ab"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid request: got %v, want InvalidArgument", err)
	}
	if got := daemon.unaryHits.Load(); got != 1 {
		t.Fatalf("invalid request reached the handler (hits = %d)", got)
	}

	// Response validation still runs on the c2d surface.
	_, err = raw.GetLocal(callCtx, &types.LocalRequest{ItemId: "bad-response"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("constraint-breaking response: got %v, want Internal", err)
	}

	// Server streaming over the socket.
	stream, err := daemonClient.GenerateLocalEvents(callCtx, &types.LocalRequest{ItemId: "abc", Count: 3})
	if err != nil {
		t.Fatalf("open server stream: %v", err)
	}
	var seqs []int32
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream recv: %v", err)
		}
		seqs = append(seqs, ev.GetSeq())
	}
	if len(seqs) != 3 || seqs[0] != 0 || seqs[2] != 2 {
		t.Fatalf("streamed seqs = %v, want [0 1 2]", seqs)
	}

	// Invalid streaming request: rejected before the handler.
	badStream, err := raw.GenerateLocalEvents(callCtx, &types.LocalRequest{ItemId: "ab", Count: 1})
	if err == nil {
		_, err = badStream.Recv()
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid streaming request: got %v, want InvalidArgument", err)
	}
	if got := daemon.streamHits.Load(); got != 1 {
		t.Fatalf("stream handler hits = %d, want 1", got)
	}

	if got := daemon.shutdownCalls.Load(); got != 0 {
		t.Fatalf("Handler.Shutdown ran while serving (calls = %d)", got)
	}

	// Shutdown: the entry returns cleanly, removes the socket file and calls
	// Handler.Shutdown once.
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("standalone c2d host returned %v on shutdown", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("standalone c2d host did not stop after its context was cancelled")
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still present after shutdown (stat err = %v)", err)
	}
	if got := daemon.shutdownCalls.Load(); got != 1 {
		t.Fatalf("Handler.Shutdown calls after shutdown = %d, want 1", got)
	}
}
