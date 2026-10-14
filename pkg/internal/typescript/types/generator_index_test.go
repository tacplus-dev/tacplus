// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateIndexFilesPreservesTemplateIndexExports(t *testing.T) {
	outputDir := t.TempDir()
	libDir := filepath.Join(outputDir, "sys", "x509")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatalf("mkdir lib dir: %v", err)
	}

	files := map[string]string{
		"ImmutableCert.ts": "export class ImmutableCertBuilder {}\nexport class ImmutableCert {}\n",
		"helpers.ts":       "export function parseCertSerial() {}\n",
		"x509_pb.ts":       "export const file_sys_x509 = {};\n",
		"index.ts":         "export { ImmutableCertBuilder, ImmutableCert } from './ImmutableCert.js';\n",
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
		"export { ImmutableCertBuilder, ImmutableCert } from './ImmutableCert.js';",
		"// Auto-generated supplemental exports",
		"export * from './helpers.js';",
		"export * from './x509_pb.js';",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("index missing %q:\n%s", want, rendered)
		}
	}
	for _, stale := range []string{
		"export * from './ImmutableCert.js';",
		"export * as ImmutableCert from './ImmutableCert.js';",
	} {
		if strings.Contains(rendered, stale) {
			t.Fatalf("index duplicated template-covered ImmutableCert export %q:\n%s", stale, rendered)
		}
	}
}
