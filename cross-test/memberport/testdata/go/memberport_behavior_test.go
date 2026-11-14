// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package memberport

// The generated Go member port (tacplus#212, on gRPC since #321, in Go since
// #322), driven over real sockets in-process: memberport-peer <-> member port
// <-> member port <-> memberport-peer, with no Hazelcast, and hand-driven
// tunnel streams for what a generated member port never sends. The Go twin of
// MemberPortBehaviorTest.java. The fleet is faked: a kernel signing key and
// its JWK, a CA (the fleet trust bundle) and serving certificates with SPIFFE
// SANs.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	libBase64 "encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wrapperJwk "example.com/grid/tacplus/model/chassis/jwk"
	keylib "example.com/grid/tacplus/model/chassis/key"
	tokenlib "example.com/grid/tacplus/model/chassis/token"
	jwkpb "example.com/grid/tacplus/model/types/sys/jwk"
	keypb "example.com/grid/tacplus/model/types/sys/key"
	memberport_pb "example.com/grid/tacplus/model/types/sys/memberport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	testTD  = "example.org"
	testSvc = "grid"
	tenantX = "/tenants/aaaaaaaa"
	tenantY = "/tenants/bbbbbbbb"
	mib     = 1024 * 1024
)

var testAud = AudienceFor(testSvc)

// ---- The fake fleet ----

type fleet struct {
	kernel *keylib.ImmutableKey
	jwkMu  sync.Mutex
	jwks   map[string]*wrapperJwk.ImmutableJwk
	caKey  *ecdsa.PrivateKey
	ca     *x509.Certificate
	serial atomic.Int64
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	f := &fleet{kernel: newKernelKey(t), jwks: make(map[string]*wrapperJwk.ImmutableJwk)}
	f.jwks[f.kernel.Proto().Id] = jwkFor(t, f.kernel)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fleet-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	f.caKey = caKey
	f.ca, _ = x509.ParseCertificate(der)
	f.serial.Store(100)
	return f
}

func newKernelKey(t *testing.T) *keylib.ImmutableKey {
	t.Helper()
	pk, err := keylib.NewEd25519Key()
	if err != nil {
		t.Fatal(err)
	}
	k, err := keylib.NewImmutableKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func jwkFor(t *testing.T, key *keylib.ImmutableKey) *wrapperJwk.ImmutableJwk {
	t.Helper()
	pub := key.PublicKey().(ed25519.PublicKey)
	j, err := wrapperJwk.NewImmutableJwk(&jwkpb.Jwk{
		Kid: key.Proto().Id,
		Kty: keypb.KeyType_KEY_TYPE_OKP,
		Crv: "Ed25519",
		X:   libBase64.RawURLEncoding.EncodeToString(pub),
	})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// lookup is the fleet's JWKS as a member port sees it.
func (f *fleet) lookup(kid string) *wrapperJwk.ImmutableJwk {
	f.jwkMu.Lock()
	defer f.jwkMu.Unlock()
	return f.jwks[kid]
}

func (f *fleet) mint(service, namespace, audience string, ttlSeconds int64) string {
	return f.mintWith(f.kernel, service, namespace, audience, time.Now().Unix()+ttlSeconds)
}

func (f *fleet) mintWith(key *keylib.ImmutableKey, service, namespace, audience string, exp int64) string {
	tok := tokenlib.NewImmutableToken(nil).
		SetIssuer("kernel").
		SetAudience(audience).
		SetSubType("service").
		SetSubject(service).
		SetNamespace(namespace).
		SetExpiration(exp)
	if err := tok.Sign(key); err != nil {
		panic(err)
	}
	return tok.GetBase64()
}

// leaf is a serving certificate naming spiffe://example.org<namespace>/services/<service>.
func (f *fleet) leaf(t *testing.T, namespace, service, keyType string) *tls.Certificate {
	t.Helper()
	var pub crypto.PublicKey
	var priv crypto.PrivateKey
	if keyType == "Ed25519" {
		p, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = p, k
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pub, priv = &k.PublicKey, k
	}
	uri, _ := url.Parse("spiffe://" + testTD + namespace + "/services/" + service)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(f.serial.Add(1)),
		Subject:      pkix.Name{CommonName: service},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.ca, pub, f.caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return &tls.Certificate{Certificate: [][]byte{der, f.ca.Raw}, PrivateKey: priv, Leaf: leaf}
}

// minter is a member's token source, like the ServiceManager's: a cached
// token, renewed once fewer than renewBefore seconds remain. It can be made to
// stall (the mint hangs) or to never renew.
type minter struct {
	fleet       *fleet
	namespace   string
	ttl         int64
	renewBefore int64
	mints       atomic.Int32
	stall       atomic.Pointer[chan struct{}]
	neverRenew  atomic.Bool
	mu          sync.Mutex
	cached      string
}

func newMinter(f *fleet, namespace string, ttl, renewBefore int64) *minter {
	return &minter{fleet: f, namespace: namespace, ttl: ttl, renewBefore: renewBefore}
}

func (m *minter) token() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != "" {
		tw, err := tokenlib.ParseToken(m.cached)
		if err != nil {
			return "", err
		}
		if m.neverRenew.Load() || tw.GetExpiration()-time.Now().Unix() > m.renewBefore {
			return m.cached, nil
		}
	}
	if s := m.stall.Load(); s != nil {
		<-*s
	}
	m.mints.Add(1)
	m.cached = m.fleet.mint(testSvc, m.namespace, testAud, m.ttl)
	return m.cached, nil
}

// ---- Fixture plumbing ----

type fixture struct {
	t     *testing.T
	fleet *fleet
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, fleet: newFleet(t)}
}

