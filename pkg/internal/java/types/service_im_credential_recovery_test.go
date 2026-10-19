// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// #66: the IdentityManager health poll used to invalidate the token cache on
// every tick and then re-mint, so a healthy service paid one token exchange
// every 30 seconds — roughly twenty times the mint load go and rust place on
// the authority, both of which re-mint only on real need (expiry approaching,
// or a credential rejection). The poll must now respect the cache.
func TestJavaIdentityManagerHealthPollDoesNotForceRemint(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")

	start := strings.Index(im, "private void startHealthMonitor()")
	if start < 0 {
		t.Fatal("IdentityManager missing startHealthMonitor()")
	}
	end := strings.Index(im[start:], "\n    /**")
	if end < 0 {
		t.Fatal("could not bound startHealthMonitor()")
	}
	monitor := im[start : start+end]

	// The poll body must not drop the cache. Either minter's invalidate() here
	// is the ~20x over-mint.
	for _, banned := range []string{"k8sMinter.invalidate()", "cloudMinter.invalidate()"} {
		if strings.Contains(monitor, banned) {
			t.Fatalf("health monitor still force-invalidates the token cache (%s) — "+
				"that re-mints unconditionally every poll:\n%s", banned, monitor)
		}
	}
	// It must still exercise acquisition, so an authority that cannot mint is noticed.
	if !strings.Contains(monitor, "getToken();") {
		t.Fatalf("health monitor must still call getToken() to detect an unmintable "+
			"credential:\n%s", monitor)
	}

	// Invalidation must survive on the rejection path — that is the ONLY place
	// a token is dropped before expiry now.
	if !strings.Contains(im, "registerCredentialInvalidator(() -> {") ||
		!strings.Contains(im, "k8s.invalidate();") ||
		!strings.Contains(im, "cloud.invalidate();") {
		t.Fatal("IdentityManager must still wire the rejection-driven cache invalidator")
	}
}

// #66: startHealthMonitor() ran even with no minter configured, so getToken()
// threw "no token source configured" on every poll and System.exit(1) killed a
// perfectly healthy no-auth service after five minutes. rust returns no monitor
// task in the same case (manager.rs start_health_monitor).
func TestJavaIdentityManagerHealthMonitorSkippedWithoutMinter(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")

	start := strings.Index(im, "private void startHealthMonitor()")
	if start < 0 {
		t.Fatal("IdentityManager missing startHealthMonitor()")
	}
	threadIdx := strings.Index(im[start:], "healthMonitor = new Thread(")
	if threadIdx < 0 {
		t.Fatal("could not find the monitor thread construction")
	}
	preamble := im[start : start+threadIdx]

	if !strings.Contains(preamble, "if (k8sMinter == null && cloudMinter == null) {") ||
		!strings.Contains(preamble, "return;") {
		t.Fatalf("startHealthMonitor must return early when no minter is configured, "+
			"or a no-auth service exits(1) after RECONNECT_TIMEOUT_MS:\n%s", preamble)
	}
}

// The 5-minute System.exit(1) is NOT a java-only defect: rust's health monitor
// does exactly the same (manager.rs.tmpl, std::process::exit(1) after
// RECONNECT_TIMEOUT). Pin the parity so neither side drifts.
func TestIdentityManagerReconnectTimeoutParityWithRust(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	rs := readJavaTemplate(t, "rust/chassis/service_im/src/manager.rs.tmpl")

	for _, want := range []string{
		"RECONNECT_POLL_MS    = 30_000",
		"RECONNECT_TIMEOUT_MS = 5 * 60 * 1_000",
		"System.exit(1);",
	} {
		if !strings.Contains(im, want) {
			t.Fatalf("java IdentityManager lost reconnect parity marker %q", want)
		}
	}
	for _, want := range []string{
		"const RECONNECT_POLL: Duration = Duration::from_secs(30);",
		"const RECONNECT_TIMEOUT: Duration = Duration::from_secs(5 * 60);",
		"std::process::exit(1);",
	} {
		if !strings.Contains(rs, want) {
			t.Fatalf("rust manager lost reconnect parity marker %q — java mirrors it", want)
		}
	}
}

