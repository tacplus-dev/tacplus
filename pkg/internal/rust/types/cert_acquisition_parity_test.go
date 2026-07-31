// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// T8b: Rust obtains its serving cert solely from the auto-TLS cert stream (no
// unary X509Mint serving cert, unlike Go), and the constructor MUST block on the
// first cert delivery before returning Self. The first-delivery await is
// necessary, not the redundant block the Go non-gating fix removed — removing it
// would serve without a cert (fail-open). See docs/DEVELOPMENT.md
// cert-acquisition invariant.
func TestRustServiceManagerBlocksOnFirstCertBeforeReturning(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	rendered := string(src)

	streamStart := strings.Index(rendered, "tls_cert_stream_loop(")
	await := strings.Index(rendered, "tls_ready_rx.await")
	returnSelf := strings.Index(rendered, "Ok(Self {")
	if streamStart < 0 || await < 0 || returnSelf < 0 {
		t.Fatalf("Rust constructor must spawn the cert stream, await first delivery, then return Self (start=%d await=%d return=%d)", streamStart, await, returnSelf)
	}
	if !(streamStart < await && await < returnSelf) {
		t.Fatalf("Rust must await first cert delivery after spawning the stream and before returning Self (start=%d await=%d return=%d)", streamStart, await, returnSelf)
	}

	// The serving cert must be the stream-delivered one, fed to the TLS incoming
	// via tls_server_certified_key, not a unary X509Mint.
	if !strings.Contains(rendered, "reloading_tls_incoming(addr, self.tls_server_certified_key.clone())") {
		t.Fatalf("Rust must serve with the stream-delivered cert via reloading_tls_incoming(tls_server_certified_key)")
	}
}
