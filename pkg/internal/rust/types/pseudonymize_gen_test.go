// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// The Rust pseudonymize primitive must match the cross-language wire contract
// ("pseud:" + hex(HMAC-SHA256(key, s)[:16])) and put the empty-key check before the
// empty-input return (NEW-8c) so a misconfigured key fails loud even on empty input.
func TestRustPseudonymizeHelperContract(t *testing.T) {
	var b strings.Builder
	generateRustPseudonymizeHelper(&b)
	got := b.String()
	for _, want := range []string{
		"pub fn tacplus_pseudonymize(s: &str, key: &[u8]) -> String {",
		"<Hmac<Sha256>>::new_from_slice(key)",
		`format!("pseud:{}", hex::encode(&bytes[..16]))`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rust pseudonymize helper missing %q:\n%s", want, got)
		}
	}
	keyCheck := strings.Index(got, "if key.is_empty() {")
	inputReturn := strings.Index(got, "if s.is_empty() {")
	if keyCheck < 0 || inputReturn < 0 || keyCheck > inputReturn {
		t.Fatalf("empty-key check must precede the empty-input return:\n%s", got)
	}
}

func TestRustPseudonymizeBytesHelperContract(t *testing.T) {
	var b strings.Builder
	generateRustPseudonymizeBytesHelper(&b)
	got := b.String()
	for _, want := range []string{
		"pub fn tacplus_pseudonymize_bytes(b: &[u8], key: &[u8]) -> Vec<u8> {",
		"mac.update(b);",
		"bytes[..16].to_vec()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rust bytes pseudonymize helper missing %q:\n%s", want, got)
		}
	}
}
