// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NEW-21 leg (a): the startup error classifier is executed by a GENERATED
// self-test emitted next to s2sm.go (isPermanentIdentityInitError is private to
// the generated sm package, so nothing outside it can call it). Pin the emitter
// itself: the render must succeed, parse as Go (generateServiceManagerIdentityErrorTest
// runs format.Source), and drive both classifier branches.
func TestGeneratedServiceManagerIdentityErrorSelfTestIsEmitted(t *testing.T) {
	dir := t.TempDir()
	data := struct{ ModuleBasePath string }{ModuleBasePath: "example.com/acme/model"}
	if err := generateServiceManagerIdentityErrorTest(data, dir); err != nil {
		t.Fatalf("generateServiceManagerIdentityErrorTest: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "s2sm_identity_error_test.go"))
	if err != nil {
		t.Fatalf("generated identity-error self-test must be written next to s2sm.go: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"package sm",
		`"example.com/acme/model/chassis/servicemanager"`,
		"isPermanentIdentityInitError(servicemanager.ErrMissingTrustDomain)",
		"isPermanentIdentityInitError(service_im.ErrMissingTrustDomain)",
		`"example.com/acme/model/chassis/service_im"`,
		`fmt.Errorf("service mesh client: %w", servicemanager.ErrMissingTrustDomain)`,
		"errors.New(servicemanager.ErrMissingTrustDomain.Error())",
		"isPermanentIdentityInitError(service_im.ErrTokenMinterNotConfigured)",
		"errors.New(service_im.ErrTokenMinterNotConfigured.Error())",
		`errors.New("connection refused")`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated identity-error self-test must exercise the startup classifier; missing %q in:\n%s", want, rendered)
		}
	}
}

// NEW-21 leg (a): the trust-domain half of the classifier must stay TYPED. A
// substring match on the trust-domain message is the seam NEW-21 removed.
func TestGoServiceManagerClassifiesMissingTrustDomainByTypedSentinel(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "if errors.Is(err, service_im.ErrMissingTrustDomain) || errors.Is(err, servicemanager.ErrMissingTrustDomain) {") {
		t.Fatal("Go ServiceManager must classify a missing trust domain by typed sentinel (errors.Is) " +
			"on BOTH init steps — identity init fails first, so classifying only the servicemanager " +
			"sentinel retries the real-world case for ~13 minutes (NEW-21)")
	}
	if strings.Contains(tmpl, `strings.Contains(err.Error(), "spiffe_trust_domain`) {
		t.Fatal("Go ServiceManager must not classify a missing trust domain by message substring (NEW-21)")
	}
}

// NEW-21 leg (b): the missing-minter half of the classifier is typed too. It was
// the last message-substring seam in the classifier — the chassis message is not
// a contract, and rewording it silently turned a permanent misconfiguration back
// into ~13 minutes of retries.
func TestGoServiceManagerClassifiesMissingTokenMinterByTypedSentinel(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "return errors.Is(err, service_im.ErrTokenMinterNotConfigured)") {
		t.Fatal("Go ServiceManager must classify a missing token minter by typed sentinel (NEW-21 leg b)")
	}
	if strings.Contains(tmpl, "strings.Contains(err.Error()") {
		t.Fatal("Go ServiceManager must not classify a permanent identity-init failure by message " +
			"substring: the chassis message is not a contract (NEW-21)")
	}
}

// NEW-21 leg (c): the empty-trust-domain tolerances are dead — NewServiceManager
// refuses an empty spiffe_trust_domain at construction — and each one was a
// fail-OPEN shape (publish an empty health identity / mint a cert with no SPIFFE
// URI SAN) rather than a failure. Re-adding one compiles and generates fine, so
// pin their absence.
func TestGoServiceManagerHasNoEmptyTrustDomainTolerance(t *testing.T) {
	sm, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	if !strings.Contains(sm, `spiffeId, idErr := wrapperSpiffe.ServiceSpiffeID(sm.cfg.GetSpiffeTrustDomain(), sm.cfg.GetLocationPath(), "{{.ServiceLabel}}")`) {
		t.Fatal("Go health identity must be built unconditionally from the configured trust domain")
	}
	if strings.Contains(sm, `if td := sm.cfg.GetSpiffeTrustDomain(); td != ""`) {
		t.Fatal("Go health identity must not tolerate an empty trust domain: publishing an empty " +
			"instance identity instead of failing is the fail-open shape (NEW-21)")
	}

	lib, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	if !strings.Contains(lib, "uriSans := []string{spiffeId}") {
		t.Fatal("Go X509Mint must always carry a SPIFFE URI SAN")
	}
	if strings.Contains(lib, `if td := bc.config.GetSpiffeTrustDomain(); td != ""`) {
		t.Fatal("Go X509Mint must not tolerate an empty trust domain: minting a cert with no " +
			"SPIFFE URI SAN instead of failing is the fail-open shape (NEW-21)")
	}
}

// The identity leg of the same contract: service_im.New is the FIRST init step,
// so a trust-domain-less deployment fails there. It must return a typed sentinel
// for the ServiceManager to classify — an fmt.Errorf string is invisible to
// errors.Is and sends a permanent misconfiguration around the retry loop.
func TestGoServiceIMReturnsTypedMissingTrustDomain(t *testing.T) {
	tmpl, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate service_im/service_im.go.tmpl: %v", err)
	}
	for _, want := range []string{
		`var ErrMissingTrustDomain = errors.New("im: spiffe_trust_domain is required for bootstrap TLS`,
		"return nil, ErrMissingTrustDomain",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("service_im must fail a missing trust domain with a typed sentinel; missing %q", want)
		}
	}
	if strings.Contains(tmpl, `return nil, fmt.Errorf("im: spiffe_trust_domain is required`) {
		t.Fatal("service_im must not return the missing-trust-domain error as an untyped fmt.Errorf: " +
			"errors.Is cannot see it and the ServiceManager retries a permanent misconfiguration (NEW-21)")
	}
}
