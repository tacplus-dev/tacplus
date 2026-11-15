// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fsroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileUnderDir(t *testing.T) {
	dir := t.TempDir()
	if err := MkdirAll(dir, filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "a", "b", "f.txt")
	if err := WriteFile(dir, path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "x" {
		t.Fatalf("read back %q, %v", got, err)
	}
}

// MkdirAll creates dir itself when it is missing, as os.MkdirAll did.
func TestMkdirAllCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := MkdirAll(dir, filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "sub")); err != nil || !fi.IsDir() {
		t.Fatalf("sub not created: %v", err)
	}
}

// A symlink under dir that leads out of it, at the file or at a directory on
// the way, is refused; one that stays inside dir is followed.
func TestSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "in"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"outdir", outside},
		{"outfile", filepath.Join(outside, "f")},
		{"indir", "in"},
	} {
		if err := os.Symlink(link.target, filepath.Join(dir, link.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	if err := WriteFile(dir, filepath.Join(dir, "outdir", "f"), []byte("x"), 0o644); err == nil {
		t.Error("write through a linked dir out of dir: want an error")
	}
	if err := WriteFile(dir, filepath.Join(dir, "outfile"), []byte("x"), 0o644); err == nil {
		t.Error("write through a linked file out of dir: want an error")
	}
	if err := MkdirAll(dir, filepath.Join(dir, "outdir", "sub"), 0o755); err == nil {
		t.Error("mkdir through a linked dir out of dir: want an error")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("written outside dir: %v", entries)
	}

	if err := WriteFile(dir, filepath.Join(dir, "indir", "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write through an in-tree link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "in", "f")); err != nil {
		t.Fatalf("in-tree link not followed: %v", err)
	}
}

func TestRelRefusesPathOutsideDir(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{filepath.Dir(dir), filepath.Join(dir, "..", "x")} {
		if _, err := Rel(dir, path); err == nil {
			t.Errorf("Rel(%q, %q): want an error", dir, path)
		}
	}
	if rel, err := Rel(dir, filepath.Join(dir, "a", "..b")); err != nil || rel != filepath.Join("a", "..b") {
		t.Errorf("Rel of a name starting with ..: %q, %v", rel, err)
	}
}
