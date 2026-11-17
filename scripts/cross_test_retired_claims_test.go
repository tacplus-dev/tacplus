// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// linkedClaimReaders runs linked_claim_readers (cross-test/lib/retired-claims.sh)
// over output and returns what it reports.
func linkedClaimReaders(t *testing.T, output string) string {
	t.Helper()
	lib, err := filepath.Abs("../cross-test/lib/retired-claims.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `set -euo pipefail; source "$1"; linked_claim_readers "$2"`, "bash", lib, output)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("linked_claim_readers: %v\n%s", err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The record-egress fixture's retired-linked-claim check (#171) runs under the
// gate's work root, target/cross-test/current-generation. Filtering every path
// that contains /target/ made it vacuous there (tacplus#351): it must fire on a
// planted reader under that root, and exclude only build output inside the
// fixture's own output.
func TestLinkedClaimReadersFiresUnderTheGateWorkRoot(t *testing.T) {
	output := filepath.Join(t.TempDir(), "target", "cross-test", "current-generation", "recordegress-go", "tacplus")
	writeFile(t, filepath.Join(output, "server", "vault.go"),
		"package server\n\nfunc pin(tok *Token) bool {\n\treturn tok.GetLinked() != \"\"\n}\n")
	writeFile(t, filepath.Join(output, "src", "Vault.java"),
		"class Vault {\n  boolean pin(Token tok) { return !\"\".equals(tok.getLinked()); }\n}\n")
	writeFile(t, filepath.Join(output, "src", "vault.rs"),
		"fn pin(tok: &Token) -> bool {\n    tok.linked().is_some()\n}\n")
	// Build output inside the fixture's output is not generated code.
	writeFile(t, filepath.Join(output, "java", "target", "generated-sources", "Old.java"),
		"class Old { Object x = tok.getLinked(); }\n")

	hits := linkedClaimReaders(t, output)
	for _, want := range []string{"server/vault.go:4:", "src/Vault.java:2:", "src/vault.rs:2:"} {
		if !strings.Contains(hits, want) {
			t.Errorf("planted reader %s not reported; got:\n%s", want, hits)
		}
	}
	if strings.Contains(hits, "Old.java") {
		t.Errorf("build output under the fixture's target/ reported; got:\n%s", hits)
	}
}

// protoc-gen-go and protoc-gen-java embed the raw descriptor as string
// literals, which carry the reserved enum name of pkg/proto/sys/token.proto
// (reserved "JWT_PAYLOAD_TYPE_LINKED"). That name in a descriptor reads nothing.
func TestLinkedClaimReadersIgnoresTheReservedNameInADescriptor(t *testing.T) {
	output := filepath.Join(t.TempDir(), "target", "cross-test", "current-generation", "recordegress-go", "tacplus")
	writeFile(t, filepath.Join(output, "model", "types", "sys", "token", "token.pb.go"),
		"package token\n\nconst file_sys_token_proto_rawDesc = \"\" +\n"+
			"\t\"\\x14JWT_PAYLOAD_TYPE_ACT\\x10\\x03\\\"\\x04\\b\\x04\\x10\\x04*\\x17JWT_PAYLOAD_TYPE_LINKED*\\x94\\x02\\n\" +\n"+
			"\t\"\\x0eTokenSignature\"\n")
	writeFile(t, filepath.Join(output, "src", "Token.java"),
		"class Token {\n  static final String[] descriptorData = {\n"+
			"    \"\\n\\024JWT_PAYLOAD_TYPE_ACT\\020\\003\\\"\\004\\010\\004\\020\\004*\\027JWT_PAYLOAD_TYPE_LINKED\" +\n"+
			"    \"\\n\"\n  };\n}\n")

	if hits := linkedClaimReaders(t, output); hits != "" {
		t.Errorf("the reserved name in a descriptor literal was reported:\n%s", hits)
	}

	// The same name outside a literal is code that names the claim.
	writeFile(t, filepath.Join(output, "server", "pin.go"),
		"package server\n\nvar _ = \"x\" + token.JwtPayloadType_JWT_PAYLOAD_TYPE_LINKED.String()\n")
	if hits := linkedClaimReaders(t, output); !strings.Contains(hits, "server/pin.go:3:") {
		t.Errorf("a reference outside a string literal was not reported; got:\n%s", hits)
	}
}
