// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// currentGeneration runs cross-test/current-generation/run.sh with args and
// returns its combined output and whether it exited zero.
func currentGeneration(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"../cross-test/current-generation/run.sh"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	return string(out), err == nil
}

// current-generation was the longest gate, 1767 s of a 46-minute `just gates`
// on its own pool in one slot (tacplus#977). It lists its shards, every
// language of every fixture, each build of a fixture of two (its longest
// shards when they were one), and the checks across them, for scripts/gates.sh
// to run each as a leg of its own: rust/base first, since every other Rust
// shard reuses the chassis self-tests it ran, those next, to start as soon
// as it passed, and checks last, after every other, since it reads their main
// fixtures and clears the work root.
func TestCurrentGenerationListsItsShards(t *testing.T) {
	out, ok := currentGeneration(t, "--list-shards")
	if !ok {
		t.Fatalf("--list-shards failed:\n%s", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 40 {
		t.Fatalf("--list-shards listed %d shards, not every language of every fixture:\n%s", len(lines), out)
	}
	if lines[0] != "rust/base" {
		t.Errorf("the first shard is %q, not rust/base, with no dependency:\n%s", lines[0], out)
	}
	var names []string
	for _, line := range lines[:len(lines)-1] {
		fields := strings.Fields(line)
		name, deps := fields[0], fields[1:]
		if slices.Contains(names, name) {
			t.Errorf("shard %s is listed twice:\n%s", name, out)
		}
		language, fixture, ok := strings.Cut(name, "/")
		if !ok || fixture == "" || !slices.Contains([]string{"go", "java", "rust", "typescript"}, language) {
			t.Errorf("shard %q is not LANGUAGE/FIXTURE[/SIDE]", name)
		}
		if language == "rust" && len(names) > 0 && !strings.HasPrefix(names[len(names)-1], "rust/") {
			t.Errorf("Rust shard %s is listed after another language's: it would start after them", name)
		}
		want := []string{}
		if language == "rust" && fixture != "base" {
			want = []string{"rust/base"}
		}
		if !slices.Equal(deps, want) {
			t.Errorf("shard %s depends on %v, not %v", name, deps, want)
		}
		names = append(names, name)
	}
	for _, want := range []string{"go/base", "java/base", "typescript/base", "go/tenantrecord/tstratum", "java/tenantrecord/tmessaging", "rust/tenantroute/harbor", "rust/tenantroute/crew", "rust/redo/redo", "go/c2sonly", "rust/healthonly"} {
		if !slices.Contains(names, want) {
			t.Errorf("no shard %s:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"java/c2sonly", "go/healthonly", "rust/eventbus-backpressure", "typescript/tenantrecord", "rust/tenantroute", "typescript/healthonly"} {
		if slices.Contains(names, unwanted) {
			t.Errorf("shard %s, a language its fixture does not build or a fixture of two builds whole:\n%s", unwanted, out)
		}
	}
	if last := strings.Fields(lines[len(lines)-1]); last[0] != "checks" || !slices.Equal(last[1:], names) {
		t.Errorf("the last shard is %q, not checks after every other shard:\n%s", lines[len(lines)-1], out)
	}

	// One language lists its own shards alone.
	out, ok = currentGeneration(t, "--list-shards", "--language", "go")
	if !ok || strings.Contains(out, "java/") || strings.Contains(out, "rust/") || !strings.HasPrefix(out, "go/base\n") {
		t.Errorf("--list-shards --language go lists other languages' shards:\n%s", out)
	}
}

// A shard the run does not list is refused, before anything is built.
func TestCurrentGenerationRefusesAnUnknownShard(t *testing.T) {
	for _, shard := range []string{"go/nope", "java/c2sonly", "nope", "rust/redo/harbor"} {
		out, ok := currentGeneration(t, "--shard", shard)
		if ok || !strings.Contains(out, "unknown shard: "+shard) {
			t.Errorf("--shard %s was not refused as unknown:\n%s", shard, out)
		}
	}
	if out, ok := currentGeneration(t, "--shard", "go/base", "--language", "go"); ok || !strings.Contains(out, "--shard runs one shard") {
		t.Errorf("--shard with --language was not refused:\n%s", out)
	}
	if out, ok := currentGeneration(t, "--fixture", "redo", "--side", "harbor"); ok || !strings.Contains(out, "--side harbor is not a build of --fixture redo") {
		t.Errorf("--side naming no build of its fixture was not refused:\n%s", out)
	}
}

// `just gates` reaches current-generation's shards through the recipes
// scripts/gates.sh looks for.
func TestCurrentGenerationShardRecipes(t *testing.T) {
	recipes := justRecipes(t, "../justfile")
	if !strings.Contains(recipes["current-generation-shards"], "cross-test/current-generation/run.sh --list-shards") {
		t.Errorf("no current-generation-shards recipe listing the shards:\n%s", recipes["current-generation-shards"])
	}
	if !strings.Contains(recipes["current-generation-shard"], "cross-test/current-generation/run.sh --shard {{shard}}") {
		t.Errorf("no current-generation-shard recipe running one:\n%s", recipes["current-generation-shard"])
	}
}

// A Rust shard's dependencies come from the compiler cache the slots share
// (cross-test/lib/sccache.sh) when the legs compile through it, not from a
// copy of rust/base's cargo directory: each was several GB, copied once for
// rust/base and once more for each slot's first Rust shard (tacplus#979).
// Without sccache (a build image that has none) the copies stay.
func TestCurrentGenerationShardsCopyNoCargoDirectoryWithSccache(t *testing.T) {
	data, err := os.ReadFile("../cross-test/current-generation/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	copier := regexp.MustCompile(`\bcp\b[^\n]*(cargo_target|cargo_seed|CROSS_TEST_CARGO_TARGET)`)
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || !copier.MatchString(line) {
			continue
		}
		guarded := false
		for j := i - 1; j >= 0 && j >= i-3; j-- {
			if strings.Contains(lines[j], "if ! sccache_active") {
				guarded = true
			}
		}
		if !guarded {
			t.Errorf("run.sh:%d copies a cargo directory outside an `if ! sccache_active`: %s", i+1, strings.TrimSpace(line))
		}
	}
}
