// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A plain (spec.store_principal) field keeps today's soft filter: a mismatch
// is dropped, not fatal.
func TestRustPrincipalFilterFiltersOnMismatchWhenNotPrivate(t *testing.T) {
	got := rustResponsePrincipalFilterBlock([]rustPrincipalListField{{Field: "items", ItemField: "owner"}}, "ListThings")
	if strings.Contains(got, "std::process::abort();") {
		t.Fatalf("a plain store_principal field must not abort on mismatch:\n%s", got)
	}
	if !strings.Contains(got, "resp.items.retain(|__row| __row.owner == __read_principal);") {
		t.Fatalf("a plain store_principal field must render the soft retain filter:\n%s", got)
	}
}

// A (spec.record).private row is stronger: reaching a foreign owner's row
// means the store's own query already leaked it, so the process aborts —
// same doctrine as (spec.store_namespace)'s existing crash.
func TestRustPrincipalFilterAbortsOnMismatchWhenPrivate(t *testing.T) {
	got := rustResponsePrincipalFilterBlock([]rustPrincipalListField{{Field: "items", ItemField: "owner", Private: true}}, "ListThings")
	if !strings.Contains(got, "std::process::abort();") {
		t.Fatalf("a private record's principal filter must abort on mismatch:\n%s", got)
	}
	if strings.Contains(got, "return Err") {
		t.Fatalf("a private record's principal filter must not degrade a mismatch to a failed call:\n%s", got)
	}
	if !strings.Contains(got, "resp.items.retain(|__row| __row.owner == __read_principal);") {
		t.Fatalf("a private record's principal filter must still retain on the same comparison:\n%s", got)
	}
}

// A response with both a private and a non-private principal-pinned field
// must render each with its own posture — mixing them in one message must
// not upgrade or downgrade the other.
func TestRustPrincipalFilterMixedPrivateAndNonPrivateFields(t *testing.T) {
	got := rustResponsePrincipalFilterBlock([]rustPrincipalListField{
		{Field: "mine", ItemField: "owner"},
		{Field: "theirs", ItemField: "owner", Private: true},
	}, "ListThings")
	if !strings.Contains(got, "resp.mine.retain(|__row| __row.owner == __read_principal);\n") {
		t.Fatalf("the non-private field must keep the plain retain with no surrounding crash check:\n%s", got)
	}
	if !strings.Contains(got, "let __priv_before = resp.theirs.len();") {
		t.Fatalf("the private field must get the before/after abort dance:\n%s", got)
	}
}

// principalFilterTail returns the generator's rendered filter with the
// caller-resolution preamble removed — see filterTail in
// namespace_render_test.go, same rationale.
func principalFilterTail(t *testing.T, private bool) string {
	t.Helper()
	block := rustResponsePrincipalFilterBlock([]rustPrincipalListField{{Field: "items", ItemField: "owner", Private: private}}, "ListThings")
	const marker = "let mut resp = resp;\n"
	i := strings.Index(block, marker)
	if i < 0 {
		t.Fatalf("rendered filter has no %q to split on:\n%s", marker, block)
	}
	return block[i+len(marker):]
}

// runGeneratedPrincipalFilter compiles the generated filter tail over one row
// owned by rowOwner, with the caller's principal bound to callerPrincipal, and
// reports what the process did.
func runGeneratedPrincipalFilter(t *testing.T, private bool, callerPrincipal, rowOwner string) (stdout string, exitCode int, signaled bool) {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the executable principal-crash control")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.rs")
	program := `struct Row { owner: String }
struct Resp { items: Vec<Row> }
fn main() {
    let __read_principal = "` + callerPrincipal + `".to_string();
    let resp = Resp { items: vec![Row { owner: "` + rowOwner + `".to_string() }] };
    let mut resp = resp;
` + principalFilterTail(t, private) + `    println!("SURVIVED {}", resp.items.len());
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write main.rs: %v", err)
	}
	bin := filepath.Join(dir, "filter")
	if out, err := exec.Command("rustc", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("rustc on the generated filter failed: %v\n%s\n--- program ---\n%s", err, out, program)
	}
	var sb strings.Builder
	run := exec.Command(bin)
	run.Stdout = &sb
	err := run.Run()
	if err == nil {
		return sb.String(), 0, false
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running the generated filter: %v", err)
	}
	return sb.String(), ee.ExitCode(), ee.ExitCode() < 0
}

// RED control, executable. A private row owned by someone else than the
// caller makes the GENERATED filter kill the process.
func TestGeneratedPrivatePrincipalFilterCrashesOnMismatch(t *testing.T) {
	stdout, code, signaled := runGeneratedPrincipalFilter(t, true, "/people/alice", "/people/bob")
	if !signaled {
		t.Fatalf("a mismatched owner on a private record must crash the process; got exit code %d, stdout %q", code, stdout)
	}
	if strings.Contains(stdout, "SURVIVED") {
		t.Fatalf("the process ran past the generated filter after a mismatch: %q", stdout)
	}
}

// PAIRED control, executable. The IDENTICAL generated private filter over a
// row owned by the caller returns it and exits normally.
func TestGeneratedPrivatePrincipalFilterPassesMatchingRow(t *testing.T) {
	stdout, code, signaled := runGeneratedPrincipalFilter(t, true, "/people/alice", "/people/alice")
	if signaled || code != 0 {
		t.Fatalf("a matching owner must pass; got exit code %d, stdout %q", code, stdout)
	}
	if strings.TrimSpace(stdout) != "SURVIVED 1" {
		t.Fatalf("the matching row must survive the generated filter, got %q", stdout)
	}
}

// CONTRAST control, executable. The SAME mismatch over a plain (non-private)
// store_principal field is dropped, not fatal — proves the abort above is
// specific to private, not a change to store_principal's existing behavior.
func TestGeneratedNonPrivatePrincipalFilterDropsMismatchWithoutCrash(t *testing.T) {
	stdout, code, signaled := runGeneratedPrincipalFilter(t, false, "/people/alice", "/people/bob")
	if signaled || code != 0 {
		t.Fatalf("a plain store_principal mismatch must not crash the process; got exit code %d (signaled=%v), stdout %q", code, signaled, stdout)
	}
	if strings.TrimSpace(stdout) != "SURVIVED 0" {
		t.Fatalf("the mismatched row must be dropped, not survive, got %q", stdout)
	}
}

// The WATCH seam's principal guard must take the same fork: private aborts,
// plain store_principal keeps its existing silent `continue`.
func TestRustWatchEgressGuardStmtsPrivateAbortsPlainContinues(t *testing.T) {
	plain := rustWatchEgressGuardStmts([]rustWatchRowGuard{{Principal: "owner"}}, "item", "Watch", "        ", false)
	if !strings.Contains(plain, "continue;") {
		t.Fatalf("a plain store_principal WATCH guard must continue past a mismatched frame:\n%s", plain)
	}
	if strings.Contains(plain, "process::abort") {
		t.Fatalf("a plain store_principal WATCH guard must not abort:\n%s", plain)
	}

	private := rustWatchEgressGuardStmts([]rustWatchRowGuard{{Principal: "owner", Private: true}}, "item", "Watch", "        ", false)
	if !strings.Contains(private, "std::process::abort();") {
		t.Fatalf("a private record's WATCH principal guard must abort on mismatch:\n%s", private)
	}
	if strings.Contains(private, "continue;") {
		t.Fatalf("a private record's WATCH principal guard must not silently continue past a mismatch:\n%s", private)
	}
}
