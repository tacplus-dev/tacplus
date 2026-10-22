// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// PC-W-7: bidi auth is STREAM-OPEN scoped (docs/API.md "Client Send Contract").
// Exactly one token is minted per connect and attached to the stream metadata;
// the send path must not preflight the token provider again, because gRPC
// metadata cannot change mid-stream — a per-send mint produces a token the
// server never sees while still being able to fail a legitimate send.
// java (grpcclient_bidi_regression_test.go) and rust
// (grpcclient_bidi_regression_test.go) already pin this; typescript did not,
// and its sendFn fetched and discarded a `freshToken` on every message.
func TestTypeScriptBidiSendDoesNotPreflightToken(t *testing.T) {
	rendered := renderTSBidiMethodForTest(t, true)

	if got := strings.Count(rendered, "await tokenProvider()"); got != 1 {
		t.Fatalf("typescript bidi client must mint exactly one token per stream open, got %d:\n%s", got, rendered)
	}
	for _, bad := range []string{
		"freshToken",
		"throw bidiUnauthenticated('auth token provider is required');",
	} {
		if strings.Contains(rendered, bad) {
			t.Fatalf("typescript bidi sendFn must not perform a per-message auth preflight; found %q:\n%s", bad, rendered)
		}
	}

	// The one mint must sit on the connect path, before the stream is opened.
	mint := strings.Index(rendered, "await tokenProvider()")
	open := strings.Index(rendered, "makeBidiStreamRequest")
	send := strings.Index(rendered, "const sendFn = async")
	if mint < 0 || open < 0 || send < 0 {
		t.Fatalf("rendered typescript bidi method missing connect/open/send landmarks:\n%s", rendered)
	}
	if !(mint < open && open < send) {
		t.Fatalf("token mint must precede stream open, which must precede sendFn (mint=%d open=%d send=%d):\n%s", mint, open, send, rendered)
	}
}
