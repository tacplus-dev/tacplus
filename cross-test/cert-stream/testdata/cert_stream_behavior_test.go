// Copyright (c) 2026 Second Star Cavalier LLC

package sm

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	x509std "crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/certstream/tacplus/model/chassis/servicemanager"
	fe "example.com/certstream/tacplus/model/s2sm/grpc/servers/svc/certstream"
	keypb "example.com/certstream/tacplus/model/types/sys/key"
	smbcertpb "example.com/certstream/tacplus/model/types/sys/smbcert"
	tlscertpb "example.com/certstream/tacplus/model/types/sys/tlscert"
	x509pb "example.com/certstream/tacplus/model/types/sys/x509"
	certstreamtypes "example.com/certstream/tacplus/model/types/svc/certstream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type authorityMode string

const (
	modeFirstSuccess      authorityMode = "first-success"
	modeRenewalSuccess    authorityMode = "renewal-success"
	modeCleanBeforeFirst  authorityMode = "clean-before-first"
	modeServerErrorBefore authorityMode = "server-error-before"
	modeServerErrorAfter  authorityMode = "server-error-after"
	modeReorderedRenewal  authorityMode = "reordered-renewal"
	modeBlockRenewal      authorityMode = "block-renewal"
)

var errNoReadySignal = errors.New("no ready signal")

type recordingHandler struct {
	mu        sync.Mutex
	tlsKeys   [][]byte
	smbKeys   [][]byte
	failTLSAt map[int]error
	failSMBAt map[int]error
}

func (h *recordingHandler) StreamMemberGrpcImpl() fe.StreamMemberServiceHandler {
	return nil
}

func (h *recordingHandler) StreamStateGrpcImpl() fe.StreamStateServiceHandler {
	return nil
}

func (h *recordingHandler) NewTlsCert(_ *x509pb.X509CertChain, privateKeyDER []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tlsKeys = append(h.tlsKeys, append([]byte(nil), privateKeyDER...))
	if err := h.failTLSAt[len(h.tlsKeys)]; err != nil {
		return err
	}
	return nil
}

func (h *recordingHandler) NewSmbCert(_ *x509pb.X509CertChain, privateKeyDER []byte, _ *x509pb.X509Cert) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.smbKeys = append(h.smbKeys, append([]byte(nil), privateKeyDER...))
	if err := h.failSMBAt[len(h.smbKeys)]; err != nil {
		return err
	}
	return nil
}

func (h *recordingHandler) Shutdown(context.Context) {}

type noopStreamMemberHandler struct{}

func (noopStreamMemberHandler) Get(context.Context, *servicemanager.Caller, *certstreamtypes.Ping) (*certstreamtypes.Ping, error) {
	return &certstreamtypes.Ping{}, nil
}

type certAuthority struct {
	tlscertpb.UnimplementedTlsCertServiceServer
	smbcertpb.UnimplementedSmbCertServiceServer

	t       *testing.T
	mode    authorityMode
	blocked chan<- struct{}
	caCert  *x509std.Certificate
	caKey   crypto.Signer
	caProto *x509pb.X509Cert
}

func newCertAuthority(t *testing.T, mode authorityMode) *certAuthority {
	return newCertAuthorityWithBlock(t, mode, nil)
}

