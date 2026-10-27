package service_im

import (
	"context"
	"errors"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	minter_pb "example.com/externaltoken/tacplus/model/types/sys/minter"
	token_pb "example.com/externaltoken/tacplus/model/types/sys/token"
	common_pb "example.com/externaltoken/tacplus/model/types/sys/tokenrequest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	extSubject      = "spiffe://td.test/system/services/caller"
	extIssuer       = "https://fleet.test"
	extServiceToken = "service-token-for-authority"
	extAudience     = "https://api.anthropic.com"
	extMaxLifetime  = int64(600)
)

// mockKernel stands in for the token minter. The bootstrap exchange hands out a
// fixed service token; the external mint enforces the contract the real kernel
// RPC must meet (the presented token is the caller's own, exp no more than
// 600 s out) and signs a fresh ES256 JWT with a new jti per call.
type mockKernel struct {
	minter_pb.UnimplementedMinterServer
	key *ecdsa.PrivateKey

	mu       sync.Mutex
	requests []*common_pb.MintExternalServiceTokenRequest
}

func (k *mockKernel) MintServiceToken(_ context.Context, req *common_pb.MintServiceTokenRequest) (*common_pb.TokenMintResponse, error) {
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	if req.GetTokenPurpose() == token_pb.TokenPurpose_TOKEN_PURPOSE_REFRESH {
		return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: "access-token", Payload: map[string]string{"exp": exp}}}, nil
	}
	if req.GetInputToken() != "access-token" {
		return nil, status.Error(codes.Unauthenticated, "phase 2 must present the phase-1 access token")
	}
	return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: extServiceToken, Payload: map[string]string{"exp": exp}}}, nil
}

func (k *mockKernel) MintExternalServiceToken(_ context.Context, req *common_pb.MintExternalServiceTokenRequest) (*common_pb.TokenMintResponse, error) {
	k.mu.Lock()
	k.requests = append(k.requests, req)
	k.mu.Unlock()
	if req.GetInputToken() != extServiceToken {
		return nil, status.Error(codes.Unauthenticated, "input_token is not the caller's kernel token")
	}
	now := time.Now().Unix()
	if req.GetExp() <= now || req.GetExp() > now+extMaxLifetime {
		return nil, status.Error(codes.InvalidArgument, "exp must be within 600 seconds")
	}
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return nil, err
	}
	jti := hex.EncodeToString(jtiBytes)
	signed, err := signES256(k.key, map[string]any{"alg": "ES256", "typ": "JWT"}, map[string]any{
		"iss": extIssuer, "sub": extSubject, "aud": req.GetAud(), "iat": now, "exp": req.GetExp(), "jti": jti,
	})
	if err != nil {
		return nil, err
	}
	return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: signed}}, nil
}

func signES256(key *ecdsa.PrivateKey, header, payload map[string]any) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func decodeSegment(t *testing.T, segment string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("base64url segment: %v", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json segment: %v", err)
	}
	return out
}

