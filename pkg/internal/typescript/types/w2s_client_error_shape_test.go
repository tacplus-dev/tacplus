// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// Same class as PAR-9, at the typescript W2S transport seam. The python W2S
// client raises a structured UNAUTHENTICATED (ProtoError code 16) and
// distinguishes "no token provider" from "provider returned an empty token"
// (w2sclient.py.tmpl:48-58). FetchTransport threw one bare, uncoded Error for
// both, so a browser caller could neither classify the failure nor tell a
// missing provider from an expired session.
func TestTypeScriptW2STransportThrowsUnauthenticatedCode(t *testing.T) {
	tmpl, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"export class W2SError extends Error {",
		"public readonly code: number;",
		"const W2S_UNAUTHENTICATED = 16;",
		"private async requireAuthHeader(): Promise<string> {",
		"throw new W2SError(W2S_UNAUTHENTICATED, 'w2sm: authenticated request requires a token provider');",
		"throw new W2SError(W2S_UNAUTHENTICATED, 'w2sm: token provider returned an empty token');",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sm.ts.tmpl missing W2S auth-error-shape piece %q", want)
		}
	}

	// Every authenticated route must go through requireAuthHeader — the unary
	// JSON path plus both SSE stream paths.
	if got := strings.Count(tmpl, "headers.Authorization = await this.requireAuthHeader();"); got != 3 {
		t.Errorf("expected all 3 authenticated FetchTransport routes to use requireAuthHeader, found %d", got)
	}

	// The old uncoded shape must not come back.
	for _, banned := range []string{
		"if (!auth) throw new Error(",
		"'w2sm: authenticated SSE request requires a token provider'",
	} {
		if strings.Contains(tmpl, banned) {
			t.Errorf("w2sm.ts.tmpl still carries the uncoded auth-error shape: %s", banned)
		}
	}

	// authHeader() keeps its `string | undefined` shape: it is part of the
	// RestTransport interface the generated W2S clients consume
	// (grpc_over_rest_gen.go:384).
	if !strings.Contains(tmpl, "async authHeader(): Promise<string | undefined> {") {
		t.Error("authHeader() must keep its RestTransport-interface signature")
	}
}
