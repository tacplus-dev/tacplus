// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

func readTypeScriptTemplate(t *testing.T, path string) string {
	t.Helper()
	src, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

func TestTypeScriptTagsRuntimeTemplateMatchesKubernetesSemantics(t *testing.T) {
	tmpl := readTypeScriptTemplate(t, "typescript/chassis/tags/index.ts.tmpl")
	for _, want := range []string{
		"export function containsAll(have: Partial<TagSet> | null | undefined, want: Partial<TagSet> | null | undefined): boolean",
		"export function matches(selector: Partial<TagSelector> | null | undefined, tags: Partial<TagSet> | null | undefined): boolean",
		"const OP_IN = 1;",
		"const OP_NOT_IN = 2;",
		"return !exists || !values.includes(value);",
		"const OP_EXISTS = 3;",
		"const OP_DOES_NOT_EXIST = 4;",
		"export function validateKey(key: string): void",
		"export function validateSelector(selector: Partial<TagSelector> | null | undefined): void",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("TypeScript tags runtime template missing %q", want)
		}
	}
}
