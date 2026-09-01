// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The (spec.namespace) request stamp is the principal stamp bound to the
// namespace claim: empty is stamped, equal passes, different is refused. All
// three arms must be present — a stamp with no refusal arm silently launders a
// forged namespace into the handler.
func TestRustRequestNamespaceBlockStampsAcceptsRefuses(t *testing.T) {
	got := rustRequestNamespaceBlock("namespace", "CreateThing")
	for _, want := range []string{
		"c.namespace()",
		"if req.namespace.is_empty() {",
		"req.namespace = __namespace;",
		"} else if req.namespace != __namespace {",
		"tonic::Status::invalid_argument(",
		"tonic::Status::unauthenticated(",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered (spec.namespace) block is missing %q:\n%s", want, got)
		}
	}
}

// The egress filter must abort, not return an error. A refusal of the one call
// is what the decision explicitly rejected: reaching the comparison at all means
// the store's own query already returned another tenant's row.
func TestRustNamespaceFilterAbortsOnMismatch(t *testing.T) {
	got := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings")
	if !strings.Contains(got, "std::process::abort();") {
		t.Fatalf("the namespace egress filter must abort the process on a mismatch:\n%s", got)
	}
	if strings.Contains(got, "return Err") {
		t.Fatalf("the namespace egress filter must not degrade a mismatch to a failed call:\n%s", got)
	}
	// Exact equality, never the level-or-below containment rule the
	// administrative target_namespace_field uses.
	if !strings.Contains(got, "__row.namespace == __read_namespace") {
		t.Fatalf("the namespace egress filter must compare for exact equality:\n%s", got)
	}
	if strings.Contains(got, "starts_with") || strings.Contains(got, "normalize_namespace") {
		t.Fatalf("the namespace egress filter must not do hierarchical containment:\n%s", got)
	}
}

// filterTail returns the generator's rendered filter with the caller-resolution
// preamble removed. Everything from `let mut resp = resp;` on is VERBATIM
// generator output — the retain and the abort — and is what the two controls
// below compile and run. The preamble is dropped only because binding a real
// tonic Caller would drag the whole chassis into a single-file rustc build.
func filterTail(t *testing.T) string {
	t.Helper()
	block := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings")
	const marker = "let mut resp = resp;\n"
	i := strings.Index(block, marker)
	if i < 0 {
		t.Fatalf("rendered filter has no %q to split on:\n%s", marker, block)
	}
	return block[i+len(marker):]
}

// runGeneratedFilter compiles the generated filter tail into a standalone binary
// over one row stamped rowNS, with the caller's namespace bound to callerNS, and
// returns what the process did.
func runGeneratedFilter(t *testing.T, callerNS, rowNS string) (stdout string, exitCode int, signaled bool) {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the executable namespace-crash control")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.rs")
	program := `struct Row { namespace: String }
struct Resp { items: Vec<Row> }
fn main() {
    let __read_namespace = "` + callerNS + `".to_string();
    let resp = Resp { items: vec![Row { namespace: "` + rowNS + `".to_string() }] };
    let mut resp = resp;
` + filterTail(t) + `    println!("SURVIVED {}", resp.items.len());
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write main.rs: %v", err)
	}
	bin := filepath.Join(dir, "filter")
	build := exec.Command("rustc", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
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
	// A Rust abort is delivered as SIGABRT, which os/exec reports as a negative
	// ExitCode. Distinguishing it from a plain non-zero exit is the point: the
	// process died, it did not return an error.
	return sb.String(), ee.ExitCode(), ee.ExitCode() < 0
}

// RED control, executable. A row stamped with another tenant's namespace makes
// the GENERATED filter kill the process.
func TestGeneratedNamespaceFilterCrashesOnMismatch(t *testing.T) {
	stdout, code, signaled := runGeneratedFilter(t, "/acme/team-a", "/globex/team-b")
	if !signaled {
		t.Fatalf("a mismatched namespace must crash the process; got exit code %d, stdout %q", code, stdout)
	}
	if strings.Contains(stdout, "SURVIVED") {
		t.Fatalf("the process ran past the generated filter after a mismatch: %q", stdout)
	}
}

// PAIRED control, executable. The IDENTICAL generated filter over a row stamped
// with the caller's OWN namespace returns it and exits normally — so the crash
// above is the mismatch, not the filter being unconditionally fatal.
func TestGeneratedNamespaceFilterPassesMatchingRow(t *testing.T) {
	stdout, code, signaled := runGeneratedFilter(t, "/acme/team-a", "/acme/team-a")
	if signaled || code != 0 {
		t.Fatalf("a matching namespace must pass; got exit code %d, stdout %q", code, stdout)
	}
	if strings.TrimSpace(stdout) != "SURVIVED 1" {
		t.Fatalf("the matching row must survive the generated filter, got %q", stdout)
	}
}

// The exact-match rule has a consequence worth pinning: a caller ABOVE the row
// in the tenant tree is a mismatch, not a containing parent. This is where the
// isolation rule deliberately parts from target_namespace_field's level-or-below
// administrative reach.
func TestGeneratedNamespaceFilterCrashesOnParentNamespace(t *testing.T) {
	stdout, _, signaled := runGeneratedFilter(t, "/acme", "/acme/team-a")
	if !signaled {
		t.Fatalf("a parent namespace is not a match under the exact-match rule; stdout %q", stdout)
	}
}
