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

// `just generated DIR` (tacplus#376, tacplus-dev/looper#108) writes the code
// tacplus generates into DIR for the build pipeline's security checks: the
// gates job runs it after `just gates` and the security job scans DIR as the
// tree generated/. Each language's tree must be one its scanners load (a Go
// module with its go.sum, a Cargo.lock, compiled Java classes, an npm lock),
// and the recipe may not change a tracked file. It is not a gate.

// fakeGenTacplus stands in for tacplus: `build` writes a model with each
// language's manifest under tacplus/model, or fails for FAKE_FAIL (a language).
const fakeGenTacplus = `#!/usr/bin/env bash
set -euo pipefail
lang="$(sed -n 's/^language: *"\{0,1\}\([a-z]*\)"\{0,1\}$/\1/p' tacplus.yaml)"
echo "tacplus $1 $lang $PWD" >>"$FAKE_LOG"
if [[ "${FAKE_FAIL:-}" == "$lang" ]]; then echo "fake generation failed for $lang" >&2; exit 1; fi
m=tacplus/model
mkdir -p "$m"
echo '{}' >"$m/registry.json"
case "$lang" in
  go) mkdir -p "$m/chassis/x509"; echo 'package x509' >"$m/chassis/x509/x509.go" ;;
  rust) printf '[workspace]\nmembers = ["."]\n' >"$m/Cargo.toml" ;;
  java) mkdir -p "$m/src/main/java/com/x"; echo 'class X {}' >"$m/src/main/java/com/x/X.java" ;;
  python) echo '[project]' >"$m/pyproject.toml" ;;
  typescript) echo '{}' >"$m/package.json" ;;
esac
`

// The toolchain stubs: each logs its call and writes what the real one would
// leave for the scanners.
var genToolStubs = map[string]string{
	"go": `echo "go $* $PWD" >>"$FAKE_LOG"
[[ "$*" == "mod tidy" ]] && echo sum >go.sum
exit 0`,
	"cargo": `echo "cargo $* $PWD" >>"$FAKE_LOG"
[[ "$1" == generate-lockfile ]] && echo lock >Cargo.lock
exit 0`,
	"mvn": `echo "mvn $* $PWD" >>"$FAKE_LOG"
mkdir -p target/classes/com/x target/maven-status target/generated-sources
echo class >target/classes/com/x/X.class`,
	"npm": `echo "npm $* $PWD" >>"$FAKE_LOG"
echo '{}' >package-lock.json`,
}

type genRun struct {
	out, calls string
	err        error
}

func runGenerated(t *testing.T, dir string, env ...string) genRun {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tacplus"), []byte(fakeGenTacplus), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range genToolStubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(tmp, "calls.log")
	script, err := filepath.Abs("generated.sh")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{script}
	if dir != "" {
		args = append(args, dir)
	}
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TACPLUS_BIN="+filepath.Join(bin, "tacplus"),
		"TACPLUS_GENERATED_WORK="+filepath.Join(tmp, "work"),
		"FAKE_LOG="+log)
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return genRun{string(out), string(calls), runErr}
}

