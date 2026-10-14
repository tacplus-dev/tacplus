// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// loadRustLibTemplate reads a template from outside the rust/types subtree
// (e.g. rust/chassis/...) for pin tests on static lib templates.
func loadRustLibTemplate(t *testing.T, path string) string {
	t.Helper()
	content, err := fs.ReadFile(templates.FS, path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	return string(content)
}

// F-1 pin: the auth_any_user wrapper path is enforce_at_or_below →
// check_caller_at_or_below. That entry point must fully verify the token
// (signature via JWK kid lookup, expiry, token class, audience) before
// trusting the namespace claim — Token::parse alone only base64-decodes, so
// without verification a forged {"alg":"none"} token would reach the handler.
func TestRustAuthorizerCheckCallerAtOrBelowVerifiesToken(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/grpcserver/src/authorizer.rs.tmpl")

	// Every authorization entry point must route through the shared
	// verification gate before reading any claim.
	for _, entryPoint := range []string{
		"pub fn check_caller_at_or_below",
		"pub async fn authorize_grpc",
		"pub async fn authenticate_grpc",
	} {
		start := strings.Index(tmpl, entryPoint)
		if start < 0 {
			t.Fatalf("authorizer template missing %q", entryPoint)
		}
		body := tmpl[start:]
		if end := strings.Index(body[1:], "\n    pub "); end > 0 {
			body = body[:end+1]
		}
		if !strings.Contains(body, "self.verify_bearer(authorization)") {
			t.Fatalf("%s must call self.verify_bearer before trusting claims:\n%s", entryPoint, body)
		}
	}

	// check_caller_at_or_below must verify before reading the namespace claim.
	ccStart := strings.Index(tmpl, "pub fn check_caller_at_or_below")
	ccBody := tmpl[ccStart:]
	verifyIdx := strings.Index(ccBody, "self.verify_bearer(authorization)")
	namespaceIdx := strings.Index(ccBody, ".namespace()")
	if verifyIdx < 0 || namespaceIdx < 0 || verifyIdx > namespaceIdx {
		t.Fatalf("check_caller_at_or_below must verify the token before reading the namespace claim:\n%s", ccBody[:600])
	}
	// The unverified parse must be gone from check_caller_at_or_below.
	ccEnd := strings.Index(ccBody[1:], "\n    pub ")
	if ccEnd > 0 && strings.Contains(ccBody[:ccEnd+1], "token::Token::parse") {
		t.Fatalf("check_caller_at_or_below must not parse the token without verification:\n%s", ccBody[:ccEnd+1])
	}

	// The shared gate must perform real verification: kid-based JWK lookup,
	// signature/expiry check, token-class rejection, and audience check.
	vbStart := strings.Index(tmpl, "fn verify_bearer")
	if vbStart < 0 {
		t.Fatalf("authorizer template missing verify_bearer")
	}
	vbBody := tmpl[vbStart:]
	if end := strings.Index(vbBody[1:], "\n    pub "); end > 0 {
		vbBody = vbBody[:end+1]
	}
	for _, want := range []string{
		"self.jwk_cache.get(&kid)",
		"tw.verify(key)",
		"unknown signing key",
		"refresh tokens cannot be used for API access",
		`token_use == "bootstrap" || token_use == "locality" || token_use == "external"`,
		"!ACCEPTED_AUDIENCES.contains(&aud)",
	} {
		if !strings.Contains(vbBody, want) {
			t.Fatalf("verify_bearer missing verification step %q:\n%s", want, vbBody)
		}
	}
}

func TestRustAuthorizerNormalizesGeneratedGrantTokens(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/grpcserver/src/authorizer.rs.tmpl")
	for _, want := range []string{
		"normalize as normalize_grants, unknown_tokens as unknown_grant_tokens",
		"unknown_grant_tokens(&raw)",
		"dropping unknown service_entitlements grant(s); token grants remain fail-closed",
		"normalize_grants(&raw).into_iter().any(|token| token == required)",
		"fn method_grant_token(fallback_service: &str, full_method: &str, fallback_method: &str) -> String",
		"let service_fqn = service_fqn.rsplit('/').next().unwrap_or(service_fqn);",
		"fn grant_token_part(value: &str) -> String",
		`let normalized = value.replace(['-', '.', '/'], "_");`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Rust Authorizer must authorize via generated grant normalization; missing %q", want)
		}
	}
}

func TestRustCollectionWatchHubStopsDrainOnRingClose(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/collection/src/lib.rs.tmpl")

	for _, want := range []string{
		"async fn is_closed(&self) -> bool",
		"if drain_ring.is_closed().await",
		"return;",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Rust WatchHub drain must stop when the last watcher closes the ring; missing %q", want)
		}
	}
}

