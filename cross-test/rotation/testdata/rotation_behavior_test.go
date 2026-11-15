// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Stream rotation (tacplus#322), copied into the generated TickerService
// wrapper package by cross-test/rotation/run.sh.
//
// The real generated server (the generated wrapper, the generated Authorizer
// verifying real signed tokens, the generated grpcserver serving TLS with
// #260's StreamCredentialExpiryInterceptor) meets the real generated client
// (s2sm/grpc/clients), with tokens that live tokenLifetime. The handler below
// and the callers in the transparency tests are plain handler and caller
// code: nothing in them mentions rotation, and each stream must outlive
// several token lifetimes with no loss, duplication or reordering. The
// fault tests then go to the wire.

package rotationfixture

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	stdx509 "crypto/x509"
	"crypto/x509/pkix"
	libBase64 "encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"example.com/rotationfixture/tacplus/model/chassis/grpcserver"
	wrapperJwk "example.com/rotationfixture/tacplus/model/chassis/jwk"
	keylib "example.com/rotationfixture/tacplus/model/chassis/key"
	"example.com/rotationfixture/tacplus/model/chassis/serviceconnection"
	"example.com/rotationfixture/tacplus/model/chassis/servicemanager"
	tokenlib "example.com/rotationfixture/tacplus/model/chassis/token"
	x509wrapper "example.com/rotationfixture/tacplus/model/chassis/x509"
	clients "example.com/rotationfixture/tacplus/model/s2sm/grpc/clients/svc/rotationfixture"
	types "example.com/rotationfixture/tacplus/model/types/svc/rotationfixture"
	pb "example.com/rotationfixture/tacplus/model/types/sys/grpcconfig"
	hostpb "example.com/rotationfixture/tacplus/model/types/sys/host"
	jwkpb "example.com/rotationfixture/tacplus/model/types/sys/jwk"
	keypb "example.com/rotationfixture/tacplus/model/types/sys/key"
	rotation_pb "example.com/rotationfixture/tacplus/model/types/sys/rotation"
	x509_pb "example.com/rotationfixture/tacplus/model/types/sys/x509"
)

// tokenLifetime is every token's lifetime. exp is whole seconds, so a token
// may expire up to a second sooner.
const tokenLifetime = 3 * time.Second

// streamFor is how long the transparency tests run a stream: several lifetimes.
const streamFor = 9 * time.Second

// ---------------------------------------------------------------------------
// The handler: plain handler code, nothing about rotation.
// ---------------------------------------------------------------------------

type handlerEnd struct {
	method string
	err    error
	// cancelled: the stream's context was done when the handler returned (or
	// within a second of its last Recv/Send failing).
	cancelled bool
	// sameContext: stream.Context() returned one value for the handler's life.
	sameContext bool
	received    []int64
}

type tickerHandler struct {
	calls atomic.Int32
	ended chan handlerEnd
}

func newTickerHandler() *tickerHandler {
	return &tickerHandler{ended: make(chan handlerEnd, 16)}
}

func cancelledSoon(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(time.Second):
		return false
	}
}

// SendTicks receives ticks until the caller closes, and answers with how many
// arrived, in order.
func (h *tickerHandler) SendTicks(_ *servicemanager.Caller, stream grpc.ClientStreamingServer[types.Tick, types.Tick]) error {
	h.calls.Add(1)
	ctx := stream.Context()
	end := handlerEnd{method: "SendTicks", sameContext: true}
	defer func() { h.ended <- end }()
	for {
		m, err := stream.Recv()
		if stream.Context() != ctx {
			end.sameContext = false
		}
		if err == io.EOF {
			verdict := "in-order"
			for i, seq := range end.received {
				if seq != int64(i+1) {
					verdict = fmt.Sprintf("out of order at %d: %d", i, seq)
					break
				}
			}
			end.err = stream.SendAndClose(&types.Tick{Seq: int64(len(end.received)), M: verdict})
			return end.err
		}
		if err != nil {
			end.err, end.cancelled = err, cancelledSoon(ctx)
			return err
		}
		end.received = append(end.received, m.Seq)
	}
}

