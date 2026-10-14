// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// T8b: Java obtains its serving cert solely from the auto-TLS/ManageCert bidi
// stream (no unary X509Mint serving cert, unlike Go), and create() MUST block on
// the first cert delivery before returning the ServiceManager. The first-delivery
// await is necessary, not the redundant block the Go non-gating fix removed —
// removing it would serve without a cert (fail-open). See docs/DEVELOPMENT.md
// cert-acquisition invariant.
func TestJavaServiceManagerBlocksOnFirstCertBeforeReturning(t *testing.T) {
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	streamStart := strings.Index(sm, "tlsCertStreamLoop(sm, stateStoreConn, identity, tlsReady, tlsError)")
	await := strings.Index(sm, "tlsReady.await();")
	returnSm := strings.Index(sm, "return sm;")
	if streamStart < 0 || await < 0 || returnSm < 0 {
		t.Fatalf("Java create() must start the cert stream, await first delivery, then return (start=%d await=%d return=%d)", streamStart, await, returnSm)
	}
	if !(streamStart < await && await < returnSm) {
		t.Fatalf("Java create() must await first cert delivery after starting the stream and before returning sm (start=%d await=%d return=%d)", streamStart, await, returnSm)
	}

	// The serving cert must be the stream-delivered one, read via the
	// GrpcServer.CertProvider, not a unary X509Mint.
	if !strings.Contains(sm, "new GrpcServer.CertProvider()") ||
		!strings.Contains(sm, "return getTlsCertChain();") {
		t.Fatalf("Java GrpcServer must serve with the stream-delivered cert via CertProvider.getTlsCertChain()")
	}
	if !strings.Contains(sm, "instead of calling X509Mint directly") {
		t.Fatalf("Java cert-acquisition model comment (stream, not unary X509Mint) must be preserved")
	}
}
