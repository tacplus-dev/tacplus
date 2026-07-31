// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

func TestEmitRustGrantsHelper(t *testing.T) {
	grants := []*spec.EntitlementGrantInfo{
		{Token: "OPENAI_OPEN_AI_SERVICE_GET_API_KEY", Package: "openai", ServiceName: "OpenAIService", Rpc: "GetAPIKey"},
		{Token: "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS", Package: "openai", ServiceName: "OpenAIService", Rpc: "WatchAPIKeys"},
		{Token: "OPENAI_ANY", Package: "openai", CapabilityScope: "ANY", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY", "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS",
		}},
		{Token: "OPENAI_READ_ANY", Package: "openai", CapabilityScope: "READ", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY",
		}},
	}

	dir := t.TempDir()
	if err := emitRustGrantsHelper(Config{OutputDir: dir, Language: "rust"}, grants); err != nil {
		t.Fatalf("emitRustGrantsHelper: %v", err)
	}

	// The grants module must be a directory module (grants.rs + mod.rs) so the
	// generated crate root can declare it with `pub mod grants;`.
	modPath := filepath.Join(dir, "grants", "mod.rs")
	modData, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("read generated grants/mod.rs: %v", err)
	}
	if !strings.Contains(string(modData), "pub mod grants;") {
		t.Errorf("grants/mod.rs does not declare the grants module")
	}

	path := filepath.Join(dir, "grants", "grants.rs")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated grants.rs: %v", err)
	}
	src := string(data)

	// Catalog entries and helper API must be present.
	for _, want := range []string{
		`"OPENAI_OPEN_AI_SERVICE_GET_API_KEY"`,
		`service: "OpenAIService"`,
		`rpc: "GetAPIKey"`,
		`"OPENAI_READ_ANY"`,
		`capability: "READ"`,
		`expands_to: &["OPENAI_OPEN_AI_SERVICE_GET_API_KEY"]`,
		"pub fn expand(token: &str) -> Vec<String>",
		"pub fn normalize<I, S>(tokens: I) -> Vec<String>",
		"pub fn unknown_tokens<I, S>(tokens: I) -> Vec<String>",
		"pub fn contains_all<I, J, S, T>(candidate: I, ceiling: J) -> bool",
		"pub fn explain(token: &str) -> Vec<String>",
		"pub fn lookup(token: &str) -> Option<Grant>",
		"pub fn is_wildcard(token: &str) -> bool",
		"Unknown grants",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated grants.rs missing %q", want)
		}
	}
}

func TestEmitRustGrantsHelperMirrorsIntoTypesCrate(t *testing.T) {
	dir := t.TempDir()
	typesDir := filepath.Join(dir, "types")
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	if err := os.WriteFile(filepath.Join(typesDir, "mod.rs"), []byte("pub mod services;\n"), 0o644); err != nil {
		t.Fatalf("write types/mod.rs: %v", err)
	}

	if err := emitRustGrantsHelper(Config{OutputDir: dir, Language: "rust"}, nil); err != nil {
		t.Fatalf("emitRustGrantsHelper: %v", err)
	}

	if _, err := os.Stat(filepath.Join(typesDir, "grants", "grants.rs")); err != nil {
		t.Fatalf("types grants helper missing: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(typesDir, "mod.rs"))
	if err != nil {
		t.Fatalf("read types/mod.rs: %v", err)
	}
	if !strings.Contains(string(content), "pub mod grants;") {
		t.Fatalf("types/mod.rs must export generated grants module:\n%s", content)
	}
}

func TestEmitGrantsHelperUnknownLanguageIsNoopRust(t *testing.T) {
	dir := t.TempDir()
	if err := emitGrantsHelper(Config{OutputDir: dir, Language: "klingon"}, nil); err != nil {
		t.Fatalf("unknown language must be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "grants")); !os.IsNotExist(err) {
		t.Fatalf("no grants dir should be created for an unimplemented language")
	}
}