// GenerateTicks sends ticks 1..req.Seq every 50 ms (forever when req.Seq is 0).
func (h *tickerHandler) GenerateTicks(_ *servicemanager.Caller, req *types.Tick, stream grpc.ServerStreamingServer[types.Tick]) error {
	h.calls.Add(1)
	ctx := stream.Context()
	end := handlerEnd{method: "GenerateTicks", sameContext: true}
	defer func() { h.ended <- end }()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for seq := int64(1); req.Seq == 0 || seq <= req.Seq; seq++ {
		select {
		case <-ctx.Done():
			end.err, end.cancelled = ctx.Err(), true
			return ctx.Err()
		case <-t.C:
		}
		if stream.Context() != ctx {
			end.sameContext = false
		}
		if err := stream.Send(&types.Tick{Seq: seq, M: "tick"}); err != nil {
			end.err, end.cancelled = err, cancelledSoon(ctx)
			return err
		}
	}
	return nil
}

// AssistTicks echoes every tick back.
func (h *tickerHandler) AssistTicks(_ *servicemanager.Caller, stream grpc.BidiStreamingServer[types.Tick, types.Tick]) error {
	h.calls.Add(1)
	ctx := stream.Context()
	end := handlerEnd{method: "AssistTicks", sameContext: true}
	defer func() { h.ended <- end }()
	for {
		m, err := stream.Recv()
		if stream.Context() != ctx {
			end.sameContext = false
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			end.err, end.cancelled = err, cancelledSoon(ctx)
			return err
		}
		end.received = append(end.received, m.Seq)
		if err := stream.Send(&types.Tick{Seq: m.Seq, M: "echo"}); err != nil {
			end.err, end.cancelled = err, cancelledSoon(ctx)
			return err
		}
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type fixture struct {
	key     *keylib.ImmutableKey
	addr    string
	handler *tickerHandler
}

func testSigningKey(t *testing.T) (*keylib.ImmutableKey, *wrapperJwk.ImmutableJwk) {
	t.Helper()
	protoKey, err := keylib.NewEd25519Key()
	if err != nil {
		t.Fatalf("NewEd25519Key: %v", err)
	}
	key, err := keylib.NewImmutableKey(protoKey)
	if err != nil {
		t.Fatalf("NewImmutableKey: %v", err)
	}
	pub, ok := key.PublicKey().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("expected ed25519 public key, got %T", key.PublicKey())
	}
	jwk, err := wrapperJwk.NewImmutableJwk(&jwkpb.Jwk{
		Kid: key.Proto().Id,
		Kty: keypb.KeyType_KEY_TYPE_OKP,
		Crv: "Ed25519",
		X:   libBase64.RawURLEncoding.EncodeToString(pub),
	})
	if err != nil {
		t.Fatalf("NewImmutableJwk: %v", err)
	}
	return key, jwk
}

// mint signs a service token for subject, audience rotationfixture, living
// tokenLifetime.
func (f *fixture) mint(subject string) (string, error) {
	tok := tokenlib.NewImmutableToken(nil).
		SetIssuer("test-issuer").
		SetAudience("rotationfixture").
		SetExpiration(time.Now().Add(tokenLifetime).Unix()).
		SetNamespace("/system").
		SetSubType("service").
		SetSubject(subject)
	if err := tok.Sign(f.key); err != nil {
		return "", err
	}
	return tok.GetBase64(), nil
}

func selfSignedCert(t *testing.T) (*x509_pb.X509CertChain, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &stdx509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rotation-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := stdx509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509wrapper.NewX509Cert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err != nil {
		t.Fatalf("NewX509Cert: %v", err)
	}
	return &x509_pb.X509CertChain{Chain: []*x509_pb.X509Cert{cert}}, priv
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// newWrapper is the generated TickerService behind the generated Authorizer,
// wired as the generated ServiceManager wires them.
func newWrapper(t *testing.T, ctx context.Context, h *tickerHandler) (*TickerService, *keylib.ImmutableKey) {
	t.Helper()
	key, jwk := testSigningKey(t)
	auth, err := grpcserver.NewAuthorizer(ctx, nil)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	auth.JwkCache().Load([]*wrapperJwk.ImmutableJwk{jwk})
	RegisterTickerServiceMethodAuth(auth)
	wrapper := NewTickerService(h)
	wrapper.SetAuthorizeFunc(auth.Authorize)
	wrapper.SetAuthenticateFunc(auth.Authenticate)
	wrapper.SetAuthenticateAtOrBelowFunc(auth.AuthenticateAtOrBelow)
	return wrapper, key
}

// startServer serves the generated TickerService (and its sibling) on the
// generated grpcserver.
func startServer(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := newTickerHandler()
	wrapper, key := newWrapper(t, ctx, h)
	cert, certKey := selfSignedCert(t)
	port := freePort(t)
	cfg := &pb.Config{ListenAddress: &hostpb.Host{Host: "127.0.0.1", Port: int32(port)}}
	srv, err := grpcserver.New(ctx, cfg, nil, cert, certKey, wrapper, nil)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}
	go func() { _ = srv.Listen(ctx) }()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	waitListening(t, addr)
	return &fixture{key: key, addr: addr, handler: h}
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server never listened on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dial(t *testing.T, addr string, creds credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func tlsCreds() credentials.TransportCredentials {
	// The certificate identity is not under test; rotation is.
	return credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
}

// tokenSource is the caller's token provider: caller-svc tokens, counted,
// until revoke (then the mint fails) or impersonate (then another subject's).
type tokenSource struct {
	f           *fixture
	mints       atomic.Int32
	revoked     atomic.Bool
	impersonate atomic.Pointer[string]
}

func (s *tokenSource) provide() (string, error) {
	if s.revoked.Load() {
		return "", errors.New("identity: caller revoked")
	}
	s.mints.Add(1)
	subject := "caller-svc"
	if other := s.impersonate.Load(); other != nil {
		subject = *other
	}
	return s.f.mint(subject)
}

// client is the generated S2S client, over the generated ChannelProvider.
func (f *fixture) client(t *testing.T, addr string, creds credentials.TransportCredentials) (*clients.TickerServiceClient, *tokenSource) {
	t.Helper()
	src := &tokenSource{f: f}
	cp := serviceconnection.NewStaticChannelProvider("rotationfixture", dial(t, addr, creds))
	return clients.NewTickerServiceClient(cp, src.provide), src
}

func (f *fixture) handlerEnd(t *testing.T, within time.Duration) handlerEnd {
	t.Helper()
	select {
	case e := <-f.handler.ended:
		return e
	case <-time.After(within):
		t.Fatalf("the handler did not return within %s", within)
		return handlerEnd{}
	}
}

func checkSequence(t *testing.T, what string, got []int64, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("%s: got %d messages, want %d (loss or duplication)", what, len(got), n)
	}
	for i, seq := range got {
		if seq != int64(i+1) {
			t.Fatalf("%s: message %d is seq %d, want %d (reordering, loss or duplication)", what, i, seq, i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// Transparency: one stream, several token lifetimes, plain code on both ends.
// ---------------------------------------------------------------------------

// TestGoRotationBidiBothWaysAcrossLifetimes: the caller sends a tick every
// 2 ms for streamFor while the handler echoes each back, both directions at
// once, across several 3 s token lifetimes. Every tick arrives at the handler
// and back exactly once, in order, on one handler call with one context and
// one connect of the caller's stream; the tokens were renewed several times.
func TestGoRotationBidiBothWaysAcrossLifetimes(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	var mu sync.Mutex
	var echoed []int64
	var connects atomic.Int32
	h := c.AssistTicks(context.Background(),
		func(func(*types.Tick) error) { connects.Add(1) },
		func(m *types.Tick) {
			mu.Lock()
			echoed = append(echoed, m.Seq)
			mu.Unlock()
		})
	start := time.Now()
	n := 0
	for time.Since(start) < streamFor {
		n++
		for h.Send(&types.Tick{Seq: int64(n), M: "up"}) != nil {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(2 * time.Millisecond)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := len(echoed)
		mu.Unlock()
		if got >= n || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.Close()
	mu.Lock()
	checkSequence(t, "echoed", echoed, n)
	mu.Unlock()
	end := f.handlerEnd(t, 5*time.Second)
	checkSequence(t, "received by the handler", end.received, n)
	// The handle's Close cancels its stream, as it always has: the handler
	// sees its end as a cancel (or a clean end, if the close won the race).
	if end.err != nil && status.Code(end.err) != codes.Canceled {
		t.Fatalf("handler ended with %v, want a clean end or the caller's cancel", end.err)
	}
	if !end.sameContext {
		t.Fatalf("the handler's stream context changed during the stream")
	}
	if got := f.handler.calls.Load(); got != 1 {
		t.Fatalf("handler called %d times, want 1 (one logical stream)", got)
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("the caller's stream connected %d times, want 1 (no reconnect)", got)
	}
	if got := src.mints.Load(); got < 4 {
		t.Fatalf("%d tokens minted over %s of %s tokens, want at least 4 (the stream did not rotate)", got, streamFor, tokenLifetime)
	}
}

// TestGoRotationServerStreamingAcrossLifetimes: 160 ticks at 50 ms, across
// several lifetimes, all delivered in order, then a clean end.
func TestGoRotationServerStreamingAcrossLifetimes(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	const n = 160
	s, err := c.GenerateTicks(context.Background(), &types.Tick{Seq: n})
	if err != nil {
		t.Fatalf("GenerateTicks: %v", err)
	}
	var got []int64
	for {
		m, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv after %d ticks: %v", len(got), err)
		}
		got = append(got, m.Seq)
	}
	checkSequence(t, "ticks", got, n)
	end := f.handlerEnd(t, 5*time.Second)
	if end.err != nil || !end.sameContext {
		t.Fatalf("handler ended with %v (same context: %v), want a clean end on one context", end.err, end.sameContext)
	}
	if got := src.mints.Load(); got < 3 {
		t.Fatalf("%d tokens minted, want at least 3 (the stream did not rotate)", got)
	}
}

// TestGoRotationClientStreamingAcrossLifetimes: 160 ticks at 50 ms, across
// several lifetimes; the handler counts them all, in order.
func TestGoRotationClientStreamingAcrossLifetimes(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	const n = 160
	s, err := c.SendTicks(context.Background())
	if err != nil {
		t.Fatalf("SendTicks: %v", err)
	}
	for i := int64(1); i <= n; i++ {
		if err := s.Send(&types.Tick{Seq: i, M: "up"}); err != nil {
			_, err = s.CloseAndRecv()
			t.Fatalf("Send %d: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := s.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	if resp.Seq != n || resp.M != "in-order" {
		t.Fatalf("handler counted %d ticks (%s), want %d in-order", resp.Seq, resp.M, n)
	}
	if got := src.mints.Load(); got < 3 {
		t.Fatalf("%d tokens minted, want at least 3 (the stream did not rotate)", got)
	}
}

// ---------------------------------------------------------------------------
// Fail closed
// ---------------------------------------------------------------------------

// endsAt runs an endless server stream and returns how it ended and when.
func endsAt(t *testing.T, c *clients.TickerServiceClient, ctx context.Context) (error, time.Time) {
	t.Helper()
	s, err := c.GenerateTicks(ctx, &types.Tick{Seq: 0})
	if err != nil {
		return err, time.Now()
	}
	for {
		if _, err := s.Recv(); err != nil {
			return err, time.Now()
		}
	}
}

// TestGoRotationMintFailureEndsAtExpiry: the caller gets its opening token
// and no other. The stream is not rotated, and ends at the token's exp with
// UNAUTHENTICATED; the handler's context is cancelled.
func TestGoRotationMintFailureEndsAtExpiry(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	ctx, cancel := context.WithTimeout(context.Background(), 4*tokenLifetime)
	defer cancel()
	opened := time.Now()
	go func() {
		for src.mints.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		src.revoked.Store(true)
	}()
	err, ended := endsAt(t, c, ctx)
	if ctx.Err() != nil {
		t.Fatalf("the stream outlived its only token: still open at the test's bound (%v)", err)
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("stream ended with %v (%v), want UNAUTHENTICATED", got, err)
	}
	if took := ended.Sub(opened); took < tokenLifetime-1500*time.Millisecond || took > tokenLifetime+3*time.Second {
		t.Fatalf("stream ended %s after open, want about one token lifetime (%s)", took, tokenLifetime)
	}
	if end := f.handlerEnd(t, 5*time.Second); !end.cancelled {
		t.Fatalf("the handler's context was not cancelled when its stream ended (%v)", end.err)
	}
}

// TestGoRotationRevokedCallerCutOff: a caller revoked mid-stream, after some
// rotations, gets no new token and is cut off within one token lifetime.
func TestGoRotationRevokedCallerCutOff(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	ctx, cancel := context.WithTimeout(context.Background(), 6*tokenLifetime)
	defer cancel()
	var revokedAt atomic.Int64
	go func() {
		for src.mints.Load() < 3 {
			time.Sleep(time.Millisecond)
		}
		src.revoked.Store(true)
		revokedAt.Store(time.Now().UnixNano())
	}()
	err, ended := endsAt(t, c, ctx)
	if ctx.Err() != nil {
		t.Fatalf("a revoked caller's stream was still open at the test's bound (%v)", err)
	}
	if revokedAt.Load() == 0 {
		t.Fatalf("the stream ended (%v) before the caller was revoked", err)
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("stream ended with %v (%v), want UNAUTHENTICATED", got, err)
	}
	if after := ended.Sub(time.Unix(0, revokedAt.Load())); after > tokenLifetime+time.Second {
		t.Fatalf("revoked caller cut off %s after revocation, want within one token lifetime (%s)", after, tokenLifetime)
	}
	f.handlerEnd(t, 5*time.Second)
}

// TestGoRotationImpersonationEndsTheStream: a rotation presenting another
// subject's token (one the method's policy admits) is refused, and the
// caller's stream ends PERMISSION_DENIED, not continued under the other name.
func TestGoRotationImpersonationEndsTheStream(t *testing.T) {
	f := startServer(t)
	c, src := f.client(t, f.addr, tlsCreds())
	ctx, cancel := context.WithTimeout(context.Background(), 4*tokenLifetime)
	defer cancel()
	go func() {
		for src.mints.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		other := "other-svc"
		src.impersonate.Store(&other)
	}()
	err, _ := endsAt(t, c, ctx)
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("stream ended with %v (%v), want PERMISSION_DENIED", got, err)
	}
	if end := f.handlerEnd(t, 5*time.Second); !end.cancelled {
		t.Fatalf("the handler's context was not cancelled (%v)", end.err)
	}
}

// ---------------------------------------------------------------------------
// The wire
// ---------------------------------------------------------------------------

const assistRotating = "/rotationfixture.TickerServiceRotating/AssistTicks"

var bidiDesc = grpc.StreamDesc{ServerStreams: true, ClientStreams: true}

// openLeg opens a leg of AssistTicks' sibling with subject's token, presenting
// joining (empty: a new session), and returns it with the server's first
// envelope or the error that refused it.
func (f *fixture) openLeg(t *testing.T, conn *grpc.ClientConn, ctx context.Context, subject, joining string) (grpc.ClientStream, *rotation_pb.Envelope, error) {
	t.Helper()
	token, err := f.mint(subject)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	cs, err := conn.NewStream(ctx, &bidiDesc, assistRotating)
	if err != nil {
		return nil, nil, err
	}
	if err := cs.SendMsg(&rotation_pb.Envelope{SessionId: joining}); err != nil && err != io.EOF {
		return nil, nil, err
	}
	first := &rotation_pb.Envelope{}
	if err := cs.RecvMsg(first); err != nil {
		return nil, nil, err
	}
	return cs, first, nil
}

func refusedWithoutReason(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	st := status.Convert(err)
	if st.Code() != want {
		t.Fatalf("%s: got %v (%v), want %v", what, st.Code(), err, want)
	}
	if st.Message() != "" {
		t.Fatalf("%s: the refusal carries a description %q; it must carry none", what, st.Message())
	}
}

// TestGoRotationAnotherSubjectRefused: a rotation presenting a valid session
// id with another subject's token (one the policy admits) is refused
// PERMISSION_DENIED with no description, as is an unknown id; the same
// subject's new token is attached.
func TestGoRotationAnotherSubjectRefused(t *testing.T) {
	f := startServer(t)
	conn := dial(t, f.addr, tlsCreds())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, first, err := f.openLeg(t, conn, ctx, "caller-svc", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	id := first.SessionId
	if id == "" || len(first.Payload) > 0 || first.Last || first.Fin {
		t.Fatalf("the server's first envelope is %v, want a session id and nothing else", first)
	}
	_, _, err = f.openLeg(t, conn, ctx, "other-svc", id)
	refusedWithoutReason(t, "another subject's rotation", err, codes.PermissionDenied)
	_, _, err = f.openLeg(t, conn, ctx, "caller-svc", "no-such-session")
	refusedWithoutReason(t, "an unknown session's rotation", err, codes.PermissionDenied)
	// A caller the method's policy does not admit is refused by the method's
	// own wrapper: the stream's status, with no description.
	intruder, _, err := f.openLeg(t, conn, ctx, "intruder-svc", "")
	if err == nil {
		err = intruder.RecvMsg(&rotation_pb.Envelope{})
	}
	refusedWithoutReason(t, "a caller the policy does not admit", err, codes.PermissionDenied)
	_, echoed, err := f.openLeg(t, conn, ctx, "caller-svc", id)
	if err != nil || echoed.SessionId != id {
		t.Fatalf("the same subject's rotation: %v (%v), want attached", echoed, err)
	}
}

// TestGoRotationCutoverLosesNothing drives a cutover by hand: messages before
// and after a last both ways, with the server's sends still in flight on the
// old leg; the handler sees every message once, in order, on one call.
func TestGoRotationCutoverLosesNothing(t *testing.T) {
	f := startServer(t)
	conn := dial(t, f.addr, tlsCreds())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old, first, err := f.openLeg(t, conn, ctx, "caller-svc", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	send := func(cs grpc.ClientStream, seq int64) {
		b, _ := proto.Marshal(&types.Tick{Seq: seq})
		if err := cs.SendMsg(&rotation_pb.Envelope{Payload: b}); err != nil {
			t.Fatalf("send %d: %v", seq, err)
		}
	}
	for i := int64(1); i <= 50; i++ {
		send(old, i)
	}
	next, echoed, err := f.openLeg(t, conn, ctx, "caller-svc", first.SessionId)
	if err != nil || echoed.SessionId != first.SessionId {
		t.Fatalf("rotation: %v (%v)", echoed, err)
	}
	if err := old.SendMsg(&rotation_pb.Envelope{Last: true}); err != nil {
		t.Fatalf("last: %v", err)
	}
	_ = old.CloseSend()
	for i := int64(51); i <= 100; i++ {
		send(next, i)
	}
	if err := next.SendMsg(&rotation_pb.Envelope{Fin: true}); err != nil {
		t.Fatalf("fin: %v", err)
	}
	var got []int64
	recv := func(cs grpc.ClientStream) (last bool, err error) {
		e := &rotation_pb.Envelope{}
		if err := cs.RecvMsg(e); err != nil {
			return false, err
		}
		if len(e.Payload) > 0 {
			m := &types.Tick{}
			if err := proto.Unmarshal(e.Payload, m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got = append(got, m.Seq)
		}
		return e.Last, nil
	}
	for {
		last, err := recv(old)
		if err != nil {
			t.Fatalf("the old leg ended (%v) before the server's last", err)
		}
		if last {
			break
		}
	}
	if err := old.RecvMsg(&rotation_pb.Envelope{}); err != io.EOF {
		t.Fatalf("the old leg ended with %v after its last, want OK", err)
	}
	for {
		if _, err := recv(next); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("the new leg ended with %v, want OK", err)
		}
	}
	checkSequence(t, "echoed across the cutover", got, 100)
	end := f.handlerEnd(t, 5*time.Second)
	checkSequence(t, "received across the cutover", end.received, 100)
	if f.handler.calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", f.handler.calls.Load())
	}
}

// TestGoRotationLegCutWithoutLastEndsTheStream: a leg that ends without its
// last (here the caller's connection is cut) ends the logical stream: the
// caller gets the status, the handler's context is cancelled, and nothing is
// spliced onto another leg.
func TestGoRotationLegCutWithoutLastEndsTheStream(t *testing.T) {
	f := startServer(t)
	proxy := startProxy(t, f.addr)
	c, _ := f.client(t, proxy.addr(), tlsCreds())
	s, err := c.GenerateTicks(context.Background(), &types.Tick{Seq: 0})
	if err != nil {
		t.Fatalf("GenerateTicks: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Recv(); err != nil {
			t.Fatalf("Recv: %v", err)
		}
	}
	proxy.cut()
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := s.Recv(); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		if code := status.Code(err); code == codes.OK || err == io.EOF {
			t.Fatalf("the cut stream ended %v (%v), want a failure status", code, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the cut stream did not end")
	}
	if end := f.handlerEnd(t, 5*time.Second); !end.cancelled {
		t.Fatalf("the handler's context was not cancelled by the cut (%v)", end.err)
	}
}

// TestGoRotationFallsBackToPlainMethod: against a server that serves only the
// plain service (no sibling), the generated client's stream goes to the plain
// method, which works, and ends at its token's exp with UNAUTHENTICATED (#260).
func TestGoRotationFallsBackToPlainMethod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newTickerHandler()
	wrapper, key := newWrapper(t, ctx, h)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := grpc.NewServer(grpc.ChainStreamInterceptor(grpcserver.StreamCredentialExpiryInterceptor()))
	types.RegisterTickerServiceServer(srv, wrapper)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	f := &fixture{key: key, addr: lis.Addr().String(), handler: h}
	c, _ := f.client(t, f.addr, insecure.NewCredentials())
	s, err := c.GenerateTicks(ctx, &types.Tick{Seq: 0})
	if err != nil {
		t.Fatalf("GenerateTicks: %v", err)
	}
	ticks := 0
	opened := time.Now()
	for {
		if _, err = s.Recv(); err != nil {
			break
		}
		ticks++
	}
	if ticks == 0 {
		t.Fatalf("no tick arrived on the plain method: %v", err)
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("the plain stream ended with %v (%v), want UNAUTHENTICATED at its token's exp", got, err)
	}
	if took := time.Since(opened); took > tokenLifetime+3*time.Second {
		t.Fatalf("the plain stream lasted %s, want it ended at its token's exp", took)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// proxy forwards TCP connections to a target until cut, when it closes them all.
type proxy struct {
	lis   net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startProxy(t *testing.T, target string) *proxy {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	p := &proxy{lis: lis}
	t.Cleanup(func() { lis.Close(); p.cut() })
	go func() {
		for {
			in, err := lis.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, in, out)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(out, in); out.Close() }()
			go func() { _, _ = io.Copy(in, out); in.Close() }()
		}
	}()
	return p
}

func (p *proxy) addr() string { return p.lis.Addr().String() }

func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}
