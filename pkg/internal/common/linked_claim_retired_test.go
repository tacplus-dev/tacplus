// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The `linked` claim is retired (#171). It named namespaces beyond the token's
// own `namespace` claim, and the generated namespace backstop accepted a row,
// a query predicate and a record write from any of them. XIDs and
// request-named routing (#170) replace it: the request says which tenant it is
// for, the token says who is asking, policy at the target decides. A token that
// widens its own namespaces is no longer part of the model, so no generated
// code in any language reads it — not the backstop, not a token accessor.
//
// Behaviour is driven per language where the backstop runs: the Java and Rust
// renders here (recordnamespace_tenant_s2s_test.go, namespace_render_test.go)
// and the record-write guard self-tests of cross-test/current-generation, each
// with a token that still carries a `linked` claim and is not widened by it.
// This keeps the claim from coming back in any one generator or template.

// linkedClaimRead matches a read of the claim in a generator or a template: its
// wire key, the JwtPayloadType value, an accessor in any language's spelling,
// or the locals the backstop bound it to.
var linkedClaimRead = regexp.MustCompile(`["']linked["']|_LINKED\b|[GgSs]et_?[Ll]inked\b|\.linked\(\)|fn linked\b|LinkedFromContext|NamespaceLinked|namespace_linked|[Cc]aller_?[Ll]inked`)

// linkedClaimSources are the trees whose files render generated code: every
// generator under pkg/internal and every template.
var linkedClaimSources = []string{"../../templates", ".."}

func TestNoGeneratedCodeReadsTheLinkedClaim(t *testing.T) {
	scanned := 0
	for _, root := range linkedClaimSources {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for i, line := range strings.Split(string(src), "\n") {
				if m := linkedClaimRead.FindString(line); m != "" {
					t.Errorf("%s:%d reads the retired `linked` claim (%q); a token's own namespace is the only one the generated code accepts (#171)", path, i+1, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no generator or template sources; the paths above have moved")
	}
}

// The wire number and name stay reserved so neither is reused for a claim with
// a different meaning a token minted before the retirement could still carry.
func TestLinkedClaimNumberIsReserved(t *testing.T) {
	src, err := os.ReadFile("../../proto/sys/token.proto")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reserved 29;", `reserved "JWT_PAYLOAD_TYPE_LINKED";`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("sys/token.proto: JwtPayloadType must carry %q", want)
		}
	}
	if strings.Contains(string(src), "JWT_PAYLOAD_TYPE_LINKED = ") {
		t.Error("sys/token.proto still declares JWT_PAYLOAD_TYPE_LINKED")
	}
}
