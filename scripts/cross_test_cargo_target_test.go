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
		cmd.Env = append(os.Environ(), "TACPLUS_CROSS_TEST_CARGO_TARGET="+target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return string(out)
	}
	artifacts := func(name string) []string {
		t.Helper()
		found, err := filepath.Glob(filepath.Join(target, "debug", "deps", "lib"+name+"-*.rlib"))
		if err != nil {
			t.Fatal(err)
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
