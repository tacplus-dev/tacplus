// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// The TLS give-up degrade reason is operator-facing health-plane text: it is
// what a human reads when an instance goes DEGRADED. go, rust and java must
// therefore emit the SAME sentence, so one runbook covers a fleet that mixes
// languages.
//
// This has already drifted once. A rust-side change to the give-up path
// rewrote the reason to "TLS cert stream failed before first delivery after N
// attempts", leaving go and java on "TLS renewal stream gave up before first
// delivery" — three per-language pins each stayed green because each only ever
// looked at its own template. Nothing compared them. This does.
func TestTLSGiveUpDegradeReasonIsIdenticalAcrossLanguages(t *testing.T) {
	const phrase = "TLS renewal stream gave up before first delivery: "

	for _, tc := range []struct {
		lang string
		dir  string
		file string
	}{
		{"go", "go/types", "service_manager.go.tmpl"},
		{"rust", "rust/types", "service_manager.rs.tmpl"},
		{"java", "java/types", "ServiceManager.java.tmpl"},
	} {
		s, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("%s: load %s: %v", tc.lang, tc.file, err)
		}
		if !strings.Contains(s, phrase) {
			t.Errorf("%s SM template does not report the shared TLS give-up degrade reason %q; the three languages must not drift apart on operator-facing health text", tc.lang, phrase)
		}
	}
}

// PC-W-8 doc leg: docs/API.md had TWO orderings of the wrapper pipeline. The
// authoritative numbered list gets it right — auth precedes validation, and
// says why (an unauthenticated caller must not be able to drive the validation
// path or distinguish outcomes from validation errors). The one-line summary
// table above it listed "inbound validation, authentication, authorization",
// which is the opposite and is the line a reader meets first.
//
// The code does auth first. The summary must say so too.
func TestApiDocSummaryPutsAuthBeforeValidation(t *testing.T) {
	data, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatalf("read API.md: %v", err)
	}
	s := string(data)

	const row = "| Service wrappers | Enforce "
	i := strings.Index(s, row)
	if i < 0 {
		t.Fatal("service-wrappers summary row not found in docs/API.md")
	}
	line := s[i:]
	if j := strings.Index(line, "\n"); j >= 0 {
		line = line[:j]
	}

	auth := strings.Index(line, "authentication")
	val := strings.Index(line, "inbound validation")
	if auth < 0 || val < 0 {
		t.Fatalf("summary row no longer names both stages: %s", line)
	}
	if auth > val {
		t.Errorf("docs/API.md summary row lists inbound validation before authentication, contradicting the numbered pipeline (auth precedes validation) and the generated wrappers:\n%s", line)
	}
}

// PC-L-1, java leg. Batch 16 taught go's Run() drain and rust's tls_ready_rx
// arm to distinguish the two faults that arrive on the first-delivery signal:
// the authority never delivering a certificate (retry budget spent) versus the
// authority delivering one and this service's own handler refusing it. Java was
// left reporting BOTH as "auto-TLS cert stream failed to deliver initial
// certificate", pointing an operator at the authority or the network when the
// fault is local.
//
// Java also retried it. A handler that refuses a valid certificate will refuse
// its successor, but the rejection surfaced as a plain RuntimeException inside
// the newInstance retry loop, so the whole ServiceManager init was re-run
// MAX_INIT_RETRIES times with backoff before failing — a permanent
// misconfiguration masked as a transient one (the NEW-21 shape). It is now
// classified permanent BY TYPE, like the other two permanent init errors.
func TestJavaDistinguishesTlsHandlerRejectionFromGiveUp(t *testing.T) {
	s, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}

	const rejectMsg = "auto-TLS handler rejected the first certificate delivery"
	if !strings.Contains(s, rejectMsg) {
		t.Errorf("java startup reports no distinct cause for a handler-rejected first certificate (want %q); an operator is sent to debug the authority for a local fault", rejectMsg)
	}

	const giveUpMsg = "auto-TLS cert stream failed to deliver initial certificate"
	if !strings.Contains(s, giveUpMsg) {
		t.Errorf("java no longer reports the give-up cause %q", giveUpMsg)
	}

	classifier := "private static boolean isPermanentIdentityInitError(Throwable t) {"
	i := strings.Index(s, classifier)
	if i < 0 {
		t.Fatal("isPermanentIdentityInitError not found — the permanent-init classifier was renamed")
	}
	body := s[i:]
	if j := strings.Index(body, "\n        return false;"); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "cur instanceof FatalTlsHandlerException") {
		t.Error("isPermanentIdentityInitError does not classify FatalTlsHandlerException as permanent: a deterministic handler rejection is retried through the whole init backoff instead of failing fast")
	}
}

