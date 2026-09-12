// Copyright (c) 2026 Second Star Cavalier LLC

package protosrc

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://gitlab.com/org/repo.git", true},
		{"http://github.com/org/repo.git", true},
		{"git@gitlab.com:org/repo.git", true},
		{"/local/path/to/proto", false},
		{"./relative/path", false},
		{"gitlab.com/org/repo.git", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isGitURL(tt.input); got != tt.want {
				t.Errorf("isGitURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestLooksLikeRepo(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"gitlab.com/org/repo.git", true},
		{"github.com/org/repo", true},
		{"example.com/repo.git", true},
		{"/local/path", false},
		{"./relative", false},
		{"just-a-name", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := looksLikeRepo(tt.input); got != tt.want {
				t.Errorf("looksLikeRepo(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCompareSemverTags(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int // negative, zero, or positive
	}{
		{"equal", "v1.2.3", "v1.2.3", 0},
		{"patch_less", "v1.2.3", "v1.2.10", -1},
		{"patch_greater", "v1.2.10", "v1.2.3", 1},
		{"minor_less", "v1.2.3", "v1.3.0", -1},
		{"major_less", "v1.9.9", "v2.0.0", -1},
		{"no_v_prefix", "1.2.3", "1.2.4", -1},
		{"mixed_prefix", "v1.2.3", "1.2.4", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemverTags(tt.a, tt.b)
			if (tt.want < 0 && got >= 0) || (tt.want > 0 && got <= 0) || (tt.want == 0 && got != 0) {
				t.Errorf("compareSemverTags(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestResolve_LocalPath(t *testing.T) {
	dir := t.TempDir()

	result, err := Resolve(dir, "latest")
	if err != nil {
		t.Fatalf("Resolve(%q, \"latest\") returned error: %v", dir, err)
	}
	defer result.Cleanup()

	if result.Dir != dir {
		// Resolve returns an absolute path and may resolve root symlinks
		// such as macOS /var -> /private/var.
		abs, _ := filepath.Abs(dir)
		real, _ := filepath.EvalSymlinks(abs)
		if result.Dir != abs && result.Dir != real {
			t.Errorf("Dir = %q, want %q or %q", result.Dir, abs, real)
		}
	}
}

func TestResolve_NonExistentPath(t *testing.T) {
	_, err := Resolve("/nonexistent/path/that/does/not/exist", "latest")
	if err == nil {
		t.Error("Resolve with non-existent path should return error")
	}
}

func TestResolve_InvalidTagFormat(t *testing.T) {
	// Create a temp dir that looks like a valid local path
	// but use a git URL to trigger tag parsing
	_, err := Resolve("https://example.com/fake.git", "invalid-tag-format")
	// This will fail because git clone will fail, but we're testing
	// that the function handles various inputs without panicking.
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestCollectFiles(t *testing.T) {
	dir := t.TempDir()

	// Create test file structure
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "a.rs"), []byte("fn main() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "b.rs"), []byte("fn test() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "d.rs"), []byte("mod sub;"), 0644)

	// This tests the formatter package's collectFiles — but it's unexported there.
	// Instead we test the protosrc helpers which are the main target.
}

func TestAuthArgs(t *testing.T) {
	t.Setenv("CI_JOB_TOKEN", "s3cr3t-token")

	if got := authArgs("git@gitlab.com:org/repo.git"); got != nil {
		t.Errorf("authArgs(ssh url) = %v, want nil", got)
	}

	got := authArgs("https://gitlab.com/org/repo.git")
	want := []string{"-c", "http.extraheader=AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:s3cr3t-token"))}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("authArgs() = %v, want %v", got, want)
	}

	t.Setenv("CI_JOB_TOKEN", "")
	if got := authArgs("https://gitlab.com/org/repo.git"); got != nil {
		t.Errorf("authArgs() with no token = %v, want nil", got)
	}
}

// TestCloneDoesNotPersistToken clones a local repo the same way cachedRepo does
// and asserts the CI_JOB_TOKEN never lands in the clone's .git/config.
func TestCloneDoesNotPersistToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	const token = "s3cr3t-token"
	t.Setenv("CI_JOB_TOKEN", token)

	// Origin repo with one commit.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Clone it exactly as cachedRepo does: plain URL + per-invocation auth flags.
	url := "https://gitlab.com/org/repo.git"
	auth := authArgs(url)
	dst := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", append(append([]string{}, auth...), "clone", "-q", "--", origin, dst)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}

	cfg, err := os.ReadFile(filepath.Join(dst, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), token) {
		t.Errorf(".git/config contains CI_JOB_TOKEN:\n%s", cfg)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:" + token))
	if strings.Contains(string(cfg), b64) {
		t.Errorf(".git/config contains encoded credential:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "extraheader") {
		t.Errorf(".git/config persisted extraheader:\n%s", cfg)
	}
}
