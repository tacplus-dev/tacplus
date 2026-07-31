// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// SMB soundness (SPIFFE plan D7), pinned at the contract level.
//
// tacplus generates the SMB cert *seam* (the SmbCertService client + the
// instance-side stream that hands the credential to Handler.NewSmbCert); it does
// NOT generate the black box's own config — the consumer wires the delivered
// credential into its black-box mTLS. So the tacplus-side
// soundness invariant lives in the smbcert.proto delivery contract: an SMB
// instance may only ever receive the isolated per-SMB CA *certificate* plus its
// own member cert chain — never a CA private key and never the true ROOT CA.
//
// X509Cert (sys/x509.proto) is a certificate only (id + base64 PEM) and
// structurally cannot carry a private key, whereas X509CertChain has an optional
// key field. So pinning `ca` to X509Cert guarantees no CA private key crosses the
// wire. These assertions fail if a future edit weakens the contract (e.g. changes
// `ca` to a chain/keypair, or adds a root-CA / private-key field to the delivery).

func readSmbCertProto(t *testing.T) string {
	t.Helper()
	// Test CWD is the package dir (pkg/internal/common/parser); the proto lives at
	// pkg/proto/sys/smbcert.proto, three levels up.
	src := filepath.Join("..", "..", "..", "proto", "sys", "smbcert.proto")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read smbcert.proto (%s): %v", src, err)
	}
	return string(data)
}

// extractMessageBody returns the `{ ... }` body of the named proto message with
// `//` comment lines stripped — the invariants below are about field
// declarations, not the prose that (intentionally) mentions "true ROOT CA".
func extractMessageBody(t *testing.T, proto, msg string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)message\s+` + regexp.QuoteMeta(msg) + `\s*\{(.*?)\n\}`)
	m := re.FindStringSubmatch(proto)
	if m == nil {
		t.Fatalf("message %s not found in smbcert.proto", msg)
	}
	commentLine := regexp.MustCompile(`(?m)^\s*//.*$`)
	return commentLine.ReplaceAllString(m[1], "")
}

func TestSmbCertDeliveryContractInvariant(t *testing.T) {
	proto := readSmbCertProto(t)
	body := extractMessageBody(t, proto, "SmbCertDelivery")

	// The CA trust anchor must be a bare certificate (x509.X509Cert), which
	// cannot hold a private key — never a chain/keypair.
	if !regexp.MustCompile(`x509\.X509Cert\s+ca\s*=`).MatchString(body) {
		t.Errorf("SmbCertDelivery.ca must be `x509.X509Cert ca` (cert-only trust anchor); body:\n%s", body)
	}
	// The member credential is delivered as a cert chain (no private key in the
	// SMB flow — the instance generated and holds its own key).
	if !regexp.MustCompile(`x509\.X509CertChain\s+member_chain\s*=`).MatchString(body) {
		t.Errorf("SmbCertDelivery.member_chain must be `x509.X509CertChain member_chain`; body:\n%s", body)
	}
	// No private-key-bearing field may appear in the delivery (no key.Key /
	// key.PrivateKey). The member key is generated locally and never delivered;
	// the per-SMB CA's key never leaves the authority.
	if regexp.MustCompile(`\bkey\.(Key|PrivateKey)\b`).MatchString(body) {
		t.Errorf("SmbCertDelivery must not carry a private key (no key.Key/key.PrivateKey); body:\n%s", body)
	}
	// Guard against a true/global ROOT CA field sneaking into the delivery.
	if regexp.MustCompile(`(?i)root[_ ]?ca|true[_ ]?root|global[_ ]?ca`).MatchString(body) {
		t.Errorf("SmbCertDelivery must not reference a true/global ROOT CA; body:\n%s", body)
	}
}

func TestSmbCertCsrSendsPublicKeyOnly(t *testing.T) {
	proto := readSmbCertProto(t)
	body := extractMessageBody(t, proto, "SmbCertCsr")
	// The client→authority CSR carries the public key only; the member private
	// key must never appear in a client message.
	if !regexp.MustCompile(`key\.PublicKey\s+public_key\s*=`).MatchString(body) {
		t.Errorf("SmbCertCsr.public_key must be `key.PublicKey public_key`; body:\n%s", body)
	}
	if regexp.MustCompile(`\bkey\.(Key|PrivateKey)\b`).MatchString(body) {
		t.Errorf("SmbCertCsr must not carry a private key; body:\n%s", body)
	}
}