func TestGoTokenForExternalMintsAFreshES256TokenOnEveryCall(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("k8s.projected.token"), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kernel := &mockKernel{key: key}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	minter_pb.RegisterMinterServer(server, kernel)
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// New() pins bootstrap TLS to a SPIFFE trust domain; this test exercises
	// what happens after bootstrap, so it wires the same pieces over a plain
	// connection to the mock kernel.
	minter, err := newK8sTokenMinter(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	minter.setConn(conn)
	im := &IdentityManager{conn: conn, k8sMinter: minter}

	before := time.Now().Unix()
	first, err := im.TokenForExternal(extAudience)
	if err != nil {
		t.Fatalf("first external token: %v", err)
	}
	second, err := im.TokenForExternal(extAudience)
	if err != nil {
		t.Fatalf("second external token: %v", err)
	}
	after := time.Now().Unix()

	if first == second {
		t.Fatal("an external token must never be reused")
	}
	var jtis []string
	for _, jwt := range []string{first, second} {
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Fatalf("compact JWT, got %d parts", len(parts))
		}
		if alg := decodeSegment(t, parts[0])["alg"]; alg != "ES256" {
			t.Fatalf("external tokens are ES256, never EdDSA; got %v", alg)
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(sig) != 64 {
			t.Fatalf("ES256 signature: %v (len %d)", err, len(sig))
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Fatal("token is not signed by the kernel's P-256 key")
		}
		claims := decodeSegment(t, parts[1])
		if claims["sub"] != extSubject || claims["aud"] != extAudience || claims["iss"] != extIssuer {
			t.Fatalf("claims = %v", claims)
		}
		exp := int64(claims["exp"].(float64))
		if exp <= before || exp > after+extMaxLifetime {
			t.Fatalf("exp %d must be within 600 s", exp)
		}
		jtis = append(jtis, claims["jti"].(string))
	}
	if jtis[0] == jtis[1] {
		t.Fatal("every call must carry a fresh jti")
	}

	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if len(kernel.requests) != 2 {
		t.Fatalf("each call must reach the kernel; nothing is cached (got %d requests)", len(kernel.requests))
	}
	for _, req := range kernel.requests {
		if req.GetInputToken() != extServiceToken {
			t.Fatalf("the caller's own kernel token is the credential, got %q", req.GetInputToken())
		}
		if req.GetAud() != extAudience {
			t.Fatalf("aud = %q", req.GetAud())
		}
		if req.GetExp() <= before || req.GetExp() > after+extMaxLifetime {
			t.Fatalf("requested exp %d must be within 600 s", req.GetExp())
		}
	}
}

// bootstrapKernel is the token authority for the bootstrap arm. The first
// refresh exchange must present the bootstrap token, each renewal must present
// the refresh token the previous exchange returned, and the third refresh token
// has been revoked. Service tokens expire one second out, so every call to the
// minter re-exchanges.
type bootstrapKernel struct {
	minter_pb.UnimplementedMinterServer

	mu            sync.Mutex
	refreshInputs []string
}

func (k *bootstrapKernel) MintServiceToken(_ context.Context, req *common_pb.MintServiceTokenRequest) (*common_pb.TokenMintResponse, error) {
	if req.GetTokenPurpose() == token_pb.TokenPurpose_TOKEN_PURPOSE_REFRESH {
		k.mu.Lock()
		k.refreshInputs = append(k.refreshInputs, req.GetInputToken())
		k.mu.Unlock()
		switch req.GetInputToken() {
		case "bootstrap-token":
			return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: "access-1"}, RefreshToken: &token_pb.Token{Base64: "refresh-1"}}, nil
		case "refresh-1":
			return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: "access-2"}, RefreshToken: &token_pb.Token{Base64: "refresh-2"}}, nil
		case "refresh-2":
			return nil, status.Error(codes.PermissionDenied, "workload revoked")
		}
		return nil, status.Errorf(codes.InvalidArgument, "unexpected refresh input %q", req.GetInputToken())
	}
	var n string
	switch req.GetInputToken() {
	case "access-1":
		n = "1"
	case "access-2":
		n = "2"
	default:
		return nil, status.Errorf(codes.Unauthenticated, "phase 2 presented %q", req.GetInputToken())
	}
	exp := strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10)
	return &common_pb.TokenMintResponse{AccessToken: &token_pb.Token{Base64: "service-token-" + n, Payload: map[string]string{"exp": exp}}}, nil
}

func TestGoBootstrapTokenIsExchangedOnceAndRenewedFromTheRefreshToken(t *testing.T) {
	bootstrapFile := filepath.Join(t.TempDir(), "bootstrap")
	if err := os.WriteFile(bootstrapFile, []byte("bootstrap-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	kernel := &bootstrapKernel{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	minter_pb.RegisterMinterServer(server, kernel)
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	minter, err := newBootstrapTokenMinter(bootstrapFile)
	if err != nil {
		t.Fatal(err)
	}
	minter.setConn(conn)
	im := &IdentityManager{conn: conn, k8sMinter: minter}

	first, err := im.GetTokenFor(extAudience)
	if err != nil {
		t.Fatalf("bootstrap exchange: %v", err)
	}
	if first != "service-token-1" {
		t.Fatalf("bootstrap exchange returned %q", first)
	}
	if _, err := os.Stat(bootstrapFile); !os.IsNotExist(err) {
		t.Fatalf("the bootstrap token file must be removed once read; stat err = %v", err)
	}

	second, err := im.GetTokenFor(extAudience)
	if err != nil {
		t.Fatalf("renewal after expiry: %v", err)
	}
	if second != "service-token-2" {
		t.Fatalf("renewal returned %q", second)
	}

	_, err = im.GetTokenFor(extAudience)
	if !errors.Is(err, ErrWorkloadRevoked) {
		t.Fatalf("a revoked workload must fail with ErrWorkloadRevoked, got %v", err)
	}

	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	want := []string{"bootstrap-token", "refresh-1", "refresh-2"}
	if strings.Join(kernel.refreshInputs, ",") != strings.Join(want, ",") {
		t.Fatalf("refresh exchanges presented %v, want %v", kernel.refreshInputs, want)
	}
}
