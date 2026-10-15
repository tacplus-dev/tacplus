// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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
	certstreamtypes "example.com/certstream/tacplus/model/types/svc/certstream"
	keypb "example.com/certstream/tacplus/model/types/sys/key"
	smbcertpb "example.com/certstream/tacplus/model/types/sys/smbcert"
	tlscertpb "example.com/certstream/tacplus/model/types/sys/tlscert"
	x509pb "example.com/certstream/tacplus/model/types/sys/x509"
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
	tlsChains []*x509pb.X509CertChain
	smbChains []*x509pb.X509CertChain
	failTLSAt map[int]error
	failSMBAt map[int]error
}

func (h *recordingHandler) StreamMemberGrpcImpl() fe.StreamMemberServiceHandler {
	return nil
}

func (h *recordingHandler) StreamStateGrpcImpl() fe.StreamStateServiceHandler {
	return nil
}

func (h *recordingHandler) NewTlsCert(chain *x509pb.X509CertChain, privateKeyDER []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tlsKeys = append(h.tlsKeys, append([]byte(nil), privateKeyDER...))
	h.tlsChains = append(h.tlsChains, chain)
	if err := h.failTLSAt[len(h.tlsKeys)]; err != nil {
		return err
	}
	return nil
}

func (h *recordingHandler) NewSmbCert(chain *x509pb.X509CertChain, privateKeyDER []byte, _ *x509pb.X509Cert) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.smbKeys = append(h.smbKeys, append([]byte(nil), privateKeyDER...))
	h.smbChains = append(h.smbChains, chain)
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

	// dialed, when non-nil, receives one token per stream this authority
	// serves, so a test can act between a failed attempt and its retry.
	dialed chan struct{}

	streamMu   sync.Mutex
	tlsStreams int
	smbStreams int
}

func (a *certAuthority) noteStream(tls bool) {
	a.streamMu.Lock()
	if tls {
		a.tlsStreams++
	} else {
		a.smbStreams++
	}
	a.streamMu.Unlock()
	if a.dialed != nil {
		select {
		case a.dialed <- struct{}{}:
		default:
		}
	}
}

