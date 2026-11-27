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

// rustSelftestFixture writes a generated-workspace stand-in under
// root/<fixture>/tacplus/model, as cross-test/current-generation lays one out:
// the root package (<base>-model), a model types crate named for the fixture's
// base (its own proto module beside the system one, and a module of its own
// inside that), a chassis crate token that reads the types crate's sys module,
// key, which token depends on, and the other chassis crates
// run_rust_spiffe_self_tests tests, empty. Path dependencies only, so cargo
// needs no registry. tokenTest is the body of token's one test.
func rustSelftestFixture(t *testing.T, root, fixture, base, tokenTest string) string {
	t.Helper()
	ws := filepath.Join(root, fixture, "tacplus", "model")
	types := strings.ReplaceAll(base, "-", "_") + "_model_types"
	members := `".", "types", "chassis/key", "chassis/token"`
	for _, crate := range []string{"jwk", "spiffe", "httpclient", "x509", "servicemanager", "grpcconfig", "grpcserver",
		"collection", "approval", "diskstore", "logger", "configpush"} {
		members += `, "chassis/` + crate + `"`
		writeFile(t, filepath.Join(ws, "chassis", crate, "Cargo.toml"),
			"[package]\nname = \""+crate+"\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
		writeFile(t, filepath.Join(ws, "chassis", crate, "src", "lib.rs"), "")
	}
	writeFile(t, filepath.Join(ws, "Cargo.toml"), "[workspace]\nresolver = \"2\"\nmembers = ["+members+"]\n\n"+
		"[package]\nname = \""+base+"-model\"\nversion = \"1.0.0\"\nedition = \"2021\"\n\n[lib]\npath = \"lib.rs\"\n")
	writeFile(t, filepath.Join(ws, "lib.rs"), "#[cfg(test)]\nmod tests {\n    #[test]\n    fn root() {}\n}\n")
	writeFile(t, filepath.Join(ws, "types", "Cargo.toml"),
		"[package]\nname = \""+base+"-model-types\"\nversion = \"1.0.0\"\nedition = \"2021\"\n\n[lib]\npath = \"mod.rs\"\n")
	writeFile(t, filepath.Join(ws, "types", "mod.rs"), "pub mod "+strings.ReplaceAll(base, "-", "_")+"svc;\npub mod sys;\n")
	writeFile(t, filepath.Join(ws, "types", strings.ReplaceAll(base, "-", "_")+"svc.rs"),
		"pub const SERVICE: &str = \""+base+"\";\n")
	// A module of the fixture's own beside the one token reaches, as each
	// project's entitlements sit beside accounting_method in generated/.
	writeFile(t, filepath.Join(ws, "types", "sys", "mod.rs"), "pub mod token;\npub mod zero;\npub mod "+strings.ReplaceAll(base, "-", "_")+"extra;\n")
	writeFile(t, filepath.Join(ws, "types", "sys", strings.ReplaceAll(base, "-", "_")+"extra.rs"),
		"pub const EXTRA: &str = \""+base+"\";\n")
	writeFile(t, filepath.Join(ws, "types", "sys", "token.rs"), "pub fn kind() -> u8 {\n    crate::sys::token::ONE + super::zero::ZERO\n}\npub const ONE: u8 = 1;\n")
	writeFile(t, filepath.Join(ws, "types", "sys", "zero.rs"), "pub const ZERO: u8 = 0;\n")
	writeFile(t, filepath.Join(ws, "chassis", "key", "Cargo.toml"),
		"[package]\nname = \"key\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	writeFile(t, filepath.Join(ws, "chassis", "key", "src", "lib.rs"), "pub fn size() -> u8 {\n    32\n}\n")
	writeFile(t, filepath.Join(ws, "chassis", "token", "Cargo.toml"),
		"[package]\nname = \"token\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\nkey = { path = \"../key\" }\n"+
			base+"-model-types = { path = \"../../types\" }\n")
	writeFile(t, filepath.Join(ws, "chassis", "token", "src", "lib.rs"),
		"use "+types+"::sys::token::kind;\n\npub fn header() -> (u8, u8) {\n    (kind(), key::size())\n}\n\n"+
			"#[cfg(test)]\nmod tests {\n    #[test]\n    fn header() {\n        "+tokenTest+"\n    }\n}\n")
	return ws
}

const tokenTestPasses = "assert_eq!(super::header(), (1, 32));"

func needRustSelftestTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"cargo", "rustc", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
}

// rustSelftestEnv isolates cargo's build directory to the test.
func rustSelftestEnv(root string) []string {
	return append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(root, "cargo-target"))
}

