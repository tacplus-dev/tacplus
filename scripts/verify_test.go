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

// Formal verification (Kani, JBMC) is not a gate (tacplus#275): it runs in
// `just verify`, which the build pipeline runs on every release tag and nightly
// when something it depends on changed. Nothing `just gates` reaches may run it.

var (
	recipeHeader = regexp.MustCompile(`^([a-z][a-z0-9-]*)( [^:=]*)?:([^=].*)?$`)
	justCall     = regexp.MustCompile(`\bjust "?\$?([a-z][a-z0-9-]*)`)
	gateLoop     = regexp.MustCompile(`for g in ([a-z0-9 -]+); do`)
	scriptRef    = regexp.MustCompile(`[A-Za-z0-9_./-]+\.sh\b`)
	// What runs a verifier: the verifiers themselves, `tacplus verify` (a
	// built binary or the launcher), and the verify recipe and its script.
	verifierRun = regexp.MustCompile(`cargo kani|\bjbmc\b|tacplus"? verify\b|TACPLUS_BIN"? verify\b|scripts/verify\.sh|\bjust verify\b`)
)

// justRecipes reads the justfile's recipes: name -> header dependencies and body.
func justRecipes(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	recipes := map[string]string{}
	name := ""
	for _, line := range strings.Split(string(data), "\n") {
		if m := recipeHeader.FindStringSubmatch(line); m != nil {
			name = m[1]
			recipes[name] = m[3] + "\n"
			continue
		}
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			name = ""
			continue
		}
		if name != "" {
			recipes[name] += line + "\n"
		}
	}
	return recipes
}

