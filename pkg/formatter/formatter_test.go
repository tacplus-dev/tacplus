// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package formatter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/codefile"
)

const unindented = "fn main() {\nlet x = 1;\n}\n"

func TestIndentFilesRewritesGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"src/lib.rs", "src/sub/mod.rs"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(unindented), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(unindented), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := indentFiles(dir, ".rs", codefile.Rust); err != nil {
		t.Fatalf("indentFiles: %v", err)
	}

	want := codefile.IndentCode(codefile.Rust, unindented)
	if want == unindented {
		t.Fatal("fixture is already indented; the test would prove nothing")
	}
	for _, rel := range []string{"src/lib.rs", "src/sub/mod.rs"} {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != unindented {
		t.Errorf("README.md was rewritten: %q", got)
	}
}

// A symlink in the output directory pointing outside it is not written
// through, as a file or as a directory (tacplus#578).
func TestIndentFilesDoesNotWriteThroughSymlinks(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.rs")
	if err := os.WriteFile(target, []byte(unindented), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirTarget := filepath.Join(outside, "d", "victim.rs")
	if err := os.WriteFile(dirTarget, []byte(unindented), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "link.rs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "d"), filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.rs"), []byte(unindented), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := indentFiles(dir, ".rs", codefile.Rust); err != nil {
		t.Fatalf("indentFiles: %v", err)
	}

	for _, p := range []string{target, dirTarget} {
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != unindented {
			t.Errorf("%s outside the output directory was rewritten: %q", p, got)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, info.Mode().Perm())
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if want := codefile.IndentCode(codefile.Rust, unindented); string(got) != want {
		t.Errorf("lib.rs = %q, want %q", got, want)
	}
}
