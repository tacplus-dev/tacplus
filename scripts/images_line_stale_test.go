// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// The line's Dockerfiles in the fixture: global ARG defaults, a --platform
// flag, stage names and the line's own base image, which is no upstream.
const (
	staleBaseDockerfile = `# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27
ARG RUST_VERSION=1
FROM golang:${GO_VERSION}-trixie AS go
FROM rust:${RUST_VERSION}-slim-trixie
ARG GO_VERSION
COPY --from=go /usr/local/go /usr/local/go
`
	staleBuildDockerfile = `ARG BASE_IMAGE=ghcr.io/x/tacplus/base:latest
ARG DOCKER_VERSION=29
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS tools
FROM docker:${DOCKER_VERSION}-cli AS docker-cli
FROM tools AS more
FROM ${BASE_IMAGE}
COPY images/smoke.sh /usr/local/bin/tacplus-smoke
`
)

// The upstream images the fixture's Dockerfiles start from.
var staleUpstream = []string{"docker:29-cli", "golang:1.27-trixie", "rust:1-slim-trixie"}

// The line's last successful rebuild: 2026-11-22T05:30:12Z.
const staleLastRun = "2026-11-22T05:30:12Z"

type staleFixture struct {
	manifests []string          // refs `docker manifest inspect` finds
	created   map[string]string // upstream ref: its linux/amd64 config's created
	runs      string            // `gh run list --json createdAt,headSha` output; %s is the rebuild's commit
	apt       string            // `apt-get -s upgrade` in build:2.6
	change    bool              // images/ changed on the branch after the rebuild
}

