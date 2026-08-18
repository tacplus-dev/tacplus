// Copyright (c) 2026 Second Star Cavalier LLC

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Pins the constraint-extraction regex: digit-bearing names (assert_base64)
// must be visible to the gate, and the name capture must not bleed into the
// proto field number.
func TestParseConstraints_DigitBearingNames(t *testing.T) {
	spec := `
syntax = "proto3";
extend google.protobuf.FieldOptions {
  bool assert_required = 60001;
  bool assert_base64 = 60016;
  DurationType assert_duration = 60008;
  int64 assert_duration_min = 60009;
}
`
	path := filepath.Join(t.TempDir(), "spec.proto")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := parseConstraints(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"assert_base64", "assert_duration", "assert_duration_min", "assert_required"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseConstraints = %v, want %v", got, want)
	}
}

func TestSpiffeSoundnessSeededViolations(t *testing.T) {
	root := t.TempDir()
	badPath := filepath.Join(root, "pkg/templates/java/chassis/x509/Bad.java.tmpl")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `
class Bad {
  void scheme(String trustDomain, String serviceName) {
    String prefix = Spiffe.SCHEME + "://" + trustDomain;
    if (value.startsWith("spiffe://")) return;
    String value = "spiffe://example.com/system/aws/services/mcp";
    String path = URI.create(value).getPath();
    String[] parts = path.substring(1).split("/");
  }
}
`
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	allowedPath := filepath.Join(root, "pkg/templates/java/chassis/spiffe/Spiffe.java.tmpl")
	if err := os.MkdirAll(filepath.Dir(allowedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowedPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	violations, err := spiffeSoundnessViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, v := range violations {
		rules = append(rules, v.Rule)
		if v.RelPath != "pkg/templates/java/chassis/x509/Bad.java.tmpl" {
			t.Fatalf("unexpected violation path: %+v", v)
		}
	}
	sort.Strings(rules)
	want := []string{
		"SPIFFE prefix comparison",
		"URI path splitting",
		"URI path splitting",
		"scheme-constant interpolation",
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %v, want %v; violations=%+v", rules, want, violations)
	}
}

func TestSpiffeSoundnessSeededBareNameDescriptorMatch(t *testing.T) {
	root := t.TempDir()
	badPath := filepath.Join(root, "pkg/internal/go/services/bad.go")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `
package services

import "strings"

func find(name string, messages []struct{ Name string }) *struct{ Name string } {
	simpleName := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		simpleName = name[idx+1:]
	}
	for i := range messages {
		msg := &messages[i]
		if msg.Name == simpleName {
			return msg
		}
	}
	return nil
}
`
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	violations, err := spiffeSoundnessViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %+v, want one", violations)
	}
	if violations[0].Rule != "bare-name descriptor match" {
		t.Fatalf("rule = %q, want bare-name descriptor match", violations[0].Rule)
	}
}

// Pins the store-vendor gate: forbidden backend-store + transport vendor names
// and store-query directives must be flagged (including "kafka" — tacplus is
// transport-neutral), store-neutral names (graphstore/columnarstore) must not
// false-positive, and excluded dirs (archive/roadmap/todo/target) must be skipped.
func TestStoreVendorSoundnessSeededViolations(t *testing.T) {
	root := t.TempDir()

	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Forbidden leaks in scanned roots (incl. the transport vendor "kafka").
	write("pkg/internal/go/types/bad.go", "// projects to ClickHouse via MergeTree\nvar x = \"dgraph\"\nvar k = \"kafka\"\n")
	write("docs/THEORY.md", "run `CREATE TABLE` then @reverse the edge\n")
	// Store-neutral names must NOT flag.
	write("pkg/internal/go/types/ok.go", "// EventBus speaks a generic broker protocol\nvar s = \"graphstore\"\nvar c = \"columnarstore\"\n")
	// Excluded dirs must be skipped even though they contain forbidden tokens.
	write("archive/old.md", "we used dgraph and clickhouse\n")
	write("roadmap/plan.md", "migrate off postgres\n")
	write("todo/ledger.md", "dgraph leak here\n")
	write("target/generated.go", "package p // clickhouse\n")

	violations, err := storeVendorViolations(root)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, v := range violations {
		got[v.RelPath] = true
		if v.RelPath != "pkg/internal/go/types/bad.go" && v.RelPath != "docs/THEORY.md" {
			t.Fatalf("unexpected violation path: %+v", v)
		}
	}
	if !got["pkg/internal/go/types/bad.go"] || !got["docs/THEORY.md"] {
		t.Fatalf("expected leaks in bad.go and THEORY.md; got %+v", violations)
	}

	// bad.go: ClickHouse, MergeTree, dgraph, kafka. THEORY.md: CREATE TABLE, @reverse.
	if len(violations) != 6 {
		t.Fatalf("violations = %d (%+v), want 6", len(violations), violations)
	}
}

// Pins token matching: a prefix-named constraint must not false-pass when only
// a longer sibling is referenced.
func TestConstraintReferenced_WordBoundary(t *testing.T) {
	tests := []struct {
		src, constraint string
		want            bool
	}{
		{`case "assert_duration":`, "assert_duration", true},
		{`case "assert_duration_min":`, "assert_duration", false},
		{`// Validate %s (assert_duration: POSITIVE)`, "assert_duration", true},
		{`case "assert_base64":`, "assert_base64", true},
		{`assert_base64x`, "assert_base64", false},
		{``, "assert_required", false},
	}
	for _, tt := range tests {
		if got := constraintReferenced(tt.src, tt.constraint); got != tt.want {
			t.Errorf("constraintReferenced(%q, %q) = %v, want %v", tt.src, tt.constraint, got, tt.want)
		}
	}
}

// -spiffe-scan runs the same rules over GENERATED output, where a message named
// e.g. IdentitySpiffeResource puts "spiffe" into the identifiers of every
// unrelated helper emitted for it. Keying the parsing rules on the bare word
// flagged Helpers.java's JSON sniff (`startsWith("{")`) and failed the
// current-generation gate; they key on the "spiffe:" scheme literal instead.
func TestSpiffeScanIgnoresSpiffeNamedIdentifiers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "model/src/main/java/Helpers.java")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	generated := `
class Helpers {
  static IdentitySpiffeResource identitySpiffeResourceFromBytes(byte[] data) {
    String dataStr = new String(data).trim();
    if (dataStr.startsWith("{") || dataStr.startsWith("[")) {
      return identitySpiffeResourceFromJson(dataStr);
    }
    return null;
  }
}
`
	if err := os.WriteFile(path, []byte(generated), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err := spiffeSoundnessViolationsIn(root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("spiffe-named identifiers must not be violations, got %+v", violations)
	}

	// The narrowing must not blind the rules: real hand-rolled parsing of a
	// spiffe:// URI in the same generated tree is still caught.
	badPath := filepath.Join(root, "model/src/main/java/Verify.java")
	bad := `
class Verify {
  boolean check(String uri, String trustDomain) {
    String prefix = "spiffe://" + trustDomain + "/";
    if (!uri.startsWith(prefix)) return false;
    String path = URI.create(uri).getPath();
    String[] parts = path.substring(1).split("/");
    return parts.length > 0;
  }
}
`
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err = spiffeSoundnessViolationsIn(root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, v := range violations {
		if v.RelPath != "model/src/main/java/Verify.java" {
			t.Fatalf("unexpected violation path: %+v", v)
		}
		rules = append(rules, v.Rule)
	}
	sort.Strings(rules)
	want := []string{"SPIFFE prefix comparison", "URI path splitting", "raw SPIFFE interpolation"}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %v, want %v; violations=%+v", rules, want, violations)
	}
}
