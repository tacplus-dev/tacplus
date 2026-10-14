// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustSkippedReExportModulesStillDeclarePublicChildren(t *testing.T) {
	tmp := t.TempDir()
	modPath := filepath.Join(tmp, "mod.rs")

	if err := createProtoModFileWithReExports(modPath, []string{"clients", "services"}, true); err != nil {
		t.Fatalf("createProtoModFileWithReExports: %v", err)
	}
	content, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	out := string(content)
	for _, want := range []string{"pub mod clients;", "pub mod services;"} {
		if !strings.Contains(out, want) {
			t.Fatalf("skipped re-export module must still expose child module %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pub use clients::*") || strings.Contains(out, "pub use services::*") {
		t.Fatalf("skipped re-export module should declare modules without glob re-exports:\n%s", out)
	}
}

func TestUpdateLibFileWithHelpersExportsGeneratedGrantsModule(t *testing.T) {
	tmp := t.TempDir()
	libPath := filepath.Join(tmp, "lib.rs")
	if err := os.WriteFile(libPath, []byte("pub mod services;\n"), 0o644); err != nil {
		t.Fatalf("write lib.rs: %v", err)
	}
	grantsDir := filepath.Join(tmp, "grants")
	if err := os.MkdirAll(grantsDir, 0o755); err != nil {
		t.Fatalf("mkdir grants: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grantsDir, "mod.rs"), []byte("pub mod grants;\n"), 0o644); err != nil {
		t.Fatalf("write grants/mod.rs: %v", err)
	}

	if err := updateLibFileWithHelpers(tmp); err != nil {
		t.Fatalf("updateLibFileWithHelpers: %v", err)
	}
	content, err := os.ReadFile(libPath)
	if err != nil {
		t.Fatalf("read lib.rs: %v", err)
	}
	if !strings.Contains(string(content), "pub mod grants;") {
		t.Fatalf("lib.rs must export generated grants module:\n%s", content)
	}
}
