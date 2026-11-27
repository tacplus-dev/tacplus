// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The deprecated tacplus image's root Dockerfile ran its CMD as root, a HIGH
// finding of the security scan (semgrep dockerfile.security.missing-user,
// #580). A tracked Dockerfile that sets a CMD or ENTRYPOINT runs it as a USER
// other than root, the rule semgrep applies.

// dockerfileRunsAsRoot reports whether a Dockerfile sets a CMD or ENTRYPOINT
// with no USER before it, or with root the last USER before it.
func dockerfileRunsAsRoot(data []byte) bool {
	user := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			user = "" // each stage starts as its base's user, root unless stated
		case "USER":
			if len(fields) > 1 {
				user, _, _ = strings.Cut(fields[1], ":")
			}
		case "CMD", "ENTRYPOINT":
			if user == "" || user == "root" || user == "0" {
				return true
			}
		}
	}
	return false
}

func TestDockerfileRunsAsRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{"no command", "FROM x\nRUN true\n", false},
		{"no user", "FROM x\nCMD [\"x\"]\n", true},
		{"root", "FROM x\nUSER root\nENTRYPOINT [\"x\"]\n", true},
		{"uid 0", "FROM x\nUSER 0:0\nCMD x\n", true},
		{"non-root", "FROM x\nUSER tacplus\nCMD x\n", false},
		{"user in an earlier stage", "FROM x AS a\nUSER tacplus\nFROM y\nCMD x\n", true},
		{"back to root", "FROM x\nUSER tacplus\nUSER root\nCMD x\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dockerfileRunsAsRoot([]byte(tc.data)); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoDockerfileRunsAsRoot(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	dir := string(bytes.TrimSpace(root))
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for _, name := range strings.Split(string(out), "\x00") {
		base := filepath.Base(name)
		if base != "Dockerfile" && !strings.HasPrefix(base, "Dockerfile.") && !strings.HasSuffix(base, ".Dockerfile") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // a tracked file deleted in the working tree
		}
		if dockerfileRunsAsRoot(data) {
			t.Errorf("%s: runs its CMD or ENTRYPOINT as root; set a non-root USER before it", name)
		}
	}
}