// rustSelftestHash returns rust_selftest_hash.py's hash of crate for the
// calls cmd (separated by :::) in the workspace ws.
func rustSelftestHash(t *testing.T, root, ws, crate string, cmd ...string) string {
	t.Helper()
	script, err := filepath.Abs("../cross-test/lib/rust_selftest_hash.py")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("python3", append([]string{"-I", script, ws}, cmd...)...)
	c.Env = rustSelftestEnv(root)
	out, err := c.Output()
	if err != nil {
		t.Fatalf("rust_selftest_hash.py %s: %v", ws, err)
	}
	hash := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("rust_selftest_hash.py printed %q", line)
		}
		if strings.HasSuffix(f[0], "-model") {
			t.Errorf("the root package was hashed: %s", line)
		}
		if f[0] == crate {
			hash = f[1]
		}
	}
	if hash == "" {
		t.Fatalf("rust_selftest_hash.py printed no hash for %s:\n%s", crate, out)
	}
	return hash
}

// Two fixtures whose chassis source differs only by the model types crate's
// name hash the same, though each types crate also carries its own modules and
// each root package is the fixture's own; a chassis source change, or a change
// to the part of the types crate the chassis crate reaches, gives a new hash.
func TestRustSelftestHashIsTheRenderedSource(t *testing.T) {
	needRustSelftestTools(t)
	root := t.TempDir()
	calls := func(base string) []string {
		return []string{"cargo", "test", "--no-fail-fast", "-p", base + "-model", "-p", "key", "-p", "token", "--lib", "--tests",
			":::", "cargo", "test", "-p", base + "-model", "-p", "token", "--doc"}
	}
	vault := rustSelftestHash(t, root, rustSelftestFixture(t, root, "vault-rust", "proto", tokenTestPasses), "token", calls("proto")...)
	healthWS := rustSelftestFixture(t, root, "healthonly-rust", "healthonly", tokenTestPasses)
	health := rustSelftestHash(t, root, healthWS, "token", calls("healthonly")...)
	if vault != health {
		t.Errorf("fixtures differing only by the types crate's name hash differently: %s, %s", vault, health)
	}

	changed := rustSelftestFixture(t, root, "changed-rust", "changed", tokenTestPasses)
	writeFile(t, filepath.Join(changed, "chassis", "key", "src", "lib.rs"), "pub fn size() -> u8 {\n    33 - 1\n}\n")
	if got := rustSelftestHash(t, root, changed, "token", calls("changed")...); got == vault {
		t.Errorf("a changed dependency crate source (key) kept the hash %s", got)
	}

	sys := rustSelftestFixture(t, root, "sys-rust", "sys", tokenTestPasses)
	writeFile(t, filepath.Join(sys, "types", "sys", "token.rs"), "pub fn kind() -> u8 {\n    crate::sys::token::ONE + super::zero::ZERO\n}\npub const ONE: u8 = 2;\n")
	if got := rustSelftestHash(t, root, sys, "token", calls("sys")...); got == vault {
		t.Errorf("a changed types module the chassis crate reaches kept the hash %s", got)
	}

	zero := rustSelftestFixture(t, root, "zero-rust", "zero", tokenTestPasses)
	writeFile(t, filepath.Join(zero, "types", "sys", "zero.rs"), "pub const ZERO: u8 = 1;\n")
	if got := rustSelftestHash(t, root, zero, "token", calls("zero")...); got == vault {
		t.Errorf("a changed types module reached through super:: kept the hash %s", got)
	}

	if got := rustSelftestHash(t, root, healthWS, "token", "cargo", "test", "-p", "token", "--lib"); got == vault {
		t.Errorf("a different call kept the hash %s", got)
	}
}

