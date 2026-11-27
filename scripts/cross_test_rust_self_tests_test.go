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

// runRustSelfTests runs current-generation's run_rust_spiffe_self_tests, taken
// from cross-test/current-generation/run.sh with the rust-selftest-once.sh it
// sources, on the generated workspace output, its logs in logDir, after the
// shell code prelude, with env added to the environment. It returns the output.
func runRustSelfTests(t *testing.T, output, logDir, prelude string, env ...string) (string, error) {
	t.Helper()
	runSh, err := filepath.Abs("../cross-test/current-generation/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	rootDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `set -euo pipefail
ROOT_DIR="$4"
die() { echo "die: $*" >&2; exit 1; }
dump_step_log() { cat "$1" >&2; }
assert_clean_log() { :; }
source "$ROOT_DIR/cross-test/lib/rust-selftest-once.sh"
eval "$(sed -n '/^rust_self_test_packages() {/,/^}/p; /^rust_self_test_calls() {/,/^}/p; /^run_rust_spiffe_self_tests() {/,/^}/p' "$1")"
` + prelude + `
run_rust_spiffe_self_tests "$2" "$3"`
	cmd := exec.Command("bash", "-c", script, "bash", runSh, output, logDir, rootDir)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// rustSelfTests runs run_rust_spiffe_self_tests with a cargo on PATH that
// records each call (its arguments and SPIFFE_VECTOR_FIXTURE) and passes, after
// prelude, with env added to the environment. It returns the calls.
func rustSelfTests(t *testing.T, output, prelude string, env ...string) []string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	fake := "#!/usr/bin/env bash\necho \"$* SPIFFE=${SPIFFE_VECTOR_FIXTURE:-}\" >>\"" + calls + "\"\necho 'test result: ok.'\n"
	if err := os.WriteFile(filepath.Join(bin, "cargo"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	env = append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)
	if out, err := runRustSelfTests(t, output, filepath.Join(t.TempDir(), "fixture"), prelude, env...); err != nil {
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
	for _, crate := range []string{"key", "jwk", "token", "spiffe", "httpclient", "wkt", "service_im", "x509", "servicemanager",
		"grpcconfig", "grpcserver", "collection", "approval", "diskstore", "logger", "configpush", "am"} {
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
	calls := rustSelfTests(t, generatedRustWorkspace(t), "")
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
	want := []string{"approval", "collection", "configpush", "diskstore", "grpcconfig", "grpcserver", "httpclient", "jwk", "key",
		"logger", "proto-model", "service_im", "servicemanager", "spiffe", "token", "wkt", "x509"}
	if got := selection(tests[0]); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the test call selects %v, want %v: %s", got, want, tests[0])
	}
	if !strings.Contains(tests[0], "/cross-test/fixtures/spiffe_vectors.json") {
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
	if has(rustSelfTests(t, generatedRustWorkspace(t), "")) {
		t.Errorf("am's tests ran in a fixture without its spool self-test")
	}
	if !has(rustSelfTests(t, generatedRustWorkspace(t, "chassis/am/tests/accounting_sink_spool.rs"), "")) {
		t.Errorf("am's tests did not run beside its spool self-test")
	}
}

// A chassis crate whose content hash passed in this gate is left out of the
// fixture's test and doc test calls and gets a cargo check instead; the root
// package, the fixture's own, always runs; what ran is recorded from the calls
// that ran (#843).
func TestRustSelfTestsLeaveOutTheCratesReused(t *testing.T) {
	passed := t.TempDir()
	for _, crate := range []string{"token", "logger", "proto-model"} {
		if err := os.WriteFile(filepath.Join(passed, "hash-"+crate), []byte("base-rust\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Each selected package's hash is hash-<name>, and each computation is
	// logged with the calls it was given.
	hashed := filepath.Join(t.TempDir(), "hashed")
	prelude := `rust_selftest_hashes() {
  shift
  echo "$*" >>"` + hashed + `"
  local w p=""
  for w in "$@"; do [[ "$p" != -p ]] || echo "$w hash-$w"; p="$w"; done | sort -u
}`
	calls := rustSelfTests(t, generatedRustWorkspace(t), prelude, "RUST_SELFTEST_PASSED_DIR="+passed)
	var tests, docs, checks []string
	for _, call := range calls {
		switch {
		case strings.HasPrefix(call, "check "):
			checks = append(checks, call)
		case strings.Contains(call, " --doc"):
			docs = append(docs, call)
		default:
			tests = append(tests, call)
		}
	}
	if len(tests) != 1 || len(docs) != 1 || len(checks) != 1 {
		t.Fatalf("want one test, one doc test and one check call, got:\n%s", strings.Join(calls, "\n"))
	}
	for _, p := range append(selection(tests[0]), selection(docs[0])...) {
		if p == "token" || p == "logger" {
			t.Errorf("a reused crate's tests ran: %s", p)
		}
	}
	if got := strings.Join(selection(checks[0]), " "); got != "logger token" || !strings.Contains(checks[0], "--tests") {
		t.Errorf("the reused crates' check: %s", checks[0])
	}
	if !strings.Contains(strings.Join(selection(tests[0]), " "), "proto-model") {
		t.Errorf("the root package did not run, though its hash is in the record: %s", tests[0])
	}
	data, err := os.ReadFile(hashed)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || strings.Contains(lines[1], "-p token") || !strings.Contains(lines[1], "-p key") {
		t.Errorf("want the hash of the full calls, then of the calls that ran:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(passed, "hash-key")); err != nil {
		t.Errorf("a crate that ran and passed was not recorded: %v", err)
	}
}
