package unaryretry

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	types "example.com/unaryretry/tacplus/model/types/contract/unaryretry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// downPeer fails every ListPings with UNAVAILABLE, the shape of a peer in outage,
// and counts the attempts that reach it.
type downPeer struct {
	types.UnimplementedRetryServiceServer
	attempts atomic.Int64
}

func (s *downPeer) ListPings(context.Context, *types.Ping) (*types.PingList, error) {
	s.attempts.Add(1)
	return nil, status.Error(codes.Unavailable, "peer down")
}

func tokenOK() (string, error) { return "test-token", nil }

func dialPeer(t *testing.T, srv types.RetryServiceServer) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	types.RegisterRetryServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// A sustained outage must not multiply load. Fifty callers each allow two
// retries, so unbounded retry sends 150 attempts; the retry budget admits a
// burst of ten retries and refills only on success, so 50 first attempts plus
// at most 10 retries reach the peer.
func TestGoUnaryRetryBudgetCapsRetriesDuringOutage(t *testing.T) {
	peer := &downPeer{}
	client := NewRetryServiceClient(dialPeer(t, peer), tokenOK).WithRetry(2, time.Millisecond)

	const calls = 50
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = client.ListPings(context.Background(), &types.Ping{M: "x"})
		}()
	}
	wg.Wait()

	if got := peer.attempts.Load(); got > calls+10 {
		t.Fatalf("outage saw %d attempts for %d calls; want at most %d (first attempts plus the 10-retry budget)", got, calls, calls+10)
	}
}

// Callers that fail together must not retry in lockstep: the wait for a given
// attempt is jittered, so repeated draws for the same attempt differ.
func TestGoUnaryRetryBackoffIsJittered(t *testing.T) {
	client := NewRetryServiceClient(nil, tokenOK).WithRetry(2, 100*time.Millisecond)

	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[client.outboundBackoff(0)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("first-retry backoff is the fixed %v for every caller; want jitter", client.outboundBackoff(0))
	}
}

// A retry must not start once the caller's deadline cannot cover its backoff.
// The caller gets the UNAVAILABLE it already saw, not a deadline it waited out
// in the backoff sleep.
func TestGoUnaryRetryStopsAtCallerDeadline(t *testing.T) {
	peer := &downPeer{}
	client := NewRetryServiceClient(dialPeer(t, peer), tokenOK).WithRetry(2, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.ListPings(ctx, &types.Ping{M: "x"})
	if code := status.Code(err); code != codes.Unavailable {
		t.Fatalf("call past its deadline returned %v (code %v); want the UNAVAILABLE from the attempt", err, code)
	}
	if got := peer.attempts.Load(); got != 1 {
		t.Fatalf("retry started with the caller's deadline inside its backoff: %d attempts; want 1", got)
	}
}
