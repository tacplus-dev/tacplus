// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// imageTags runs images-tags.sh in the fixture repo and returns the tag list it prints.
func imageTags(t *testing.T, dir, refType, refName, sha string) string {
	t.Helper()
	script, err := filepath.Abs("images-tags.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, refType, refName, sha)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("images-tags.sh %s %s %s: %v\n%s", refType, refName, sha, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestImagesTags(t *testing.T) {
	// The tags on the fixture repo when the run computes its list: the run's own
	// tag and every release already pushed, in push order.
	cases := []struct {
		name string
		tags []string
		ref  string
		want string
	}{
		{
			name: "newest release takes every floating tag",
			tags: []string{"v2.0.0", "v2.0.1"},
			ref:  "v2.0.1",
			want: "2.0.1 2.0 2 latest",
		},
		{
			// The bug in #160: v2.0.11 was cut after v2.3.1 and moved latest (and 2)
			// back to the 2.0 line.
			name: "patch on an older minor moves only its own line",
			tags: []string{"v2.0.10", "v2.3.1", "v2.0.11"},
			ref:  "v2.0.11",
			want: "2.0.11 2.0",
		},
		{
			name: "patch on the newest minor of an older major keeps latest",
			tags: []string{"v2.3.0", "v3.0.0", "v2.3.1"},
			ref:  "v2.3.1",
			want: "2.3.1 2.3 2",
		},
		{
			// Parallel tags: v2.3.1's run sees v2.3.2 already pushed.
			name: "older patch of the same minor moves nothing",
			tags: []string{"v2.3.1", "v2.3.2"},
			ref:  "v2.3.1",
			want: "2.3.1",
		},
		{
			// Lexical order would put 2.0.9 above 2.0.10.
			name: "numeric, not lexical, order",
			tags: []string{"v2.0.9", "v2.0.10"},
			ref:  "v2.0.10",
			want: "2.0.10 2.0 2 latest",
		},
		{
			name: "a new minor takes latest",
			tags: []string{"v2.9.3", "v2.10.0"},
			ref:  "v2.10.0",
			want: "2.10.0 2.10 2 latest",
		},
		{
			// A pre-release is not a release: it never moves a floating tag, and it
			// does not hold one back from the release it precedes.
			name: "pre-release gets only its own tag",
			tags: []string{"v2.3.2", "v2.4.0-rc.1"},
			ref:  "v2.4.0-rc.1",
			want: "2.4.0-rc.1",
		},
		{
			name: "a pre-release does not outrank a release",
			tags: []string{"v2.4.0-rc.1", "v2.3.2"},
			ref:  "v2.3.2",
			want: "2.3.2 2.3 2 latest",
		},
		{
			name: "main push",
			tags: []string{"v2.3.2"},
			ref:  "",
			want: "main-abcdef1 main",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			r.commit("init", "README")
			for _, tg := range c.tags {
				r.tag(tg)
			}
			refType, refName := "tag", c.ref
			if c.ref == "" {
				refType, refName = "branch", "main"
			}
			if got := imageTags(t, r.dir, refType, refName, "abcdef1234567890"); got != c.want {
				t.Errorf("tags = %q, want %q", got, c.want)
			}
		})
	}
}