func newCertAuthorityWithBlock(t *testing.T, mode authorityMode, blocked chan<- struct{}) *certAuthority {
	t.Helper()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509std.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cert-stream-fixture-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509std.KeyUsageCertSign | x509std.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509std.CreateCertificate(rand.Reader, tmpl, tmpl, caKey.Public(), caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err := x509std.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	return &certAuthority{
		t:       t,
		mode:    mode,
		blocked: blocked,
		caCert:  cert,
		caKey:   caKey,
		caProto: protoCert("fixture-ca", der),
	}
}

func (a *certAuthority) ManageCert(stream tlscertpb.TlsCertService_ManageCertServer) error {
	pub, err := recvTLSInitAndCSR(stream)
	if err != nil {
		return err
	}
	if a.mode == modeCleanBeforeFirst {
		return nil
	}
	if a.mode == modeServerErrorBefore {
		return stream.Send(&tlscertpb.TlsCertServerMsg{
			Msg: &tlscertpb.TlsCertServerMsg_Error{Error: "fixture before first delivery"},
		})
	}
	if err := stream.Send(&tlscertpb.TlsCertServerMsg{
		Msg: &tlscertpb.TlsCertServerMsg_Cert{Cert: &tlscertpb.TlsCertDelivery{
			Chain: a.signChain(pub, "tls-initial"),
		}},
	}); err != nil {
		return err
	}
	if a.mode == modeServerErrorAfter {
		return stream.Send(&tlscertpb.TlsCertServerMsg{
			Msg: &tlscertpb.TlsCertServerMsg_Error{Error: "fixture after first delivery"},
		})
	}
	if a.mode != modeRenewalSuccess && a.mode != modeReorderedRenewal && a.mode != modeBlockRenewal {
		return nil
	}
	if err := stream.Send(&tlscertpb.TlsCertServerMsg{
		Msg: &tlscertpb.TlsCertServerMsg_Renew{Renew: &tlscertpb.TlsCertRenew{Reason: "fixture renewal"}},
	}); err != nil {
		return err
	}
	renewPub, err := recvTLSCSR(stream)
	if err != nil {
		return err
	}
	if a.mode == modeBlockRenewal {
		return a.blockUntilStreamDone(stream.Context())
	}
	if a.mode == modeReorderedRenewal {
		if err := stream.Send(&tlscertpb.TlsCertServerMsg{
			Msg: &tlscertpb.TlsCertServerMsg_Cert{Cert: &tlscertpb.TlsCertDelivery{
				Chain: a.signChain(pub, "tls-stale-renewal"),
			}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&tlscertpb.TlsCertServerMsg{
			Msg: &tlscertpb.TlsCertServerMsg_Renew{Renew: &tlscertpb.TlsCertRenew{Reason: "fixture renewal retry"}},
		}); err != nil {
			return err
		}
		renewPub, err = recvTLSCSR(stream)
		if err != nil {
			return err
		}
	}
	return stream.Send(&tlscertpb.TlsCertServerMsg{
		Msg: &tlscertpb.TlsCertServerMsg_Cert{Cert: &tlscertpb.TlsCertDelivery{
			Chain: a.signChain(renewPub, "tls-renewal"),
		}},
	})
}

func (a *certAuthority) ManageSmbCert(stream smbcertpb.SmbCertService_ManageSmbCertServer) error {
	pub, err := recvSMBInitAndCSR(stream)
	if err != nil {
		return err
	}
	if a.mode == modeCleanBeforeFirst {
		return nil
	}
	if a.mode == modeServerErrorBefore {
		return stream.Send(&smbcertpb.SmbCertServerMsg{
			Msg: &smbcertpb.SmbCertServerMsg_Error{Error: "fixture before first delivery"},
		})
	}
	if err := stream.Send(&smbcertpb.SmbCertServerMsg{
		Msg: &smbcertpb.SmbCertServerMsg_Cert{Cert: &smbcertpb.SmbCertDelivery{
			MemberChain: a.signChain(pub, "smb-initial"),
			Ca:          a.caProto,
		}},
	}); err != nil {
		return err
	}
	if a.mode == modeServerErrorAfter {
		return stream.Send(&smbcertpb.SmbCertServerMsg{
			Msg: &smbcertpb.SmbCertServerMsg_Error{Error: "fixture after first delivery"},
		})
	}
	if a.mode != modeRenewalSuccess && a.mode != modeReorderedRenewal && a.mode != modeBlockRenewal {
		return nil
	}
	if err := stream.Send(&smbcertpb.SmbCertServerMsg{
		Msg: &smbcertpb.SmbCertServerMsg_Renew{Renew: &smbcertpb.SmbCertRenew{Reason: "fixture renewal"}},
	}); err != nil {
		return err
	}
	renewPub, err := recvSMBCSR(stream)
	if err != nil {
		return err
	}
	if a.mode == modeBlockRenewal {
		return a.blockUntilStreamDone(stream.Context())
	}
	if a.mode == modeReorderedRenewal {
		if err := stream.Send(&smbcertpb.SmbCertServerMsg{
			Msg: &smbcertpb.SmbCertServerMsg_Cert{Cert: &smbcertpb.SmbCertDelivery{
				MemberChain: a.signChain(pub, "smb-stale-renewal"),
				Ca:          a.caProto,
			}},
		}); err != nil {
			return err
		}
		if err := stream.Send(&smbcertpb.SmbCertServerMsg{
			Msg: &smbcertpb.SmbCertServerMsg_Renew{Renew: &smbcertpb.SmbCertRenew{Reason: "fixture renewal retry"}},
		}); err != nil {
			return err
		}
		renewPub, err = recvSMBCSR(stream)
		if err != nil {
			return err
		}
	}
	return stream.Send(&smbcertpb.SmbCertServerMsg{
		Msg: &smbcertpb.SmbCertServerMsg_Cert{Cert: &smbcertpb.SmbCertDelivery{
			MemberChain: a.signChain(renewPub, "smb-renewal"),
			Ca:          a.caProto,
		}},
	})
}

func (a *certAuthority) blockUntilStreamDone(ctx context.Context) error {
	if a.blocked != nil {
		select {
		case a.blocked <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (a *certAuthority) signChain(pub any, commonName string) *x509pb.X509CertChain {
	a.t.Helper()
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		a.t.Fatalf("generate serial: %v", err)
	}
	tmpl := &x509std.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509std.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509std.ExtKeyUsage{x509std.ExtKeyUsageServerAuth, x509std.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509std.CreateCertificate(rand.Reader, tmpl, a.caCert, pub, a.caKey)
	if err != nil {
		a.t.Fatalf("create leaf cert: %v", err)
	}
	return &x509pb.X509CertChain{Chain: []*x509pb.X509Cert{
		protoCert(commonName, der),
		a.caProto,
	}}
}

func TestGoTlsCertStreamBehavior(t *testing.T) {
	t.Run("first delivery and renewal succeed with distinct local keys", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runTLSFixture(t, modeRenewalSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-delivery stream end, got %v", err)
		}
		if ready != nil {
			t.Fatalf("ready signal = %v, want nil success", ready)
		}
		if len(h.tlsKeys) != 2 {
			t.Fatalf("expected initial + renewal TLS deliveries, got %d", len(h.tlsKeys))
		}
		if string(h.tlsKeys[0]) == string(h.tlsKeys[1]) {
			t.Fatal("renewal should use a fresh pending TLS private key")
		}
	})

	t.Run("clean end before first delivery is startup failure", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runTLSFixture(t, modeCleanBeforeFirst, h)
		if delivered {
			t.Fatal("did not expect delivery before clean pre-first end")
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected EOF before first delivery, got %v", err)
		}
		if !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
		if len(h.tlsKeys) != 0 {
			t.Fatalf("handler should not be called before first delivery, got %d", len(h.tlsKeys))
		}
	})

	t.Run("server error before first delivery is startup failure", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runTLSFixture(t, modeServerErrorBefore, h)
		if delivered {
			t.Fatal("did not expect delivery before server error")
		}
		if err == nil || !strings.Contains(err.Error(), "server error: fixture before first delivery") {
			t.Fatalf("expected server error before first delivery, got %v", err)
		}
		if !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})

	t.Run("server error after first delivery is reconnectable", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runTLSFixture(t, modeServerErrorAfter, h)
		if !delivered {
			t.Fatal("expected first delivery before server error")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "server error: fixture after first delivery") {
			t.Fatalf("expected server error after first delivery, got %v", err)
		}
		if len(h.tlsKeys) != 1 {
			t.Fatalf("expected exactly one installed TLS cert before reconnectable error, got %d", len(h.tlsKeys))
		}
	})

	t.Run("first handler failure is fatal", func(t *testing.T) {
		handlerErr := errors.New("reject first TLS cert")
		h := &recordingHandler{failTLSAt: map[int]error{1: handlerErr}}
		delivered, err, ready := runTLSFixture(t, modeFirstSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be attempted")
		}
		if err != errTlsFirstCertFatal {
			t.Fatalf("expected errTlsFirstCertFatal, got %v", err)
		}
		if !errors.Is(ready, handlerErr) {
			t.Fatalf("ready signal = %v, want handler error", ready)
		}
		// The ready signal must ALSO carry errTlsFirstCertFatal. Run()'s
		// background drain is the only consumer, and it discriminates on this
		// sentinel to decide which degrade reason to report: a rejected
		// delivery is this service's own handler refusing a certificate the
		// authority DID sign, not the authority failing to deliver one. Without
		// the sentinel both faults printed "gave up before first delivery" and
		// pointed the operator at the wrong half of the system.
		if !errors.Is(ready, errTlsFirstCertFatal) {
			t.Fatalf("ready signal = %v, want it wrapped with errTlsFirstCertFatal so Run can name the cause", ready)
		}
	})

	t.Run("renewal handler failure keeps loop nonfatal", func(t *testing.T) {
		h := &recordingHandler{failTLSAt: map[int]error{2: errors.New("reject renewal TLS cert")}}
		delivered, err, ready := runTLSFixture(t, modeRenewalSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-renewal stream end, got %v", err)
		}
		if len(h.tlsKeys) != 2 {
			t.Fatalf("expected renewal handler attempt, got %d calls", len(h.tlsKeys))
		}
	})

	t.Run("stale renewal cert is ignored until a fresh CSR is signed", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runTLSFixture(t, modeReorderedRenewal, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-renewal stream end, got %v", err)
		}
		if len(h.tlsKeys) != 2 {
			t.Fatalf("expected initial + valid renewal TLS deliveries, got %d", len(h.tlsKeys))
		}
		if string(h.tlsKeys[0]) == string(h.tlsKeys[1]) {
			t.Fatal("valid renewal should install the fresh pending TLS key")
		}
	})

	t.Run("missing authority connection fails before first delivery", func(t *testing.T) {
		readyCh := make(chan error, 1)
		delivered, err := runTlsCertStream(context.Background(), func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, readyCh)
		if delivered {
			t.Fatal("did not expect delivery without connection")
		}
		if err == nil || !strings.Contains(err.Error(), "no active connection") {
			t.Fatalf("expected no active connection error, got %v", err)
		}
		if ready := drainReady(readyCh); !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})
}

func TestGoSmbCertStreamBehavior(t *testing.T) {
	t.Run("first delivery and renewal succeed with distinct local keys", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runSMBFixture(t, modeRenewalSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-delivery stream end, got %v", err)
		}
		if ready != nil {
			t.Fatalf("ready signal = %v, want nil success", ready)
		}
		if len(h.smbKeys) != 2 {
			t.Fatalf("expected initial + renewal SMB deliveries, got %d", len(h.smbKeys))
		}
		if string(h.smbKeys[0]) == string(h.smbKeys[1]) {
			t.Fatal("renewal should use a fresh pending SMB private key")
		}
	})

	t.Run("clean end before first delivery is startup failure", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runSMBFixture(t, modeCleanBeforeFirst, h)
		if delivered {
			t.Fatal("did not expect delivery before clean pre-first end")
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected EOF before first delivery, got %v", err)
		}
		if !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
		if len(h.smbKeys) != 0 {
			t.Fatalf("handler should not be called before first delivery, got %d", len(h.smbKeys))
		}
	})

	t.Run("server error before first delivery is startup failure", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runSMBFixture(t, modeServerErrorBefore, h)
		if delivered {
			t.Fatal("did not expect delivery before server error")
		}
		if err == nil || !strings.Contains(err.Error(), "server error: fixture before first delivery") {
			t.Fatalf("expected server error before first delivery, got %v", err)
		}
		if !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})

	t.Run("server error after first delivery is reconnectable", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runSMBFixture(t, modeServerErrorAfter, h)
		if !delivered {
			t.Fatal("expected first delivery before server error")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "server error: fixture after first delivery") {
			t.Fatalf("expected server error after first delivery, got %v", err)
		}
		if len(h.smbKeys) != 1 {
			t.Fatalf("expected exactly one installed SMB cert before reconnectable error, got %d", len(h.smbKeys))
		}
	})

	t.Run("first handler failure is fatal", func(t *testing.T) {
		handlerErr := errors.New("reject first SMB cert")
		h := &recordingHandler{failSMBAt: map[int]error{1: handlerErr}}
		delivered, err, ready := runSMBFixture(t, modeFirstSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be attempted")
		}
		if err != errSmbFirstCertFatal {
			t.Fatalf("expected errSmbFirstCertFatal, got %v", err)
		}
		if !errors.Is(ready, handlerErr) {
			t.Fatalf("ready signal = %v, want handler error", ready)
		}
	})

	t.Run("renewal handler failure keeps loop nonfatal", func(t *testing.T) {
		h := &recordingHandler{failSMBAt: map[int]error{2: errors.New("reject renewal SMB cert")}}
		delivered, err, ready := runSMBFixture(t, modeRenewalSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-renewal stream end, got %v", err)
		}
		if len(h.smbKeys) != 2 {
			t.Fatalf("expected renewal handler attempt, got %d calls", len(h.smbKeys))
		}
	})

	t.Run("stale renewal cert is ignored until a fresh CSR is signed", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, err, ready := runSMBFixture(t, modeReorderedRenewal, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected clean post-renewal stream end, got %v", err)
		}
		if len(h.smbKeys) != 2 {
			t.Fatalf("expected initial + valid renewal SMB deliveries, got %d", len(h.smbKeys))
		}
		if string(h.smbKeys[0]) == string(h.smbKeys[1]) {
			t.Fatal("valid renewal should install the fresh pending SMB key")
		}
	})

	t.Run("missing authority connection fails before first delivery", func(t *testing.T) {
		readyCh := make(chan error, 1)
		delivered, err := runSmbCertStream(context.Background(), func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, readyCh)
		if delivered {
			t.Fatal("did not expect delivery without connection")
		}
		if err == nil || !strings.Contains(err.Error(), "no active connection") {
			t.Fatalf("expected no active connection error, got %v", err)
		}
		if ready := drainReady(readyCh); !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})
}

