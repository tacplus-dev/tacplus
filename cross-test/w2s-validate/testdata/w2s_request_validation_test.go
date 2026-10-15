// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Runtime coverage for W2S REQUEST (ingress) validation in the generated Go
// REST dispatch plane. A real gRPC backend serves the generated
// CatalogService; the generated restdispatch.Dispatch / DispatchStream are
// driven directly (no hand-rolled dispatch logic) with both an invalid and a
// valid body. The invalid body must be rejected BEFORE the backend is
// reached; the valid body must reach it. The backend hit counters are what
// make the rejection meaningful: without them a dispatcher that rejected
// everything would look identical to a correct one.

package restdispatch_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	restdispatch "example.com/w2svalidate/model/s2sm/rest_dispatch"
	catalog "example.com/w2svalidate/model/types/services/catalog"
)

const (
	unaryPath   = "/catalog.CatalogService/GetItem"
	streamPath  = "/catalog.CatalogService/WatchItems"
	invalidBody = `{"owner":"not-an-email"}`
	validBody   = `{"owner":"owner@example.com"}`
)

type recordingBackend struct {
	catalog.UnimplementedCatalogServiceServer
	unaryHits  atomic.Int32
	streamHits atomic.Int32
}

func (b *recordingBackend) GetItem(_ context.Context, req *catalog.GetItemRequest) (*catalog.GetItemResponse, error) {
	b.unaryHits.Add(1)
	return &catalog.GetItemResponse{Owner: req.GetOwner()}, nil
}

func (b *recordingBackend) WatchItems(req *catalog.GetItemRequest, stream grpc.ServerStreamingServer[catalog.GetItemResponse]) error {
	b.streamHits.Add(1)
	return stream.Send(&catalog.GetItemResponse{Owner: req.GetOwner()})
}

// fixtureDispatcher hands every service the one loopback connection and no auth.
type fixtureDispatcher struct {
	conn *grpc.ClientConn
}

func (d fixtureDispatcher) Conn(context.Context, string) (grpc.ClientConnInterface, error) {
	return d.conn, nil
}

func (d fixtureDispatcher) AuthHeader(context.Context, string) string { return "" }

// startBackend serves the real generated CatalogService on a loopback port
// and returns a dispatcher wired to it plus the backend's hit counters.
func startBackend(t *testing.T) (fixtureDispatcher, *recordingBackend) {
	t.Helper()
	backend := &recordingBackend{}
	conn := serve(t, func(s *grpc.Server) { catalog.RegisterCatalogServiceServer(s, backend) })
	return fixtureDispatcher{conn: conn}, backend
}

func serve(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback port: %v", err)
	}
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial backend: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestGoW2SUnaryDispatchValidatesRequestBeforeForwarding(t *testing.T) {
	dispatcher, backend := startBackend(t)
	ctx := context.Background()

	_, rejected := restdispatch.Dispatch(ctx, dispatcher, unaryPath, []byte(invalidBody), "req-invalid")
	if rejected == nil {
		t.Fatal("invalid request body must be rejected, not forwarded")
	}
	if rejected.Status != http.StatusBadRequest {
		t.Fatalf("invalid request rejected with wrong status: %d %s", rejected.Status, rejected.Body)
	}
	if !strings.Contains(rejected.Body, "request validation failed") {
		t.Fatalf("invalid request rejected for the wrong reason: %s", rejected.Body)
	}
	if hits := backend.unaryHits.Load(); hits != 0 {
		t.Fatalf("invalid request reached the backend service (%d hits)", hits)
	}

	// Positive control: the same seam must still forward a valid request.
	ok, herr := restdispatch.Dispatch(ctx, dispatcher, unaryPath, []byte(validBody), "req-valid")
	if herr != nil {
		t.Fatalf("valid request must be forwarded: %d %s", herr.Status, herr.Body)
	}
	if !strings.Contains(ok, "owner@example.com") {
		t.Fatalf("valid response did not carry the request owner: %s", ok)
	}
	if hits := backend.unaryHits.Load(); hits != 1 {
		t.Fatalf("valid request did not reach the backend service (%d hits)", hits)
	}
}

func TestGoW2SStreamDispatchValidatesRequestBeforeForwarding(t *testing.T) {
	dispatcher, backend := startBackend(t)
	ctx := context.Background()

	rejected := httptest.NewRecorder()
	restdispatch.DispatchStream(ctx, dispatcher, streamPath, []byte(invalidBody), "sse-invalid", rejected)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("invalid SSE request was not rejected: %d %s", rejected.Code, rejected.Body.String())
	}
	if !strings.Contains(rejected.Body.String(), "request validation failed") {
		t.Fatalf("invalid SSE request rejected for the wrong reason: %s", rejected.Body.String())
	}
	if hits := backend.streamHits.Load(); hits != 0 {
		t.Fatalf("invalid SSE request reached the backend service (%d hits)", hits)
	}

	// Positive control: the same seam must still open a valid stream.
	ok := httptest.NewRecorder()
	restdispatch.DispatchStream(ctx, dispatcher, streamPath, []byte(validBody), "sse-valid", ok)
	if ok.Code != http.StatusOK {
		t.Fatalf("valid SSE request was not accepted: %d %s", ok.Code, ok.Body.String())
	}
	if ct := ok.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("valid SSE request did not answer an event stream: Content-Type %q", ct)
	}
	if body := ok.Body.String(); !strings.Contains(body, "data: ") || !strings.Contains(body, "owner@example.com") {
		t.Fatalf("valid SSE stream did not carry the request owner: %s", body)
	}
	if hits := backend.streamHits.Load(); hits != 1 {
		t.Fatalf("valid SSE request did not reach the backend service (%d hits)", hits)
	}
}