func (fx *fixture) memberWith(tokens TokenSource, cert *atomic.Pointer[tls.Certificate], jwks JwkLookup) *MemberPort {
	fx.t.Helper()
	p, err := New(testSvc, testTD, []*x509.Certificate{fx.fleet.ca}, tokens, jwks, cert.Load)
	if err != nil {
		fx.t.Fatalf("New: %v", err)
	}
	fx.t.Cleanup(p.Close)
	return p
}

func certRef(c *tls.Certificate) *atomic.Pointer[tls.Certificate] {
	var p atomic.Pointer[tls.Certificate]
	p.Store(c)
	return &p
}

func (fx *fixture) member(namespace string) *MemberPort {
	return fx.memberWith(newMinter(fx.fleet, namespace, 300, 60).token,
		certRef(fx.fleet.leaf(fx.t, namespace, testSvc, "Ed25519")), fx.fleet.lookup)
}

func (fx *fixture) listen(p *MemberPort, target string) *net.TCPAddr {
	fx.t.Helper()
	addr, err := p.Listen("127.0.0.1:0", target)
	if err != nil {
		fx.t.Fatalf("Listen: %v", err)
	}
	return addr
}

func (fx *fixture) forward(p *MemberPort, remote *net.TCPAddr) *net.TCPAddr {
	fx.t.Helper()
	addr, err := p.Forward(remote.String())
	if err != nil {
		fx.t.Fatalf("Forward: %v", err)
	}
	return addr
}

// tunnel is peer client -> a.Forward -> b.Listen -> peer server; it returns
// the address the client dials.
func (fx *fixture) tunnel(a, b *MemberPort, server *peerServer) *net.TCPAddr {
	return fx.forward(a, fx.listen(b, server.address()))
}

func (fx *fixture) echo() *peerServer {
	return newPeerServer(fx.t, echoMode, 0, 0, 0, 16*1024, 0)
}

func (fx *fixture) push(pushSeed, pushBytes, expectSeed int64) *peerServer {
	return newPeerServer(fx.t, pushMode, pushSeed, pushBytes, expectSeed, 16*1024, 0)
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (fx *fixture) serverResult(s *peerServer, id int32, timeout time.Duration) peerResult {
	fx.t.Helper()
	waitFor(fx.t, "server result", timeout, func() bool { _, ok := s.result(id); return ok })
	r, _ := s.result(id)
	return r
}

// ---- Raw member tunnel client (for faults) ----

// rawConn is a gRPC channel to a member port that skips the member port's own
// checks of the receiver.
func (fx *fixture) rawConn(addr *net.TCPAddr) *grpc.ClientConn {
	fx.t.Helper()
	conn, err := grpc.NewClient("passthrough:///"+addr.String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})))
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// rawStream is one member tunnel stream, driven by hand: what a peer that is
// not a generated member port can send.
type rawStream struct {
	t        *testing.T
	cs       grpc.ClientStream
	cancel   context.CancelFunc
	mu       sync.Mutex
	received chan *memberport_pb.MemberChunk
	closed   chan *status.Status
}

func (fx *fixture) raw(conn *grpc.ClientConn, token string) *rawStream {
	fx.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fx.t.Cleanup(cancel)
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	cs, err := conn.NewStream(ctx, &StreamDesc, FullMethod)
	if err != nil {
		fx.t.Fatalf("NewStream: %v", err)
	}
	r := &rawStream{t: fx.t, cs: cs, cancel: cancel, received: make(chan *memberport_pb.MemberChunk, 4096), closed: make(chan *status.Status, 1)}
	go func() {
		for {
			c := &memberport_pb.MemberChunk{}
			if err := cs.RecvMsg(c); err != nil {
				if err == io.EOF {
					r.closed <- status.New(codes.OK, "")
				} else {
					r.closed <- status.Convert(err)
				}
				return
			}
			r.received <- c
		}
	}()
	return r
}

func (r *rawStream) send(c *memberport_pb.MemberChunk) *rawStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.cs.SendMsg(c)
	return r
}

func (r *rawStream) next() *memberport_pb.MemberChunk {
	r.t.Helper()
	select {
	case c := <-r.received:
		return c
	case <-time.After(10 * time.Second):
		r.t.Fatalf("no chunk within 10s")
		return nil
	}
}

func (r *rawStream) status(timeout time.Duration) *status.Status {
	r.t.Helper()
	select {
	case s := <-r.closed:
		return s
	case <-time.After(timeout):
		r.t.Fatalf("the stream did not end within %v", timeout)
		return nil
	}
}

func openChunk() *memberport_pb.MemberChunk { return &memberport_pb.MemberChunk{} }