// PC-L-1, renewal-delivery leg. The auto-TLS renewal loop has three ways to
// refuse a delivered certificate and keep serving the previous one: the leaf
// does not match the key we asked for, the delivered private key does not
// parse, and the application handler rejects the certificate. All three have
// the same consequence — the instance goes on serving a certificate that WILL
// expire — but only the unparseable-key path reported it to the health plane.
// The other two were log-only in every language, so the instance reported Ready
// right up to the outage and the operator's first signal was the expiry itself.
//
// Every "keeping the previous certificate" path must reach reportTLSDegraded /
// report_tls_degraded, and must do so with the same operator-facing sentence in
// all three languages (same reasoning as the give-up reason above).
func TestRenewalKeepingPreviousCertReportsDegradedInEveryLanguage(t *testing.T) {
	// The two reasons that had no health-plane signal at all.
	phrases := []string{
		"renewed TLS certificate does not match the requested key, keeping the previous certificate: ",
		"TLS handler rejected a renewed certificate, keeping the previous certificate: ",
	}

	for _, tc := range []struct {
		lang    string
		dir     string
		file    string
		degrade string
	}{
		{"go", "go/types", "service_manager.go.tmpl", "degrade("},
		{"rust", "rust/types", "service_manager.rs.tmpl", "report_tls_degraded(tls_degrade, "},
		{"java", "java/types", "ServiceManager.java.tmpl", "sm.reportTLSDegraded("},
	} {
		s, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("%s: load %s: %v", tc.lang, tc.file, err)
		}
		for _, phrase := range phrases {
			idx := strings.Index(s, phrase)
			if idx < 0 {
				t.Errorf("%s SM template never reports the degrade reason %q; a renewal that keeps the previous certificate must not be log-only", tc.lang, phrase)
				continue
			}
			// The phrase must be an ARGUMENT to the degrade call, not just a log
			// line that happens to contain the same words.
			head := s[:idx]
			call := strings.LastIndex(head, tc.degrade)
			if call < 0 || strings.Contains(head[call:], "\n") {
				t.Errorf("%s SM template mentions %q but not as an argument to %s — the reason must reach the health plane, not only the log", tc.lang, phrase, tc.degrade)
			}
		}
	}
}

// PC-L-1, rust serving-cert install leg. go routes a failed rebuild of the
// renewed serving certificate into reportTLSDegraded (grpcserver's
// SetCertDegradeReporter). rust's equivalent — installing the rebuilt
// CertifiedKey into the handshake resolver's store — swallowed a poisoned-lock
// failure into a tracing::error!. That store is what every later handshake
// reads, so once it is unwritable the instance serves its current certificate
// to expiry while still reporting Ready.
func TestRustServingCertInstallFailureReportsDegraded(t *testing.T) {
	s, err := tmplutil.LoadTemplate("rust/types", "service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("load service_manager.rs.tmpl: %v", err)
	}
	const logLine = `tracing::error!("Failed to install TLS server identity: {}", e);`
	idx := strings.Index(s, logLine)
	if idx < 0 {
		t.Fatalf("rust SM template no longer has the serving-cert install failure arm (%q); re-target this test", logLine)
	}
	rest := s[idx+len(logLine):]
	// The degrade call must be the very next statement in the same arm.
	end := strings.Index(rest, "}")
	if end < 0 {
		end = len(rest)
	}
	if !strings.Contains(rest[:end], "report_tls_degraded(tls_degrade, ") {
		t.Error("rust SM template logs a failed TLS serving-identity install but never reports it degraded; the instance keeps serving the previous certificate to expiry while reporting Ready (go does report this via SetCertDegradeReporter)")
	}
}
