// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

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
	got := rustRequestNamespaceBlock("namespace", "CreateThing", "req", "        ")
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
	got := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings", false)
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
	block := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings", false)
	const marker = "let mut resp = resp;\n"
	i := strings.Index(block, marker)
	if i < 0 {
		t.Fatalf("rendered filter has no %q to split on:\n%s", marker, block)
	}
	return block[i+len(marker):]
}

// runGeneratedFilter compiles the generated filter tail into a standalone binary
// over one row stamped rowNS, with the caller's namespace bound to callerNS and
// its linked claim bound to linked, and returns what the process did.
func runGeneratedFilter(t *testing.T, callerNS, rowNS string, linked []string) (stdout string, exitCode int, signaled bool) {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the executable namespace-crash control")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.rs")
	linkedLiteral := "vec![]"
	if len(linked) > 0 {
		linkedLiteral = `vec!["` + strings.Join(linked, `".to_string(), "`) + `".to_string()]`
	}
	program := `struct Row { namespace: String }
struct Resp { items: Vec<Row> }
fn main() {
    let __read_namespace = "` + callerNS + `".to_string();
    let __read_namespace_linked: Vec<String> = ` + linkedLiteral + `;
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
	stdout, code, signaled := runGeneratedFilter(t, "/acme/team-a", "/globex/team-b", nil)
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
	stdout, code, signaled := runGeneratedFilter(t, "/acme/team-a", "/acme/team-a", nil)
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
	stdout, _, signaled := runGeneratedFilter(t, "/acme", "/acme/team-a", nil)
	if !signaled {
		t.Fatalf("a parent namespace is not a match under the exact-match rule; stdout %q", stdout)
	}
}

// THE TWO-TENANT ARMS, executable, for the s2s record plane's runtime gate.
//
// The property is the one the gate exists for: a /tenants/<t> peer reading the
// record plane sees its own rows and never another tenant's, while a /system
// peer reading the SAME generated code keeps the fleet-wide read the plane was
// built to serve. Render-only assertions cannot tell those two apart.

// gatedFilterTail returns the generator's gated filter from the __ns_pinned
// binding on. Everything returned is VERBATIM generator output — the gate, the
// retain and the abort. Only the caller-resolution `?` chain above it is
// dropped, because binding a real tonic Caller would drag the whole chassis in.
func gatedFilterTail(t *testing.T) string {
	t.Helper()
	block := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings", true)
	const marker = "let __ns_pinned = "
	i := strings.Index(block, marker)
	if i < 0 {
		t.Fatalf("the gated filter has no %q to split on:\n%s", marker, block)
	}
	return block[i:]
}

// runGatedFilter compiles the gated filter tail over an acme row plus, when
// withBeta, a second row belonging to another tenant, and reports what the
// process did.
func runGatedFilter(t *testing.T, callerNS string, withBeta bool) (stdout string, exitCode int, signaled bool) {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the executable two-tenant control")
	}
	rows := `vec![Row { namespace: "/tenants/acme".to_string(), id: "r-acme".to_string() }]`
	if withBeta {
		rows = `vec![Row { namespace: "/tenants/acme".to_string(), id: "r-acme".to_string() }, Row { namespace: "/tenants/beta".to_string(), id: "r-beta".to_string() }]`
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.rs")
	program := `struct Row { namespace: String, id: String }