func joinChunk(id string) *memberport_pb.MemberChunk {
	return &memberport_pb.MemberChunk{SessionId: id}
}

func dataChunk(b ...byte) *memberport_pb.MemberChunk {
	return &memberport_pb.MemberChunk{Payload: b}
}

// openSession opens a new session and returns its id.
func openSession(t *testing.T, r *rawStream) string {
	t.Helper()
	first := r.send(openChunk()).next()
	if first.SessionId == "" {
		t.Fatalf("the server's first chunk names the session: %v", first)
	}
	return first.SessionId
}

// assertRefused checks the status of a refused stream: the code, and no reason text.
func assertRefused(t *testing.T, code codes.Code, s *status.Status, what string) {
	t.Helper()
	if s.Code() != code {
		t.Fatalf("%s: status %v, want %v", what, s.Code(), code)
	}
	if s.Message() != "" {
		t.Fatalf("%s: a refusal carries no reason text, got %q", what, s.Message())
	}
}

// dropped reads until the peer drops the connection; true when it did within the timeout.
func dropped(c net.Conn, timeout time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			var ne net.Error
			return !(errors.As(err, &ne) && ne.Timeout())
		}
	}
}

func dialTCP(t *testing.T, addr net.Addr) *net.TCPConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr.String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c.(*net.TCPConn)
}

// ==== Correctness ====

func TestMemberPortBytesCrossIntactBothDirectionsFromOneByteToMultiMegabyte(t *testing.T) {
	fx := newFixture(t)
	a, b := fx.member(tenantX), fx.member(tenantX)
	for _, n := range []int64{1, 1000, 65_536, 65_537, 3*mib + 17} {
		server := fx.push(2_000+n, n, 1_000+n)
		fwd := fx.tunnel(a, b, server)
		if client := exchange(fwd, 1_000+n, n, 2_000+n); !client.ok(n) {
			t.Fatalf("client side of %d bytes: %v", n, client)
		}
		if r := fx.serverResult(server, 1, 10*time.Second); !r.ok(n) {
			t.Fatalf("server side of %d bytes: %v", n, r)
		}
	}
	fwd := fx.tunnel(a, b, fx.echo())
	if r := exchange(fwd, 77, 2*mib, 77); !r.ok(2 * mib) {
		t.Fatalf("echo: %v", r)
	}
}

func TestMemberPortManyConcurrentConnections(t *testing.T) {
	fx := newFixture(t)
	echo := fx.echo()
	fwd := fx.tunnel(fx.member(tenantX), fx.member(tenantX), echo)
	results := make([]peerResult, 48)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seed := int64(10_000 + i)
			results[i] = exchange(fwd, seed, 256*1024+seed, seed)
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if !r.ok(256*1024 + int64(10_000+i)) {
			t.Fatalf("connection %d: %v", i, r)
		}
	}
	if got := echo.accepted.Load(); got != 48 {
		t.Fatalf("accepted %d connections, want 48", got)
	}
}

func TestMemberPortHalfCloseInEitherDirection(t *testing.T) {
	fx := newFixture(t)
	a, b := fx.member(tenantX), fx.member(tenantX)
	// The client half-closes at once; the server still pushes 1 MiB after.
	if r := exchange(fx.tunnel(a, b, fx.push(5, mib, 6)), 6, 0, 5); !r.ok(mib) {
		t.Fatalf("client half-closed first: %v", r)
	}
	// The server half-closes at once; the client still sends 1 MiB after.
	serverDone := fx.push(7, 0, 8)
	if r := exchange(fx.tunnel(a, b, serverDone), 8, mib, 7); !r.ok(0) {
		t.Fatalf("server half-closed first: %v", r)
	}
	if r := fx.serverResult(serverDone, 1, 10*time.Second); !r.ok(mib) {
		t.Fatalf("server side: %v", r)
	}
}

func TestMemberPortByteSessionRunsAcrossSeveralTokenLifetimesBothWaysAtOnce(t *testing.T) {
	fx := newFixture(t)
	// 3-second tokens renewed with 2 seconds left: the client rotates at 75%
	// of each lifetime, about every 2.25 seconds.
	am, bm := newMinter(fx.fleet, tenantX, 3, 2), newMinter(fx.fleet, tenantX, 3, 2)
	a := fx.memberWith(am.token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), fx.fleet.lookup)
	b := fx.memberWith(bm.token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), fx.fleet.lookup)
	// Both directions trickle at once for about 8 seconds, so every cutover
	// has bytes in flight each way.
	server := newPeerServer(t, pushMode, 21, 800_000, 22, 4096, 20*time.Millisecond)
	fwd := fx.tunnel(a, b, server)
	start := time.Now()
	r := exchangeWith(fwd, 22, 800_000, 21, 4096, 20*time.Millisecond, 0)
	elapsed := time.Since(start)
	if !r.ok(800_000) {
		t.Fatalf("client: no loss, duplication or reordering: %v", r)
	}
	if sr := fx.serverResult(server, 1, 10*time.Second); !sr.ok(800_000) {
		t.Fatalf("server: %v", sr)
	}
	if elapsed < 6500*time.Millisecond {
		t.Fatalf("the session must outlive two token lifetimes, took %v", elapsed)
	}
	if got := server.accepted.Load(); got != 1 {
		t.Fatalf("one byte session: the target was dialled %d times", got)
	}
	if got := am.mints.Load(); got < 3 {
		t.Fatalf("the client rotated onto new tokens: %d mints", got)
	}
}

