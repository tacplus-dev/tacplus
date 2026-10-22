// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateModFileAllowsAmbiguousHelperGlobReexports(t *testing.T) {
	out := t.TempDir()
	subdir := "types/services/demo"
	if err := os.MkdirAll(filepath.Join(out, subdir), 0755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	if err := generateModFile(out, subdir); err != nil {
		t.Fatalf("generateModFile: %v", err)
	}

	renderedBytes, err := os.ReadFile(filepath.Join(out, subdir, "mod.rs"))
	if err != nil {
		t.Fatalf("read mod.rs: %v", err)
	}
	rendered := string(renderedBytes)
	if !strings.Contains(rendered, "#[allow(ambiguous_glob_reexports, unused_imports)]pub use helpers::*;") {
		t.Fatalf("helper re-export should allow ambiguous_glob_reexports:\n%s", rendered)
	}
}