func TestGoCertStreamShutdownEscape(t *testing.T) {
	t.Run("TLS startup backoff exits on shutdown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		readyCh := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, readyCh)
		}()

		time.Sleep(20 * time.Millisecond)
		cancel()
		waitLoopExit(t, done, "TLS startup backoff")
		if ready := drainReady(readyCh); !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})

	t.Run("SMB startup backoff exits on shutdown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		readyCh := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			smbCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, readyCh)
		}()

		time.Sleep(20 * time.Millisecond)
		cancel()
		waitLoopExit(t, done, "SMB startup backoff")
		if ready := drainReady(readyCh); !errors.Is(ready, errNoReadySignal) {
			t.Fatalf("ready signal = %v, want none", ready)
		}
	})

	t.Run("TLS renewal attempt exits on shutdown", func(t *testing.T) {
		conn, cleanup, blocked := startAuthorityWithBlock(t, modeBlockRenewal)
		defer cleanup()
		ctx, cancel := context.WithCancel(context.Background())
		readyCh := make(chan error, 1)
		h := &recordingHandler{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, nil, readyCh)
		}()

		if ready := waitReady(t, readyCh, "TLS first delivery"); ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		waitSignal(t, blocked, "TLS renewal CSR")
		cancel()
		waitLoopExit(t, done, "TLS renewal attempt")
		if len(h.tlsKeys) != 1 {
			t.Fatalf("expected only the startup TLS cert before shutdown, got %d", len(h.tlsKeys))
		}
	})

	t.Run("SMB renewal attempt exits on shutdown", func(t *testing.T) {
		conn, cleanup, blocked := startAuthorityWithBlock(t, modeBlockRenewal)
		defer cleanup()
		ctx, cancel := context.WithCancel(context.Background())
		readyCh := make(chan error, 1)
		h := &recordingHandler{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			smbCertStreamLoop(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, readyCh)
		}()

		if ready := waitReady(t, readyCh, "SMB first delivery"); ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		waitSignal(t, blocked, "SMB renewal CSR")
		cancel()
		waitLoopExit(t, done, "SMB renewal attempt")
		if len(h.smbKeys) != 1 {
			t.Fatalf("expected only the startup SMB cert before shutdown, got %d", len(h.smbKeys))
		}
	})
}

