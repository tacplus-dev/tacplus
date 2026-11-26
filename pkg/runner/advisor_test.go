// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

func TestResolveAdvisorDir(t *testing.T) {
	cwd := t.TempDir()
	out := filepath.Join(cwd, "target", "generated-sources", "model")
	cfg := &tacplusyaml.Config{Advisor: &tacplusyaml.Advisor{Path: "advisor"}}

	got, err := resolveAdvisorDir(cwd, out, cfg, "rust")
	if err != nil || got != filepath.Join(cwd, "advisor") {
		t.Fatalf("resolveAdvisorDir = %q, %v", got, err)
	}
	if got, err := resolveAdvisorDir(cwd, out, cfg, "go"); err != nil || got != filepath.Join(cwd, "advisor") {
		t.Fatalf("go: resolveAdvisorDir = %q, %v", got, err)
	}
	// Java's gate is the generated LlmRule over jdeps (tacplus#909).
	if got, err := resolveAdvisorDir(cwd, out, cfg, "java"); err != nil || got != filepath.Join(cwd, "advisor") {
		t.Fatalf("java: resolveAdvisorDir = %q, %v", got, err)
	}
	if got, err := resolveAdvisorDir(cwd, out, &tacplusyaml.Config{}, "go"); got != "" || err != nil {
		t.Fatalf("no advisor: %q, %v", got, err)
	}
	if got, err := resolveAdvisorDir(cwd, out, nil, "go"); got != "" || err != nil {
		t.Fatalf("no tacplus.yaml: %q, %v", got, err)
	}
}

func TestResolveAdvisorDirRefuses(t *testing.T) {
	cwd := t.TempDir()
	out := filepath.Join(cwd, "model")
	for _, tc := range []struct {
		path, lang, want string
	}{
		// tacplus deletes and regenerates its output: an advisor there would be lost.
		{"model/advisor", "rust", "inside the generated output"},
		{"model", "rust", "inside the generated output"},
		// The generated output inside the advisor would make it the advisor's own code.
		{".", "rust", "contains the generated output"},
		// Client-only targets have no service, so no core and no advisor.
		{"advisor", "python", "client-only"},
		{"advisor", "typescript", "client-only"},
		{"advisor", "swift", "client-only"},
		{"advisor", "csharp", "client-only"},
	} {
		cfg := &tacplusyaml.Config{Advisor: &tacplusyaml.Advisor{Path: tc.path}}
		_, err := resolveAdvisorDir(cwd, out, cfg, tc.lang)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("resolveAdvisorDir(%q, %s) = %v, want %q", tc.path, tc.lang, err, tc.want)
		}
	}
}