// rustSelftestRun runs current-generation's run_rust_spiffe_self_tests on the
// stand-in workspace ws with the record passed, its logs in root/logs/<fixture>.
// It returns its output, its log directory and its error.
func rustSelftestRun(t *testing.T, root, passed, fixture, ws string) (string, string, error) {
	t.Helper()
	logDir := filepath.Join(root, "logs", fixture)
	out, err := runRustSelfTests(t, ws, logDir, "",
		"CARGO_TARGET_DIR="+filepath.Join(root, "cargo-target"), "RUST_SELFTEST_PASSED_DIR="+passed)
	return out, logDir, err
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

// A chassis crate's tests run once per hash per gate: a second fixture with the
// same source reuses them (a cargo check of the crate, the hash and the
// fixture they passed in logged), while its root package's tests run; a
// fixture with a changed chassis source runs them, and a failure is never
// reused.
func TestRustSelftestReusedRunsEachSourceOnce(t *testing.T) {
	needRustSelftestTools(t)
	root := t.TempDir()
	passed := filepath.Join(root, "passed")
	if err := os.MkdirAll(passed, 0o755); err != nil {
		t.Fatal(err)
	}

	vault := rustSelftestFixture(t, root, "vault-rust", "proto", tokenTestPasses)
	out, logs, err := rustSelftestRun(t, root, passed, "vault-rust", vault)
	if err != nil || !strings.Contains(out, "rust self-test ran: token hash ") || !strings.Contains(readLog(t, filepath.Join(logs, "rust-test.log")), "test tests::header ... ok") {
		t.Fatalf("first fixture did not run token's tests: %v\n%s", err, out)
	}

	health := rustSelftestFixture(t, root, "healthonly-rust", "healthonly", tokenTestPasses)
	out, logs, err = rustSelftestRun(t, root, passed, "healthonly-rust", health)
	want := regexp.MustCompile("rust self-test reused: token hash [0-9a-f]{64} passed in fixture vault-rust")
	if err != nil || !want.MatchString(out) || !want.MatchString(readLog(t, filepath.Join(logs, "rust-selftest-hash.log"))) {
		t.Fatalf("same-source fixture did not reuse token's tests: %v\n%s", err, out)
	}
	test := readLog(t, filepath.Join(logs, "rust-test.log"))
	if strings.Contains(test, "tests::header") || !strings.Contains(test, "test tests::root ... ok") {
		t.Errorf("a reused crate's tests ran, or the root package's did not:\n%s", test)
	}
	if check := readLog(t, filepath.Join(logs, "rust-check-reused.log")); !strings.Contains(check, "Checking token") {
		t.Errorf("no cargo check of the reused crate:\n%s", check)
	}

	// A changed chassis source with a failing self-test: it runs, and fails.
	broken := rustSelftestFixture(t, root, "broken-rust", "broken", "assert_eq!(super::header(), (2, 32));")
	out, logs, err = rustSelftestRun(t, root, passed, "broken-rust", broken)
	if err == nil || strings.Contains(out, "rust self-test reused: token") || !strings.Contains(readLog(t, filepath.Join(logs, "rust-test.log")), "test tests::header ... FAILED") {
		t.Fatalf("a fixture with a changed chassis source did not run its failing self-test: %v\n%s", err, out)
	}
	// The same failing source in another fixture runs again: a failure is not a pass.
	out, logs, err = rustSelftestRun(t, root, passed, "broken2-rust", rustSelftestFixture(t, root, "broken2-rust", "brokentwo", "assert_eq!(super::header(), (2, 32));"))
	if err == nil || !strings.Contains(readLog(t, filepath.Join(logs, "rust-test.log")), "test tests::header ... FAILED") {
		t.Fatalf("a failed hash was reused: %v\n%s", err, out)
	}
}

// A parallel gate runs each fixture as a job of its own, this script again
// with --fixture (tacplus#812). The gate empties the record of passed steps
// once; its jobs share it, or no job would reuse another's step. A run of one
// fixture on its own starts its own record.
func TestRustSelftestStartSharesThePoolsRecordWithItsJobs(t *testing.T) {
	lib, err := filepath.Abs("../cross-test/lib/rust-selftest-once.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "passed")
	run := func(env []string, script string) string {
		t.Helper()
		c := exec.Command("bash", "-c", "set -euo pipefail; source \"$1\"; DIR=\"$2\"\n"+script, "bash", lib, dir)
		c.Env = append(os.Environ(), env...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return string(out)
	}
	stale := []string{"RUST_SELFTEST_PASSED_DIR=" + dir}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("an earlier gate\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The gate (its record named in the environment or not) starts it empty,
	// and its job, a child process, keeps what the gate's jobs recorded.
	out := run(stale, `rust_selftest_start "$DIR" false
ls -A "$DIR" | sed 's/^/gate sees /'
echo base >"$DIR/hash1"
bash -c 'set -euo pipefail; source "$1"; rust_selftest_start "$2" true; ls -A "$RUST_SELFTEST_PASSED_DIR" | sed "s/^/job sees /"' bash "$1" "$DIR"`)
	if strings.Contains(out, "stale") {
		t.Errorf("the gate kept an earlier gate's record:\n%s", out)
	}
	if !strings.Contains(out, "job sees hash1") {
		t.Errorf("a job of the gate emptied the gate's record:\n%s", out)
	}

	// One fixture run on its own, the record not its caller's: empty.
	out = run(nil, `rust_selftest_start "$DIR" true; ls -A "$DIR" | sed 's/^/alone sees /'`)
	if strings.Contains(out, "alone sees") {
		t.Errorf("a run of one fixture on its own kept a record it did not start:\n%s", out)
	}
}
