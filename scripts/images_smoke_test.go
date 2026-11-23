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
		dep("github.com/containerd/containerd/v2", "v2.4.2") + dep("github.com/moby/go-archive", "v0.3.3") +
		dep("golang.org/x/net", "v0.62.0")
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
		// CVE-2026-95839 (#930): buildx through 0.38.x vendors containerd v2.4.1.
		{"buildx below the containerd floor", strings.Replace(buildxOK, "v2.4.2", "v2.4.1", 1), gh(dep("golang.org/x/net", "v0.62.0")), "carries github.com/containerd/containerd/v2 v2.4.1, below v2.4.2"},
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

// idmapCheck sources images/smoke.sh and runs its idmap_check against a fake
// newuidmap and newgidmap. Each fake's capabilities are what a fake getcap
// prints for it: the text of <fake>.cap, nothing when there is none.
func idmapCheck(t *testing.T, setuid bool, uidCap, gidCap string) (string, error) {
	t.Helper()
	smoke, err := filepath.Abs("../images/smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("getcap", "#!/usr/bin/env bash\n[[ -f $1.cap ]] && echo \"$1 $(cat \"$1.cap\")\"\nexit 0\n")
	for name, c := range map[string]string{"newuidmap": uidCap, "newgidmap": gidCap} {
		write(name, "")
		if c != "" {
			write(name+".cap", c)
		}
		if setuid {
			if err := os.Chmod(filepath.Join(bin, name), 0o755|os.ModeSetuid); err != nil {
				t.Fatal(err)
			}
		}
	}
	cmd := exec.Command("bash", "-c", `source "$1" && idmap_check`, "smoke", smoke)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestSmokeIdmapCheck (#830): rootless BuildKit's newuidmap and newgidmap
// carry file capabilities, never Debian's setuid bit. Setuid root, they write
// the map as root, which needs CAP_SYS_ADMIN a container without --privileged
// lacks: on GitHub's runner the write failed, Operation not permitted.
func TestSmokeIdmapCheck(t *testing.T) {
	for _, tc := range []struct {
		name           string
		setuid         bool
		uidCap, gidCap string
		refused        string // empty: accepted
	}{
		{"file capabilities", false, "cap_setuid=ep", "cap_setgid=ep", ""},
		{"setuid root, as Debian ships them", true, "", "", "newuidmap is setuid"},
		{"setuid and file capabilities", true, "cap_setuid=ep", "cap_setgid=ep", "newuidmap is setuid"},
		{"no capabilities", false, "", "", "newuidmap has no cap_setuid=ep"},
		{"newgidmap without its capability", false, "cap_setuid=ep", "", "newgidmap has no cap_setgid=ep"},
		{"each with the other's capability", false, "cap_setgid=ep", "cap_setuid=ep", "newuidmap has no cap_setuid=ep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := idmapCheck(t, tc.setuid, tc.uidCap, tc.gidCap)
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

// TestZigbuildVersionCheck (#830, #195): the image build and the smoke test
// ask cargo-zigbuild for its version as the program itself. `cargo zigbuild
// --version` passes the flag to the zigbuild subcommand, which refuses it
// (unexpected argument '--version'), so every build of the image failed.
func TestZigbuildVersionCheck(t *testing.T) {
	for _, path := range []string{"../images/build.Dockerfile", "../images/smoke.sh"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "cargo zigbuild --version") {
			t.Errorf("%s runs `cargo zigbuild --version`, which cargo-zigbuild refuses; run `cargo-zigbuild --version`", path)
		}
		if !strings.Contains(string(data), "cargo-zigbuild --version") {
			t.Errorf("%s does not check `cargo-zigbuild --version`", path)
		}
	}
}
