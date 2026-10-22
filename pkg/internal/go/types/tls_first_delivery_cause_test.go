// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"strings"
	"testing"
)

// PC-L-1 (new leg, batch 16): Run()'s TLS first-delivery drain used to report
// ONE degrade reason for TWO unrelated faults.
//
//   - the authority never delivered a certificate (retry budget spent), and
//   - the authority DID deliver one and this service's own handler rejected it
//     (errTlsFirstCertFatal)
//
// both arrived on tlsReadyCh and both were reported as "TLS renewal stream gave
// up before first delivery". An operator reading that for the second case goes
// and debugs the authority or the network — the wrong half of the system, since
// the certificate arrived fine and the local handler refused it.
//
// The drain must therefore discriminate on the sentinel. The behavioural half
// of this — that runTlsCertStream actually WRAPS the handler error with
// errTlsFirstCertFatal so errors.Is can see it — is pinned against real
// generated code in cross-test/cert-stream/testdata/cert_stream_behavior_test.go
// ("first handler failure is fatal").
func TestGoTLSFirstDeliveryDrainNamesItsCause(t *testing.T) {
	s := readGoServiceManagerTemplate(t)

	for _, want := range []string{
		// The sentinel must be wrapped onto the channel, not sent bare.
		`readyCh <- fmt.Errorf("%w: %w", errTlsFirstCertFatal, err)`,
		// ...and the drain must branch on it.
		"if errors.Is(err, errTlsFirstCertFatal) {",
		`degrade("TLS handler rejected the first renewed certificate: " + err.Error())`,
		// The give-up wording stays put — go is the reference for the
		// cross-language parity pin in pkg/internal/common.
		`degrade("TLS renewal stream gave up before first delivery: " + err.Error())`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("go SM template missing first-delivery cause discrimination:\n%s", want)
		}
	}

	// The bare send is what the old code did; if it comes back the drain
	// silently loses the ability to tell the two faults apart. Scope the
	// negative to runTlsCertStream — the SMB twin legitimately sends bare,
	// because a rejected first SMB cert aborts Run outright (there is no boot
	// credential to fall back to) so nothing downstream has to discriminate.
	body := goFuncBody(t, s, "func runTlsCertStream(")
	if strings.Contains(body, "delivered = true\nreadyCh <- err\n") {
		t.Error("handler-rejection outcome must be sentinel-wrapped before it is sent on readyCh, not sent bare")
	}
}

// goFuncBody returns the template text from the start of the named func to the
// start of the next top-level func, which is enough to scope a negative pin.
func goFuncBody(t *testing.T, s, decl string) string {
	t.Helper()
	i := strings.Index(s, decl)
	if i < 0 {
		t.Fatalf("declaration %q not found in template", decl)
	}
	rest := s[i+len(decl):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		return rest[:j]
	}
	return rest
}

// The sentinel's doc comment claimed "startup must abort (Run returns the
// handler error)". That is false for the TLS stream: unlike SMB, it does not
// gate Listen — the server already serves the X509Mint boot certificate, so Run
// keeps going and only marks the instance DEGRADED. Comments tell the truth.
func TestGoTLSFatalSentinelCommentDoesNotClaimStartupAborts(t *testing.T) {
	s := readGoServiceManagerTemplate(t)

	idx := strings.Index(s, "var errTlsFirstCertFatal")
	if idx < 0 {
		t.Fatal("errTlsFirstCertFatal declaration not found")
	}
	// The doc comment is the block immediately above the declaration.
	head := s[:idx]
	start := strings.LastIndex(head, "// errTlsFirstCertFatal")
	if start < 0 {
		t.Fatal("errTlsFirstCertFatal doc comment not found")
	}
	doc := head[start:]

	if strings.Contains(doc, "startup must abort") || strings.Contains(doc, "Run returns the handler error") {
		t.Error("errTlsFirstCertFatal doc comment must not claim startup aborts — the TLS stream does not gate Listen; Run drains the signal and marks the instance DEGRADED")
	}
	if !strings.Contains(doc, "fatal to the STREAM, not to startup") {
		t.Error("errTlsFirstCertFatal doc comment must state that it is fatal to the stream, not to startup")
	}
}

func readGoServiceManagerTemplate(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../templates/go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	return string(data)
}
