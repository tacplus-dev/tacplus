// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes files (slash paths relative to dir) with mode 0o644, except
// where modes names another.
func fixture(t *testing.T, dir string, files map[string]string, modes map[string]os.FileMode) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if m, ok := modes[rel]; ok {
			mode = m
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSweep: a dry run lists exactly the files the rename changes, skipping
// the deny-listed directories, binary files and files without a token, and
// -apply rewrites those files, keeping their mode.
func TestSweep(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, map[string]string{
		"a.go":                  "import \"x/libs/y\"\nvar libDir = 1\n",
		"b/c.md":                "Libs and LIBS, Mlibs/x.proto\n",
		"b/none.txt":            "nothing here, lib only\n",
		"b/run.sh":              "echo libs\n",
		"bin.dat":               "libs\x00libs",
		".git/config":           "libs\n",
		"node_modules/p/i.js":   "libs\n",
		"sub/roadmap/plan.md":   "libs\n",
		"sub/sysrename/main.go": "libs\n",
	}, map[string]os.FileMode{"b/run.sh": 0o755})

	var out bytes.Buffer
	st, err := sweep(root, false, &out)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"a.go", filepath.Join("b", "c.md"), filepath.Join("b", "run.sh")}, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("dry run listed\n%s\nwant\n%s", out.String(), want)
	}
	if st.files != 3 || st.hits != 5 || st.singularBefore != st.singularAfter {
		t.Fatalf("dry run stats %+v", st)
	}
	if got := read(t, filepath.Join(root, "a.go")); !strings.Contains(got, "x/libs/y") {
		t.Fatalf("dry run wrote a.go: %q", got)
	}

	out.Reset()
	st2, err := sweep(root, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if st2 != st || out.Len() != 0 {
		t.Fatalf("apply stats %+v (dry run %+v), output %q", st2, st, out.String())
	}
	for rel, want := range map[string]string{
		"a.go":                  "import \"x/sys/y\"\nvar libDir = 1\n",
		"b/c.md":                "Sys and SYS, Msys/x.proto\n",
		"b/none.txt":            "nothing here, lib only\n",
		"b/run.sh":              "echo sys\n",
		"bin.dat":               "libs\x00libs",
		".git/config":           "libs\n",
		"node_modules/p/i.js":   "libs\n",
		"sub/roadmap/plan.md":   "libs\n",
		"sub/sysrename/main.go": "libs\n",
	} {
		if got := read(t, filepath.Join(root, filepath.FromSlash(rel))); got != want {
			t.Errorf("%s after apply = %q, want %q", rel, got, want)
		}
	}
	fi, err := os.Stat(filepath.Join(root, "b", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("b/run.sh mode after apply = %v, want 0755", fi.Mode().Perm())
	}
}

// TestSweepSymlinkOutsideRoot: a symlink in the root that points outside it,
// to a file or a directory, is neither read (not listed) nor written.
func TestSweepSymlinkOutsideRoot(t *testing.T) {
	outside := t.TempDir()
	fixture(t, outside, map[string]string{"secret.go": "libs\n", "d/f.go": "libs\n"}, nil)
	root := t.TempDir()
	fixture(t, root, map[string]string{"in.go": "libs\n"}, nil)
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "link.go")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := os.Symlink(filepath.Join(outside, "d"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	for _, apply := range []bool{false, true} {
		var out bytes.Buffer
		st, err := sweep(root, apply, &out)
		if err != nil {
			t.Fatalf("apply=%v: %v", apply, err)
		}
		if strings.Contains(out.String(), "link") {
			t.Errorf("apply=%v listed a symlink out of the root:\n%s", apply, out.String())
		}
		if !apply && (st.files != 1 || out.String() != "in.go\n") {
			t.Errorf("dry run: stats %+v, listed %q, want only in.go", st, out.String())
		}
	}
	for _, rel := range []string{"secret.go", "d/f.go"} {
		if got := read(t, filepath.Join(outside, filepath.FromSlash(rel))); got != "libs\n" {
			t.Errorf("outside %s was rewritten: %q", rel, got)
		}
	}
	if got := read(t, filepath.Join(root, "in.go")); got != "sys\n" {
		t.Errorf("in.go after apply = %q, want %q", got, "sys\n")
	}
}
