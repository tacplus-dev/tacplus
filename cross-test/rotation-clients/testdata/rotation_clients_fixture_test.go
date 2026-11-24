// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Stream rotation for the client-only targets (tacplus#323), copied into the
// generated ViewerService wrapper package by cross-test/rotation-clients/run.sh.
//
// The real generated C2S server (the generated wrapper serving the
// ViewerServiceRotating sibling, the generated Authorizer verifying real signed
// user tokens, #260's StreamCredentialExpiryInterceptor ending every stream at
// its token's exp) meets a generated client of another language: run.sh passes
// its command in ROTATION_CLIENT_CMD and this test runs it against the server.
// The client asks the control endpoint for its tokens (each lives
// tokenLifetime) and for what the handler saw. The handler below is plain
// handler code: nothing in it mentions rotation.
//
// Plaintext: the transport's identity is not under test, rotation is (the
// rotation fixture's TLS legs cover the generated grpcserver).

package rotationclients

import (
	"context"
	"crypto/ed25519"
	libBase64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"example.com/rotationclients/tacplus/model/chassis/grpcserver"
	wrapperJwk "example.com/rotationclients/tacplus/model/chassis/jwk"
	keylib "example.com/rotationclients/tacplus/model/chassis/key"
	"example.com/rotationclients/tacplus/model/chassis/servicemanager"
	tokenlib "example.com/rotationclients/tacplus/model/chassis/token"
	types "example.com/rotationclients/tacplus/model/types/svc/rotationclients"
	jwkpb "example.com/rotationclients/tacplus/model/types/sys/jwk"
	keypb "example.com/rotationclients/tacplus/model/types/sys/key"
)

// tokenLifetime is every token's lifetime. exp is whole seconds, so a token
// may expire up to a second sooner.
const tokenLifetime = 3 * time.Second

// ---------------------------------------------------------------------------
// The handler: plain handler code, nothing about rotation.
// ---------------------------------------------------------------------------

// handlerEnd is what one handler call saw, as the client reads it from /ends.
type handlerEnd struct {
	Method string `json:"method"`
	// Code is the status the handler returned ("OK" for nil).
	Code string `json:"code"`
	// Cancelled: the stream's context was done when the handler returned (or
	// within a second of its last Recv/Send failing).
	Cancelled bool `json:"cancelled"`
	// SameContext: stream.Context() returned one value for the handler's life.
	SameContext bool `json:"sameContext"`
	// Received is how many messages arrived; InOrder that they were 1..n.
	Received int  `json:"received"`
	InOrder  bool `json:"inOrder"`
	// Subject is the caller the handler was given.
	Subject string `json:"subject"`
}

type viewerHandler struct {
	mu   sync.Mutex
	ends []handlerEnd
}

func cancelledSoon(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(time.Second):
		return false
	}
}

func inOrder(seqs []int64) bool {
	for i, seq := range seqs {
		if seq != int64(i+1) {
			return false
		}
	}
	return true
}

func (h *viewerHandler) record(end *handlerEnd, err error, received []int64) {
	end.Code = status.Code(err).String()
	if err == nil {
		end.Code = "OK"
	}
	end.Received, end.InOrder = len(received), inOrder(received)
	h.mu.Lock()
	h.ends = append(h.ends, *end)
	h.mu.Unlock()
}

func subjectOf(caller *servicemanager.Caller) string {
	if caller == nil || caller.Token() == nil {
		return ""
	}
	return caller.Token().GetSubject()
}

// SendTicks receives ticks until the caller closes, and answers with how many
// arrived and whether they were in order.
func (h *viewerHandler) SendTicks(caller *servicemanager.Caller, stream grpc.ClientStreamingServer[types.Tick, types.Tick]) (err error) {
	ctx := stream.Context()
	end := handlerEnd{Method: "SendTicks", SameContext: true, Subject: subjectOf(caller)}
	var received []int64
	defer func() { h.record(&end, err, received) }()
	for {
		m, rerr := stream.Recv()
		if stream.Context() != ctx {
			end.SameContext = false
		}
		if rerr == io.EOF {
			verdict := "in-order"
			if !inOrder(received) {
				verdict = "out-of-order"
			}
			return stream.SendAndClose(&types.Tick{Seq: int64(len(received)), M: verdict})
		}
		if rerr != nil {
			end.Cancelled = cancelledSoon(ctx)
			return rerr
		}
		received = append(received, m.Seq)
	}
}

// GenerateTicks sends ticks 1..req.Seq every 50 ms (forever when req.Seq is 0).
func (h *viewerHandler) GenerateTicks(caller *servicemanager.Caller, req *types.Tick, stream grpc.ServerStreamingServer[types.Tick]) (err error) {
	ctx := stream.Context()
	end := handlerEnd{Method: "GenerateTicks", SameContext: true, Subject: subjectOf(caller)}
	var sent []int64
	defer func() { h.record(&end, err, sent) }()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for seq := int64(1); req.Seq == 0 || seq <= req.Seq; seq++ {
		select {
		case <-ctx.Done():
			end.Cancelled = true
			return ctx.Err()
		case <-t.C:
		}
		if stream.Context() != ctx {
			end.SameContext = false
		}
		if serr := stream.Send(&types.Tick{Seq: seq, M: "tick"}); serr != nil {
			end.Cancelled = cancelledSoon(ctx)
			return serr
		}
		sent = append(sent, seq)
	}
	return nil
}

