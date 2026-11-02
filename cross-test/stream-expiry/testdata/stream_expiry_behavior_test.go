// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Stream credential expiry (issue #260), copied into the generated
// StreamerService wrapper package by cross-test/stream-expiry/run.sh.
//
// API.md's Client Send Contract: a client-streaming or bidi stream carries one
// token, attached when it opens, and servers terminate streams carrying expired
// credentials. Each streaming shape is opened against the real generated server
// (the generated wrapper, the generated Authorizer verifying a real signed
// token, the generated grpcserver serving TLS) with a token a few seconds from
// exp that is never rotated, while the handler keeps the stream open. The
// server must end each stream at the token's exp with UNAUTHENTICATED.

package streamexpiry

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
	"io"
	"math/big"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"example.com/streamexpiry/tacplus/model/chassis/grpcserver"
	wrapperJwk "example.com/streamexpiry/tacplus/model/chassis/jwk"
	keylib "example.com/streamexpiry/tacplus/model/chassis/key"
	"example.com/streamexpiry/tacplus/model/chassis/servicemanager"
	tokenlib "example.com/streamexpiry/tacplus/model/chassis/token"
	types "example.com/streamexpiry/tacplus/model/types/svc/streamexpiry"
	pb "example.com/streamexpiry/tacplus/model/types/sys/grpcconfig"
	hostpb "example.com/streamexpiry/tacplus/model/types/sys/host"
	jwkpb "example.com/streamexpiry/tacplus/model/types/sys/jwk"
	keypb "example.com/streamexpiry/tacplus/model/types/sys/key"
	x509_pb "example.com/streamexpiry/tacplus/model/types/sys/x509"
	x509wrapper "example.com/streamexpiry/tacplus/model/chassis/x509"
)

// tokenLifetime is how far out the short-lived token's exp is. exp is whole
// seconds, so the stream may end up to a second before open+tokenLifetime.
const tokenLifetime = 3 * time.Second

// expirySlack is how long after exp the server may take to end a stream.
const expirySlack = 5 * time.Second

// holdOpenHandler keeps every stream open until the stream itself ends: the
// client-streaming and bidi handlers receive until Recv fails, the
// server-streaming handler sends a tick every 200 ms until Send fails or the
// stream's context ends. None of them ever ends a stream on its own.
type holdOpenHandler struct{}

func (holdOpenHandler) SendTicks(_ *servicemanager.Caller, stream grpc.ClientStreamingServer[types.Tick, types.Tick]) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func (holdOpenHandler) GenerateTicks(_ *servicemanager.Caller, _ *types.Tick, stream grpc.ServerStreamingServer[types.Tick]) error {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-t.C:
			if err := stream.Send(&types.Tick{M: "tick"}); err != nil {
				return err
			}
		}
	}
}

func (holdOpenHandler) AssistTicks(_ *servicemanager.Caller, stream grpc.BidiStreamingServer[types.Tick, types.Tick]) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
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

// testAudience is the audience the generated Authorizer accepts, its service
// label, which run.sh reads from the generated code.
func testAudience() string {
	if aud := os.Getenv("STREAM_EXPIRY_AUDIENCE"); aud != "" {
		return aud
	}
	return "streamexpiry"
}

// serviceToken mints a service token for caller-svc, audience testAudience(),
// expiring at exp.
func serviceToken(t *testing.T, key *keylib.ImmutableKey, exp time.Time) string {
	t.Helper()
	tok := tokenlib.NewImmutableToken(nil).
		SetIssuer("test-issuer").
		SetAudience(testAudience()).
		SetExpiration(exp.Unix()).
		SetNamespace("/system").
		SetSubType("service").
		SetSubject("caller-svc")
	if err := tok.Sign(key); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return tok.GetBase64()
}

func selfSignedCert(t *testing.T) (*x509_pb.X509CertChain, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &stdx509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "stream-expiry-test"},
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

// startServer serves the generated StreamerService behind the generated
// Authorizer on the generated grpcserver, wired as the generated
// ServiceManager wires them, and returns a client to it and the signing key the
// Authorizer trusts.
func startServer(t *testing.T) (types.StreamerServiceClient, *keylib.ImmutableKey) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	key, jwk := testSigningKey(t)
	auth, err := grpcserver.NewAuthorizer(ctx, nil)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	auth.JwkCache().Load([]*wrapperJwk.ImmutableJwk{jwk})
	RegisterStreamerServiceMethodAuth(auth)

	wrapper := NewStreamerService(holdOpenHandler{})
	wrapper.SetAuthorizeFunc(auth.Authorize)
	wrapper.SetAuthenticateFunc(auth.Authenticate)
	wrapper.SetAuthenticateAtOrBelowFunc(auth.AuthenticateAtOrBelow)

	cert, certKey := selfSignedCert(t)
	port := freePort(t)
	cfg := &pb.Config{ListenAddress: &hostpb.Host{Host: "127.0.0.1", Port: int32(port)}}
	srv, err := grpcserver.New(ctx, cfg, nil, cert, certKey, wrapper, nil)
	if err != nil {
		t.Fatalf("grpcserver.New: %v", err)
	}
	go func() { _ = srv.Listen(ctx) }()

	// The certificate identity is not under test; stream expiry is.
	conn, err := grpc.NewClient("127.0.0.1:"+strconv.Itoa(port),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return types.NewStreamerServiceClient(conn), key
}

func bearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

// streamShape opens one stream shape and blocks until the server ends it,
// returning the error the client saw. Client-sending shapes keep sending every
// 200 ms, so the stream is in use, not idle, when its token expires.
type streamShape struct {
	name string
	run  func(ctx context.Context, c types.StreamerServiceClient) error
}

var streamShapes = []streamShape{
	{"client-streaming", func(ctx context.Context, c types.StreamerServiceClient) error {
		s, err := c.SendTicks(ctx)
		if err != nil {
			return err
		}
		for {
			if err := s.Send(&types.Tick{M: "up"}); err != nil {
				// io.EOF: the server ended the stream; its status comes
				// from CloseAndRecv.
				_, err = s.CloseAndRecv()
				return err
			}
			time.Sleep(200 * time.Millisecond)
		}
	}},
	{"server-streaming", func(ctx context.Context, c types.StreamerServiceClient) error {
		s, err := c.GenerateTicks(ctx, &types.Tick{M: "go"})
		if err != nil {
			return err
		}
		for {
			if _, err := s.Recv(); err != nil {
				return err
			}
		}
	}},
	{"bidi", func(ctx context.Context, c types.StreamerServiceClient) error {
		s, err := c.AssistTicks(ctx)
		if err != nil {
			return err
		}
		recvErr := make(chan error, 1)
		go func() {
			for {
				if _, err := s.Recv(); err != nil {
					recvErr <- err
					return
				}
			}
		}()
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case err := <-recvErr:
				return err
			case <-t.C:
				// A failed Send means the stream ended; Recv reports how.
				_ = s.Send(&types.Tick{M: "chat"})
			}
		}
	}},
}

// TestGoStreamEndsAtTokenExp: each streaming shape, opened with a token
// tokenLifetime from exp and never rotated, is ended by the server at exp with
// UNAUTHENTICATED. A server that checks the token only at open leaves the
// stream open until the client's own bound and fails the test.
func TestGoStreamEndsAtTokenExp(t *testing.T) {
	client, key := startServer(t)
	for _, shape := range streamShapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			exp := time.Now().Add(tokenLifetime)
			ctx, cancel := context.WithTimeout(bearer(context.Background(), serviceToken(t, key, exp)),
				tokenLifetime+expirySlack)
			defer cancel()
			opened := time.Now()
			err := shape.run(ctx, client)
			ended := time.Now()

			if ctx.Err() != nil {
				t.Fatalf("%s stream outlived its token: still open %s after exp, ended only by the client's own bound (%v)",
					shape.name, ended.Sub(exp).Round(time.Millisecond), err)
			}
			if got := status.Code(err); got != codes.Unauthenticated {
				t.Fatalf("%s stream ended with %v (%v), want UNAUTHENTICATED", shape.name, got, err)
			}
			// exp is whole seconds: the token expires at most a second before
			// opened+tokenLifetime. Ending well before that would be a stream
			// refused or cut early, not one ended at exp.
			if earliest := time.Unix(exp.Unix(), 0).Add(-500 * time.Millisecond); ended.Before(earliest) {
				t.Fatalf("%s stream ended %s after open, before its token's exp (%s after open): %v",
					shape.name, ended.Sub(opened).Round(time.Millisecond),
					time.Unix(exp.Unix(), 0).Sub(opened).Round(time.Millisecond), err)
			}
		})
	}
}

// TestGoStreamOutlivesWindowWithLongToken is the control: the same stream with
// a token an hour from exp is still open after the short token's window, so the
// test above measures expiry, not some other way the server ends streams.
func TestGoStreamOutlivesWindowWithLongToken(t *testing.T) {
	client, key := startServer(t)
	ctx, cancel := context.WithTimeout(bearer(context.Background(), serviceToken(t, key, time.Now().Add(time.Hour))),
		tokenLifetime+2*time.Second)
	defer cancel()
	err := streamShapes[2].run(ctx, client)
	if ctx.Err() == nil {
		t.Fatalf("bidi stream with an hour-long token ended before the client's bound: %v", err)
	}
	if got := status.Code(err); got != codes.DeadlineExceeded && !errors.Is(err, io.EOF) && got != codes.Canceled {
		t.Fatalf("bidi stream with an hour-long token ended with %v (%v), want the client's own deadline", got, err)
	}
}