// ==== Backpressure ====

func TestMemberPortSlowReaderBoundsBufferingAndDropsNothing(t *testing.T) {
	fx := newFixture(t)
	server := fx.push(31, 0, 32)
	server.readPause.Store(int64(time.Hour)) // reads one chunk, then stalls
	fwd := fx.tunnel(fx.member(tenantX), fx.member(tenantX), server)
	const total = 64 * mib
	var written atomic.Int64
	c := dialTCP(t, fwd)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		stream := newPeerStream(32)
		buf := make([]byte, 64*1024)
		for written.Load() < total {
			n := min(int64(len(buf)), total-written.Load())
			stream.fill(buf[:n])
			if _, err := c.Write(buf[:n]); err != nil {
				return // The assertion on the server's result reports it.
			}
			written.Add(n)
		}
		_ = c.CloseWrite()
	}()
	time.Sleep(3 * time.Second)
	if buffered := written.Load(); buffered >= 24*mib {
		t.Fatalf("a stalled reader must stall the writer, not buffer: %d bytes written of %d", buffered, total)
	}
	// Unstall the reader: every byte must still arrive, in order.
	server.readPause.Store(0)
	server.clearResults()
	select {
	case <-writerDone:
	case <-time.After(120 * time.Second):
		t.Fatal("the writer did not finish")
	}
	if written.Load() != total {
		t.Fatalf("written %d of %d", written.Load(), total)
	}
	if r := fx.serverResult(server, 1, 120*time.Second); !r.ok(total) {
		t.Fatalf("server: %v", r)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > 512*mib {
		t.Fatalf("heap stays bounded: %d bytes", ms.HeapAlloc)
	}
}

// ==== Refusals: fail fast, nothing forwarded ====

func (fx *fixture) listenerOnX(behind *peerServer) *net.TCPAddr {
	return fx.listen(fx.member(tenantX), behind.address())
}

// assertOpenRefused opens a stream with token and asserts how it is refused,
// and that nothing was dialled.
func (fx *fixture) assertOpenRefused(code codes.Code, what, token string) {
	fx.t.Helper()
	behind := fx.echo()
	r := fx.raw(fx.rawConn(fx.listenerOnX(behind)), token)
	r.send(openChunk()).send(dataChunk(1, 2, 3))
	assertRefused(fx.t, code, r.status(5*time.Second), what)
	time.Sleep(200 * time.Millisecond)
	if behind.accepted.Load() != 0 {
		fx.t.Fatalf("%s: nothing may reach the peer behind the port", what)
	}
}

func TestMemberPortBadMissingOrExpiredTokensAreUnauthenticated(t *testing.T) {
	fx := newFixture(t)
	fx.assertOpenRefused(codes.Unauthenticated, "no token", "")
	fx.assertOpenRefused(codes.Unauthenticated, "garbage token", "not-a-token")
	// Ten seconds past: inside the JWT clock-skew window, still refused.
	fx.assertOpenRefused(codes.Unauthenticated, "expired token", fx.fleet.mint(testSvc, tenantX, testAud, -10))
	fx.assertOpenRefused(codes.Unauthenticated, "gRPC audience", fx.fleet.mint(testSvc, tenantX, testSvc, 300))
	rogue := newKernelKey(t)
	fx.assertOpenRefused(codes.Unauthenticated, "unknown signing key",
		fx.fleet.mintWith(rogue, testSvc, tenantX, testAud, time.Now().Unix()+300))
}

func TestMemberPortValidTokensNotAllowedHereArePermissionDenied(t *testing.T) {
	fx := newFixture(t)
	fx.assertOpenRefused(codes.PermissionDenied, "tenant Y's token", fx.fleet.mint(testSvc, tenantY, testAud, 300))
	fx.assertOpenRefused(codes.PermissionDenied, "another service", fx.fleet.mint("other", tenantX, testAud, 300))
}

func TestMemberPortADataChunkCannotOpenAStream(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	r := fx.raw(fx.rawConn(fx.listenerOnX(behind)), fx.fleet.mint(testSvc, tenantX, testAud, 300))
	r.send(dataChunk(1, 2, 3))
	assertRefused(t, codes.PermissionDenied, r.status(5*time.Second), "data before the session opens")
	if behind.accepted.Load() != 0 {
		t.Fatal("nothing may reach the peer behind the port")
	}
}

func TestMemberPortPlaintextAndNonGrpcTlsPeersAreRefused(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	port := fx.listenerOnX(behind)
	plain := dialTCP(t, port)
	_, _ = plain.Write(append([]byte("HZC"), make([]byte, 64)...))
	if !dropped(plain, 12*time.Second) {
		t.Fatal("a plaintext peer must be dropped")
	}
	// A #212 TLS-framing peer (ALPN tacplus-member/1) must be refused.
	refused := func() bool {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", port.String(),
			&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"tacplus-member/1"}})
		if err != nil {
			return true
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte{1, 0, 0, 0, 1, 0}); err != nil {
			return true
		}
		_, err = c.Read(make([]byte, 1))
		return err != nil
	}()
	if !refused {
		t.Fatal("a #212 TLS-framing peer (ALPN tacplus-member/1) must be refused")
	}
	time.Sleep(200 * time.Millisecond)
	if behind.accepted.Load() != 0 {
		t.Fatal("nothing may reach the peer behind the port")
	}
}