// AssistTicks echoes every tick back.
func (h *viewerHandler) AssistTicks(caller *servicemanager.Caller, stream grpc.BidiStreamingServer[types.Tick, types.Tick]) (err error) {
	ctx := stream.Context()
	end := handlerEnd{Method: "AssistTicks", SameContext: true, Subject: subjectOf(caller)}
	var received []int64
	defer func() { h.record(&end, err, received) }()
	for {
		m, rerr := stream.Recv()
		if stream.Context() != ctx {
			end.SameContext = false
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			end.Cancelled = cancelledSoon(ctx)
			return rerr
		}
		received = append(received, m.Seq)
		if serr := stream.Send(&types.Tick{Seq: m.Seq, M: "echo"}); serr != nil {
			end.Cancelled = cancelledSoon(ctx)
			return serr
		}
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

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

// mint signs a user token for subject, audience rotationclients, living
// tokenLifetime: the token a gateway-facing client holds.
func mint(key *keylib.ImmutableKey, subject string) (string, error) {
	tok := tokenlib.NewImmutableToken(nil).
		SetIssuer("test-issuer").
		SetAudience("rotationclients").
		SetExpiration(time.Now().Add(tokenLifetime).Unix()).
		SetNamespace("/tenants/acme").
		SetSubType("user").
		SetSubject(subject)
	if err := tok.Sign(key); err != nil {
		return "", err
	}
	return tok.GetBase64(), nil
}

// newWrapper is the generated ViewerService behind the generated Authorizer,
// wired as the generated ServiceManager wires them.
func newWrapper(t *testing.T, ctx context.Context, h *viewerHandler) (*ViewerService, *keylib.ImmutableKey) {
	t.Helper()
	key, jwk := testSigningKey(t)
	auth, err := grpcserver.NewAuthorizer(ctx, nil)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	auth.JwkCache().Load([]*wrapperJwk.ImmutableJwk{jwk})
	// No RegisterViewerServiceMethodAuth: its rpcs are auth_any_user with no
	// auth_services, so the generator emits none (nor does the ServiceManager
	// call one).
	wrapper := NewViewerService(h)
	wrapper.SetAuthorizeFunc(auth.Authorize)
	wrapper.SetAuthenticateFunc(auth.Authenticate)
	wrapper.SetAuthenticateAtOrBelowFunc(auth.AuthenticateAtOrBelow)
	return wrapper, key
}

// serve serves on a fresh loopback port behind #260's expiry interceptor:
// the sibling too when rotating, else the plain service alone (a server
// generated before the sibling).
//
// Its HTTP/2 windows are static. With the default (dynamic) windows grpc-go
// sends a BDP PING each time data arrives with none outstanding, hundreds a
// second under a client stream's ticks on loopback, and swift-nio-http2 closes
// the connection past 200 inbound PING/SETTINGS frames in 30s: the Swift
// client's every stream ended UNAVAILABLE "Stream unexpectedly closed."
// whether it rotated or not. Static windows send no BDP pings; the generated
// grpcserver's own exposure is #958.
func serve(t *testing.T, wrapper *ViewerService, rotating bool) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := grpc.NewServer( // nosemgrep: go.grpc.security.grpc-server-insecure-connection.grpc-server-insecure-connection -- loopback-only test fixture
		grpc.ChainStreamInterceptor(grpcserver.StreamCredentialExpiryInterceptor()),
		grpc.InitialWindowSize(1<<20),
		grpc.InitialConnWindowSize(16<<20),
	)
	if rotating {
		if err := wrapper.RegisterServices(srv); err != nil {
			t.Fatalf("RegisterServices: %v", err)
		}
	} else {
		types.RegisterViewerServiceServer(srv, wrapper)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// TestRotationClients runs the client leg of ROTATION_CLIENT_CMD against the
// generated server. The control endpoint serves:
//
//	GET /token?sub=<subject>  a fresh token for subject (counted)
//	GET /mints                how many tokens were minted
//	GET /ends                 the handler ends so far, oldest first, and clears them
func TestRotationClients(t *testing.T) {
	cmdline := os.Getenv("ROTATION_CLIENT_CMD")
	if cmdline == "" {
		t.Skip("ROTATION_CLIENT_CMD is not set: run.sh runs this test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &viewerHandler{}
	wrapper, key := newWrapper(t, ctx, h)
	rotatingAddr := serve(t, wrapper, true)
	plainAddr := serve(t, wrapper, false)

	var mints atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Query().Get("sub")
		if sub == "" {
			http.Error(w, "sub is required", http.StatusBadRequest)
			return
		}
		tok, err := mint(key, sub)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mints.Add(1)
		_, _ = io.WriteString(w, tok)
	})
	mux.HandleFunc("/mints", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, mints.Load())
	})
	mux.HandleFunc("/ends", func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		ends := h.ends
		h.ends = nil
		h.mu.Unlock()
		if ends == nil {
			ends = []handlerEnd{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ends)
	})
	ctl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctlSrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = ctlSrv.Serve(ctl) }()
	t.Cleanup(func() { _ = ctlSrv.Close() })

	cmdCtx, cmdCancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cmdCancel()
	cmd := exec.CommandContext(cmdCtx, "sh", "-c", cmdline)
	cmd.Env = append(os.Environ(),
		"ROTATION_ADDR="+rotatingAddr,
		"ROTATION_PLAIN_ADDR="+plainAddr,
		"ROTATION_CONTROL=http://"+ctl.Addr().String(),
		fmt.Sprintf("ROTATION_TOKEN_LIFETIME_MS=%d", tokenLifetime.Milliseconds()),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Short: run.sh quotes it in the build's verdict ("client: signal: killed"
	// when the client outlived its 8 minutes).
	if err := cmd.Run(); err != nil {
		t.Fatalf("client: %v", err)
	}
}
