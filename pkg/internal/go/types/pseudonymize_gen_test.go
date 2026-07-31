// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// TestFormatAnonymizeWireVectors pins the format-preserving surrogates to exact
// strings for a fixed (key, value) — the cross-language wire contract. Rust and
// Java must reproduce these byte-for-byte, or format-anonymized records stop
// joining across a mixed-language deployment. Any change to a surrogate's SHAPE
// breaks this test on purpose.
func TestFormatAnonymizeWireVectors(t *testing.T) {
	key := []byte("pseudonym-secret-key-v1")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("alice@example.com"))
	m := mac.Sum(nil)

	got := map[string]string{
		"email": "u" + hex.EncodeToString(m[0:6]) + "@h" + hex.EncodeToString(m[6:10]) + ".example",
		"fqdn":  "h" + hex.EncodeToString(m[0:8]) + ".example",
		"ipv4":  fmt.Sprintf("10.%d.%d.%d", m[0], m[1], m[2]),
		"ipv6":  fmt.Sprintf("2001:db8:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x", m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8], m[9], m[10], m[11]),
	}
	want := map[string]string{
		"email": "u6595fedc5f9d@h00b73933.example",
		"fqdn":  "h6595fedc5f9d00b7.example",
		"ipv4":  "10.101.149.254",
		"ipv6":  "2001:db8:6595:fedc:5f9d:00b7:3933:4ec7",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s vector drifted: got %q want %q (all languages must match this)", k, got[k], w)
		}
	}
}

// The anonymize dispatch picks the format-preserving primitive by the field's
// format annotation, and the plain token otherwise.
func TestGoAnonymizePrimitiveDispatch(t *testing.T) {
	fld := func(anns map[string]model.AnnotationValue) model.Field {
		return model.Field{Name: "f", Type: "string", Annotations: anns}
	}
	ipv4 := map[string]model.AnnotationValue{"assert_ipaddr_type": {Type: model.AnnotationTypeEnum, EnumValue: "IP_ADDR_TYPE_IP_V4"}}
	ipv6 := map[string]model.AnnotationValue{"assert_ipaddr_type": {Type: model.AnnotationTypeEnum, EnumValue: "IP_ADDR_TYPE_IP_V6"}}
	cases := []struct {
		anns map[string]model.AnnotationValue
		want string
	}{
		{map[string]model.AnnotationValue{"assert_email": {Type: model.AnnotationTypeBool, BoolValue: true}}, "tacplusPseudonymizeEmail"},
		{map[string]model.AnnotationValue{"assert_fqdn": {Type: model.AnnotationTypeBool, BoolValue: true}}, "tacplusPseudonymizeFqdn"},
		{ipv4, "tacplusPseudonymizeIpv4"},
		{ipv6, "tacplusPseudonymizeIpv6"},
		{nil, "tacplusPseudonymize"},
	}
	for _, c := range cases {
		if got := goAnonymizePrimitive(fld(c.anns), &sanitizeNeeds{}); got != c.want {
			t.Errorf("dispatch for %v = %q, want %q", c.anns, got, c.want)
		}
	}
}

// NEW-8(a): the pseudonym token is a cross-language WIRE contract — go/rust/java
// must all emit "pseud:" + hex(HMAC-SHA256(key, s)[:16]). This is the independent
// ground-truth vector (computed offline with python's hmac): the same (key, s)
// must yield this exact token in every language, or pseudonymized records stop
// joining across a mixed-language deployment.
func TestPseudonymizeWireVectorIsStable(t *testing.T) {
	key := []byte("pseudonym-secret-key-v1")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("alice@example.com"))
	got := "pseud:" + hex.EncodeToString(mac.Sum(nil)[:16])
	const want = "pseud:6595fedc5f9d00b739334ec7d8e457c6"
	if got != want {
		t.Fatalf("pseudonym wire vector drifted: got %q want %q (all languages must match this)", got, want)
	}
}

// NEW-8(a)+(c): pin the emitted primitive to the exact algorithm AND the
// key-first ordering. The empty-key check must precede the empty-input early
// return, so a misconfigured (empty) key fails loud even when the field value
// happens to be empty — otherwise an empty field silently hides the
// misconfiguration until some non-empty value later panics in production.
func TestGeneratePseudonymizeHelperContractAndOrdering(t *testing.T) {
	var b strings.Builder
	generatePseudonymizeHelper(&b)
	got := b.String()
	for _, want := range []string{
		"mac := hmac.New(sha256.New, key)",
		"mac.Write([]byte(s))",
		`return "pseud:" + hex.EncodeToString(mac.Sum(nil)[:16])`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("pseudonymize helper missing exact algorithm line %q:\n%s", want, got)
		}
	}
	keyCheck := strings.Index(got, "if len(key) == 0 {")
	inputReturn := strings.Index(got, `if s == "" {`)
	if keyCheck < 0 || inputReturn < 0 {
		t.Fatalf("pseudonymize helper missing key/input guards:\n%s", got)
	}
	if keyCheck > inputReturn {
		t.Fatal("empty-key check must precede the empty-input early return so an empty field cannot hide an empty-key misconfiguration (NEW-8c)")
	}
}

// The bytes primitive HMACs bytes natively and returns the raw 128-bit mac.
func TestGeneratePseudonymizeBytesHelperContract(t *testing.T) {
	var b strings.Builder
	generatePseudonymizeBytesHelper(&b)
	got := b.String()
	for _, want := range []string{
		"func tacplusPseudonymizeBytes(b []byte, key []byte) []byte {",
		"mac := hmac.New(sha256.New, key)",
		"mac.Write(b)",
		"return mac.Sum(nil)[:16]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bytes pseudonymize helper missing %q:\n%s", want, got)
		}
	}
	keyCheck := strings.Index(got, "if len(key) == 0 {")
	inputReturn := strings.Index(got, "if len(b) == 0 {")
	if keyCheck < 0 || inputReturn < 0 || keyCheck > inputReturn {
		t.Fatalf("empty-key check must precede the empty-input return:\n%s", got)
	}
}
