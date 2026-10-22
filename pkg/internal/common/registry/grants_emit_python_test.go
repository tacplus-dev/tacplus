// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

func TestEmitPythonGrantsHelper(t *testing.T) {
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
	if err := emitPythonGrantsHelper(Config{OutputDir: dir, Language: "python", PackageBase: "contractclient_model"}, grants); err != nil {
		t.Fatalf("emitPythonGrantsHelper: %v", err)
	}

	// OutputDir is the PROJECT output dir (registry.json location, consistent
	// across languages); the grants subpackage lands under the package path
	// derived from PackageBase.
	path := filepath.Join(dir, "contractclient_model", "grants", "__init__.py")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated grants/__init__.py: %v", err)
	}
	src := string(data)

	// Catalog entries and helper API must be present.
	for _, want := range []string{
		`"OPENAI_OPEN_AI_SERVICE_GET_API_KEY": Grant(`,
		`service="OpenAIService"`,
		`rpc="GetAPIKey"`,
		`"OPENAI_READ_ANY": Grant(`,
		`capability="READ"`,
		`expands_to=("OPENAI_OPEN_AI_SERVICE_GET_API_KEY",)`,
		`expands_to=("OPENAI_OPEN_AI_SERVICE_GET_API_KEY", "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS")`,
		"class Grant:",
		"def lookup(token: str) -> Optional[Grant]:",
		"def is_wildcard(token: str) -> bool:",
		"def expand(token: str) -> List[str]:",
		"def normalize(tokens: List[str]) -> List[str]:",
		"def unknown_tokens(tokens: List[str]) -> List[str]:",
		"def contains_all(candidate: List[str], ceiling: List[str]) -> bool:",
		"def explain(token: str) -> List[str]:",
		"Unknown grants stay fail-closed",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated grants/__init__.py missing %q", want)
		}
	}

	// If a Python interpreter is available, the generated module must byte-compile
	// (mirrors the cross-test's `python -m compileall`).
	pyCompileAll(t, path)
}

// A dotted PackageBase maps to a nested package directory under the project
// output dir (a.b.c → <out>/a/b/c/grants/__init__.py).
func TestEmitPythonGrantsHelperDottedPackageBase(t *testing.T) {
	grants := []*spec.EntitlementGrantInfo{
		{Token: "OPENAI_OPEN_AI_SERVICE_GET_API_KEY", Package: "openai", ServiceName: "OpenAIService", Rpc: "GetAPIKey"},
	}

	dir := t.TempDir()
	if err := emitPythonGrantsHelper(Config{OutputDir: dir, Language: "python", PackageBase: "acme.contract.model"}, grants); err != nil {
		t.Fatalf("emitPythonGrantsHelper: %v", err)
	}

	path := filepath.Join(dir, "acme", "contract", "model", "grants", "__init__.py")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("grants subpackage not emitted under the dotted package path: %v", err)
	}
}

// pyCompileAll byte-compiles path with the system python if one is on PATH;
// otherwise it skips (CI without Python still gets the textual assertions).
func pyCompileAll(t *testing.T, path string) {
	t.Helper()
	var py string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python interpreter found; skipping py_compile check")
	}
	out, err := exec.Command(py, "-m", "py_compile", path).CombinedOutput()
	if err != nil {
		t.Fatalf("generated grants module failed py_compile: %v\n%s", err, out)
	}
}
