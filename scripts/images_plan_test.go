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

// repo is a throwaway git history built one commit at a time.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q")
	return r
}

func (r *repo) git(args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commit writes each file (contents: the message) and commits them.
func (r *repo) commit(msg string, files ...string) {
	r.t.Helper()
	for _, f := range files {
		p := filepath.Join(r.dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(msg), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.git("add", f)
	}
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

func (r *repo) tag(name string) {
	r.git("tag", name)
}

// plan runs images-plan.sh in the fixture repo and returns its "<base> <build>" output.
func plan(t *testing.T, dir, event, refType, rebuild string) string {
	t.Helper()
	script, err := filepath.Abs("images-plan.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, event, refType, rebuild)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("images-plan.sh %s %s %s: %v\n%s", event, refType, rebuild, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestImagesPlan(t *testing.T) {
	cases := []struct {
		name    string
		history func(r *repo) // builds the history; the last commit is what the run is for
		event   string
		ref     string
		rebuild string
		want    string
	}{
		{
			name: "main push, build.Dockerfile changed in the pushed commit",
			history: func(r *repo) {
				r.commit("init", "README")
				r.commit("edit build", "images/build.Dockerfile")
			},
			event: "push", ref: "branch", want: "false true",
		},
		{
			name: "main push, base.Dockerfile changed in the pushed commit",
			history: func(r *repo) {
				r.commit("init", "README")
				r.commit("edit base", "images/base.Dockerfile")
			},
			event: "push", ref: "branch", want: "true true",
		},
		{
			name: "main push, nothing under images/ changed",
			history: func(r *repo) {
				r.commit("init", "README")
				r.commit("docs", "README")
			},
			event: "push", ref: "branch", want: "false false",
		},
		{
			// The bug in #22: a backport changed build.Dockerfile on the release
			// line, and the tag was cut on a later commit. Only the previous
			// release tag can show the change.
			name: "tag, build.Dockerfile changed by a commit before the tag's own",
			history: func(r *repo) {
				r.commit("init", "README")
				r.tag("v2.0.0")
				r.commit("backport", "images/build.Dockerfile")
				r.commit("release notes", "README")
				r.tag("v2.0.1")
			},
			event: "push", ref: "tag", want: "false true",
		},
		{
			name: "tag, base.Dockerfile changed by a commit before the tag's own",
			history: func(r *repo) {
				r.commit("init", "README")
				r.tag("v2.0.0")
				r.commit("backport", "images/base.Dockerfile")
				r.commit("release notes", "README")
				r.tag("v2.0.1")
			},
			event: "push", ref: "tag", want: "true true",
		},
		{
			name: "tag, image change already shipped in the previous tag",
			history: func(r *repo) {
				r.commit("init", "images/build.Dockerfile")
				r.tag("v2.0.0")
				r.commit("release notes", "README")
				r.tag("v2.0.1")
			},
			event: "push", ref: "tag", want: "false false",
		},
		{
			// No earlier release on this line to diff against: rebuild everything.
			name: "first tag, nothing to diff against",
			history: func(r *repo) {
				r.commit("init", "README")
				r.commit("build", "images/build.Dockerfile")
				r.tag("v2.0.0")
			},
			event: "push", ref: "tag", want: "true true",
		},
		{
			name: "schedule rebuilds base and build",
			history: func(r *repo) {
				r.commit("init", "README")
			},
			event: "schedule", ref: "branch", want: "true true",
		},
		{
			name: "manual run, rebuild=tacplus",
			history: func(r *repo) {
				r.commit("init", "README")
			},
			event: "workflow_dispatch", ref: "branch", rebuild: "tacplus", want: "false false",
		},
		{
			name: "manual run, rebuild=build",
			history: func(r *repo) {
				r.commit("init", "README")
			},
			event: "workflow_dispatch", ref: "branch", rebuild: "build", want: "false true",
		},
		{
			name: "manual run, rebuild=base",
			history: func(r *repo) {
				r.commit("init", "README")
			},
			event: "workflow_dispatch", ref: "branch", rebuild: "base", want: "true true",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			c.history(r)
			if got := plan(t, r.dir, c.event, c.ref, c.rebuild); got != c.want {
				t.Errorf("plan = %q, want %q", got, c.want)
			}
		})
	}
}
