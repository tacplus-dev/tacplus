// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every cross-test leg that builds generated Rust compiles the same heavy
// dependency set (tonic, prost, tokio, rustls). A per-leg CARGO_TARGET_DIR
// compiles it once per leg and grew target/cross-test by tens of GB in one
// run, so legs share one directory (cross-test/lib/cargo-target.sh). A leg
// that must not share says so with a "separate-cargo-target:" comment on the
// line that sets it.
func TestCrossTestLegsShareOneCargoTarget(t *testing.T) {
	scripts, err := filepath.Glob("../cross-test/*/run.sh")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no cross-test run.sh found: %v", err)
	}
	setter := regexp.MustCompile(`CARGO_TARGET_DIR=("[^"]*"|\S+)`)
	invoker := regexp.MustCompile(`\bcargo (build|test|check|run)\b`)
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		usesCargo := false
		sourcesLib := false
		for i, line := range lines {
			if strings.Contains(line, "lib/cargo-target.sh") {
				sourcesLib = true
			}
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if invoker.MatchString(line) {
				usesCargo = true
			}
			m := setter.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if m[1] == `"$CROSS_TEST_CARGO_TARGET"` || strings.Contains(line, "separate-cargo-target:") {
				continue
			}
			t.Errorf("%s:%d sets its own CARGO_TARGET_DIR %s; use \"$CROSS_TEST_CARGO_TARGET\" or justify with a separate-cargo-target: comment", path, i+1, m[1])
		}
		if usesCargo && !sourcesLib {
			t.Errorf("%s runs cargo but does not source cross-test/lib/cargo-target.sh", path)
		}
	}
}

// The shared directory is bounded by each leg removing its own crates once it
// passes (prune_cargo_workspace in cross-test/lib/cargo-target.sh). Without
// that, one gate run grew it past 100G and filled the runner's disk
// (tacplus#282). Every leg that builds Rust must call it.
func TestCrossTestLegsPruneTheirCargoWorkspace(t *testing.T) {
	scripts, err := filepath.Glob("../cross-test/*/run.sh")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no cross-test run.sh found: %v", err)
	}
	invoker := regexp.MustCompile(`\bcargo (build|test|check|run)\b`)
	pruner := regexp.MustCompile(`^\s*prune_cargo_workspace\s`)
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		usesCargo := false
		prunes := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if invoker.MatchString(line) {
				usesCargo = true
			}
			if pruner.MatchString(line) {
				prunes = true
			}
		}
		if usesCargo && !prunes {
			t.Errorf("%s runs cargo but never calls prune_cargo_workspace once its leg passes", path)
		}
	}
}

