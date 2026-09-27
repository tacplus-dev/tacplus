// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"go/format"
	"strings"
	"testing"
	"text/template"
)

// H1 / SEC-1: the service_im bootstrap connection carries the workload's
// projected identity token to the token authority, so it must pin the peer's
// SPIFFE trust domain — never accept any CA-chain-valid cert (CA-only trust is
// fail-open: any CA-issued cert could impersonate the authority and harvest the
// token). This guards against a regression back to CA-only bootstrap TLS.
func TestServiceIMBootstrapPinsTrustDomain(t *testing.T) {
	rendered := renderServiceIMForTest(t)

	for _, expected := range []string{
		// Fail-closed: refuse bootstrap TLS with no trust domain to pin.
		"trustDomain := cfg.GetSpiffeTrustDomain()",
		"spiffe_trust_domain is required for bootstrap TLS",
		// Chain validation is still performed...
		"cs.PeerCertificates[0].Verify(opts)",
		// ...AND the peer's SPIFFE trust domain is pinned after it.
		"wrapperX509.VerifyPeerInTrustDomain(cs.PeerCertificates[0], trustDomain)",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("service_im bootstrap TLS missing trust-domain pin %q:\n%s", expected, rendered)
		}
	}
}

func renderServiceIMForTest(t *testing.T) string {
	t.Helper()
	tmplStr, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate: %v", err)
	}
	tmpl, err := template.New("service_im").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_im template: %v", err)
	}
	data := map[string]any{
		"GrpcconfigTypesPath":        "example.com/p/tacplus/types/sys/grpcconfig",
		"StateServiceTypesPath":      "example.com/p/tacplus/types/services/kernel",
		"CommonTypesPath":            "example.com/p/tacplus/types/sys/tokenrequest",
		"CloudIdentityTypesPath":     "example.com/p/tacplus/types/sys/cloudidentity",
		"CloudVendorTypesPath":       "example.com/p/tacplus/types/sys/cloudvendor",
		"KeyTypesPath":               "example.com/p/tacplus/types/sys/key",
		"KeyLibPath":                 "example.com/p/tacplus/chassis/key",
		"TokenLibPath":               "example.com/p/tacplus/chassis/token",
		"TokenTypesPath":             "example.com/p/tacplus/types/sys/token",
		"X509LibPath":                "example.com/p/tacplus/chassis/x509",
		"ServiceAudience":            "demo",
		"MintServiceName":            "Kernel",
		"AudienceMappings":           []any{},
		"KeepaliveTimeSecs":          30,
		"KeepaliveTimeoutSecs":       10,
		"KeepalivePermitMinTimeSecs": 10,
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_im template: %v", err)
	}
	// Format to ensure the rendered chassis is syntactically valid Go — a
	// broken edit to the template surfaces here rather than in a downstream build.
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		t.Fatalf("rendered service_im.go is not valid Go: %v\n%s", err, out.String())
	}
	return string(formatted)
}

// TestServiceIMInterceptorsRespectExplicitAuthorization guards against the
// aud=kernel EventBus-publish bug (2026-09-27): a proto package can host RPCs
// with different physical audiences (e.g. a record's own state-store RPCs,
// mapped to kernel, alongside that same package's EventBus-publish virtual
// service, hosted by eventbus) — audienceFromMethod's per-package heuristic
// cannot distinguish them. Generated call sites that know their real target
// audience (EventBus/trace/accounting publishers, the ServiceHealthState
// query) attach their own bearer token before dialing; the blanket
// Unary/StreamInterceptor must never mint and append a second, heuristic
// token on top of one already present, or the wrong one can be the one a
// downstream service honors.
func TestServiceIMInterceptorsRespectExplicitAuthorization(t *testing.T) {
	rendered := renderServiceIMForTest(t)

	if !strings.Contains(rendered, "func hasOutgoingAuthorization(ctx context.Context) bool") {
		t.Fatalf("service_im missing hasOutgoingAuthorization helper:\n%s", rendered)
	}

	for _, fn := range []string{"UnaryInterceptor", "StreamInterceptor"} {
		start := strings.Index(rendered, "func (im *IdentityManager) "+fn+"(")
		if start < 0 {
			t.Fatalf("service_im missing %s", fn)
		}
		end := strings.Index(rendered[start:], "\n}\n")
		if end < 0 {
			t.Fatalf("could not find end of %s body", fn)
		}
		body := rendered[start : start+end]
		if !strings.Contains(body, "hasOutgoingAuthorization(ctx)") {
			t.Fatalf("%s does not check hasOutgoingAuthorization before minting:\n%s", fn, body)
		}
	}
}

