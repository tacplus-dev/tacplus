// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"regexp"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// PC-L-12 residual (b). A renewal-keypair generation failure inside a cert
// stream must END the session: the authority has asked for a CSR the service
// cannot produce, so staying in the receive loop leaves a stream that will
// never answer and a certificate that quietly runs to expiry. Reconnecting is
// the recovery path — a fresh session re-runs the whole handshake.
//
// Java's handling is pinned behaviourally by
// pkg/internal/java/types/cert_renewal_keygen_failure_test.go. Go and rust had
// NO mechanical evidence for either leg: their handling was read and confirmed
// correct by inspection only, so a refactor that turned the error into a
// log-and-continue would have been silent in both languages and in both
// streams (TLS and SMB member cert).
func TestRenewalKeygenFailureEndsTheStreamSession(t *testing.T) {
	t.Run("go", func(t *testing.T) {
		s, err := tmplutil.LoadTemplate("go/types", "service_manager.go.tmpl")
		if err != nil {
			t.Fatalf("load service_manager.go.tmpl: %v", err)
		}
		// Both renewal arms (TLS and SMB) regenerate the keypair. Each must
		// return the error rather than fall through to the next Recv.
		re := regexp.MustCompile(`(?m)^privateKeyDER, publicKeyDER, pubProto, err = generateKeyAndProto\(\)\nif err != nil \{\nreturn delivered, fmt\.Errorf\("generate renewal keypair: %w", err\)\n\}`)
		if got := len(re.FindAllString(s, -1)); got != 2 {
			t.Errorf("go SM template has %d renewal-keygen arms that end the session, want 2 (TLS + SMB member cert); a renewal keygen failure must not be swallowed into the receive loop", got)
		}
	})

	t.Run("rust", func(t *testing.T) {
		s, err := tmplutil.LoadTemplate("rust/types", "service_manager.rs.tmpl")
		if err != nil {
			t.Fatalf("load service_manager.rs.tmpl: %v", err)
		}
		// The `?` is what ends the session; a `match`/`unwrap_or` here would
		// keep the loop alive with no key to sign.
		const renewal = "let (new_private_key_der, new_public_key_der, new_pub_proto) = generate_key_and_proto()?;"
		if got := strings.Count(s, renewal); got != 2 {
			t.Errorf("rust SM template has %d renewal-keygen sites propagating the failure with `?`, want 2 (TLS + SMB member cert); without it the stream stays open and never answers the CSR request", got)
		}
	})
}