// prune_cargo_workspace removes the workspace's own crates from the shared
// directory and keeps what they depend on, so the next leg still finds its
// dependencies built.
func TestPruneCargoWorkspaceKeepsSharedDependencies(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// shared_dep stands for the registry dependencies every leg shares: it is
	// outside the leg's workspace. leg_member is the leg's own generated crate.
	write("shared_dep/Cargo.toml", "[package]\nname = \"shared_dep\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	write("shared_dep/src/lib.rs", "pub fn answer() -> u32 { 42 }\n")
	write("leg/Cargo.toml", "[package]\nname = \"leg_member\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nshared_dep = { path = \"../shared_dep\" }\n")
	write("leg/src/lib.rs", "pub fn answer() -> u32 { shared_dep::answer() }\n")
	target := filepath.Join(root, "shared-target")

	run := func(script string) string {
		t.Helper()
		lib, err := filepath.Abs("../cross-test/lib/cargo-target.sh")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", "set -euo pipefail; ROOT_DIR="+root+"; source "+lib+"; "+script)
		cmd.Dir = filepath.Join(root, "leg")
		// No sccache server of its own: TestCrossTestCargoTargetSharesDependenciesAcrossSlots tests it.
		cmd.Env = append(os.Environ(), "TACPLUS_CROSS_TEST_CARGO_TARGET="+target, "TACPLUS_CROSS_TEST_SCCACHE=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return string(out)
	}
	// Cargo before 1.100 puts a crate's rlib in debug/deps; 1.100's build-dir
	// layout puts it in debug/build/<crate>/<hash>/out (tacplus#829).
	artifacts := func(name string) []string {
		t.Helper()
		var found []string
		for _, pattern := range []string{
			filepath.Join(target, "debug", "deps", "lib"+name+"-*.rlib"),
			filepath.Join(target, "debug", "build", name, "*", "out", "lib"+name+"-*.rlib"),
		} {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				t.Fatal(err)
			}
			found = append(found, matches...)
		}
		return found
	}

	run("cargo build --offline --quiet")
	if len(artifacts("leg_member")) == 0 || len(artifacts("shared_dep")) == 0 {
		t.Fatalf("the build left no rlib to prune in %s", target)
	}
	run(`prune_cargo_workspace "$PWD"`)
	if got := artifacts("leg_member"); len(got) != 0 {
		t.Errorf("prune_cargo_workspace kept the leg's own crate: %v", got)
	}
	if len(artifacts("shared_dep")) == 0 {
		t.Errorf("prune_cargo_workspace removed the dependency the next leg shares")
	}
	// A directory without a Cargo.toml (a leg that generated no Rust) is not an error.
	run(`prune_cargo_workspace "` + root + `"`)
}

// A cross-test leg builds its generated crates once, under a path no later leg
// reuses: their incremental caches are never read, and debuginfo makes every
// test binary slower to link. cargo-target.sh turns both off for every leg that
// shares the directory (tacplus#812), unless the caller set them.
func TestCrossTestCargoTargetBuildsWithoutIncrementalOrDebuginfo(t *testing.T) {
	lib, err := filepath.Abs("../cross-test/lib/cargo-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	vars := []string{"CARGO_INCREMENTAL", "CARGO_PROFILE_DEV_DEBUG", "CARGO_PROFILE_TEST_DEBUG"}
	run := func(env ...string) string {
		t.Helper()
		script := "set -euo pipefail; ROOT_DIR=" + t.TempDir() + "; source " + lib + "; bash -c 'for v in " + strings.Join(vars, " ") + "; do echo \"$v=${!v-unset}\"; done'"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "TACPLUS_CROSS_TEST_CARGO_TARGET="+t.TempDir(), "TACPLUS_CROSS_TEST_SCCACHE=0")
		for _, v := range vars {
			cmd.Env = append(cmd.Env, v+"=")
		}
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	// An empty value is unset to cargo too.
	out := run()
	for _, v := range vars {
		if !strings.Contains(out, v+"=0\n") {
			t.Errorf("%s is not 0 for the legs:\n%s", v, out)
		}
	}
	if out := run("CARGO_INCREMENTAL=1"); !strings.Contains(out, "CARGO_INCREMENTAL=1\n") {
		t.Errorf("the caller's CARGO_INCREMENTAL was overridden:\n%s", out)
	}
}

// fakeSccache puts an sccache on a new PATH that records each call's
// arguments and the CARGO_TARGET_DIR it saw, one line a call, in the returned
// log, and returns that PATH.
func fakeSccache(t *testing.T) (path, log string) {
	t.Helper()
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "sccache.log")
	script := "#!/usr/bin/env bash\necho \"CARGO_TARGET_DIR=${CARGO_TARGET_DIR-unset} $*\" >>" + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "sccache"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin + string(os.PathListSeparator) + os.Getenv("PATH"), log
}

// cargoTargetEnv sources cross-test/lib/cargo-target.sh with ROOT_DIR root
// and env, runs script after it, and returns its output.
func cargoTargetEnv(t *testing.T, root, script string, env ...string) string {
	t.Helper()
	lib, err := filepath.Abs("../cross-test/lib/cargo-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail; ROOT_DIR="+root+"; source "+lib+"; "+script)
	cmd.Env = append(os.Environ(), "TACPLUS_CROSS_TEST_CARGO_TARGET="+t.TempDir(),
		"RUSTC_WRAPPER=", "SCCACHE_DIR=", "TACPLUS_CROSS_TEST_SCCACHE=")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// Cargo locks a target directory for a whole build, so the job slots of a
// gate run each build in their own; until tacplus#979 every Rust shard but
// rust/base started from a copy of the several GB rust/base built. Now every
// leg's rustc goes through sccache, when it is on PATH, with one cache
// directory for them all: a dependency compiled in one slot is a cache hit in
// the next. sccache keys a compile on the CARGO_ variables of its
// environment, and each slot's CARGO_TARGET_DIR differs, so the wrapper
// drops it: rustc never reads it.
func TestCrossTestCargoTargetCompilesThroughSccache(t *testing.T) {
	root := t.TempDir()
	path, log := fakeSccache(t)
	show := `echo "RUSTC_WRAPPER=${RUSTC_WRAPPER-unset}"; echo "SCCACHE_DIR=${SCCACHE_DIR-unset}"`

	out := cargoTargetEnv(t, root, show+`; CARGO_TARGET_DIR=/slot1 "$RUSTC_WRAPPER" rustc --crate-name dep`, "PATH="+path)
	wrapper, err := filepath.Abs("../cross-test/lib/sccache-rustc.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "RUSTC_WRAPPER="+wrapper+"\n") {
		t.Errorf("with sccache on PATH, RUSTC_WRAPPER is not %s:\n%s", wrapper, out)
	}
	if !strings.Contains(out, "SCCACHE_DIR="+filepath.Join(root, "target", "cross-test", "sccache")+"\n") {
		t.Errorf("sccache's cache is not the one directory under target/cross-test:\n%s", out)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "CARGO_TARGET_DIR=unset rustc --crate-name dep\n") {
		t.Errorf("the wrapper did not run sccache rustc without the slot's CARGO_TARGET_DIR:\n%s", calls)
	}
	// The server starts once, before any cargo: started by a rustc cargo ran,
	// it would take that cargo's jobserver.
	if !strings.Contains(string(calls), "--start-server") {
		t.Errorf("the lib did not start sccache's server:\n%s", calls)
	}

	// The build's own cache (tacplus-cache env: RUSTC_WRAPPER=sccache and its
	// SCCACHE_ settings) is kept, through the wrapper.
	out = cargoTargetEnv(t, root, show, "PATH="+path, "RUSTC_WRAPPER=sccache", "SCCACHE_DIR=/cache/sccache")
	if !strings.Contains(out, "RUSTC_WRAPPER="+wrapper+"\n") || !strings.Contains(out, "SCCACHE_DIR=/cache/sccache\n") {
		t.Errorf("the build's sccache configuration was not kept:\n%s", out)
	}
	// Another wrapper, no sccache, or TACPLUS_CROSS_TEST_SCCACHE=0: no sccache.
	if out := cargoTargetEnv(t, root, show, "PATH="+path, "RUSTC_WRAPPER=/bin/other"); !strings.Contains(out, "RUSTC_WRAPPER=/bin/other\n") {
		t.Errorf("the caller's own RUSTC_WRAPPER was replaced:\n%s", out)
	}
	if out := cargoTargetEnv(t, root, show, "PATH="+path, "TACPLUS_CROSS_TEST_SCCACHE=0"); strings.Contains(out, "sccache-rustc.sh") {
		t.Errorf("TACPLUS_CROSS_TEST_SCCACHE=0 still compiles through sccache:\n%s", out)
	}
	if out := cargoTargetEnv(t, root, show, "PATH="+t.TempDir()+string(os.PathListSeparator)+"/usr/bin:/bin"); strings.Contains(out, "sccache-rustc.sh") {
		t.Errorf("without sccache on PATH, RUSTC_WRAPPER is the wrapper:\n%s", out)
	}
}

// The point of the wrapper, with the real sccache: a dependency built in one
// slot's target directory is a cache hit in another's.
func TestCrossTestCargoTargetSharesDependenciesAcrossSlots(t *testing.T) {
	for _, tool := range []string{"cargo", "sccache"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shared_dep/Cargo.toml", "[package]\nname = \"shared_dep\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	write("shared_dep/src/lib.rs", "pub fn answer() -> u32 { 42 }\n")
	write("leg/Cargo.toml", "[package]\nname = \"leg_member\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nshared_dep = { path = \"../shared_dep\" }\n")
	write("leg/src/lib.rs", "pub fn answer() -> u32 { shared_dep::answer() }\n")
	// A server of this test's own, on a port of its own, stopped after it.
	port := freePort(t)
	env := []string{"SCCACHE_SERVER_PORT=" + port, "SCCACHE_DIR=" + filepath.Join(root, "sccache")}
	t.Cleanup(func() {
		cmd := exec.Command("sccache", "--stop-server")
		cmd.Env = append(os.Environ(), env...)
		_ = cmd.Run()
	})
	hits := func(slot string) string {
		t.Helper()
		out := cargoTargetEnv(t, root, `cd "$ROOT_DIR/leg" && sccache --zero-stats >/dev/null && cargo build --offline --quiet && sccache --show-stats`,
			append(env, "TACPLUS_CROSS_TEST_CARGO_TARGET="+filepath.Join(root, slot))...)
		for _, line := range strings.Split(out, "\n") {
			if fields := strings.Fields(line); len(fields) == 3 && fields[0] == "Cache" && fields[1] == "hits" {
				return fields[2]
			}
		}
		t.Fatalf("no cache hits line in sccache's stats:\n%s", out)
		return ""
	}
	hits("cargo-slot1")
	if got := hits("cargo-slot2"); got == "0" {
		t.Errorf("the second slot's build had no sccache hits: its dependency was compiled again")
	}
}

// freePort returns a TCP port on 127.0.0.1 nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}
