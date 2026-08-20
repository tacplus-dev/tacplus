// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// PC-L-6 / PC-L-12 class: when the authority asks for a renewal and the client
// cannot answer — keypair generation throws, or the CSR send throws — the
// renewal request goes unanswered. go returns the session error
// ("generate renewal keypair" / "send renewal Csr", service_manager.go.tmpl)
// and rust propagates with `?`, so in both the stream loop reconnects and
// re-CSRs. Java used to log a warning and keep receiving on a stream with no
// renewal in flight, so the certificate silently ran to expiry.
//
// This pins that both java renewal handlers (TLS + SMB member cert) end the
// session instead of swallowing, for the loop to retry.
func TestJavaCertRenewalKeygenFailureEndsSession(t *testing.T) {
	tmpl, err := loadTemplate("ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	blocks := renewHandlerBlocks(t, tmpl)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 renewal handlers (TLS + SMB) in the java SM template, found %d", len(blocks))
	}
	for i, block := range blocks {
		catches := renewHandlerCatches(t, block, i)
		if len(catches) != 2 {
			t.Fatalf("renewal handler %d: expected a keygen catch and a CSR-send catch, found %d", i, len(catches))
		}
		for j, c := range catches {
			if !strings.Contains(c, "endSession.accept(") {
				t.Errorf("renewal handler %d catch %d: must end the session via endSession so runXCertStream rethrows, the call is cancelled, and the loop reconnects; catch:\n%s", i, j, c)
			}
		}
	}

	// endSession is what "end the session" means now, so it has to do all
	// three things: record the error, release the loop, and CANCEL the call.
	// Without the cancel the authority keeps sending on a stream the loop has
	// already failed and onNext keeps applying certs — a fail-open.
	for _, required := range []string{
		"streamError.compareAndSet(null, err);",
		"done.countDown();",
		"call.cancel(\"ManageCert session ended\", err);",
		"call.cancel(\"ManageSmbCert session ended\", err);",
	} {
		if !strings.Contains(tmpl, required) {
			t.Errorf("java SM template endSession is missing %q", required)
		}
	}

	// The old swallow-and-continue shape must not come back in either handler.
	for _, banned := range []string{
		`LOG.warning("Failed to generate renewal keypair: "`,
		`LOG.warning("Failed to generate SMB renewal keypair: "`,
	} {
		if strings.Contains(tmpl, banned) {
			t.Errorf("java SM template still swallows a renewal-keypair failure: %s", banned)
		}
	}
}

// renewHandlerBlocks returns the body of every `else if (msg.hasRenew())` branch
// in the template, each cut at the following `else if (!msg.getError()` arm.
func renewHandlerBlocks(t *testing.T, tmpl string) []string {
	t.Helper()
	const start = "} else if (msg.hasRenew()) {"
	const end = "} else if (!msg.getError().isEmpty()) {"

	var out []string
	rest := tmpl
	for {
		i := strings.Index(rest, start)
		if i < 0 {
			return out
		}
		rest = rest[i+len(start):]
		j := strings.Index(rest, end)
		if j < 0 {
			t.Fatalf("renewal handler at offset %d has no following error arm — template shape changed", i)
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

// renewHandlerCatches splits one renewal-handler body into its catch bodies.
func renewHandlerCatches(t *testing.T, block string, handler int) []string {
	t.Helper()
	const marker = "} catch (Exception e) {"

	parts := strings.Split(block, marker)
	if len(parts) < 2 {
		t.Fatalf("renewal handler %d has no catch block:\n%s", handler, block)
	}
	return parts[1:]
}