// TestServiceIMAudienceMappingsForStatePackages verifies that when
// AudienceMappings include state-package → state-store entries, the rendered
// audienceFromMethod switch contains the correct case clauses mapping
// cross-service state proto packages to the state store's audience.
// This prevents the bug where a state watch (e.g. WatchConfig owned by "oauth")
// would mint a token for "oauth" instead of "kernel", causing UNAUTHENTICATED.
func TestServiceIMAudienceMappingsForStatePackages(t *testing.T) {
	tmplStr, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate: %v", err)
	}
	tmpl, err := template.New("service_im").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_im template: %v", err)
	}
	data := map[string]any{
		"GrpcconfigTypesPath":        "example.com/p/tacplus/types/sys/grpcconfig",
		"StateServiceTypesPath":      "example.com/p/tacplus/types/services/kernel",
		"CommonTypesPath":            "example.com/p/tacplus/types/sys/tokenrequest",
		"CloudIdentityTypesPath":     "example.com/p/tacplus/types/sys/cloudidentity",
		"CloudVendorTypesPath":       "example.com/p/tacplus/types/sys/cloudvendor",
		"KeyTypesPath":               "example.com/p/tacplus/types/sys/key",
		"KeyLibPath":                 "example.com/p/tacplus/chassis/key",
		"TokenLibPath":               "example.com/p/tacplus/chassis/token",
		"TokenTypesPath":             "example.com/p/tacplus/types/sys/token",
		"X509LibPath":                "example.com/p/tacplus/chassis/x509",
		"ServiceAudience":            "eventbus",
		"MintServiceName":            "Kernel",
		"KeepaliveTimeSecs":          30,
		"KeepaliveTimeoutSecs":       10,
		"KeepalivePermitMinTimeSecs": 10,
		"AudienceMappings": []map[string]string{
			{"Package": "oauth", "Audience": "kernel"},
			{"Package": "msggateway", "Audience": "kernel"},
			{"Package": "opa", "Audience": "kernel"},
		},
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_im template: %v", err)
	}
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		t.Fatalf("rendered service_im.go is not valid Go: %v\n%s", err, out.String())
	}
	rendered := string(formatted)

	// Each state-package mapping must appear as a case in audienceFromMethod.
	for _, tc := range []struct{ pkg, aud string }{
		{"oauth", "kernel"},
		{"msggateway", "kernel"},
		{"opa", "kernel"},
	} {
		caseClause := `case "` + tc.pkg + `":`
		returnClause := `return "` + tc.aud + `"`
		if !strings.Contains(rendered, caseClause) {
			t.Errorf("audienceFromMethod missing case for state package %q:\n%s", tc.pkg, rendered)
		}
		if !strings.Contains(rendered, returnClause) {
			t.Errorf("audienceFromMethod missing return %q for state package %q", tc.aud, tc.pkg)
		}
	}
}

// TestServiceManagerLibRendersValidGo syntax-checks the rendered
// servicemanager_lib.go — including the per-service ServiceDialOpts / spiffeTLSDialOpt
// helpers (H1 mesh-pinning parity) — so a broken template edit fails here rather
// than in a downstream service build.
func TestServiceManagerLibRendersValidGo(t *testing.T) {
	tmplStr, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("smlib").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse servicemanager_lib template: %v", err)
	}
	data := map[string]any{
		"CommonTypesPath":            "ex/types/sys/tokenrequest",
		"GrpcconfigTypesPath":        "ex/types/sys/grpcconfig",
		"HostTypesPath":              "ex/types/sys/host",
		"ImLibPath":                  "ex/chassis/service_im",
		"JwkLibPath":                 "ex/chassis/jwk",
		"JwkTypesPath":               "ex/types/sys/jwk",
		"KeyTypesPath":               "ex/types/sys/key",
		"LoggerTypesPath":            "ex/types/sys/logger",
		"MintServiceName":            "Kernel",
		"ServiceAudience":            "demo",
		"SpiffeLibPath":              "ex/chassis/spiffe",
		"StateServiceTypesPath":      "ex/types/services/kernel",
		"TokenLibPath":               "ex/chassis/token",
		"TraceTypesPath":             "ex/types/sys/trace",
		"X509LibPath":                "ex/chassis/x509",
		"X509MintMethod":             "X509Mint",
		"X509TypesPath":              "ex/types/sys/x509",
		"KeepaliveTimeSecs":          30,
		"KeepaliveTimeoutSecs":       10,
		"KeepalivePermitMinTimeSecs": 10,
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute servicemanager_lib template: %v", err)
	}
	if _, err := format.Source([]byte(out.String())); err != nil {
		t.Fatalf("rendered servicemanager_lib.go is not valid Go: %v", err)
	}
}