func trackedStatus(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=no")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGenerated(t *testing.T) {
	t.Run("writes each language's tree, loadable by its scanners", func(t *testing.T) {
		before := trackedStatus(t)
		dir := filepath.Join(t.TempDir(), "generated")
		r := runGenerated(t, dir)
		if r.err != nil {
			t.Fatalf("generated.sh: %v\n%s", r.err, r.out)
		}
		for _, want := range []string{
			// The base fixture, in every language a service or client generates.
			"base/go/go.mod",
			"base/go/go.sum",
			"base/go/tacplus/model/chassis/x509/x509.go",
			"base/rust/tacplus/model/Cargo.lock",
			"base/java/pom.xml",
			"base/python/tacplus/model/pyproject.toml",
			"base/typescript/tacplus/model/package-lock.json",
			// A full service build: servers, S2S clients, the state singleton.
			"requiresdevice/go/go.sum",
			"requiresdevice/rust/tacplus/model/Cargo.lock",
			"requiresdevice/java/target/classes/com/x/X.class",
			// gitleaks reads the tree with the generated code's allowlist.
			".gitleaks.toml",
		} {
			if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
				t.Errorf("no %s in the generated tree: %v", want, err)
			}
		}
		// Python and TypeScript are client targets: a service build refuses them.
		for _, absent := range []string{"requiresdevice/python", "requiresdevice/typescript"} {
			if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
				t.Errorf("%s was generated: a client-only target for a service build", absent)
			}
		}
		// Bytecode for SpotBugs, of one set (SpotBugs reads them all in one
		// heap), and nothing else of Maven's build.
		if entries, err := os.ReadDir(filepath.Join(dir, "requiresdevice/java/target")); err != nil || len(entries) != 1 || entries[0].Name() != "classes" {
			t.Errorf("requiresdevice/java/target holds %v (err %v), want classes only", entries, err)
		}
		if n := strings.Count(r.calls, "mvn "); n != 1 {
			t.Errorf("mvn ran %d times, want once (the requiresdevice set); calls:\n%s", n, r.calls)
		}
		if _, err := os.Stat(filepath.Join(dir, "base/java/target")); err == nil {
			t.Error("base/java was compiled: only one set's bytecode fits SpotBugs' heap")
		}
		// Each set in the languages the current-generation gate compiles it in.
		for _, absent := range []string{"c2sonly/rust", "healthonly/go", "zerorpc/java", "eventbus-backpressure/go"} {
			if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
				t.Errorf("%s was generated: the gate does not compile that set in that language", absent)
			}
		}
		// Each go.mod names the fixture's own module, so its imports resolve.
		mod, _ := os.ReadFile(filepath.Join(dir, "requiresdevice/go/go.mod"))
		if !strings.HasPrefix(string(mod), "module gitlab.com/example/dummy/vault\n") {
			t.Errorf("requiresdevice/go/go.mod: want module gitlab.com/example/dummy/vault, got:\n%s", mod)
		}
		for _, want := range []string{"go mod tidy", "cargo generate-lockfile", "mvn -q -DskipTests compile", "npm install --package-lock-only"} {
			if !strings.Contains(r.calls, want) {
				t.Errorf("generated.sh did not run %q; calls:\n%s", want, r.calls)
			}
		}
		// The logs stay out of the scanned tree.
		if logs, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*.log")); len(logs) > 0 {
			t.Errorf("logs in the scanned tree: %v", logs)
		}
		if after := trackedStatus(t); after != before {
			t.Errorf("generated.sh changed tracked files:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
	t.Run("no directory is a usage error", func(t *testing.T) {
		if r := runGenerated(t, ""); r.err == nil || !strings.Contains(r.out, "Usage") {
			t.Fatalf("generated.sh without DIR: err %v\n%s", r.err, r.out)
		}
	})
	t.Run("a directory in the checkout that git does not ignore is refused", func(t *testing.T) {
		root, err := filepath.Abs("..")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "docs", "generated-by-test")
		t.Cleanup(func() { os.RemoveAll(dir) })
		r := runGenerated(t, dir)
		if r.err == nil {
			t.Fatalf("generated.sh wrote into %s, a directory of the checkout\n%s", dir, r.out)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s exists after the refusal", dir)
		}
		if strings.Contains(r.calls, "tacplus build") {
			t.Errorf("generated.sh generated before refusing; calls:\n%s", r.calls)
		}
	})
	t.Run("a directory that is not empty is refused", func(t *testing.T) {
		dir := t.TempDir()
		keep := filepath.Join(dir, "keep.txt")
		if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if r := runGenerated(t, dir); r.err == nil {
			t.Fatalf("generated.sh wrote into a directory that was not empty\n%s", r.out)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("the directory's own file is gone: %v", err)
		}
	})
	t.Run("a jobs count that is not a positive number is refused", func(t *testing.T) {
		r := runGenerated(t, filepath.Join(t.TempDir(), "generated"), "TACPLUS_GENERATED_JOBS=0")
		if r.err == nil || !strings.Contains(r.out, "TACPLUS_GENERATED_JOBS") {
			t.Fatalf("generated.sh with TACPLUS_GENERATED_JOBS=0: err %v\n%s", r.err, r.out)
		}
	})
	t.Run("one set at a time writes the same tree", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "generated")
		if r := runGenerated(t, dir, "TACPLUS_GENERATED_JOBS=1"); r.err != nil {
			t.Fatalf("generated.sh: %v\n%s", r.err, r.out)
		}
		if _, err := os.Stat(filepath.Join(dir, "tenantslice-tenancy/java/pom.xml")); err != nil {
			t.Errorf("the last set is missing: %v", err)
		}
	})
	t.Run("a generation that fails fails it, with its log", func(t *testing.T) {
		r := runGenerated(t, filepath.Join(t.TempDir(), "generated"), "FAKE_FAIL=rust")
		if r.err == nil {
			t.Fatalf("generated.sh passed though rust generation failed\n%s", r.out)
		}
		if !strings.Contains(r.out, "fake generation failed for rust") {
			t.Errorf("the failure does not show the generation's log:\n%s", r.out)
		}
	})
}

// The recipe runs after the gates, as the gates job's own step: no gate runs
// it, and it is a call of the script (its contract lives there).
func TestGatesDoNotRunGenerated(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	recipes := justRecipes(t, filepath.Join(root, "justfile"))
	body, ok := recipes["generated"]
	if !ok {
		t.Fatal("justfile has no generated recipe: the security checks scan no generated code (looper#108)")
	}
	if !strings.Contains(body, "scripts/generated.sh") {
		t.Errorf("the generated recipe does not call scripts/generated.sh:\n%s", body)
	}
	for _, name := range reached(recipes, "gates") {
		if name == "generated" {
			t.Error("just gates reaches the generated recipe")
		}
		if strings.Contains(recipes[name], "scripts/generated.sh") {
			t.Errorf("just gates reaches recipe %s, which runs scripts/generated.sh", name)
		}
	}
}
