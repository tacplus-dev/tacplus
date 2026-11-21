// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package formatter

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// fakeBlack puts an executable named black on PATH that sleeps for the given
// time, then succeeds, and shortens the deadline for the test.
func fakeBlack(t *testing.T, sleep string, base, perFile time.Duration) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nexec sleep " + sleep + "\n"
	if err := os.WriteFile(filepath.Join(bin, "black"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldBase, oldPerFile := formatBaseTimeout, formatPerFileTimeout
	formatBaseTimeout, formatPerFileTimeout = base, perFile
	t.Cleanup(func() { formatBaseTimeout, formatPerFileTimeout = oldBase, oldPerFile })
}

func writePythonFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		name := filepath.Join(dir, "m"+strconv.Itoa(i)+".py")
		if err := os.WriteFile(name, []byte("x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A formatter slower than the base deadline is not killed when the output has
// files enough to justify it: the deadline scales with the files to format
// (tacplus#918, black killed on a loaded build pod).
func TestFormatPythonDeadlineScalesWithFiles(t *testing.T) {
	fakeBlack(t, "2", time.Second, time.Second)
	dir := t.TempDir()
	writePythonFiles(t, dir, 4)

	if err := Format("python", dir); err != nil {
		t.Fatalf("Format: %v (a 2s formatter on 4 files must fit a 1s+4×1s deadline)", err)
	}
}

// A formatter that outlives its deadline fails with an error naming the
// deadline, not a bare "signal: killed".
func TestFormatPythonDeadlineErrorNamesDeadline(t *testing.T) {
	fakeBlack(t, "10", 200*time.Millisecond, 100*time.Millisecond)
	dir := t.TempDir()
	writePythonFiles(t, dir, 2)

	err := Format("python", dir)
	if err == nil {
		t.Fatal("Format succeeded; want a deadline error")
	}
	for _, want := range []string{"black", "deadline", "400ms", "2 files"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}