func TestRustTagsRuntimeTemplateMatchesKubernetesSemantics(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/tags/src/lib.rs.tmpl")
	for _, want := range []string{
		"pub fn contains_all(have: Option<&TagSet>, want: Option<&TagSet>) -> bool",
		"pub fn matches(selector: Option<&TagSelector>, tags: Option<&TagSet>) -> bool",
		"const OP_IN: i32 = 1;",
		"const OP_NOT_IN: i32 = 2;",
		"OP_NOT_IN => match value",
		"const OP_EXISTS: i32 = 3;",
		"const OP_DOES_NOT_EXIST: i32 = 4;",
		"pub fn validate_key(key: &str) -> Result<(), String>",
		"pub fn validate_selector(selector: &TagSelector) -> Result<(), String>",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Rust tags runtime template missing %q", want)
		}
	}
}

func TestRustHealthReporterWrapsCheckerAndClassifiesFirstPublish(t *testing.T) {
	tmpl := loadRustLibTemplate(t, "rust/chassis/healthreporter/src/lib.rs.tmpl")

	for _, want := range []string{
		"oneshot::Sender<Result<(), String>>",
		"new_with_ready_and_status",
		"initial_status: InstanceStatus",
		"status: initial_status",
		"permanent first health publish failure",
		"std::panic::catch_unwind",
		"InstanceStatus::Unhealthy",
		"tonic::Code::PermissionDenied",
		"MAX_FIRST_PUBLISH_ATTEMPTS",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Rust HealthReporter must wrap/check/classify health publishing; missing %q", want)
		}
	}

	managerTmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}
	for _, want := range []string{
		"HealthReporter::new_with_ready_and_status",
		"InstanceStatus::Healthy",
	} {
		if !strings.Contains(managerTmpl, want) {
			t.Fatalf("Rust ServiceManager must initialize health before first publish; missing %q", want)
		}
	}
	if strings.Contains(managerTmpl, "health_reporter.set_status(InstanceStatus::Healthy);") {
		t.Fatalf("Rust ServiceManager must not race first publish by setting healthy after reporter start")
	}
}

// auth_any_user means "any authenticated user can call this RPC". The optional
// target_namespace_field on such a method is an authored FIXED namespace
// literal, not a request field name; with none authored there is nothing to
// compare against, so the gate is authentication alone. The former "/" floor
// gated on is_at_or_below(caller, "/"), true only for a caller sitting exactly
// at "/" — every real caller denied.
func TestRustAuthAnyUserWrapperGate(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		targetNamespaceField string
		wantCall             string
		notWant              string
	}{
		{
			name:     "no target namespace field authenticates only",
			wantCall: "self.enforce_authenticated(&request).await?;",
			notWant:  "self.enforce_at_or_below(",
		},
		{
			name:                 "declared literal keeps the fixed namespace gate",
			targetNamespaceField: "/tenants/acme",
			wantCall:             `self.enforce_at_or_below(&request, "/tenants/acme").await?;`,
			notWant:              "self.enforce_authenticated(",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := mustGenerateRustGRPCWrapper(
				t,
				&parser.ServiceInfo{Name: "ContractApi"},
				&parser.MethodInfo{
					Name:                 "PublicRead",
					InputType:            "ContractRequest",
					OutputType:           "ContractResponse",
					FullInputType:        "contractclient.ContractRequest",
					FullOutputType:       "contractclient.ContractResponse",
					AuthAnyUser:          true,
					TargetNamespaceField: tc.targetNamespaceField,
				},
				"contractclient",
				"contract",
				map[string]string{},
				"contract",
				map[string]service.AccountingInfo{},
				proto.SkippedTraceTypes{},
				nil,
			)

			if !strings.Contains(rendered, tc.wantCall) {
				t.Fatalf("auth_any_user wrapper missing %q:\n%s", tc.wantCall, rendered)
			}
			if strings.Contains(rendered, tc.notWant) {
				t.Fatalf("auth_any_user wrapper must not contain %q:\n%s", tc.notWant, rendered)
			}
		})
	}
}

