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
)

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