func TestMemberPortAStreamThatNeverSendsItsFirstChunkIsDroppedAtTheAdmissionDeadline(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	r := fx.raw(fx.rawConn(fx.listenerOnX(behind)), fx.fleet.mint(testSvc, tenantX, testAud, 300))
	start := time.Now()
	s := r.status(20 * time.Second)
	elapsed := time.Since(start)
	if s.Code() == codes.OK || elapsed > 14*time.Second {
		t.Fatalf("dropped at the 10-second admission deadline: %v after %v", s, elapsed)
	}
	if behind.accepted.Load() != 0 {
		t.Fatal("nothing reached the peer behind the port")
	}
}

// ==== Sessions: binding and faults ====

func TestMemberPortARotationPresentingAnotherSubjectsTokenIsRefused(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	conn := fx.rawConn(fx.listenerOnX(behind))
	first := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	id := openSession(t, first)
	otherTenant := fx.raw(conn, fx.fleet.mint(testSvc, tenantY, testAud, 300))
	otherTenant.send(joinChunk(id))
	assertRefused(t, codes.PermissionDenied, otherTenant.status(5*time.Second), "tenant Y's token with X's session id")
	otherService := fx.raw(conn, fx.fleet.mint("other", tenantX, testAud, 300))
	otherService.send(joinChunk(id))
	assertRefused(t, codes.PermissionDenied, otherService.status(5*time.Second), "another service's token with the session id")
	unknown := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	unknown.send(joinChunk("no-such-session"))
	assertRefused(t, codes.PermissionDenied, unknown.status(5*time.Second), "an unknown session id")
	// The session itself is untouched by the refused rotations.
	first.send(dataChunk(7, 8, 9))
	if got := first.next().Payload; !bytes.Equal(got, []byte{7, 8, 9}) {
		t.Fatalf("the session still echoes: %v", got)
	}
	if behind.accepted.Load() != 1 {
		t.Fatalf("accepted %d, want 1", behind.accepted.Load())
	}
}

func TestMemberPortAnOldStreamCutOffWithoutLastDropsTheSessionAndDoesNotSplice(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	conn := fx.rawConn(fx.listenerOnX(behind))
	old := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	id := openSession(t, old)
	old.send(dataChunk(1, 2, 3))
	if got := old.next().Payload; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("echo: %v", got)
	}
	next := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	if got := next.send(joinChunk(id)).next().SessionId; got != id {
		t.Fatalf("the rotation is attached: got id %q", got)
	}
	// The old stream ends with no last: a fault, not a cutover.
	old.cancel()
	next.send(dataChunk(4, 5, 6))
	if s := next.status(5 * time.Second); s.Code() == codes.OK {
		t.Fatalf("the new stream is dropped with the session: %v", s)
	}
	r := fx.serverResult(behind, 1, 5*time.Second)
	if got := behind.bytesReceived.Load(); got != 3 {
		t.Fatalf("nothing sent on the new stream was spliced on: %d bytes received", got)
	}
	if !strings.Contains(r.err, "reset") {
		t.Fatalf("the connection behind the port is reset, not closed: %v", r)
	}
}

// fakeReceiver is a receiver with a valid tenant-X certificate: it answers
// each stream with token in its headers and attaches it, then hands each data
// chunk to onData (which may end the stream by returning done).
func (fx *fixture) fakeReceiver(token string, onData func(ss grpc.ServerStream, c *memberport_pb.MemberChunk) (done bool)) *net.TCPAddr {
	fx.t.Helper()
	cert := fx.fleet.leaf(fx.t, tenantX, testSvc, "EC")
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{*cert}})))
	open := StreamDesc
	open.Handler = func(_ any, ss grpc.ServerStream) error {
		if err := ss.SendHeader(metadata.Pairs(TokenHeader, token)); err != nil {
			return err
		}
		first := true
		for {
			c := &memberport_pb.MemberChunk{}
			if err := ss.RecvMsg(c); err != nil {
				return nil
			}
			if first {
				first = false
				if err := ss.SendMsg(joinChunk("fake-session")); err != nil {
					return err
				}
			} else if len(c.Payload) > 0 && onData(ss, c) {
				return nil
			}
		}
	}
	srv.RegisterService(&grpc.ServiceDesc{ServiceName: ServiceName, HandlerType: (*any)(nil), Streams: []grpc.StreamDesc{open}}, struct{}{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fx.t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	fx.t.Cleanup(srv.Stop)
	return lis.Addr().(*net.TCPAddr)
}

func TestMemberPortAReceiverEndingItsStreamWithoutLastResetsTheSendersLocalConnection(t *testing.T) {
	fx := newFixture(t)
	// A receiver that echoes one chunk, then ends the stream OK with no last:
	// the sender must not take that for a clean end.
	recv := fx.fakeReceiver(fx.fleet.mint(testSvc, tenantX, testAud, 300), func(ss grpc.ServerStream, c *memberport_pb.MemberChunk) bool {
		_ = ss.SendMsg(dataChunk(c.Payload...))
		return true
	})
	c := dialTCP(t, fx.forward(fx.member(tenantX), recv))
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, 3)
	if _, err := io.ReadFull(c, echoed); err != nil || !bytes.Equal(echoed, []byte{1, 2, 3}) {
		t.Fatalf("echoed %v, %v", echoed, err)
	}
	_, err := c.Read(make([]byte, 1))
	if err == nil || err == io.EOF || !strings.Contains(describe(err), "reset") {
		t.Fatalf("a stream that ends without last is a fault, not an EOF: the local connection is reset: %v", err)
	}
}