// F-1 pin: dropping the namespace check for a bare auth_any_user method must not
// weaken TOKEN verification. The authenticate path has to reach the same
// verify_bearer gate (kid lookup, signature, expiry, token class, audience) that
// check_caller_at_or_below uses — otherwise a forged token would reach the handler.
func TestRustAuthAnyUserAuthenticatePathVerifiesToken(t *testing.T) {
	tmpl, err := loadTemplate("servicewrapper.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicewrapper.rs.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "a.authenticate(authorization).await") {
		t.Fatalf("enforce_authenticated must delegate to the Authorizer trait's authenticate:\n%s", tmpl)
	}

	managerTmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}
	if !strings.Contains(managerTmpl, ".authenticate_caller(authorization)") {
		t.Fatalf("Rust service manager adapter must route auth_any_user authentication through authenticate_caller")
	}

	authorizerTmpl := loadRustLibTemplate(t, "rust/chassis/grpcserver/src/authorizer.rs.tmpl")
	acStart := strings.Index(authorizerTmpl, "pub fn authenticate_caller")
	if acStart < 0 {
		t.Fatalf("authorizer template missing authenticate_caller")
	}
	acBody := authorizerTmpl[acStart:]
	if end := strings.Index(acBody[1:], "\n    pub "); end > 0 {
		acBody = acBody[:end+1]
	}
	if !strings.Contains(acBody, "self.verify_bearer(authorization)") {
		t.Fatalf("authenticate_caller must verify the bearer token:\n%s", acBody)
	}
}

// F-2 pin (TCG-001 contract): noop skips the handler but NOT auth. The auth
// call must run unconditionally before the trace/noop early return — never
// inside an `if !is_noop` guard — matching the Go generator's ordering.
func TestRustNoopTraceDoesNotSkipAuth(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   *parser.MethodInfo
		authCall string
	}{
		{
			name: "entitled",
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
			},
			authCall: "self.authorize_method(",
		},
		{
			name: "auth_any_user",
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
				AuthAnyUser:    true,
			},
			authCall: "self.enforce_authenticated(",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := mustGenerateRustGRPCWrapper(
				t,
				&parser.ServiceInfo{Name: "ContractApi"},
				tc.method,
				"contractclient",
				"contract",
				map[string]string{},
				"contract",
				map[string]service.AccountingInfo{},
				proto.SkippedTraceTypes{},
				nil,
			)

			if strings.Contains(rendered, "if !is_noop") {
				t.Fatalf("auth must not be guarded by !is_noop (noop skips the handler, not auth):\n%s", rendered)
			}
			authIdx := strings.Index(rendered, tc.authCall)
			noopDecl := strings.Index(rendered, "let is_noop")
			noopReturn := strings.Index(rendered, "if is_noop {")
			if authIdx < 0 || noopDecl < 0 || noopReturn < 0 {
				t.Fatalf("rendered wrapper missing auth call %q or trace/noop blocks:\n%s", tc.authCall, rendered)
			}
			if !(noopDecl < authIdx && authIdx < noopReturn) {
				t.Fatalf("auth must run after the noop flags are computed and before the noop early return:\n%s", rendered)
			}
		})
	}
}

func TestRustNoopTraceDoesNotSkipRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method *parser.MethodInfo
		svc    *parser.ServiceInfo
	}{
		{
			name: "protected",
			svc:  &parser.ServiceInfo{Name: "ContractApi"},
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
			},
		},
		{
			name: "auth_any_user",
			svc:  &parser.ServiceInfo{Name: "ContractApi"},
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
				AuthAnyUser:    true,
			},
		},
		{
			name: "auth_disabled",
			svc:  &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true},
			method: &parser.MethodInfo{
				Name:           "PublicRead",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
				AuthDisabled:   true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := mustGenerateRustGRPCWrapper(
				t,
				tc.svc,
				tc.method,
				"contractclient",
				"contract",
				map[string]string{},
				"contract",
				map[string]service.AccountingInfo{},
				proto.SkippedTraceTypes{},
				nil,
			)

			validation := strings.Index(rendered, "req.validate()")
			noop := strings.Index(rendered, "if is_noop {")
			if validation < 0 || noop < 0 {
				t.Fatalf("Rust trace wrapper missing validation or noop branch:\n%s", rendered)
			}
			if validation > noop {
				t.Fatalf("Rust trace noop must not bypass request validation:\n%s", rendered)
			}
		})
	}
}

// F-2 pin (PAGE): the PAGE wrapper skips the unary trace-v2 dance entirely, so
// it must emit the plain auth line — always executed — and no noop machinery.
func TestRustPageWrapperAlwaysRunsAuth(t *testing.T) {
	m := rustPageMethod()
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)

	if !strings.Contains(rendered, "self.authorize_method(") {
		t.Fatalf("PAGE wrapper missing unconditional authorize_method call:\n%s", rendered)
	}
	if strings.Contains(rendered, "is_noop") || strings.Contains(rendered, "is_tracing") {
		t.Fatalf("PAGE wrapper must not emit unused trace/noop flags:\n%s", rendered)
	}
}
