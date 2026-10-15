// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// NEW-21 leg (a), java side. The generated ServiceManagerIdentityErrorTest
// executes the classifier in every service fixture current-generation builds;
// this pins the shape as well: the trust-domain branch must be TYPED (the
// substring seam is what NEW-21 removed) and must walk the cause chain, since
// the exception arrives wrapped.
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

	// The missing-minter arm is the second permanent class, and it is typed too
	// (NEW-21 leg b): no message-substring classification survives here.
	if !strings.Contains(rendered, "if (cur instanceof IdentityManager.TokenMinterNotConfiguredException) {") {
		t.Fatal("Java ServiceManager must still treat a missing token minter as permanent, by type")
	}
	if strings.Contains(rendered, "msg.contains(") || strings.Contains(rendered, "cur.getMessage()") {
		t.Fatal("Java ServiceManager must not classify a permanent identity-init failure by message " +
			"substring: the message is not a contract and drifts silently (NEW-21)")
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

// NEW-21 leg (b), java parity. The missing-minter arm was the classifier's last
// message-substring seam; it now matches the typed exception the identity chassis
// throws, and every no-exchanger throw site raises that type.
func TestJavaIdentityChassisThrowsTypedMissingMinter(t *testing.T) {
	im := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	for _, want := range []string{
		"public static final class TokenMinterNotConfiguredException extends IllegalStateException {",
		`throw new TokenMinterNotConfiguredException("IM: service token minter is not configured");`,
	} {
		if !strings.Contains(im, want) {
			t.Fatalf("java IdentityManager must raise a TYPED missing-minter failure; missing %q", want)
		}
	}

	for _, file := range []string{
		"java/chassis/service_im/CloudMinter.java.tmpl",
		"java/chassis/service_im/KubernetesMinter.java.tmpl",
	} {
		src := readJavaTemplate(t, file)
		if !strings.Contains(src, `throw new IdentityManager.TokenMinterNotConfiguredException("service token minter is not configured");`) {
			t.Fatalf("%s must raise the typed missing-minter failure (NEW-21 leg b)", file)
		}
		if strings.Contains(src, `throw new IllegalStateException("service token minter is not configured");`) {
			t.Fatalf("%s must not raise the untyped missing-minter failure: the ServiceManager "+
				"classifies it by type, so an IllegalStateException is retried for ~13 minutes", file)
		}
	}
}