func runTLSFixture(t *testing.T, mode authorityMode, h *recordingHandler) (bool, error, error) {
	t.Helper()
	conn, cleanup := startAuthority(t, mode)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readyCh := make(chan error, 1)
	delivered, err := runTlsCertStream(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, nil, readyCh)
	return delivered, err, drainReady(readyCh)
}

func runSMBFixture(t *testing.T, mode authorityMode, h *recordingHandler) (bool, error, error) {
	t.Helper()
	conn, cleanup := startAuthority(t, mode)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readyCh := make(chan error, 1)
	delivered, err := runSmbCertStream(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, readyCh)
	return delivered, err, drainReady(readyCh)
}

func startAuthority(t *testing.T, mode authorityMode) (*grpc.ClientConn, func()) {
	conn, cleanup, _ := startAuthorityWithBlock(t, mode)
	return conn, cleanup
}

func startAuthorityWithBlock(t *testing.T, mode authorityMode) (*grpc.ClientConn, func(), <-chan struct{}) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	blocked := make(chan struct{}, 1)
	authority := newCertAuthorityWithBlock(t, mode, blocked)
	tlscertpb.RegisterTlsCertServiceServer(srv, authority)
	smbcertpb.RegisterSmbCertServiceServer(srv, authority)
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve bufconn authority: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn authority: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}, blocked
}

