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
)

// rustSelfTests runs current-generation's run_rust_spiffe_self_tests, taken
// from cross-test/current-generation/run.sh, on a generated workspace laid out
// under output, with a cargo on PATH that records each call (its arguments and
// SPIFFE_VECTOR_FIXTURE) and passes. It returns the calls.
func rustSelfTests(t *testing.T, output string) []string {
	t.Helper()
	runSh, err := filepath.Abs("../cross-test/current-generation/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	fake := "#!/usr/bin/env bash\necho \"$* SPIFFE=${SPIFFE_VECTOR_FIXTURE:-}\" >>\"" + calls + "\"\necho 'test result: ok.'\n"
	if err := os.WriteFile(filepath.Join(bin, "cargo"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail
ROOT_DIR=/root-dir
die() { echo "die: $*" >&2; exit 1; }
dump_step_log() { cat "$1" >&2; }
assert_clean_log() { :; }
eval "$(sed -n '/^rust_self_test_packages() {/,/^}/p; /^run_rust_spiffe_self_tests() {/,/^}/p' "$1")"
run_rust_spiffe_self_tests "$2" "$3"`
	cmd := exec.Command("bash", "-c", script, "bash", runSh, output, t.TempDir())
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run_rust_spiffe_self_tests: %v\n%s", err, out)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// generatedRustWorkspace lays out the crates a generated Rust workspace has:
// the root package and the chassis crates the self-tests run.
func generatedRustWorkspace(t *testing.T, extra ...string) string {
	t.Helper()
	output := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(output, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Cargo.toml", "[workspace]\nmembers = [\".\"]\n\n[package]\nname = \"proto-model\"\nversion = \"1.0.0\"\n")
	for _, crate := range []string{"key", "jwk", "token", "spiffe", "httpclient", "x509", "servicemanager",
		"grpcconfig", "grpcserver", "collection", "approval", "diskstore", "logger", "am"} {
		write("chassis/"+crate+"/Cargo.toml", "[package]\nname = \""+crate+"\"\n")
	}
	for _, rel := range extra {
		write(rel, "")
	}
	return output
}

// selection returns the packages a cargo call selects with -p.
func selection(call string) []string {
	var packages []string
	fields := strings.Fields(call)
	for i, f := range fields {
		if f == "-p" && i+1 < len(fields) {
			packages = append(packages, fields[i+1])
		}
	}
	sort.Strings(packages)
	return packages
}

// Each `cargo test -p <crate>` resolves the dependencies' features for its own
// selection, so every one of them compiled the generated types crate again:
// about a minute each, thirteen times per Rust fixture, most of
// current-generation's time (tacplus#812). The crates' unit and integration
// tests run in one cargo call that builds them all once, and their doc tests in
// one more that compiles nothing new.
func TestRustSelfTestsBuildTheWorkspaceOnce(t *testing.T) {
	calls := rustSelfTests(t, generatedRustWorkspace(t))
	var tests, docs []string
	for _, call := range calls {
		switch {
		case !strings.HasPrefix(call, "test "):
			t.Errorf("unexpected cargo call: %s", call)
		case strings.Contains(call, " --doc"):
			docs = append(docs, call)
		default:
			tests = append(tests, call)
		}
	}
	if len(tests) != 1 || len(docs) != 1 {
		t.Fatalf("want one cargo test call for the tests and one for the doc tests, got:\n%s", strings.Join(calls, "\n"))
	}
	want := []string{"approval", "collection", "diskstore", "grpcconfig", "grpcserver", "httpclient", "jwk", "key",
		"logger", "proto-model", "servicemanager", "spiffe", "token", "x509"}
	if got := selection(tests[0]); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the test call selects %v, want %v: %s", got, want, tests[0])
	}
	if !strings.Contains(tests[0], "SPIFFE=/root-dir/cross-test/fixtures/spiffe_vectors.json") {
		t.Errorf("the test call does not give the spiffe crate its vectors: %s", tests[0])
	}
	// The logger crate's doc examples have never compiled (run.sh says why):
	// only its unit tests run.
	for _, p := range selection(docs[0]) {
		if p == "logger" {
			t.Errorf("the doc test call selects logger: %s", docs[0])
		}
	}
	if !strings.Contains(tests[0], "--no-fail-fast") {
		t.Errorf("one crate's failing tests would hide the next one's: %s", tests[0])
	}
}

// The am crate's tests run where the accounting-sink fixture installs its
// spool self-test (#34), and only there, as before.
func TestRustSelfTestsRunAmWhereItsSpoolTestIs(t *testing.T) {
	has := func(calls []string) bool {
		for _, p := range selection(calls[0]) {
			if p == "am" {
				return true
			}
		}
		return false
	}
	if has(rustSelfTests(t, generatedRustWorkspace(t))) {
		t.Errorf("am's tests ran in a fixture without its spool self-test")
	}
	if !has(rustSelfTests(t, generatedRustWorkspace(t, "chassis/am/tests/accounting_sink_spool.rs"))) {
		t.Errorf("am's tests did not run beside its spool self-test")
	}
}