// #66: the subscriber retried UNAUTHENTICATED with backoff but never dropped
// the rejected credential, so every retry replayed the same dead token. rust's
// subscriber calls note_if_credential_rejection on that arm; java must too.
func TestJavaSubscriberInvalidatesRejectedCredential(t *testing.T) {
	sub := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")

	if !strings.Contains(sub, "{{$.IdentityManagerFQN}}.noteIfCredentialRejection(e, seen);") {
		t.Fatalf("Subscriber reconnect loop must call "+
			"IdentityManager.noteIfCredentialRejection(e, seen) so a rejected credential "+
			"is re-minted instead of replayed:\n%s", sub)
	}
	// It belongs on the retry path, before the sleep — not after it.
	noteIdx := strings.Index(sub, ".noteIfCredentialRejection(e, seen);")
	sleepIdx := strings.Index(sub, "Thread.sleep(backoff);")
	if sleepIdx < 0 || noteIdx > sleepIdx {
		t.Fatal("noteIfCredentialRejection must run before the backoff sleep")
	}

	// rust deliberately does NOT re-mint on PERMISSION_DENIED; the java
	// classifier enforces that, so the subscriber must delegate rather than
	// invalidate on the status code itself.
	if strings.Contains(sub, "noteCredentialRejection()") {
		t.Fatal("Subscriber must use noteIfCredentialRejection (which excludes " +
			"PERMISSION_DENIED), not the unconditional noteCredentialRejection")
	}
}

// #66: the TLS cert renewal stream is the only auth-carrying loop a service
// with no state watches has. Without a rejection note it never re-mints, and
// without an acceptance note it can only ever climb toward degraded. rust wires
// both in tls_cert_stream_loop; java must too.
func TestJavaTlsCertStreamReportsCredentialOutcome(t *testing.T) {
	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	if !strings.Contains(sm, "{{.ChassisBase}}.service_im.IdentityManager.noteIfCredentialRejection(e, seen);") {
		t.Fatal("tlsCertStreamLoop must drop a rejected credential before retrying")
	}
	if !strings.Contains(sm, "{{.ChassisBase}}.service_im.IdentityManager.noteCredentialAccepted();") {
		t.Fatal("a delivered TLS cert proves the credential was accepted and must " +
			"clear the degraded counter")
	}

	// The rejection note must be inside the retry loop, not the fatal-handler arm.
	loopIdx := strings.Index(sm, "private static void tlsCertStreamLoop(")
	if loopIdx < 0 {
		t.Fatal("ServiceManager missing tlsCertStreamLoop")
	}
	noteIdx := strings.Index(sm[loopIdx:], ".noteIfCredentialRejection(e, seen);")
	fatalIdx := strings.Index(sm[loopIdx:], "if (e instanceof FatalTlsHandlerException) {")
	if noteIdx < 0 || fatalIdx < 0 || noteIdx < fatalIdx {
		t.Fatal("noteIfCredentialRejection must sit after the FatalTlsHandlerException " +
			"arm, on the retry path only")
	}
}

// The degraded floor and the per-call invalidation primitives themselves —
// claims 1 and 2 of #66, already present at HEAD. Pin them so they stay wired.
func TestJavaCredentialDegradedFloorStaysWired(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	for _, want := range []string{
		"private static final int DEGRADED_AFTER = 3;",
		"public static boolean credentialsDegraded()",
		"public static boolean noteIfCredentialRejection(Throwable t, int seen)",
		"public static void noteCredentialAccepted()",
	} {
		if !strings.Contains(im, want) {
			t.Fatalf("IdentityManager lost credential-recovery primitive %q", want)
		}
	}
	// PERMISSION_DENIED must not classify as a rejection — re-minting the same
	// identity would loop forever against a real authorization refusal.
	if strings.Contains(im, "Code.PERMISSION_DENIED") {
		t.Fatal("isCredentialRejection must not treat PERMISSION_DENIED as a rejection")
	}

	sm := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	if !strings.Contains(sm, "setDegradedProbe({{.ChassisBase}}.service_im.IdentityManager::credentialsDegraded)") {
		t.Fatal("ServiceManager must wire credentialsDegraded as the readiness degraded probe")
	}
}
