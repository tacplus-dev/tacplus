// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// NEW-21 leg (a), java side. The classifier is a private method of the
// per-service generated ServiceManager, and the java self-test harness only runs
// chassis-level test classes (cross-test/current-generation/run.sh:259), so
// there is nowhere to execute it the way the go leg does. Pin the shape instead:
// the trust-domain branch must be TYPED (the substring seam is what NEW-21
// removed) and must walk the cause chain, since the exception arrives wrapped.
func TestJavaServiceManagerClassifiesMissingTrustDomainByType(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"for (Throwable cur = t; cur != null; cur = cur.getCause()) {",
		"if (cur instanceof ServiceConnection.MissingTrustDomainException) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java ServiceManager must classify a missing trust domain by type, walking the cause chain; missing %q", want)
		}
	}
	if strings.Contains(rendered, `msg.contains("spiffe_trust_domain`) {
		t.Fatal("Java ServiceManager must not classify a missing trust domain by message substring (NEW-21)")
	}

	typedIdx := strings.Index(rendered, "if (cur instanceof ServiceConnection.MissingTrustDomainException) {")
	minterIdx := strings.Index(rendered, `msg.contains("service token minter is not configured")`)
	if minterIdx < 0 {
		t.Fatal("Java ServiceManager must still treat a missing token minter as permanent")
	}
	if typedIdx > minterIdx {
		t.Fatal("the typed trust-domain check must precede the message-substring fallback, so the " +
			"typed seam is what classifies it")
	}
}

// NEW-21 leg (c): ServiceConnection.channelFactory(cfg) already threw
// MissingTrustDomainException before this ServiceManager existed, so the
// empty-trust-domain branch here was dead — and it was the fail-OPEN shape,
// publishing an empty instance identity into the fleet health plane instead of
// failing. Re-adding it compiles and generates fine, so pin its absence.
func TestJavaServiceManagerHasNoEmptyTrustDomainTolerance(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	if !strings.Contains(rendered, "String spiffeId = Spiffe.serviceSpiffeId(") {
		t.Fatal("Java health identity must be built unconditionally from the configured trust domain")
	}
	if strings.Contains(rendered, "if (!config.getSpiffeTrustDomain().isEmpty()) {") {
		t.Fatal("Java health identity must not tolerate an empty trust domain: publishing an empty " +
			"instance identity instead of failing is the fail-open shape (NEW-21)")
	}
}
