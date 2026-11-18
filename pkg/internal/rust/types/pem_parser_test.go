// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// The rustls PEM-file crate is unmaintained with no fixed version
// (RUSTSEC-2025-0134, #308). Its PEM parsing lives on in rustls-pki-types
// (`pem::PemObject`), reached through rustls's `pki_types` re-export, so no
// generated Rust crate may depend on it or call it again. Its names are built
// here rather than spelled, so a grep of the tree for them finds nothing.
func TestRustChassisDoesNotUseUnmaintainedPemCrate(t *testing.T) {
	crate := "rustls" + "-pemfile"
	banned := []string{crate, strings.ReplaceAll(crate, "-", "_")}

	err := fs.WalkDir(templates.FS, "rust", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			return err
		}
		for _, name := range banned {
			if strings.Contains(string(body), name) {
				t.Errorf("%s references %s (RUSTSEC-2025-0134); use rustls::pki_types::pem::PemObject", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk rust templates: %v", err)
	}

	required, err := requiredWorkspaceDependencyRefs(t.TempDir(), true, true, true, true)
	if err != nil {
		t.Fatalf("requiredWorkspaceDependencyRefs: %v", err)
	}
	if _, ok := required[crate]; ok {
		t.Fatalf("a cert-stream workspace must not require %s from dependencies.yaml", crate)
	}
}
