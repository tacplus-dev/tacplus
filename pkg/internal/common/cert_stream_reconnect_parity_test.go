// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
)

// funcBody returns the template text from the first occurrence of start up to
// the earliest of the given end markers after it (or to EOF). It is
// deliberately crude: these templates are flat, and the point is to prove a
// call site sits INSIDE the per-attempt function rather than in the enclosing
// reconnect loop.
func funcBody(t *testing.T, src, start string, ends ...string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("cannot find %q in template — the reconnect pin no longer proves anything", start)
	}
	rest := src[i+len(start):]
	cut := len(rest)
	for _, end := range ends {
		if j := strings.Index(rest, end); j >= 0 && j < cut {
			cut = j
		}
	}
	return rest[:cut]
}

// docs/API.md: "Reconnect loops re-resolve the authority endpoint on every
// attempt; a certificate stream must not pin a dead channel, stub, observer, or
// connection forever after the authority endpoint changes."
//
// Nothing pinned that. All three languages get it right today — go passes a
// connProvider func and calls it inside runTlsCertStream, java calls
// stateStoreConn.conn() inside runTlsCertStream, rust awaits
// conn_provider.conn() inside run_tls_cert_stream — but the difference between
// correct and broken is one hoisted variable: move the acquisition above the
// retry loop and the stream reconnects forever to an authority address that
// has moved, with no test failing. This pins the acquisition INSIDE the
// per-attempt function, for both the auto-TLS and the SMB member-cert stream
// (PC-L-2, PC-L-3 anti-channel-capture leg).
func TestCertStreamsReResolveTheConnectionPerAttempt(t *testing.T) {
	goSM, err := tmplutil.LoadTemplate("go/types", "service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("load service_manager.go.tmpl: %v", err)
	}
	for _, fn := range []string{"func runTlsCertStream(", "func runSmbCertStream("} {
		body := funcBody(t, goSM, fn, "\nfunc ")
		if !strings.Contains(body, "conn := connProvider()") {
			t.Errorf("go %s must acquire the connection per attempt; missing %q inside it",
				fn, "conn := connProvider()")
		}
	}
	for _, loop := range []string{"func tlsCertStreamLoop(", "func smbCertStreamLoop("} {
		body := funcBody(t, goSM, loop, "\nfunc ")
		if strings.Contains(body, "connProvider()") {
			t.Errorf("go %s must not resolve the connection once for the whole reconnect loop", loop)
		}
	}

	javaSM, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}
	for _, fn := range []string{
		"private static void runTlsCertStream(",
		"private static void runSmbCertStream(",
	} {
		body := funcBody(t, javaSM, fn, "\n    private static", "\n\t    private static")
		if !strings.Contains(body, "ManagedChannel channel = ") || !strings.Contains(body, ".conn();") {
			t.Errorf("java %s must resolve the channel per attempt from the ServiceConnection", fn)
		}
	}
	for _, loop := range []string{
		"private static void tlsCertStreamLoop(",
		"private static void smbCertStreamLoop(",
	} {
		body := funcBody(t, javaSM, loop, "\n    private static", "\n\t    private static")
		if strings.Contains(body, "ManagedChannel") {
			t.Errorf("java %s must not hold a ManagedChannel across reconnect attempts", loop)
		}
	}

	rustSM, err := tmplutil.LoadTemplate("rust/types", "service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("load service_manager.rs.tmpl: %v", err)
	}
	for _, fn := range []string{"async fn run_tls_cert_stream(", "async fn run_smb_cert_stream("} {
		body := funcBody(t, rustSM, fn, "\nasync fn ")
		if !strings.Contains(body, "conn_provider.conn().await") {
			t.Errorf("rust %s must await conn_provider.conn() per attempt", fn)
		}
	}
}

// PC-L-3: java's first-delivery retry budget had no pin at all. The chain that
// matters is the exhaustion one — after MAX_INIT_RETRIES the loop must report
// the instance degraded, hand startup the cause, release the latch, and STOP.
// Drop any one of those and the service either retries forever, blocks startup
// forever, or comes up believing it has a certificate it never received.
func TestJavaCertStreamRetryExhaustionEndsStartup(t *testing.T) {
	javaSM, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}
	body := funcBody(t, javaSM, "private static void tlsCertStreamLoop(", "\n    private static", "\n\t    private static")

	i := strings.Index(body, "if (attempt >= MAX_INIT_RETRIES) {")
	if i < 0 {
		t.Fatalf("java TLS cert-stream loop has no bounded first-delivery retry budget (PC-L-3)")
	}
	chain := body[i:]
	for _, step := range []string{
		`sm.reportTLSDegraded("TLS renewal stream gave up before first delivery: `,
		"readyError.set(e);",
		"readyLatch.countDown();",
		"return;",
	} {
		j := strings.Index(chain, step)
		if j < 0 {
			t.Fatalf("java retry-exhaustion path must %s — missing %q (PC-L-3)",
				"report, record the cause, release startup, and stop", step)
		}
		chain = chain[j+len(step):]
	}
}
