// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// H1 / SEC-1: the IdentityManager bootstrap connection carries the workload's
// projected identity token to the token authority, so it must pin the peer's
// SPIFFE trust domain — never accept CA-only trust (any CA-issued cert could
// impersonate the authority and harvest that token).
func TestJavaIdentityManagerBootstrapPinsTrustDomain(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	if !strings.Contains(im, "ServiceConnection.buildTrustDomainClientSslContext(") {
		t.Fatalf("IdentityManager bootstrap TLS must pin the trust domain via "+
			"ServiceConnection.buildTrustDomainClientSslContext:\n%s", im)
	}
	// The old CA-only shape (raw CA-PEM trust manager, no identity pin) must be gone.
	if strings.Contains(im, "sslBuilder.trustManager(new ByteArrayInputStream(") {
		t.Fatalf("IdentityManager bootstrap TLS still installs a CA-only trust manager (fail-open):\n%s", im)
	}

	sc := readJavaTemplate(t, "java/chassis/serviceconnection/ServiceConnection.java.tmpl")
	for _, want := range []string{
		"public static SslContext buildTrustDomainClientSslContext(",
		"static void verifyPeerInTrustDomain(",
		"new TrustDomainTrustManager(",
	} {
		if !strings.Contains(sc, want) {
			t.Fatalf("ServiceConnection missing trust-domain pin primitive %q:\n%s", want, sc)
		}
	}
}

// TestJavaIdentityManagerFixedAudienceCredentials verifies that:
//  1. IdentityManager exposes callCredentialsForAudience(String) for fixed-audience
//     state-store connections (so state watches mint tokens for kernel, not the
//     owning service's proto package).
//  2. ServiceManager wires state clients with callCredentialsForAudience, not the
//     per-method callCredentials().
//  3. The TLS cert stream (ManageCert) uses callCredentialsForAudience with the
//     kernel audience — callCredentials() would resolve "tlscert" from the method
//     name and mint a token for the wrong service.
func TestJavaIdentityManagerFixedAudienceCredentials(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")

	// The method must exist — it takes a fixed audience string and returns CallCredentials.
	if !strings.Contains(im, "public CallCredentials callCredentialsForAudience(String fixedAudience)") {
		t.Fatal("IdentityManager missing callCredentialsForAudience(String) method")
	}
	// It must call getTokenFor with the fixed audience, not audienceFromMethod.
	if !strings.Contains(im, "String token = getTokenFor(fixedAudience)") {
		t.Fatal("callCredentialsForAudience must call getTokenFor(fixedAudience)")
	}

	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	// State clients must use callCredentialsForAudience with the state store ID,
	// NOT the dynamic callCredentials() that resolves audience from the method name.
	if !strings.Contains(sm, `identity.callCredentialsForAudience("{{.StateStoreServiceID}}")`) {
		t.Fatal("ServiceManager must wire state clients with callCredentialsForAudience(StateStoreServiceID)")
	}

	// TLS cert stream (ManageCert) is served by the state store (kernel).
	// callCredentials() resolves "tlscert" from the method name, minting a
	// token for the wrong audience. The stub must use callCredentialsForAudience.
	tlsStubIdx := strings.Index(sm, "TlsCertServiceGrpc.newStub(channel)")
	if tlsStubIdx < 0 {
		// Template may not emit TLS cert code when HasAutoTls is false — skip.
		return
	}
	// The withCallCredentials call immediately following the stub must use the
	// fixed-audience variant, not the method-resolving callCredentials().
	tlsTail := sm[tlsStubIdx:]
	credIdx := strings.Index(tlsTail, ".withCallCredentials(")
	if credIdx < 0 {
		t.Fatal("TLS cert stream stub must attach call credentials")
	}
	credSnippet := tlsTail[credIdx : credIdx+120]
	if !strings.Contains(credSnippet, `callCredentialsForAudience("{{.StateStoreServiceID}}")`) {
		t.Fatalf("TLS cert stream must use callCredentialsForAudience(StateStoreServiceID), not callCredentials();\ngot: %s", credSnippet)
	}
}
