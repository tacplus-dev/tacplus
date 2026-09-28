// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// #62: go's credential-rejection recovery (982b54c0) wired the state-watch
// paths only. The subscriber retried UNAUTHENTICATED with backoff but never
// dropped the rejected credential, so every retry replayed the same dead
// token — the cache is keyed on expiry and an authority restart invalidates a
// token that has NOT expired. rust's subscriber.rs.tmpl calls
// note_if_credential_rejection on that arm and java's Subscriber.java.tmpl
// gained it in #66; go was the last outlier.
func TestGoSubscriberInvalidatesRejectedCredential(t *testing.T) {
	outDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:      outDir,
		ModuleBasePath: "example.com/project/tacplus",
		Subscriptions: []tactype.SubscriptionEntry{{
			SourcePackage:   "alpha",
			SourceStateRoot: "SharedEventBus",
			SourceField:     "event_stream",
			TargetService:   "events",
			SourceTypeFqn:   "alpha.Event",
			SourceProtoDir:  "services",
		}},
	}

	if err := generateGoSubscribers(cfg); err != nil {
		t.Fatalf("generateGoSubscribers: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "subscribe", "alpha",
		"SharedEventBus", "event_stream", "subscriber.go"))
	if err != nil {
		t.Fatalf("read generated subscriber: %v", err)
	}
	rendered := string(data)

	if !strings.Contains(rendered, `service_im "example.com/project/tacplus/chassis/service_im"`) {
		t.Fatalf("subscriber must import the chassis identity manager:\n%s", rendered)
	}
	if !strings.Contains(rendered, "service_im.NoteIfCredentialRejection(err)") {
		t.Fatalf("subscriber reconnect loop must call "+
			"service_im.NoteIfCredentialRejection(err) so a rejected credential is "+
			"re-minted instead of replayed:\n%s", rendered)
	}

	// It belongs on the auth arm of the retry path, before the backoff sleep —
	// not after it.
	authIdx := strings.Index(rendered, "case codes.PermissionDenied, codes.Unauthenticated:")
	noteIdx := strings.Index(rendered, "service_im.NoteIfCredentialRejection(err)")
	sleepIdx := strings.Index(rendered, "case <-time.After(sleep):")
	if authIdx < 0 || sleepIdx < 0 || noteIdx < authIdx || noteIdx > sleepIdx {
		t.Fatalf("NoteIfCredentialRejection must sit on the auth-error arm, before the "+
			"backoff sleep (auth=%d note=%d sleep=%d)", authIdx, noteIdx, sleepIdx)
	}

	// rust and java deliberately do NOT re-mint on PERMISSION_DENIED; the go
	// classifier enforces that, so the subscriber must delegate rather than
	// invalidate on the status code itself.
	if strings.Contains(rendered, "service_im.NoteCredentialRejection()") {
		t.Fatal("subscriber must use NoteIfCredentialRejection (which excludes " +
			"PERMISSION_DENIED), not the unconditional NoteCredentialRejection")
	}
}

// Sys-only generation never emits chassis/service_im, so the subscriber must
// not reference it there.
func TestGoSubscriberOmitsIdentityManagerInSysOnly(t *testing.T) {
	outDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:      outDir,
		ModuleBasePath: "example.com/project/tacplus",
		SysOnly:        true,
		Subscriptions: []tactype.SubscriptionEntry{{
			SourcePackage:   "alpha",
			SourceStateRoot: "SharedEventBus",
			SourceField:     "event_stream",
			TargetService:   "events",
			SourceTypeFqn:   "alpha.Event",
			SourceProtoDir:  "services",
		}},
	}

	if err := generateGoSubscribers(cfg); err != nil {
		t.Fatalf("generateGoSubscribers: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "subscribe", "alpha",
		"SharedEventBus", "event_stream", "subscriber.go"))
	if err != nil {
		t.Fatalf("read generated subscriber: %v", err)
	}
	if strings.Contains(string(data), "service_im") {
		t.Fatalf("sys-only subscriber must not reference chassis/service_im:\n%s", data)
	}
}

// #62: the TLS cert renewal stream is the only auth-carrying loop a service
// with no state watches has. Without a rejection note it never re-mints, and
// without an acceptance note it can only ever climb toward degraded. rust
// wires both in tls_cert_stream_loop and java gained them in #66.
func TestGoTlsCertStreamReportsCredentialOutcome(t *testing.T) {
	sm, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("load service_manager template: %v", err)
	}

	loopIdx := strings.Index(sm, "func tlsCertStreamLoop(")
	if loopIdx < 0 {
		t.Fatal("service_manager missing tlsCertStreamLoop")
	}
	loop := sm[loopIdx:]
	if end := strings.Index(loop, "\nfunc runTlsCertStream("); end > 0 {
		loop = loop[:end]
	}

	if !strings.Contains(loop, "service_im.NoteCredentialAccepted()") {
		t.Fatalf("a delivered TLS cert proves the credential was accepted and must "+
			"clear the degraded counter:\n%s", loop)
	}
	if !strings.Contains(loop, "service_im.NoteIfCredentialRejection(err)") {
		t.Fatalf("tlsCertStreamLoop must drop a rejected credential before retrying:\n%s", loop)
	}

	// The rejection note belongs on the retry path only — after the
	// ctx-cancelled / first-cert-fatal arm that returns.
	fatalIdx := strings.Index(loop, "err == errTlsFirstCertFatal")
	noteIdx := strings.Index(loop, "service_im.NoteIfCredentialRejection(err)")
	if fatalIdx < 0 || noteIdx < fatalIdx {
		t.Fatal("NoteIfCredentialRejection must sit after the errTlsFirstCertFatal " +
			"arm, on the retry path only")
	}
}

// The degraded floor and the per-call invalidation primitives themselves —
// landed for go in 982b54c0. Pin them so they stay wired.
func TestGoCredentialDegradedFloorStaysWired(t *testing.T) {
	im, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("load service_im template: %v", err)
	}
	for _, want := range []string{
		"const degradedAfter = 3",
		"func CredentialsDegraded() bool",
		"func NoteIfCredentialRejection(err error)",
		"func NoteCredentialAccepted()",
		"RegisterCredentialInvalidator(func() {",
	} {
		if !strings.Contains(im, want) {
			t.Fatalf("service_im lost credential-recovery primitive %q", want)
		}
	}
	// PermissionDenied must not classify as a rejection — re-minting the same
	// identity would loop forever against a real authorization refusal.
	if strings.Contains(im, "codes.PermissionDenied") {
		t.Fatal("IsCredentialRejection must not treat PermissionDenied as a rejection")
	}

	sm, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("load service_manager template: %v", err)
	}
	if !strings.Contains(sm, "hr.SetDegradedProbe(service_im.CredentialsDegraded)") {
		t.Fatal("service_manager must wire CredentialsDegraded as the readiness degraded probe")
	}
}
