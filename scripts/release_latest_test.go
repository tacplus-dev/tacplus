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

// releaseLatest runs release-latest.sh in the fixture repo and returns what it prints.
func releaseLatest(t *testing.T, dir, tag string) string {
	t.Helper()
	script, err := filepath.Abs("release-latest.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, tag)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release-latest.sh %s: %v\n%s", tag, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestReleaseLatest(t *testing.T) {
	// The tags on the fixture repo when the release job runs: every release
	// already pushed and the job's own tag, in push order.
	cases := []struct {
		name string
		tags []string
		ref  string
		want string
	}{
		{
			// The bug in #337: v2.4.11 was published after v2.6.1 and took Latest.
			name: "patch on an older line is not Latest",
			tags: []string{"v2.4.10", "v2.6.1", "v2.4.11"},
			ref:  "v2.4.11",
			want: "false",
		},
		{
			name: "then a patch on the newest line is",
			tags: []string{"v2.4.10", "v2.6.1", "v2.4.11", "v2.6.2"},
			ref:  "v2.6.2",
			want: "true",
		},
		{
			name: "patch on the newest minor of an older major is not Latest",
			tags: []string{"v2.6.1", "v3.0.0", "v2.6.2"},
			ref:  "v2.6.2",
			want: "false",
		},
		{
			// Parallel tags: v2.6.1's job sees v2.6.2 already pushed.
			name: "older patch of the newest line is not Latest",
			tags: []string{"v2.6.1", "v2.6.2"},
			ref:  "v2.6.1",
			want: "false",
		},
		{
			// Lexical order would put 2.9 above 2.10.
			name: "numeric, not lexical, order",
			tags: []string{"v2.9.3", "v2.10.0"},
			ref:  "v2.10.0",
			want: "true",
		},
		{
			name: "pre-release is never Latest",
			tags: []string{"v2.6.1", "v2.7.0-rc.1"},
			ref:  "v2.7.0-rc.1",
			want: "false",
		},
		{
			name: "a pre-release does not outrank a release",
			tags: []string{"v2.7.0-rc.1", "v2.6.2"},
			ref:  "v2.6.2",
			want: "true",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			r.commit("init", "README")
			for _, tg := range c.tags {
				r.tag(tg)
			}
			if got := releaseLatest(t, r.dir, c.ref); got != c.want {
				t.Errorf("latest = %q, want %q", got, c.want)
			}
		})
	}
}

// The release job publishes with the script's answer, not GitHub's default.
func TestReleaseWorkflowDecidesLatest(t *testing.T) {
	b, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`git fetch --force --tags origin`,
		`latest="$(scripts/release-latest.sh "$GITHUB_REF_NAME")"`,
		`--latest="$latest"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("release.yml does not contain %s", want)
		}
	}
}
