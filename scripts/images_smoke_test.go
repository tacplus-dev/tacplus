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

// goFloors sources images/smoke.sh and runs its go_floors_check against a
// fake docker-buildx and a fake gh. Each fake is a text file holding the
// `go version -m` output of the program it stands for; a fake go prints it.
func goFloors(t *testing.T, buildxDeps, ghDeps string) (string, error) {
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
	write := func(path, data string) {
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "go"), "#!/usr/bin/env bash\n[[ $1 == version && $2 == -m ]] && exec cat \"$3\"\nexit 2\n")
	buildx := filepath.Join(dir, "docker-buildx")
	write(buildx, buildxDeps)
	write(filepath.Join(bin, "gh"), ghDeps)
	cmd := exec.Command("bash", "-c", `source "$1" && go_floors_check`, "smoke", smoke)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TACPLUS_SMOKE_BUILDX="+buildx)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func dep(module, version string) string {
	return "\tdep\t" + module + "\t" + version + "\th1:x=\n"
}

func TestSmokeGoFloors(t *testing.T) {
	buildxOK := "/usr/local/libexec/docker/cli-plugins/docker-buildx: go1.27.3\n" +
		dep("github.com/moby/go-archive", "v0.3.3") + dep("golang.org/x/net", "v0.62.0")
	gh := func(deps string) string {
		return "/usr/local/bin/gh: go1.27.3\n\tpath\tgithub.com/cli/cli/v2/cmd/gh\n" + deps
	}
	for _, tc := range []struct {
		name, buildx, gh string
		refused          string // empty: accepted
	}{
		{"gh at the x/net floor", buildxOK, gh(dep("golang.org/x/net", "v0.62.0")), ""},
		{"gh past the x/net floor", buildxOK, gh(dep("golang.org/x/net", "v0.100.1")), ""},
		// CVE-2026-78661 (#350): gh's releases build with x/net v0.60.0.
		{"gh below the x/net floor", buildxOK, gh(dep("golang.org/x/net", "v0.60.0")), "carries golang.org/x/net v0.60.0, below v0.62.0"},
		{"gh without x/net", buildxOK, gh(dep("golang.org/x/text", "v0.30.0")), "carries golang.org/x/net none, below v0.62.0"},
		{"buildx below the go-archive floor", strings.Replace(buildxOK, "v0.3.3", "v0.2.0", 1), gh(dep("golang.org/x/net", "v0.62.0")), "carries github.com/moby/go-archive v0.2.0, below v0.3.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := goFloors(t, tc.buildx, tc.gh)
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
