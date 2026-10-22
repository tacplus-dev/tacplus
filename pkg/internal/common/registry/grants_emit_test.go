// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

func TestEmitGoGrantsHelper(t *testing.T) {
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
	if err := emitGoGrantsHelper(Config{OutputDir: dir, Language: "go"}, grants); err != nil {
		t.Fatalf("emitGoGrantsHelper: %v", err)
	}

	path := filepath.Join(dir, "grants", "grants.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated grants.go: %v", err)
	}
	src := string(data)

	// Must be valid, parseable Go in package grants.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.AllErrors)
	if err != nil {
		t.Fatalf("generated grants.go does not parse: %v", err)
	}
	if f.Name.Name != "grants" {
		t.Fatalf("package = %q, want grants", f.Name.Name)
	}

	// Catalog entries and helper API must be present. Match on robust fragments
	// (gofmt reflows map-literal alignment, so do not assert an exact literal).
	for _, want := range []string{
		`"OPENAI_OPEN_AI_SERVICE_GET_API_KEY":`,
		`Service: "OpenAIService"`,
		`RPC: "GetAPIKey"`,
		`"OPENAI_READ_ANY":`,
		`Capability: "READ"`,
		`ExpandsTo: []string{"OPENAI_OPEN_AI_SERVICE_GET_API_KEY"}`,
		"func Expand(token string) []string",
		"func Normalize(tokens []string) []string",
		"func UnknownTokens(tokens []string) []string",
		"func ContainsAll(candidate, ceiling []string) bool",
		"func Explain(token string) []string",
		"func Lookup(token string) (Grant, bool)",
		"func IsWildcard(token string) bool",
		"Unknown grants stay fail-closed",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated grants.go missing %q", want)
		}
	}
}

func TestEmitGrantsHelperUnknownLanguageIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := emitGrantsHelper(Config{OutputDir: dir, Language: "klingon"}, nil); err != nil {
		t.Fatalf("unknown language must be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "grants")); !os.IsNotExist(err) {
		t.Fatalf("no grants dir should be created for an unimplemented language")
	}
}
