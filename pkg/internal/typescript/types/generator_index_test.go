// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateIndexFilesPreservesTemplateIndexExports(t *testing.T) {
	outputDir := t.TempDir()
	libDir := filepath.Join(outputDir, "sys", "saml")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatalf("mkdir lib dir: %v", err)
	}

	files := map[string]string{
		"SamlAssertion.ts": "export class SamlAssertionBuilder {}\nexport class SamlAssertion {}\n",
		"helpers.ts":       "export function parseSamlStatusCode() {}\n",
		"saml_pb.ts":       "export const file_sys_saml = {};\n",
		"index.ts":         "export { SamlAssertionBuilder, SamlAssertion } from './SamlAssertion.js';\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(libDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := generateIndexFiles(filepath.Join(outputDir, "sys")); err != nil {
		t.Fatalf("generateIndexFiles: %v", err)
	}

	renderedBytes, err := os.ReadFile(filepath.Join(libDir, "index.ts"))
	if err != nil {
		t.Fatalf("read generated index: %v", err)
	}
	rendered := string(renderedBytes)

	for _, want := range []string{
		"export { SamlAssertionBuilder, SamlAssertion } from './SamlAssertion.js';",
		"// Auto-generated supplemental exports",
		"export * from './helpers.js';",
		"export * from './saml_pb.js';",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("index missing %q:\n%s", want, rendered)
		}
	}
	for _, stale := range []string{
		"export * from './SamlAssertion.js';",
		"export * as SamlAssertion from './SamlAssertion.js';",
	} {
		if strings.Contains(rendered, stale) {
			t.Fatalf("index duplicated template-covered SamlAssertion export %q:\n%s", stale, rendered)
		}
	}
}