func recvTLSInitAndCSR(stream tlscertpb.TlsCertService_ManageCertServer) (any, error) {
	msg, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("recv tls init: %w", err)
	}
	if msg.GetInit() == nil {
		return nil, fmt.Errorf("first TLS message = %T, want init", msg.GetMsg())
	}
	return recvTLSCSR(stream)
}

func recvTLSCSR(stream tlscertpb.TlsCertService_ManageCertServer) (any, error) {
	msg, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("recv tls csr: %w", err)
	}
	if msg.GetCsr() == nil {
		return nil, fmt.Errorf("TLS message = %T, want csr", msg.GetMsg())
	}
	return publicKeyFromProto(msg.GetCsr().GetPublicKey())
}

func recvSMBInitAndCSR(stream smbcertpb.SmbCertService_ManageSmbCertServer) (any, error) {
	msg, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("recv smb init: %w", err)
	}
	if msg.GetInit() == nil {
		return nil, fmt.Errorf("first SMB message = %T, want init", msg.GetMsg())
	}
	return recvSMBCSR(stream)
}

func recvSMBCSR(stream smbcertpb.SmbCertService_ManageSmbCertServer) (any, error) {
	msg, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("recv smb csr: %w", err)
	}
	if msg.GetCsr() == nil {
		return nil, fmt.Errorf("SMB message = %T, want csr", msg.GetMsg())
	}
	return publicKeyFromProto(msg.GetCsr().GetPublicKey())
}

func publicKeyFromProto(pub *keypb.PublicKey) (any, error) {
	if pub == nil {
		return nil, fmt.Errorf("missing public key")
	}
	raw, err := decodeBase64(pub.GetBase64())
	if err != nil {
		return nil, err
	}
	if block, _ := pem.Decode(raw); block != nil {
		raw = block.Bytes
	}
	key, err := x509std.ParsePKIXPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse CSR public key: %w", err)
	}
	return key, nil
}

func protoCert(id string, der []byte) *x509pb.X509Cert {
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &x509pb.X509Cert{
		Id:     id,
		Base64: base64.RawURLEncoding.EncodeToString(pemBytes),
	}
}

func decodeBase64(encoded string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if raw, err := enc.DecodeString(encoded); err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func drainReady(ch <-chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return errNoReadySignal
	}
}

func waitReady(t *testing.T, ch <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not report ready", label)
		return nil
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not reach blocking point", label)
	}
}

func waitLoopExit(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("%s did not exit after cancellation", label)
	}
}