func TestMemberPortCutoverWithLastInBothDirectionsLosesNothing(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	conn := fx.rawConn(fx.listenerOnX(behind))
	old := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	id := openSession(t, old)
	old.send(dataChunk(1, 2, 3))
	next := fx.raw(conn, fx.fleet.mint(testSvc, tenantX, testAud, 300))
	if got := next.send(joinChunk(id)).next().SessionId; got != id {
		t.Fatalf("the rotation is attached: got id %q", got)
	}
	// Bytes sent on the new stream before the old one's last wait for it.
	next.send(dataChunk(7, 8))
	old.send(dataChunk(4, 5, 6)).send(&memberport_pb.MemberChunk{Last: true})
	var all []byte
	for {
		c := old.next()
		all = append(all, c.Payload...)
		if c.Last {
			break
		}
	}
	if s := old.status(5 * time.Second); s.Code() != codes.OK {
		t.Fatalf("the server ends the old stream after its own last: %v", s)
	}
	for len(all) < 8 {
		all = append(all, next.next().Payload...)
	}
	if !bytes.Equal(all, []byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("in order across the cutover, each way: %v", all)
	}
}

// ==== Failure modes ====

func TestMemberPortAMintFailureDropsTheSessionAtExpiry(t *testing.T) {
	fx := newFixture(t)
	am := newMinter(fx.fleet, tenantX, 3, 2)
	a := fx.memberWith(am.token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), fx.fleet.lookup)
	fwd := fx.tunnel(a, fx.member(tenantX), fx.echo())
	_, _ = am.token() // the first token is in hand
	stall := make(chan struct{})
	am.stall.Store(&stall)
	defer close(stall)
	start := time.Now()
	r := exchangeWith(fwd, 51, 2_000_000, 51, 2048, 10*time.Millisecond, 0)
	elapsed := time.Since(start)
	if r.ok(2_000_000) || r.firstMismatch >= 0 {
		t.Fatalf("cut, intact: %v", r)
	}
	if !r.wasReset() {
		t.Fatalf("the local connection is reset: %v", r)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("dropped at the token's expiry, took %v", elapsed)
	}
}

func TestMemberPortARevokedCallerIsCutOffWithinOneLifetime(t *testing.T) {
	fx := newFixture(t)
	// The identity is revoked: it gets no new token, only the one in hand.
	revoked := newMinter(fx.fleet, tenantX, 3, 1)
	a := fx.memberWith(revoked.token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), fx.fleet.lookup)
	fwd := fx.tunnel(a, fx.member(tenantX), fx.echo())
	_, _ = revoked.token()
	revoked.neverRenew.Store(true)
	start := time.Now()
	r := exchangeWith(fwd, 41, 2_000_000, 41, 2048, 10*time.Millisecond, 0)
	elapsed := time.Since(start)
	if r.ok(2_000_000) || r.firstMismatch >= 0 {
		t.Fatalf("cut, intact: %v", r)
	}
	if elapsed > 4500*time.Millisecond {
		t.Fatalf("cut off within one token lifetime, took %v", elapsed)
	}
	// And a new connection with the expired token in hand is refused at once.
	if again := exchange(fwd, 42, 1000, 42); again.ok(1000) || again.received != 0 {
		t.Fatalf("refused: %v", again)
	}
}