// lineStale runs images-line-stale.sh for release/2.6 in a fixture repository,
// with a fake docker and gh. It returns the script's output and the upstream
// refs it inspected.
func lineStale(t *testing.T, f staleFixture) (string, []string, error) {
	t.Helper()
	script, err := filepath.Abs("images-line-stale.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	fake := filepath.Join(dir, "fake")
	for _, d := range []string{filepath.Join(repo, "images"), filepath.Join(fake, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "release/2.6")
	write(filepath.Join(repo, "images", "base.Dockerfile"), staleBaseDockerfile, 0o644)
	write(filepath.Join(repo, "images", "build.Dockerfile"), staleBuildDockerfile, 0o644)
	write(filepath.Join(repo, "README.md"), "line 2.6\n", 0o644)
	git("add", ".")
	git("commit", "-qm", "the line's rebuild")
	rebuilt := git("rev-parse", "HEAD")
	// A commit outside images/ since the rebuild changes no image.
	write(filepath.Join(repo, "README.md"), "line 2.6, a backport\n", 0o644)
	if f.change {
		write(filepath.Join(repo, "images", "build.Dockerfile"), staleBuildDockerfile+"RUN true\n", 0o644)
	}
	git("commit", "-qam", "a backport")
	git("update-ref", "refs/remotes/origin/release/2.6", "HEAD")
	head := git("rev-parse", "HEAD")

	for i, m := range f.manifests {
		f.manifests[i] = strings.ReplaceAll(m, "HEAD7", head[:7])
	}
	write(filepath.Join(fake, "manifests"), strings.Join(f.manifests, "\n")+"\n", 0o644)
	write(filepath.Join(fake, "apt"), f.apt, 0o644)
	write(filepath.Join(fake, "runs.json"), strings.ReplaceAll(f.runs, "%s", rebuilt), 0o644)
	for ref, created := range f.created {
		write(filepath.Join(fake, strings.ReplaceAll(ref, "/", "_")+".json"),
			`{"linux/amd64":{"created":"`+created+`"},"linux/arm64":{"created":"2020-01-01T00:00:00Z"}}`, 0o644)
	}
	write(filepath.Join(fake, "bin", "docker"), `#!/usr/bin/env bash
fake="$(dirname "$0")/.."
case "$1 $2" in
  "manifest inspect") grep -qxF "$3" "$fake/manifests" ;;
  "buildx imagetools")
    [[ "$3 $5 $6" == "inspect --format {{json .Image}}" ]] || { echo "unexpected: docker $*" >&2; exit 2; }
    echo "$4" >> "$fake/inspected"
    cat "$fake/${4//\//_}.json" ;;
  "run --rm")
    [[ "$*" == "run --rm --pull always --user 0 --entrypoint sh ghcr.io/x/tacplus/build:2.6 "* ]] || { echo "unexpected: docker $*" >&2; exit 2; }
    cat "$fake/apt" ;;
  "rmi "*) ;;
  *) echo "unexpected: docker $*" >&2; exit 2 ;;
esac
`, 0o755)
	write(filepath.Join(fake, "bin", "gh"), `#!/usr/bin/env bash
fake="$(dirname "$0")/.."
[[ "$1 $2 $3 $4" == "run list --repo x/tacplus" ]] || { echo "unexpected: gh $*" >&2; exit 2; }
[[ " $* " == *" --workflow images.yml --branch release/2.6 --status success "* ]] || { echo "unexpected: gh $*" >&2; exit 2; }
while [[ $# -gt 0 && $1 != --jq ]]; do shift; done
jq -r "$2" "$fake/runs.json"
`, 0o755)

	cmd := exec.Command("bash", script, "release/2.6")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(fake, "bin")+":"+os.Getenv("PATH"),
		"NS=ghcr.io/x/tacplus", "GITHUB_REPOSITORY=x/tacplus")
	out, err := cmd.CombinedOutput()
	var inspected []string
	if data, rerr := os.ReadFile(filepath.Join(fake, "inspected")); rerr == nil {
		inspected = strings.Fields(string(data))
		sort.Strings(inspected)
	}
	return strings.TrimSpace(string(out)), inspected, err
}

// TestImagesLineStale: the nightly line rebuild (#966) builds a line only
// when something its images are made of is newer than its last rebuild: its
// images/ on the branch, an upstream image it starts from, or a Debian
// package. Otherwise it publishes nothing, so a dated tag means a new image.
func TestImagesLineStale(t *testing.T) {
	today := time.Now().UTC().Format("20060102")
	older := map[string]string{
		"golang:1.27-trixie": "2026-11-20T01:02:03.123456Z",
		"rust:1-slim-trixie": "2026-11-01T00:00:00Z",
		"docker:29-cli":      "2026-11-21T23:59:59Z",
	}
	with := func(ref, created string) map[string]string {
		m := map[string]string{}
		for k, v := range older {
			m[k] = v
		}
		m[ref] = created
		return m
	}
	runs := `[{"createdAt":"` + staleLastRun + `","headSha":"%s"}]`
	noUpgrade := "Reading package lists...\nBuilding dependency tree...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	line := []string{"ghcr.io/x/tacplus/build:2.6"}
	for _, tc := range []struct {
		name      string
		f         staleFixture
		want      string   // the output's prefix
		inspected []string // nil: not checked
	}{
		{
			name:      "nothing new since the last rebuild",
			f:         staleFixture{manifests: line, created: older, runs: runs, apt: noUpgrade},
			want:      "current: nothing new since the line's rebuild of " + staleLastRun,
			inspected: staleUpstream,
		},
		{
			name: "a Debian package to upgrade",
			f: staleFixture{manifests: line, created: older, runs: runs, apt: "Inst libkrb5-3 [1.21.3-5] (1.21.3-5+deb13u1 Debian-Security:13/stable-security [amd64])\n" +
				"Inst libgssapi-krb5-2 [1.21.3-5] (1.21.3-5+deb13u1 Debian-Security:13/stable-security [amd64])\n" +
				"Conf libkrb5-3 (1.21.3-5+deb13u1 Debian-Security:13/stable-security [amd64])\n"},
			want: "rebuild: Debian packages to upgrade in ghcr.io/x/tacplus/build:2.6: libkrb5-3 libgssapi-krb5-2",
		},
		{
			name: "an upstream image updated since",
			f:    staleFixture{manifests: line, created: with("docker:29-cli", "2026-11-22T06:00:00Z"), runs: runs, apt: noUpgrade},
			want: "rebuild: docker:29-cli was updated 2026-11-22T06:00:00Z",
		},
		{
			name: "images/ changed on the branch since",
			f:    staleFixture{manifests: line, created: older, runs: runs, apt: noUpgrade, change: true},
			want: "rebuild: images/ changed on release/2.6 since",
		},
		{
			name: "no successful line rebuild yet",
			f:    staleFixture{manifests: line, created: older, runs: `[]`, apt: noUpgrade},
			want: "rebuild: no line rebuild of release/2.6 has succeeded yet",
		},
		{
			name: "no build image of the line yet",
			f:    staleFixture{created: older, runs: runs, apt: noUpgrade},
			want: "rebuild: ghcr.io/x/tacplus/build:2.6 does not exist",
		},
		{
			// The dated tag is immutable: a second rebuild of the commit the same
			// day would push it again.
			name: "today's dated tag already published",
			f: staleFixture{manifests: append([]string{"ghcr.io/x/tacplus/build:" + today + "-HEAD7"}, line...), created: with("docker:29-cli", "2026-11-22T06:00:00Z"),
				runs: runs, apt: "Inst libkrb5-3 [1.21.3-5] (1.21.3-5+deb13u1 Debian-Security:13/stable-security [amd64])\n"},
			want: "current: ghcr.io/x/tacplus/build:" + today + "-",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, inspected, err := lineStale(t, tc.f)
			if err != nil {
				t.Fatalf("images-line-stale.sh: %v\n%s", err, out)
			}
			if !strings.HasPrefix(out, tc.want) || strings.Contains(out, "\n") {
				t.Errorf("got\n%s\nwant one line starting\n%s", out, tc.want)
			}
			if tc.inspected != nil && strings.Join(inspected, " ") != strings.Join(tc.inspected, " ") {
				t.Errorf("inspected %v, want %v", inspected, tc.inspected)
			}
		})
	}
}