struct Resp { items: Vec<Row> }
fn main() {
    let __read_namespace = "` + callerNS + `".to_string();
    let __read_namespace_linked: Vec<String> = vec![];
    let resp = Resp { items: ` + rows + ` };
    ` + gatedFilterTail(t) + `    let ids: Vec<&str> = resp.items.iter().map(|r| r.id.as_str()).collect();
    println!("DELIVERED {:?}", ids);
}
`
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write main.rs: %v", err)
	}
	bin := filepath.Join(dir, "filter")
	if out, err := exec.Command("rustc", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("rustc on the gated filter failed: %v\n%s\n--- program ---\n%s", err, out, program)
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
		t.Fatalf("running the gated filter: %v", err)
	}
	return sb.String(), ee.ExitCode(), ee.ExitCode() < 0
}

// A /tenants/<t> peer is PINNED: its own rows are delivered, and a foreign
// tenant's row reaching the backstop kills the process exactly as on the c2s
// half — the gate does not soften the boundary, it only decides who has one.
func TestRustTenantS2S_GatedFilterPinsATenantCaller(t *testing.T) {
	stdout, code, _ := runGatedFilter(t, "/tenants/acme", false)
	if code != 0 || !strings.Contains(stdout, `DELIVERED ["r-acme"]`) {
		t.Fatalf("a tenant caller must still receive its OWN rows: exit %d, stdout %q", code, stdout)
	}
	stdout, code, signaled := runGatedFilter(t, "/tenants/acme", true)
	if !signaled {
		t.Fatalf("a /tenants/beta row must not be delivered to a /tenants/acme caller; exit %d, stdout %q", code, stdout)
	}
	if strings.Contains(stdout, "DELIVERED") {
		t.Fatalf("a /tenants/beta row leaked to a /tenants/acme caller: %q", stdout)
	}
}

// The regression control the gate exists FOR: the /system peer reading the same
// generated code keeps every row, fleet-wide, and the abort stays unreachable.
func TestRustTenantS2S_GatedFilterKeepsSystemPeerFleetWide(t *testing.T) {
	stdout, code, signaled := runGatedFilter(t, "/system/services/argus", true)
	if signaled || code != 0 {
		t.Fatalf("a /system peer must keep its fleet-wide read: exit %d, stdout %q", code, stdout)
	}
	if !strings.Contains(stdout, `DELIVERED ["r-acme", "r-beta"]`) {
		t.Fatalf("a /system peer must see both tenants' rows, got %q", stdout)
	}
}

// private-records-scope step 2: a row stamped with a namespace the caller is
// LINKED to (not its own) survives the filter — the backstop's second, additive
// acceptance path.
func TestGeneratedNamespaceFilterPassesLinkedRow(t *testing.T) {
	stdout, code, signaled := runGeneratedFilter(t, "/tenants/acme", "/tenants/beta", []string{"/tenants/beta"})
	if signaled || code != 0 {
		t.Fatalf("a row in a linked namespace must pass; got exit code %d, stdout %q", code, stdout)
	}
	if strings.TrimSpace(stdout) != "SURVIVED 1" {
		t.Fatalf("the linked row must survive the generated filter, got %q", stdout)
	}
}

// A namespace NOT in the caller's own or linked set is still a mismatch: the
// linked claim widens the accepted set, it does not disable the check.
func TestGeneratedNamespaceFilterCrashesOnUnlinkedRow(t *testing.T) {
	stdout, _, signaled := runGeneratedFilter(t, "/tenants/acme", "/tenants/gamma", []string{"/tenants/beta"})
	if !signaled {
		t.Fatalf("a row in neither the caller's own nor its linked namespaces must still crash; stdout %q", stdout)
	}
}

// And the UNGATED (c2s) filter over the identical rows still drops and aborts:
// the gate is additive, not a weakening of the half that already had a boundary.
func TestRustNamespaceFilterUngatedHasNoSystemExemption(t *testing.T) {
	got := rustResponseNamespaceFilterBlock([]rustNamespaceListField{{Field: "items", ItemField: "namespace"}}, "ListThings", false)
	if strings.Contains(got, "__ns_pinned") || strings.Contains(got, `starts_with("/system")`) {
		t.Fatalf("the ungated filter must carry no runtime gate:\n%s", got)
	}
	stdout, _, signaled := runGeneratedFilter(t, "/system/services/argus", "/tenants/beta", nil)
	if !signaled {
		t.Fatalf("without the gate a /system caller is pinned like any other; stdout %q", stdout)
	}
}