// reached returns every recipe `start` runs: its dependencies, the recipes its
// body calls with `just`, and the gate list of the gates loop.
func reached(recipes map[string]string, start string) []string {
	seen := map[string]bool{}
	var order []string
	var walk func(string)
	walk = func(name string) {
		body, ok := recipes[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		order = append(order, name)
		header := strings.SplitN(body, "\n", 2)[0]
		for _, dep := range strings.Fields(header) {
			walk(dep)
		}
		for _, m := range justCall.FindAllStringSubmatch(body, -1) {
			walk(m[1])
		}
		if m := gateLoop.FindStringSubmatch(body); m != nil {
			for _, g := range strings.Fields(m[1]) {
				walk(g)
			}
		}
	}
	walk(start)
	return order
}

func TestGatesRunNoFormalVerification(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	recipes := justRecipes(t, filepath.Join(root, "justfile"))
	if _, ok := recipes["verify"]; !ok {
		t.Fatal("justfile has no verify recipe: formal verification has no entry point")
	}
	gates := reached(recipes, "gates")
	if len(gates) < 10 {
		t.Fatalf("read only %d recipes from gates (%v): the justfile parser is out of step with the justfile", len(gates), gates)
	}
	for _, name := range gates {
		if name == "verify" || name == "verify-changed" {
			t.Errorf("just gates reaches the %s recipe", name)
		}
		body := recipes[name]
		if m := verifierRun.FindString(body); m != "" {
			t.Errorf("just gates reaches recipe %s, which runs formal verification (%q)", name, m)
		}
		for _, ref := range scriptRef.FindAllString(body, -1) {
			data, err := os.ReadFile(filepath.Join(root, ref))
			if err != nil {
				continue // not a repository path (a here-doc name, a variable)
			}
			if m := verifierRun.FindString(string(data)); m != "" {
				t.Errorf("just gates reaches %s (recipe %s), which runs formal verification (%q)", ref, name, m)
			}
		}
	}
}

// verifyChanged runs verify-changed.sh in dir and returns its stdout.
func verifyChanged(t *testing.T, dir string, args ...string) string {
	t.Helper()
	script, err := filepath.Abs("verify-changed.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("verify-changed.sh %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) head() string {
	r.t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestVerifyChanged(t *testing.T) {
	cases := []struct {
		name    string
		changed []string // files changed after the verified commit
		want    string   // "run" or "skip"
	}{
		{"nothing changed", nil, "skip"},
		{"docs only", []string{"docs/USER.md", "README.md", "STATE.md"}, "skip"},
		{"another gate's cross-test", []string{"cross-test/jwk-alg/run.sh"}, "skip"},
		{"the justfile", []string{"justfile"}, "skip"},
		{"the generator", []string{"pkg/internal/rust/types/proof.go"}, "run"},
		{"a template", []string{"pkg/internal/java/types/templates/x.tmpl"}, "run"},
		{"an embedded proto", []string{"pkg/proto/sys/spec.proto"}, "run"},
		{"the verified fixture's protos", []string{"cross-test/proto/services2/x.proto"}, "run"},
		{"the fixture's dependency pins", []string{"cross-test/dependencies.yaml"}, "run"},
		{"the verify command", []string{"cmd/verify/cmd.go"}, "run"},
		{"the compiler's dependencies", []string{"go.mod"}, "run"},
		{"the compiler entry", []string{"main.go"}, "run"},
		{"the toolchain pins", []string{"images/build.Dockerfile"}, "run"},
		{"the base image", []string{"images/base.Dockerfile"}, "run"},
		{"the verify script", []string{"scripts/verify.sh"}, "run"},
		{"the change detection", []string{"scripts/verify-changed.sh"}, "run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			r.commit("base", "README.md", "pkg/a.go", "cross-test/proto/a.proto")
			verified := r.head()
			r.commit("after", c.changed...)
			got := verifyChanged(t, r.dir, verified)
			switch c.want {
			case "run":
				if got != "run" {
					t.Fatalf("changed %v: got %q, want run", c.changed, got)
				}
			case "skip":
				if want := "skip: unchanged since " + verified + ", verified then"; got != want {
					t.Fatalf("changed %v: got %q, want %q", c.changed, got, want)
				}
			}
		})
	}
}

func TestVerifyChangedRunsWhenInDoubt(t *testing.T) {
	r := newRepo(t)
	r.commit("base", "README.md")
	verified := r.head()
	r.commit("after", "docs/x.md")
	for name, args := range map[string][]string{
		"no record":                nil,
		"an empty record":          {""},
		"a sha this history lacks": {"0123456789012345678901234567890123456789"},
		"not a commit":             {"--not-a-sha"},
		"too many arguments":       {verified, verified},
	} {
		t.Run(name, func(t *testing.T) {
			if got := verifyChanged(t, r.dir, args...); got != "run" {
				t.Fatalf("args %q: got %q, want run", args, got)
			}
		})
	}
	t.Run("outside a git checkout", func(t *testing.T) {
		if got := verifyChanged(t, t.TempDir(), verified); got != "run" {
			t.Fatalf("got %q, want run", got)
		}
	})
	t.Run("an uncommitted change", func(t *testing.T) {
		head := r.head()
		if err := os.MkdirAll(filepath.Join(r.dir, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.dir, "pkg", "new.go"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := verifyChanged(t, r.dir, head); got != "run" {
			t.Fatalf("an untracked pkg/new.go: got %q, want run", got)
		}
	})
}

// fakeTacplus is a tacplus stand-in for verify.sh: `build` records the config
// it generated from, and `verify` writes a verification report, or fails as
// FAKE_VERIFY_FAIL (a language) says, or writes none for FAKE_VERIFY_NONE.
const fakeTacplus = `#!/usr/bin/env bash
set -euo pipefail
lang="$(sed -n 's/^language: *"\{0,1\}\([a-z]*\)"\{0,1\}$/\1/p' tacplus.yaml)"
echo "$1 $lang $PWD" >>"$FAKE_LOG"
case "$1" in
  build) mkdir -p tacplus ;;
  verify)
    [[ "${FAKE_VERIFY_NONE:-}" == "$lang" ]] && exit 0
    if [[ "${FAKE_VERIFY_FAIL:-}" == "$lang" ]]; then
      printf '# Formal Verification Report\n### harness_%s\n**Status:** FAILED\ncounterexample\n' "$lang" >verification-report.md
      echo "verification failed: 1 failed" >&2
      exit 1
    fi
    printf '# Formal Verification Report\n**Status:** SUCCESS\n' >verification-report.md ;;
esac
`

func runVerify(t *testing.T, env []string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(bin, []byte(fakeTacplus), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "calls.log")
	script, err := filepath.Abs("verify.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "TACPLUS_BIN="+bin, "FAKE_LOG="+log, "TACPLUS_VERIFY_WORK="+filepath.Join(dir, "work"))
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return string(out), string(calls), runErr
}

func TestVerify(t *testing.T) {
	t.Run("generates and verifies rust and java", func(t *testing.T) {
		out, calls, err := runVerify(t, nil)
		if err != nil {
			t.Fatalf("verify.sh: %v\n%s", err, out)
		}
		for _, want := range []string{"build rust ", "verify rust ", "build java ", "verify java "} {
			if !strings.Contains(calls, want) {
				t.Errorf("verify.sh did not run %q; calls:\n%s", want, calls)
			}
		}
		if strings.Contains(calls, " go ") {
			t.Errorf("verify.sh verified Go, which is not formally verified by design; calls:\n%s", calls)
		}
	})
	t.Run("one language", func(t *testing.T) {
		out, calls, err := runVerify(t, nil, "java")
		if err != nil {
			t.Fatalf("verify.sh java: %v\n%s", err, out)
		}
		if strings.Contains(calls, "rust") {
			t.Errorf("verify.sh java ran rust; calls:\n%s", calls)
		}
	})
	t.Run("a failed harness fails it and is reported, and the other language still runs", func(t *testing.T) {
		out, calls, err := runVerify(t, []string{"FAKE_VERIFY_FAIL=rust"})
		if err == nil {
			t.Fatalf("verify.sh passed with a failed rust harness\n%s", out)
		}
		if !strings.Contains(out, "harness_rust") || !strings.Contains(out, "counterexample") {
			t.Errorf("the failure does not show the report (harness, counterexample):\n%s", out)
		}
		if !strings.Contains(calls, "verify java ") {
			t.Errorf("a rust failure stopped java; calls:\n%s", calls)
		}
	})
	t.Run("no harness verified fails it", func(t *testing.T) {
		out, _, err := runVerify(t, []string{"FAKE_VERIFY_NONE=java"})
		if err == nil || !strings.Contains(out, "no harness") {
			t.Fatalf("verify.sh passed though java verified no harness (err %v)\n%s", err, out)
		}
	})
	t.Run("an unknown language is refused", func(t *testing.T) {
		if out, _, err := runVerify(t, nil, "go"); err == nil {
			t.Fatalf("verify.sh go passed\n%s", out)
		}
	})
}