func TestMemberPortARefusedTunnelResetsItsLocalConnectionWellUnderASecond(t *testing.T) {
	fx := newFixture(t)
	behind := fx.echo()
	xPort := fx.listen(fx.member(tenantX), behind.address())
	// Warm up the TLS and gRPC paths on an admitted tunnel first.
	if r := exchange(fx.forward(fx.member(tenantX), xPort), 1, 1000, 1); !r.ok(1000) {
		t.Fatalf("warm-up: %v", r)
	}
	c := dialTCP(t, fx.forward(fx.member(tenantY), xPort))
	if _, err := c.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err := c.Read(make([]byte, 1))
	elapsed := time.Since(start)
	var ne net.Error
	if err == nil || err == io.EOF || (errors.As(err, &ne) && ne.Timeout()) || !strings.Contains(describe(err), "reset") {
		t.Fatalf("ECONNRESET, not a timeout or EOF: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("reset well under a second, took %v", elapsed)
	}
	if behind.accepted.Load() != 1 {
		t.Fatal("the refused tunnel opened no local connection behind the port")
	}
}

func TestMemberPortUnreachableJwksRefusesEveryoneUntilKeysArrive(t *testing.T) {
	fx := newFixture(t)
	var keysMu sync.Mutex
	none := map[string]*wrapperJwk.ImmutableJwk{}
	lookup := func(kid string) *wrapperJwk.ImmutableJwk {
		keysMu.Lock()
		defer keysMu.Unlock()
		return none[kid]
	}
	b := fx.memberWith(newMinter(fx.fleet, tenantX, 300, 60).token, certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519")), lookup)
	echo := fx.echo()
	fwd := fx.forward(fx.member(tenantX), fx.listen(b, echo.address()))
	if r := exchange(fwd, 61, 1000, 61); r.ok(1000) {
		t.Fatalf("no JWKS, no admission: %v", r)
	}
	if echo.accepted.Load() != 0 {
		t.Fatal("nothing may reach the peer behind the port")
	}
	keysMu.Lock()
	none[fx.fleet.kernel.Proto().Id] = fx.fleet.lookup(fx.fleet.kernel.Proto().Id)
	keysMu.Unlock()
	if r := exchange(fwd, 62, 1000, 62); !r.ok(1000) {
		t.Fatalf("admitted once the keys arrive: %v", r)
	}
}

// presentedSerial is the serial of the certificate the member port at addr serves.
func presentedSerial(t *testing.T, addr *net.TCPAddr) *big.Int {
	t.Helper()
	c, err := tls.Dial("tcp", addr.String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].SerialNumber
}

func TestMemberPortCertificateRotatesMidStream(t *testing.T) {
	fx := newFixture(t)
	bCert := certRef(fx.fleet.leaf(t, tenantX, testSvc, "Ed25519"))
	b := fx.memberWith(newMinter(fx.fleet, tenantX, 300, 60).token, bCert, fx.fleet.lookup)
	server := fx.push(71, 300_000, 72)
	bAddr := fx.listen(b, server.address())
	fwd := fx.forward(fx.member(tenantX), bAddr)
	before := presentedSerial(t, bAddr)
	done := make(chan peerResult, 1)
	go func() { done <- exchangeWith(fwd, 72, 300_000, 71, 2048, 10*time.Millisecond, 0) }()
	time.Sleep(500 * time.Millisecond)
	bCert.Store(fx.fleet.leaf(t, tenantX, testSvc, "EC"))
	if r := <-done; !r.ok(300_000) {
		t.Fatalf("the stream in flight survives a rotation: %v", r)
	}
	if presentedSerial(t, bAddr).Cmp(before) == 0 {
		t.Fatal("a new connection gets the new certificate")
	}
	echo := fx.echo()
	if r := exchange(fx.forward(fx.member(tenantX), fx.listen(b, echo.address())), 73, 5000, 73); !r.ok(5000) {
		t.Fatalf("members still connect after the rotation: %v", r)
	}
}

func TestMemberPortSameKeyTypeRotationsKeepEveryHandshakeWorking(t *testing.T) {
	fx := newFixture(t)
	// EC to EC, several times, with many handshakes in between: a key of one
	// snapshot served with the chain of another fails the handshake only when
	// the algorithms match, so this is the rotation that would show it.
	bCert := certRef(fx.fleet.leaf(t, tenantX, testSvc, "EC"))
	b := fx.memberWith(newMinter(fx.fleet, tenantX, 300, 60).token, bCert, fx.fleet.lookup)
	bAddr := fx.listen(b, fx.echo().address())
	for round := 0; round < 4; round++ {
		if got, want := presentedSerial(t, bAddr), bCert.Load().Leaf.SerialNumber; got.Cmp(want) != 0 {
			t.Fatalf("round %d serves the current certificate: %v, want %v", round, got, want)
		}
		// A new forward is a new channel: its handshake sees this round's certificate.
		fwd := fx.forward(fx.member(tenantX), bAddr)
		for i := 0; i < 12; i++ {
			seed := int64(round*100 + i)
			if r := exchange(fwd, seed, 1000, seed); !r.ok(1000) {
				t.Fatalf("round %d connection %d: %v", round, i, r)
			}
		}
		bCert.Store(fx.fleet.leaf(t, tenantX, testSvc, "EC"))
	}
}

// ==== The sender checks the receiver ====

func TestMemberPortReceiverCertificateOfAnotherTenantIsRefusedBySender(t *testing.T) {
	fx := newFixture(t)
	// B holds tenant X's tokens but presents a certificate naming tenant Y.
	b := fx.memberWith(newMinter(fx.fleet, tenantX, 300, 60).token, certRef(fx.fleet.leaf(t, tenantY, testSvc, "Ed25519")), fx.fleet.lookup)
	echo := fx.echo()
	if r := exchange(fx.forward(fx.member(tenantX), fx.listen(b, echo.address())), 81, 1000, 81); r.ok(1000) || r.received != 0 {
		t.Fatalf("refused at the handshake: %v", r)
	}
	if echo.accepted.Load() != 0 {
		t.Fatal("nothing may reach the peer behind the port")
	}
	// A certificate of another service is refused the same way.
	c := fx.memberWith(newMinter(fx.fleet, tenantX, 300, 60).token, certRef(fx.fleet.leaf(t, tenantX, "other", "EC")), fx.fleet.lookup)
	echo2 := fx.echo()
	if r := exchange(fx.forward(fx.member(tenantX), fx.listen(c, echo2.address())), 82, 1000, 82); r.ok(1000) || r.received != 0 {
		t.Fatalf("another service's certificate: %v", r)
	}
	if echo2.accepted.Load() != 0 {
		t.Fatal("nothing may reach the peer behind the port")
	}
}

func TestMemberPortReceiverTokenOfAnotherTenantIsRefusedBySender(t *testing.T) {
	fx := newFixture(t)
	var dataChunks atomic.Int32
	count := func(counter *atomic.Int32) func(grpc.ServerStream, *memberport_pb.MemberChunk) bool {
		return func(grpc.ServerStream, *memberport_pb.MemberChunk) bool {
			counter.Add(1)
			return false
		}
	}
	fwd := fx.forward(fx.member(tenantX), fx.fakeReceiver(fx.fleet.mint(testSvc, tenantY, testAud, 300), count(&dataChunks)))
	if r := exchange(fwd, 91, 1000, 91); r.ok(1000) || r.received != 0 {
		t.Fatalf("refused: %v", r)
	}
	time.Sleep(300 * time.Millisecond)
	if dataChunks.Load() != 0 {
		t.Fatal("the sender sends no bytes to a receiver it refused")
	}
	// The receiver's own valid member token is admitted: the same fake then works.
	var admittedChunks atomic.Int32
	ok := fx.forward(fx.member(tenantX), fx.fakeReceiver(fx.fleet.mint(testSvc, tenantX, testAud, 300), count(&admittedChunks)))
	c := dialTCP(t, ok)
	if _, err := c.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bytes to reach the admitted receiver", 5*time.Second, func() bool { return admittedChunks.Load() > 0 })
}

func TestMemberPortTwoTenantsMembersNeverConnect(t *testing.T) {
	fx := newFixture(t)
	x1, x2, y1 := fx.member(tenantX), fx.member(tenantX), fx.member(tenantY)
	behindX2 := fx.echo()
	x2Port := fx.listen(x2, behindX2.address())
	if r := exchange(fx.forward(x1, x2Port), 101, 5000, 101); !r.ok(5000) {
		t.Fatalf("same tenant connects: %v", r)
	}
	if cross := exchange(fx.forward(y1, x2Port), 102, 5000, 102); cross.ok(5000) || cross.received != 0 {
		t.Fatalf("tenant Y cannot reach tenant X: %v", cross)
	}
	if behindX2.accepted.Load() != 1 {
		t.Fatal("only tenant X's connection reached the peer")
	}
}

func TestMemberPortTargetAndLocalForwardMustBeLoopback(t *testing.T) {
	fx := newFixture(t)
	a := fx.member(tenantX)
	if _, err := a.Listen("127.0.0.1:0", "192.0.2.1:5701"); err == nil {
		t.Fatal("a non-loopback target is refused")
	}
	if _, err := a.ForwardOn("127.0.0.1:1", "0.0.0.0:0"); err == nil {
		t.Fatal("a non-loopback local forward address is refused")
	}
}

// ==== Soak (opt-in): overhead against a direct connection ====

func TestMemberPortSoakOverheadAgainstDirect(t *testing.T) {
	if os.Getenv("MEMBERPORT_SOAK") != "1" {
		t.Skip("MEMBERPORT_SOAK=1 runs the soak")
	}
	fx := newFixture(t)
	const total = 256 * mib
	direct := fx.echo()
	directAddr, _ := net.ResolveTCPAddr("tcp", direct.address())
	t0 := time.Now()
	if r := exchangeWith(directAddr, 1, total, 1, 64*1024, 0, 0); !r.ok(total) {
		t.Fatalf("direct: %v", r)
	}
	directTime := time.Since(t0)
	fwd := fx.tunnel(fx.member(tenantX), fx.member(tenantX), fx.echo())
	t1 := time.Now()
	if r := exchangeWith(fwd, 1, total, 1, 64*1024, 0, 0); !r.ok(total) {
		t.Fatalf("tunnelled: %v", r)
	}
	tunnelTime := time.Since(t1)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("memberport soak: echo %d MiB direct %.1f MiB/s, tunnelled %.1f MiB/s; 1-byte RTT direct %v, tunnelled %v; heap in use %d MiB",
		total/mib, float64(total/mib)/directTime.Seconds(), float64(total/mib)/tunnelTime.Seconds(),
		rtt(t, directAddr), rtt(t, fwd), ms.HeapInuse/mib)
}

func rtt(t *testing.T, addr net.Addr) time.Duration {
	c := dialTCP(t, addr)
	best := time.Hour
	b := []byte{1}
	for i := 0; i < 200; i++ {
		start := time.Now()
		if _, err := c.Write(b); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(c, b); err != nil {
			t.Fatal(err)
		}
		best = min(best, time.Since(start))
	}
	return best
}