func (a *certAuthority) streamCounts() (int, int) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	return a.tlsStreams, a.smbStreams
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
	a.noteStream(true)
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
	a.noteStream(false)
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
		// And Run's drain, given that signal, names the handler (PC-L-2b).
		readyCh := make(chan error, 1)
		readyCh <- ready
		var reasons []string
		drainTLSFirstDelivery(context.Background(), readyCh, func(r string) { reasons = append(reasons, r) })
		if len(reasons) != 1 || !strings.HasPrefix(reasons[0], "TLS handler rejected the first renewed certificate: ") {
			t.Fatalf("degrade calls = %q, want exactly one naming this service's handler as the fault", reasons)
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
	// PC-L-1's DEGRADED-marking seam. Both renewal paths below keep the
	// PREVIOUS certificate in service: the instance goes on serving a cert
	// that will expire, so without a health signal it reports Ready right up
	// to the outage. The reason text is what the operator reads, so it must
	// name which half of the system faulted.
	t.Run("renewal handler rejection degrades with the handler's reason", func(t *testing.T) {
		handlerErr := errors.New("reject renewal TLS cert")
		h := &recordingHandler{failTLSAt: map[int]error{2: handlerErr}}
		delivered, _, ready, reasons := runTLSFixtureDegrade(t, modeRenewalSuccess, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if len(reasons) != 1 {
			t.Fatalf("degrade calls = %v, want exactly one for the rejected renewal", reasons)
		}
		if !strings.Contains(reasons[0], "TLS handler rejected a renewed certificate") {
			t.Fatalf("degrade reason = %q, want it to name this service's handler as the fault", reasons[0])
		}
		if !strings.Contains(reasons[0], handlerErr.Error()) {
			t.Fatalf("degrade reason = %q, want it to carry the handler's own error", reasons[0])
		}
	})

	t.Run("stale renewal cert degrades with a key-mismatch reason", func(t *testing.T) {
		h := &recordingHandler{}
		delivered, _, ready, reasons := runTLSFixtureDegrade(t, modeReorderedRenewal, h)
		if !delivered {
			t.Fatal("expected first delivery to be reported")
		}
		if ready != nil {
			t.Fatalf("first ready signal = %v, want nil", ready)
		}
		if len(reasons) != 1 {
			t.Fatalf("degrade calls = %v, want exactly one for the discarded stale cert", reasons)
		}
		if !strings.Contains(reasons[0], "does not match the requested key") {
			t.Fatalf("degrade reason = %q, want it to name the authority's cert as the fault", reasons[0])
		}
	})

	t.Run("clean renewal does not degrade", func(t *testing.T) {
		h := &recordingHandler{}
		_, _, _, reasons := runTLSFixtureDegrade(t, modeRenewalSuccess, h)
		if len(reasons) != 0 {
			t.Fatalf("degrade calls = %v, want none on a healthy renewal", reasons)
		}
	})

	t.Run("missing authority connection fails before first delivery", func(t *testing.T) {
		readyCh := make(chan error, 1)
		delivered, err := runTlsCertStream(context.Background(), func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh)
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

	// PC-L-2 behavioral leg. connProvider is invoked fresh on every attempt so a
	// retry RE-RESOLVES the authority endpoint. Template text pins prove the
	// call site sits inside runTlsCertStream; only this proves the effect: move
	// the target between a failed attempt and its retry, and the retry must land
	// on the new authority instead of a connection memoized from attempt one.
	t.Run("retry re-resolves the target and dials the swapped authority", func(t *testing.T) {
		stale := newCertAuthority(t, modeServerErrorBefore)
		stale.dialed = make(chan struct{}, 1)
		staleConn, staleCleanup := startAuthorityFor(t, stale)
		defer staleCleanup()
		fresh := newCertAuthority(t, modeFirstSuccess)
		freshConn, freshCleanup := startAuthorityFor(t, fresh)
		defer freshCleanup()

		target := &swappableTarget{conn: staleConn}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		readyCh := make(chan error, 1)
		h := &recordingHandler{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			tlsCertStreamLoop(ctx, target.provider, "certstream", h, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 0)
		}()

		waitSignal(t, stale.dialed, "TLS attempt against the stale target")
		target.swap(freshConn)

		// The loop backs off a couple of seconds before its first retry.
		if ready := waitReadyWithin(t, readyCh, "TLS delivery after target swap", 15*time.Second); ready != nil {
			t.Fatalf("ready signal = %v, want nil from the swapped target", ready)
		}
		cancel()
		waitLoopExit(t, done, "TLS target-swap loop")

		if calls := target.calls(); calls < 2 {
			t.Fatalf("connProvider calls = %d, want one per attempt (at least 2)", calls)
		}
		if staleTLS, _ := stale.streamCounts(); staleTLS != 1 {
			t.Fatalf("stale authority served %d ManageCert streams, want only the one failed attempt", staleTLS)
		}
		if freshTLS, _ := fresh.streamCounts(); freshTLS < 1 {
			t.Fatal("retry never reached the swapped authority: the loop reused a stale connection")
		}
		if len(h.tlsChains) != 1 {
			t.Fatalf("expected exactly one TLS delivery, got %d", len(h.tlsChains))
		}
		if got := h.tlsChains[0].GetChain()[1].GetBase64(); got != fresh.caProto.GetBase64() {
			t.Fatal("delivered TLS cert was not signed by the swapped authority's CA")
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

	// PC-L-2 behavioral leg, SMB member-cert half. Same contract, separate loop.
	t.Run("retry re-resolves the target and dials the swapped authority", func(t *testing.T) {
		stale := newCertAuthority(t, modeServerErrorBefore)
		stale.dialed = make(chan struct{}, 1)
		staleConn, staleCleanup := startAuthorityFor(t, stale)
		defer staleCleanup()
		fresh := newCertAuthority(t, modeFirstSuccess)
		freshConn, freshCleanup := startAuthorityFor(t, fresh)
		defer freshCleanup()

		target := &swappableTarget{conn: staleConn}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		readyCh := make(chan error, 1)
		h := &recordingHandler{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			smbCertStreamLoop(ctx, target.provider, "certstream", h, func() (string, error) { return "token", nil }, readyCh)
		}()

		waitSignal(t, stale.dialed, "SMB attempt against the stale target")
		target.swap(freshConn)

		if ready := waitReadyWithin(t, readyCh, "SMB delivery after target swap", 15*time.Second); ready != nil {
			t.Fatalf("ready signal = %v, want nil from the swapped target", ready)
		}
		cancel()
		waitLoopExit(t, done, "SMB target-swap loop")

		if calls := target.calls(); calls < 2 {
			t.Fatalf("connProvider calls = %d, want one per attempt (at least 2)", calls)
		}
		if _, staleSMB := stale.streamCounts(); staleSMB != 1 {
			t.Fatalf("stale authority served %d ManageSmbCert streams, want only the one failed attempt", staleSMB)
		}
		if _, freshSMB := fresh.streamCounts(); freshSMB < 1 {
			t.Fatal("retry never reached the swapped authority: the loop reused a stale connection")
		}
		if len(h.smbChains) != 1 {
			t.Fatalf("expected exactly one SMB delivery, got %d", len(h.smbChains))
		}
		if got := h.smbChains[0].GetChain()[1].GetBase64(); got != fresh.caProto.GetBase64() {
			t.Fatal("delivered SMB member cert was not signed by the swapped authority's CA")
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
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 0)
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
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 0)
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

// TestGoTlsCertStreamRetryExhaustion drives the give-up-before-first-delivery
// path in real time. The production budget (certStreamMaxInitRetries = 30) takes
// ~812s of backoff to spend honestly, which is why this path had no behavioral
// pin; tlsCertStreamLoop now takes the budget as a parameter so a test can spend
// it in seconds (PC-L-3).
func TestGoTlsCertStreamRetryExhaustion(t *testing.T) {
	t.Run("TLS loop gives up after the injected budget", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		readyCh := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			// A nil connection fails every attempt before first delivery, so the
			// loop can only exit by exhausting its budget. Budget 3 costs 2s+4s
			// of backoff instead of the production budget's ~812s.
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 3)
		}()

		ready := waitReadyWithin(t, readyCh, "TLS retry exhaustion", 15*time.Second)
		if ready == nil {
			t.Fatal("ready signal = nil, want a give-up error")
		}
		if errors.Is(ready, errTlsFirstCertFatal) {
			t.Fatalf("ready signal = %v, want a give-up error, not a handler rejection", ready)
		}
		if !strings.Contains(ready.Error(), "TLS cert stream failed before first delivery after 3 attempts") {
			t.Fatalf("ready signal = %v, want the give-up error naming 3 attempts", ready)
		}
		waitLoopExit(t, done, "TLS retry exhaustion")
	})

	// PC-L-2b: the give-up error reaches Run's background drain, which must
	// report the instance DEGRADED as a give-up: not as a handler rejection
	// (that points the operator at the wrong half of the system) and not
	// silently. Driven with the signal a real loop sends after spending its
	// budget, through the same drainTLSFirstDelivery Run starts.
	t.Run("give-up is reported DEGRADED as a give-up", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		readyCh := make(chan error, 1)
		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 3)
		}()
		reasons := make(chan string, 2)
		drainDone := make(chan struct{})
		go func() {
			defer close(drainDone)
			drainTLSFirstDelivery(ctx, readyCh, func(r string) { reasons <- r })
		}()

		select {
		case r := <-reasons:
			if !strings.HasPrefix(r, "TLS renewal stream gave up before first delivery: ") {
				t.Fatalf("degrade reason = %q, want it to report the stream giving up", r)
			}
			if !strings.Contains(r, "after 3 attempts") {
				t.Fatalf("degrade reason = %q, want it to carry the give-up error", r)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the give-up was never reported DEGRADED")
		}
		waitLoopExit(t, drainDone, "TLS first-delivery drain")
		waitLoopExit(t, loopDone, "TLS retry exhaustion")
		if len(reasons) != 0 {
			t.Fatalf("extra degrade call %q, want exactly one", <-reasons)
		}
	})

	t.Run("default budget is not the injected one", func(t *testing.T) {
		// Guards the hard constraint behind the seam: a caller that passes no
		// budget keeps the production one. A budget of 3 gives up after ~6s, so
		// silence past that window proves the default is materially larger.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		readyCh := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			tlsCertStreamLoop(ctx, func() grpc.ClientConnInterface { return nil }, "certstream", &recordingHandler{}, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh, 0)
		}()

		select {
		case ready := <-readyCh:
			t.Fatalf("default-budget loop gave up early with %v", ready)
		case <-time.After(8 * time.Second):
		}
		cancel()
		waitLoopExit(t, done, "default budget")
	})
}

func runTLSFixture(t *testing.T, mode authorityMode, h *recordingHandler) (bool, error, error) {
	t.Helper()
	conn, cleanup := startAuthority(t, mode)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readyCh := make(chan error, 1)
	delivered, err := runTlsCertStream(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, nil, func(string) {}, readyCh)
	return delivered, err, drainReady(readyCh)
}

// runTLSFixtureDegrade mirrors runTLSFixture but records what the stream
// reports through PC-L-1's degrade seam, so a test can assert WHICH renewal
// fault the health plane was told about, not merely that the stream survived.
func runTLSFixtureDegrade(t *testing.T, mode authorityMode, h *recordingHandler) (bool, error, error, []string) {
	t.Helper()
	conn, cleanup := startAuthority(t, mode)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readyCh := make(chan error, 1)
	var reasons []string
	degrade := func(reason string) { reasons = append(reasons, reason) }
	delivered, err := runTlsCertStream(ctx, func() grpc.ClientConnInterface { return conn }, "certstream", h, func() (string, error) { return "token", nil }, nil, degrade, readyCh)
	return delivered, err, drainReady(readyCh), reasons
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
	blocked := make(chan struct{}, 1)
	authority := newCertAuthorityWithBlock(t, mode, blocked)
	conn, cleanup := startAuthorityFor(t, authority)
	return conn, cleanup, blocked
}

// startAuthorityFor serves one already-constructed authority on its own bufconn
// listener, so a test can stand up two distinct authorities and swap the
// connection handed to a cert-stream loop between them.
func startAuthorityFor(t *testing.T, authority *certAuthority) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
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
	}
}

// swappableTarget models an authority endpoint that MOVES. connProvider must be
// invoked fresh on every attempt, so a retry sees whatever the target resolves
// to now — not a handle memoized from the first attempt.
type swappableTarget struct {
	mu    sync.Mutex
	conn  grpc.ClientConnInterface
	count int
}

func (s *swappableTarget) provider() grpc.ClientConnInterface {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return s.conn
}

func (s *swappableTarget) swap(conn grpc.ClientConnInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = conn
}

func (s *swappableTarget) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
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
	return waitReadyWithin(t, ch, label, 2*time.Second)
}

func waitReadyWithin(t *testing.T, ch <-chan error, label string, d time.Duration) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
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
