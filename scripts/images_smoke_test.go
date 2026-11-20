// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// goStdlibCheck sources images/smoke.sh and runs its go_stdlib_check against
// a fake docker CLI: a text file holding the `go version` output of the
// program it stands for, which a fake go prints.
func goStdlibCheck(t *testing.T, version string) (string, error) {
	t.Helper()
	smoke, err := filepath.Abs("../images/smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/usr/bin/env bash\n[[ $1 == version && $# == 2 ]] && exec cat \"$2\"\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(docker+": "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1" && go_stdlib_check`, "smoke", smoke)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TACPLUS_SMOKE_DOCKER="+docker)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestSmokeGoStdlibCheck (#901): the docker CLI is copied from
// docker:DOCKER_VERSION-cli, built with whatever Go upstream used. Its
// standard library must be at least the fixed release of its own Go line
// (CVE-2026-97054: go1.26.11, go1.27.4); a line older than every floor is
// unfixed, a line newer than every floor never had the bug.
func TestSmokeGoStdlibCheck(t *testing.T) {
	for _, tc := range []struct {
		version string
		refused string // empty: accepted
	}{
		{"go1.27.4", ""},
		{"go1.27.10", ""},
		{"go1.26.11", ""},
		{"go1.28.0", ""},
		{"go1.26.10", "docker is built with go1.26.10, below go1.26.11"},
		{"go1.27.3", "docker is built with go1.27.3, below go1.27.4"},
		{"go1.25.9", "docker is built with go1.25.9, below go1.26.11"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			out, err := goStdlibCheck(t, tc.version)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want refused with %q\n%s", tc.refused, out)
			}
			if !strings.Contains(out, tc.refused) {
				t.Fatalf("refused with\n%s\nwant %q", out, tc.refused)
			}
		})
	}
}
