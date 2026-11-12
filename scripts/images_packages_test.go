// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// packagesCheck runs images/packages-check.sh against a fake dpkg status
// file and one declared list.
func packagesCheck(t *testing.T, status, declared string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("../images/packages-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	statusFile := filepath.Join(dir, "status")
	list := filepath.Join(dir, "declared")
	for path, data := range map[string]string{statusFile: status, list: declared} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", script, list)
	cmd.Env = append(os.Environ(), "TACPLUS_DPKG_STATUS="+statusFile)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pkg is one dpkg status stanza; fields are "Name: value" lines.
func pkg(name, status string, fields ...string) string {
	s := "Package: " + name + "\nStatus: " + status + "\n"
	for _, f := range fields {
		s += f + "\n"
	}
	return s + "Description: " + name + "\n a continuation line: Depends: not-a-field\n\n"
}

func installed(name string, fields ...string) string {
	return pkg(name, "install ok installed", fields...)
}

// TestImagesPackagesCheck: the images carry no Debian package beyond their
// declared lists (images/*.packages), Debian's base system (Essential,
// Priority required or important) and what those depend on (#554): the
// build image carried libopenexr-dev (CRITICAL CVE-2026-42216/-42217)
// because its base came from buildpack-deps, which installs ImageMagick's
// -dev packages.
func TestImagesPackagesCheck(t *testing.T) {
	base := installed("libc6", "Priority: required", "Essential: no") +
		installed("dash", "Priority: optional", "Essential: yes") +
		installed("netbase", "Priority: important")
	for _, tc := range []struct {
		name, status, declared string
		refused                []string // empty: accepted
	}{
		{
			name: "declared packages and their dependencies",
			status: base +
				installed("jq", "Priority: optional", "Depends: libjq1 (= 1.7.1-6), libc6 (>= 2.34)") +
				installed("libjq1", "Priority: optional", "Pre-Depends: libonig5:any") +
				installed("libonig5", "Priority: optional"),
			declared: "# a comment\njq  # what it is for\n",
		},
		{
			// Either alternative of a dependency may be the one installed.
			name: "a dependency's alternative",
			status: base +
				installed("python3-venv", "Priority: optional", "Depends: python3-pip-whl | python3-setuptools-whl") +
				installed("python3-setuptools-whl", "Priority: optional"),
			declared: "python3-venv\n",
		},
		{
			// trixie's dnsutils is bind9-dnsutils, which provides it.
			name: "a declared virtual package",
			status: base +
				installed("bind9-dnsutils", "Priority: standard", "Provides: dnsutils (= 1:9.20)", "Depends: bind9-libs"),
			declared: "dnsutils\n",
		},
		{
			name: "a dependency on a virtual package",
			status: base +
				installed("git", "Priority: optional", "Depends: perl:any, git-man (>> 1:2.47)") +
				installed("perl-base", "Priority: required", "Provides: perl (= 5.40.1)") +
				installed("git-man", "Priority: optional"),
			declared: "git\n",
		},
		{
			name: "a removed package's leftover configuration",
			status: base +
				pkg("libopenexr-dev", "deinstall ok config-files", "Priority: optional"),
			declared: "",
		},
		{
			name: "an undeclared package and what it pulls in",
			status: base +
				installed("jq", "Priority: optional") +
				installed("libmagickwand-dev", "Priority: optional", "Depends: libmagickcore-dev") +
				installed("libmagickcore-dev", "Priority: optional", "Depends: libopenexr-dev, libc6") +
				installed("libopenexr-dev", "Priority: optional", "Depends: libopenexr-3-1-30 (= 3.1.13-2)") +
				installed("libopenexr-3-1-30", "Priority: optional"),
			declared: "jq\n",
			refused:  []string{"libmagickwand-dev", "libmagickcore-dev", "libopenexr-dev", "libopenexr-3-1-30"},
		},
		{
			name:     "a declared package not installed",
			status:   base + installed("jq", "Priority: optional"),
			declared: "jq\nripgrep\n",
			refused:  []string{"declared but not installed: ripgrep"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := packagesCheck(t, tc.status, tc.declared)
			if len(tc.refused) == 0 {
				if err != nil {
					t.Fatalf("refused: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want refused naming %q\n%s", tc.refused, out)
			}
			for _, want := range tc.refused {
				if !strings.Contains(out, want) {
					t.Errorf("refused with\n%s\nwant it to name %q", out, want)
				}
			}
			if strings.Contains(out, "jq") && !strings.Contains(strings.Join(tc.refused, " "), "jq") {
				t.Errorf("refused a declared package:\n%s", out)
			}
		})
	}
}

// TestImagesInstallTheirDeclaredPackages: each Dockerfile installs its apt
// packages from its declared list and fails its build on a package outside
// it, and neither starts from an image that brings undeclared packages of its
// own: buildpack-deps (rust:X-trixie, golang:X-trixie) did (#554).
func TestImagesInstallTheirDeclaredPackages(t *testing.T) {
	for _, tc := range []struct {
		file, final string
		lists       []string
	}{
		{"base", "rust:${RUST_VERSION}-slim-trixie", []string{"base"}},
		{"build", "${BASE_IMAGE}", []string{"base", "build"}},
	} {
		data, err := os.ReadFile("../images/" + tc.file + ".Dockerfile")
		if err != nil {
			t.Fatal(err)
		}
		src := strings.ReplaceAll(string(data), "\\\n", " ")
		froms := regexp.MustCompile(`(?m)^FROM (\S+)`).FindAllStringSubmatch(src, -1)
		if got := froms[len(froms)-1][1]; got != tc.final {
			t.Errorf("%s.Dockerfile: final stage FROM %s, want %s", tc.file, got, tc.final)
		}
		final := src[strings.LastIndex(src, "\nFROM "):]
		for _, l := range tc.lists {
			if !strings.Contains(final, "COPY images/"+l+".packages /usr/local/share/tacplus/packages/"+l+"\n") {
				t.Errorf("%s.Dockerfile: does not copy images/%s.packages into its final stage", tc.file, l)
			}
		}
		install := regexp.MustCompile(`[^\n]*apt-get install[^\n]*`).FindAllString(final, -1)
		if len(install) != 1 || !strings.Contains(install[0], "/usr/local/share/tacplus/packages/"+tc.file+" | xargs") {
			t.Errorf("%s.Dockerfile: its final stage installs %q; want one apt-get install, of images/%s.packages", tc.file, install, tc.file)
		}
		want := "RUN tacplus-packages-check"
		for _, l := range tc.lists {
			want += " /usr/local/share/tacplus/packages/" + l
		}
		if !strings.Contains(final, "COPY images/packages-check.sh /usr/local/bin/tacplus-packages-check\n") ||
			!strings.HasSuffix(strings.TrimSpace(lastRun(final)), strings.TrimPrefix(want, "RUN ")) {
			t.Errorf("%s.Dockerfile: its last RUN is not %q", tc.file, want)
		}
	}
}

// lastRun is the last RUN instruction of a Dockerfile's source.
func lastRun(src string) string {
	runs := regexp.MustCompile(`(?m)^RUN .*$`).FindAllString(src, -1)
	if len(runs) == 0 {
		return ""
	}
	return runs[len(runs)-1]
}
