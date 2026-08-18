// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// The chassis/servicemanager and chassis/service_im packages each have TWO
// generated variants: the token-exchanger one (templates) and the no-exchanger
// one (the inline sources in this file's generators). isPermanentIdentityInitError
// is emitted UNCONDITIONALLY into every generated sm package, so every sentinel it
// names must exist in BOTH variants — otherwise a no-exchanger build does not
// compile.
//
// The reachable shape is a build whose entire server surface is non-S2S (a
// C2S-only front, for instance): serverServices is non-empty, so sm/ and the two
// chassis packages are all generated (generator.go:446-455,489), but
// s2sGenerationEnabled is false, so ValidateTokenMinters does not demand a token
// minter and DiscoverTokenExchangerPackage returns nil — the no-exchanger
// variants. (NOT a c2d-only daemon: c2d services are filtered out of
// serverServices at generator.go:1198, so such a build emits c2dm/ and no sm/ at
// all.)
//
// This was live: the NEW-21 classifier referenced servicemanager.ErrMissingTrustDomain,
// which the no-exchanger variant never declared. Nothing caught it — no gate
// generates a no-exchanger build, and both variants pass `go vet` on their own.
//
// Scoped to the classifier on purpose: the rest of the template's chassis
// references sit under {{if .HasAutoTls}} / {{if .HasServiceHealthState}}, which
// no-exchanger builds cannot reach (both features need an S2S client, which makes
// ValidateTokenMinters demand a minter), so sweeping the whole file would report
// unreachable combinations as defects.
func TestGoChassisVariantsExposeEveryIdentityClassifierSentinel(t *testing.T) {
	smTmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	classifier := classifierBody(t, smTmpl)

	dir := t.TempDir()
	config := &tactype.Config{ModuleBasePath: "example.com/consumer/model", OutputDir: dir}
	if err := generateStateClientLibWithoutTokenExchanger(config); err != nil {
		t.Fatalf("generate no-exchanger servicemanager: %v", err)
	}
	if err := generateIdentityManagerWithoutTokenExchanger(config); err != nil {
		t.Fatalf("generate no-exchanger service_im: %v", err)
	}

	exchangerServiceIM, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate service_im: %v", err)
	}
	exchangerServiceManager, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}

	for _, pkg := range []struct {
		alias     string
		exchanger string
		noMinter  string
	}{
		{"servicemanager", exchangerServiceManager, readFile(t, filepath.Join(dir, "chassis", "servicemanager", "servicemanager.go"))},
		{"service_im", exchangerServiceIM, readFile(t, filepath.Join(dir, "chassis", "service_im", "service_im.go"))},
	} {
		for _, symbol := range exportedSymbolsUsed(classifier, pkg.alias) {
			decl := regexp.MustCompile(`(?m)^(var|func|type|const)\s+` + symbol + `\b`)
			if !decl.MatchString(pkg.exchanger) {
				t.Errorf("isPermanentIdentityInitError uses %s.%s, but the token-exchanger variant of "+
					"chassis/%s does not declare it", pkg.alias, symbol, pkg.alias)
			}
			if !decl.MatchString(pkg.noMinter) {
				t.Errorf("isPermanentIdentityInitError uses %s.%s, but the NO-token-exchanger variant of "+
					"chassis/%s does not declare it: a build with no S2S surface (so no authoritative "+
					"token minter is required) does not compile", pkg.alias, symbol, pkg.alias)
			}
		}
	}
}

// classifierBody returns the source of isPermanentIdentityInitError, the one
// function whose chassis references are emitted into every build.
func classifierBody(t *testing.T, tmpl string) string {
	t.Helper()
	const signature = "func isPermanentIdentityInitError(err error) bool {"
	start := strings.Index(tmpl, signature)
	if start < 0 {
		t.Fatalf("service_manager.go.tmpl no longer declares %q", signature)
	}
	end := strings.Index(tmpl[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of isPermanentIdentityInitError")
	}
	return tmpl[start : start+end]
}

// exportedSymbolsUsed returns the exported identifiers the source references
// through the given package alias, e.g. service_im.ErrMissingTrustDomain.
// Method calls on values (sm.identity.GetToken) are not package references and
// do not match, because the alias must be preceded by a non-selector character.
func exportedSymbolsUsed(source, alias string) []string {
	re := regexp.MustCompile(`(^|[^\w.])` + alias + `\.([A-Z]\w*)`)
	seen := map[string]bool{}
	var symbols []string
	for _, m := range re.FindAllStringSubmatch(source, -1) {
		if seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		symbols = append(symbols, m[2])
	}
	return symbols
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}
