// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for the generated Go C2D server. It lives in the generated
// s2sm package so it can call newC2DServer, the same generated function
// ServiceManager.Run mounts the service_c2d wrappers with, and hosts the
// generated LocalDaemon wrapper on the fixture's declared socket_path. Calls
// go over the real Unix socket, through the generated c2dm client.
//
// What each assertion would catch:
//   - a valid call succeeds with NO token and NO authorizer wired: a wrapper
//     that did not skip token auth for c2d fails closed ("authorizer not
//     configured") instead;
//   - an invalid request sent on the raw stub (the c2dm client validates
//     before sending, so it would never reach the server) is InvalidArgument
//     and never reaches the handler: the wrapper still validates;
//   - a handler response that breaks its constraints is Internal: the full
//     handler pipeline runs, not just the request half;
//   - the socket file is a socket, mode 0600, and replaced a stale file left
//     at the path: the access boundary and the stale-socket removal;
//   - the socket file is gone after shutdown.

package sm

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
	c2dserver_fe "example.com/c2dserver/model/s2sm/grpc/servers/svc/c2dserver"
	types "example.com/c2dserver/model/types/svc/c2dserver"
)

const socketPath = "/tmp/tacplus-c2d-server-check/daemon.sock"

type recordingDaemon struct {
	unaryHits  atomic.Int32
	streamHits atomic.Int32
}

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

func TestGeneratedC2DServerOverSocket(t *testing.T) {
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

	daemon := &recordingDaemon{}
	srv := newC2DServer(c2dserver_fe.NewLocalDaemon(daemon))
	if srv.SocketPath() != socketPath {
		t.Fatalf("generated c2d server socket = %q, want the declared socket_path %q", srv.SocketPath(), socketPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	var client *c2dm.ServiceManager
	for {
		info, err := os.Stat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case err := <-serveErr:
			t.Fatalf("c2d server exited before listening: %v", err)
		case <-dialCtx.Done():
			t.Fatalf("c2d socket never appeared at %s", socketPath)
		case <-time.After(20 * time.Millisecond):
		}
	}
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("c2d socket mode = %o, want 0600 (the socket ACL is the c2d auth boundary)", perm)
	}

	client, err = c2dm.Dial(dialCtx, socketPath)
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

	// Shutdown: Serve returns cleanly and removes the socket file.
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("c2d server returned %v on shutdown", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("c2d server did not stop after its context was cancelled")
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still present after shutdown (stat err = %v)", err)
	}
}
