// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// The Java pseudonymize primitive must produce the same "pseud:" + hex(first 16
// bytes) token as Go/Rust (cross-language wire contract) and put the empty-key
// check before the empty-input return (NEW-8c).
func TestJavaPseudonymizeHelperContract(t *testing.T) {
	var b strings.Builder
	generateJavaPseudonymizeHelper(&b)
	got := b.String()
	for _, want := range []string{
		"public static String tacplusPseudonymize(String s, byte[] key) {",
		`javax.crypto.Mac.getInstance("HmacSHA256")`,
		`StringBuilder sb = new StringBuilder("pseud:");`,
		"for (int i = 0; i < 16; i++)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("java pseudonymize helper missing %q:\n%s", want, got)
		}
	}
	keyCheck := strings.Index(got, "if (key.length == 0)")
	inputReturn := strings.Index(got, "if (s.isEmpty())")
	if keyCheck < 0 || inputReturn < 0 || keyCheck > inputReturn {
		t.Fatalf("empty-key check must precede the empty-input return:\n%s", got)
	}
}

func TestJavaPseudonymizeBytesHelperContract(t *testing.T) {
	var b strings.Builder
	generateJavaPseudonymizeBytesHelper(&b)
	got := b.String()
	for _, want := range []string{
		"public static com.google.protobuf.ByteString tacplusPseudonymizeBytes(com.google.protobuf.ByteString b, byte[] key) {",
		"com.google.protobuf.ByteString.copyFrom(full, 0, 16)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("java bytes pseudonymize helper missing %q:\n%s", want, got)
		}
	}
}
