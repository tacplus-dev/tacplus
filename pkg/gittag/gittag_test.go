// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package gittag

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	// Create a temporary git repo with a tag
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("commit", "--allow-empty", "-m", "initial")

	// No tags yet — should error
	_, err := Detect(dir)
	if err == nil {
		t.Fatal("Detect should fail when no tags exist")
	}

	// Add a non-matching tag — should still error
	run("tag", "release-1")
	_, err = Detect(dir)
	if err == nil {
		t.Fatal("Detect should fail when no v* tags exist")
	}

	// Add a matching tag
	run("tag", "v1.2.3")
	tag, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if tag != "v1.2.3" {
		t.Errorf("Detect = %q, want %q", tag, "v1.2.3")
	}

	// Add another commit and a newer tag
	run("commit", "--allow-empty", "-m", "second")
	run("tag", "v1.3.0")
	tag, err = Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if tag != "v1.3.0" {
		t.Errorf("Detect = %q, want %q", tag, "v1.3.0")
	}
}

func TestDetect_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	// Ensure no .git directory exists anywhere up the tree
	isolatedDir := filepath.Join(dir, "isolated")
	if err := os.MkdirAll(isolatedDir, 0755); err != nil {
		t.Fatal(err)
	}
	_, err := Detect(isolatedDir)
	if err == nil {
		t.Fatal("Detect should fail in a non-git directory")
	}
}

func TestToMinorLatest(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.2.3", "1.2.latest"},
		{"1.2.3", "1.2.latest"},
		{"v1.2", "1.2.latest"},
		{"1.2", "1.2.latest"},
		{"v0.1.0", "0.1.latest"},
		{"v10.20.30", "10.20.latest"},
	}
	for _, tt := range tests {
		got := ToMinorLatest(tt.input)
		if got != tt.want {
			t.Errorf("ToMinorLatest(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
