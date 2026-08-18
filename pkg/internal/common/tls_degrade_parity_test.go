// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
)

// The TLS give-up degrade reason is operator-facing health-plane text: it is
// what a human reads when an instance goes DEGRADED. go, rust and java must
// therefore emit the SAME sentence, so one runbook covers a fleet that mixes
// languages.
//
// This has already drifted once. A rust-side change to the give-up path
// rewrote the reason to "TLS cert stream failed before first delivery after N
// attempts", leaving go and java on "TLS renewal stream gave up before first
// delivery" — three per-language pins each stayed green because each only ever
// looked at its own template. Nothing compared them. This does.
func TestTLSGiveUpDegradeReasonIsIdenticalAcrossLanguages(t *testing.T) {
	const phrase = "TLS renewal stream gave up before first delivery: "

	for _, tc := range []struct {
		lang string
		dir  string
		file string
	}{
		{"go", "go/types", "service_manager.go.tmpl"},
		{"rust", "rust/types", "service_manager.rs.tmpl"},
		{"java", "java/types", "ServiceManager.java.tmpl"},
	} {
		s, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("%s: load %s: %v", tc.lang, tc.file, err)
		}
		if !strings.Contains(s, phrase) {
			t.Errorf("%s SM template does not report the shared TLS give-up degrade reason %q; the three languages must not drift apart on operator-facing health text", tc.lang, phrase)
		}
	}
}

// PC-W-8 doc leg: docs/API.md had TWO orderings of the wrapper pipeline. The
// authoritative numbered list gets it right — auth precedes validation, and
// says why (an unauthenticated caller must not be able to drive the validation
// path or distinguish outcomes from validation errors). The one-line summary
// table above it listed "inbound validation, authentication, authorization",
// which is the opposite and is the line a reader meets first.
//
// The code does auth first. The summary must say so too.
func TestApiDocSummaryPutsAuthBeforeValidation(t *testing.T) {
	data, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatalf("read API.md: %v", err)
	}
	s := string(data)

	const row = "| Service wrappers | Enforce "
	i := strings.Index(s, row)
	if i < 0 {
		t.Fatal("service-wrappers summary row not found in docs/API.md")
	}
	line := s[i:]
	if j := strings.Index(line, "\n"); j >= 0 {
		line = line[:j]
	}

	auth := strings.Index(line, "authentication")
	val := strings.Index(line, "inbound validation")
	if auth < 0 || val < 0 {
		t.Fatalf("summary row no longer names both stages: %s", line)
	}
	if auth > val {
		t.Errorf("docs/API.md summary row lists inbound validation before authentication, contradicting the numbered pipeline (auth precedes validation) and the generated wrappers:\n%s", line)
	}
}
