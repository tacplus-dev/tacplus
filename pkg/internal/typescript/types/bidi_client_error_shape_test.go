// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

// Same class as PAR-9 (python clients) and the batch-7 typescript state client,
// at the typescript BIDI client seam. grpcclient.ts.tmpl's metadata() already
// stamps grpc.status.UNAUTHENTICATED; the bidi stream-open auth seam threw a
// bare uncoded Error, so a caller could not tell "no credential" from any other
// failure.
func TestTypeScriptBidiClientThrowsUnauthenticatedCode(t *testing.T) {
	rendered := renderTSBidiMethodForTest(t, true)

	if want := "throw bidiUnauthenticated('auth token is empty');"; !strings.Contains(rendered, want) {
		t.Errorf("rendered typescript bidi method missing %q:\n%s", want, rendered)
	}
	for _, bad := range []string{
		"throw new Error('auth token provider is required');",
		"throw new Error('auth token is empty');",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("typescript bidi auth seam still throws an uncoded Error: %q", bad)
		}
	}

	// The constructor the seam calls must be emitted with the bidi helpers, and
	// must carry the same code the unary seam uses.
	helpers := tsBidiHelpers()
	for _, want := range []string{
		"function bidiUnauthenticated(message: string): Error & { code?: number } {",
		"err.code = grpc.status.UNAUTHENTICATED;",
	} {
		if !strings.Contains(helpers, want) {
			t.Errorf("bidi helper block missing %q:\n%s", want, helpers)
		}
	}
}

// The seam is auth-gated: a method that requires no auth emits neither throw,
// so the helper's caller only exists where auth is actually required.
func TestTypeScriptBidiClientWithoutAuthOmitsAuthSeam(t *testing.T) {
	rendered := renderTSBidiMethodForTest(t, false)
	if strings.Contains(rendered, "bidiUnauthenticated") {
		t.Errorf("auth-free bidi method must not emit the auth seam:\n%s", rendered)
	}
}

func renderTSBidiMethodForTest(t *testing.T, authRequired bool) string {
	t.Helper()
	tmplStr, err := loadTemplate("grpcclient_bidi.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("bidi").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse bidi template: %v", err)
	}
	data := struct {
		MethodName         string
		MethodLower        string
		ServiceName        string
		InputType          string
		OutputType         string
		HandleType         string
		FullPath           string
		Serialize          string
		Deserialize        string
		AuthRequired       bool
		RequestValidation  string
		ResponseValidation string
		RouteOpen          string
	}{
		MethodName:   "Exchange",
		MethodLower:  "exchange",
		ServiceName:  "DemoService",
		InputType:    "DemoRequest",
		OutputType:   "DemoResponse",
		HandleType:   "ExchangeBidiHandle",
		FullPath:     "/demo.DemoService/Exchange",
		Serialize:    "(msg: DemoRequest) => Buffer.from(toBinary(DemoRequestSchema, msg))",
		Deserialize:  "(buf: Buffer) => fromBinary(DemoResponseSchema, new Uint8Array(buf))",
		AuthRequired: authRequired,
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute bidi template: %v", err)
	}
	return out.String()
}
