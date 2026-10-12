// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// The gateway requires aud == route service, but FetchTransport called
// tokenProvider() with no argument although every generated client passes
// opts.service, so a browser app calling two authenticated services needed one
// ServiceManager per service. Parity with Go's TokenProviderFor(service): the
// provider receives the target service name.
func TestTypeScriptW2STokenProviderReceivesService(t *testing.T) {
	tmpl, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		// Optional parameter: a zero-argument provider still type-checks.
		"export type TokenProvider = (service?: string) => string | undefined | Promise<string | undefined>;",
		"const token = await this.tokenProvider(service);",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sm.ts.tmpl missing %q", want)
		}
	}
	if got := strings.Count(tmpl, "const token = await this.tokenProvider(service);"); got != 2 {
		t.Errorf("authHeader and requireAuthHeader must both pass the service to the provider, found %d", got)
	}
	if strings.Contains(tmpl, "this.tokenProvider()") {
		t.Error("w2sm.ts.tmpl still calls tokenProvider() without the target service")
	}

	// WebSocket frames authenticate via RestTransport.authHeader; it must carry
	// the service too.
	helpers := tsW2SClientHelpers("scottypersonal", true)
	if !strings.Contains(helpers, "await this.transport.authHeader('scottypersonal');") {
		t.Errorf("W2S WebSocket auth must pass the target service to authHeader:\n%s", helpers)
	}
}

// FetchTransport threw a plain Error("gRPC-over-REST error: <status>") on a
// non-2xx response, so a caller could not tell a refused refresh (401) from a
// network blip without parsing the message. It must throw W2SError carrying the
// HTTP status and the gRPC code mapped as Go's w2sHTTPStatusToError does.
func TestTypeScriptW2STransportErrorCarriesStatusAndCode(t *testing.T) {
	tmpl, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"public readonly status?: number;",
		"constructor(code: number, message: string, status?: number) {",
		"function w2sHttpStatusToCode(status: number): number {",
		"case 400: return 3;  // INVALID_ARGUMENT",
		"case 401: return 16; // UNAUTHENTICATED",
		"case 403: return 7;  // PERMISSION_DENIED",
		"case 404: return 5;  // NOT_FOUND",
		"case 409: return 6;  // ALREADY_EXISTS",
		"case 429: return 8;  // RESOURCE_EXHAUSTED",
		"case 501: return 12; // UNIMPLEMENTED",
		"case 503: return 14; // UNAVAILABLE",
		"case 504: return 4;  // DEADLINE_EXCEEDED",
		"default: return 2;   // UNKNOWN",
		"throw new W2SError(\n        w2sResponseCode(resp, errorBody),\n        `gRPC-over-REST error: ${resp.status} ${resp.statusText}`,\n        resp.status,\n      );",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sm.ts.tmpl missing %q", want)
		}
	}
	if strings.Contains(tmpl, "throw new Error(`gRPC-over-REST error:") {
		t.Error("w2sm.ts.tmpl still throws an untyped transport error")
	}
}

// The gateway answers every gRPC code it does not map (FAILED_PRECONDITION,
// ABORTED, INTERNAL, ...) with HTTP 500, so the HTTP mapping alone reported a
// FAILED_PRECONDITION as UNKNOWN. The code the gateway carries must win: the
// grpc-status header, else a numeric `code` in the JSON error body, else the
// HTTP status mapping.
func TestTypeScriptW2STransportErrorPrefersCarriedGrpcCode(t *testing.T) {
	tmpl, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"return typeof code === 'number' && Number.isInteger(code) && code >= 1 && code <= 16;",
		"const header = resp.headers.get('grpc-status')?.trim();",
		"const code = (JSON.parse(body) as { code?: unknown } | null)?.code;",
		"  return w2sHttpStatusToCode(resp.status);\n}",
		"const errorBody = await resp.text().catch(() => '');",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sm.ts.tmpl missing %q", want)
		}
	}
	header := strings.Index(tmpl, "resp.headers.get('grpc-status')")
	body := strings.Index(tmpl, "JSON.parse(body)")
	fallback := strings.Index(tmpl, "  return w2sHttpStatusToCode(resp.status);")
	if !(header < body && body < fallback) {
		t.Error("w2sResponseCode must try the grpc-status header, then the JSON body code, then the HTTP mapping")
	}
}

// Web composed a tenant-owned service id from tenantSpiffeId + SERVICES_SEGMENT
// by hand; the chassis must generate the builder.
func TestTypeScriptSpiffeTenantServiceBuilder(t *testing.T) {
	impl := readTSTemplate(t, "typescript/chassis/spiffe/SpiffeId.ts.tmpl")
	if !strings.Contains(impl, "return tenantSpiffeId(trustDomain, tenant) + '/' + SERVICES_SEGMENT + '/' + svc;") {
		t.Error("tenantServiceSpiffeId must compose tenantSpiffeId + /services/<svc>")
	}
	index := readTSTemplate(t, "typescript/chassis/spiffe/index.ts.tmpl")
	if !strings.Contains(index, "  tenantServiceSpiffeId,\n") {
		t.Error("chassis/spiffe index must export tenantServiceSpiffeId")
	}
	test := readTSTemplate(t, "typescript/chassis/spiffe/SpiffeId.test.ts.tmpl")
	if !strings.Contains(test, "testTenantServiceSpiffeId();") {
		t.Error("generated SPIFFE test must exercise tenantServiceSpiffeId")
	}
}
