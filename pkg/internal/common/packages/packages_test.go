// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package packages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// manager.go tests
// ---------------------------------------------------------------------------

// Helper to create a temp YAML config file and return its path
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return path
}

func TestLoadPackageRegistry_FlatStructure(t *testing.T) {
	yaml := `
go:
  dependencies:
    google.golang.org/protobuf: v1.31.0
    google.golang.org/grpc: v1.58.0
rust:
  dependencies:
    prost: "0.12"
python:
  dependencies:
    protobuf: "4.24"
java:
  dependencies:
    com.google.protobuf:protobuf-java: "3.24.0"
typescript:
  dependencies:
    "@bufbuild/protobuf": "1.4.0"
`
	path := writeTempConfig(t, yaml)
	reg, err := LoadPackageRegistry(path)
	if err != nil {
		t.Fatalf("LoadPackageRegistry() error = %v", err)
	}

	if v, ok := reg.Go.Dependencies["google.golang.org/protobuf"]; !ok || v != "v1.31.0" {
		t.Errorf("Go protobuf version = %q, ok = %v", v, ok)
	}
	if v, ok := reg.Rust.Dependencies["prost"]; !ok || v != "0.12" {
		t.Errorf("Rust prost version = %q, ok = %v", v, ok)
	}
	if v, ok := reg.Python.Dependencies["protobuf"]; !ok || v != "4.24" {
		t.Errorf("Python protobuf version = %q, ok = %v", v, ok)
	}
	if v, ok := reg.Java.Dependencies["com.google.protobuf:protobuf-java"]; !ok || v != "3.24.0" {
		t.Errorf("Java protobuf version = %q, ok = %v", v, ok)
	}
	if v, ok := reg.TypeScript.Dependencies["@bufbuild/protobuf"]; !ok || v != "1.4.0" {
		t.Errorf("TypeScript protobuf version = %q, ok = %v", v, ok)
	}
}

func TestLoadPackageRegistry_NestedLangStructure(t *testing.T) {
	yaml := `
lang:
  go:
    dependencies:
      google.golang.org/protobuf: v1.31.0
  rust:
    dependencies:
      prost: "0.12"
  python:
    dependencies:
      protobuf: "4.24"
  java:
    dependencies:
      com.google.protobuf:protobuf-java: "3.24.0"
  typescript:
    dependencies:
      "@bufbuild/protobuf": "1.4.0"
`
	path := writeTempConfig(t, yaml)
	reg, err := LoadPackageRegistry(path)
	if err != nil {
		t.Fatalf("LoadPackageRegistry() error = %v", err)
	}

	// Should be promoted from Lang to top-level
	if v, ok := reg.Go.Dependencies["google.golang.org/protobuf"]; !ok || v != "v1.31.0" {
		t.Errorf("Go protobuf version = %q, ok = %v (expected promotion from lang:)", v, ok)
	}
	if v, ok := reg.Rust.Dependencies["prost"]; !ok || v != "0.12" {
		t.Errorf("Rust prost version = %q, ok = %v", v, ok)
	}
}

func TestLoadPackageRegistry_NoDependencies(t *testing.T) {
	yaml := `
tools:
  protoc: "25.1"
`
	path := writeTempConfig(t, yaml)
	_, err := LoadPackageRegistry(path)
	if err == nil {
		t.Error("LoadPackageRegistry() expected error for config with no dependencies, got nil")
	}
	if !strings.Contains(err.Error(), "no dependencies found") {
		t.Errorf("expected 'no dependencies found' error, got: %v", err)
	}
}

func TestLoadPackageRegistry_FileNotFound(t *testing.T) {
	_, err := LoadPackageRegistry("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("LoadPackageRegistry() expected error for missing file, got nil")
	}
}

func TestLoadPackageRegistry_InvalidYAML(t *testing.T) {
	path := writeTempConfig(t, "{{invalid yaml content")
	_, err := LoadPackageRegistry(path)
	if err == nil {
		t.Error("LoadPackageRegistry() expected error for invalid YAML, got nil")
	}
}

func TestLoadPackageRegistry_FlatTakesPrecedenceOverNested(t *testing.T) {
	// When both flat and nested exist, flat should win (flat has data, nested is ignored)
	yaml := `
go:
  dependencies:
    google.golang.org/protobuf: v1.32.0
lang:
  go:
    dependencies:
      google.golang.org/protobuf: v1.31.0
rust:
  dependencies:
    prost: "0.12"
`
	path := writeTempConfig(t, yaml)
	reg, err := LoadPackageRegistry(path)
	if err != nil {
		t.Fatalf("LoadPackageRegistry() error = %v", err)
	}

	// Flat Go deps are already populated, so nested should NOT override
	if v := reg.Go.Dependencies["google.golang.org/protobuf"]; v != "v1.32.0" {
		t.Errorf("expected flat value v1.32.0, got %q", v)
	}
}

func TestPackageRegistry_GetGoVersion(t *testing.T) {
	reg := &PackageRegistry{
		Go: LanguageConfig{
			Dependencies: map[string]string{
				"google.golang.org/grpc":     "v1.58.0",
				"google.golang.org/protobuf": "v1.31.0",
				"github.com/foo/bar":         "v2.0.0",
			},
		},
	}

	tests := []struct {
		name       string
		modulePath string
		wantVer    string
		wantOk     bool
	}{
		{
			name:       "exact match",
			modulePath: "google.golang.org/grpc",
			wantVer:    "v1.58.0",
			wantOk:     true,
		},
		{
			name:       "subpackage match",
			modulePath: "google.golang.org/grpc/codes",
			wantVer:    "v1.58.0",
			wantOk:     true,
		},
		{
			name:       "deep subpackage match",
			modulePath: "google.golang.org/grpc/internal/resolver/dns",
			wantVer:    "v1.58.0",
			wantOk:     true,
		},
		{
			name:       "not found",
			modulePath: "example.com/unknown/pkg",
			wantVer:    "",
			wantOk:     false,
		},
		{
			name:       "empty string",
			modulePath: "",
			wantVer:    "",
			wantOk:     false,
		},
		{
			name:       "partial domain does not match",
			modulePath: "google.golang.org",
			wantVer:    "",
			wantOk:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVer, gotOk := reg.GetGoVersion(tt.modulePath)
			if gotVer != tt.wantVer || gotOk != tt.wantOk {
				t.Errorf("GetGoVersion(%q) = (%q, %v), want (%q, %v)",
					tt.modulePath, gotVer, gotOk, tt.wantVer, tt.wantOk)
			}
		})
	}
}

func TestPackageRegistry_GetRustVersion(t *testing.T) {
	reg := &PackageRegistry{
		Rust: LanguageConfig{
			Dependencies: map[string]string{
				"prost": "0.12",
				"serde": "1.0",
			},
		},
	}

	tests := []struct {
		name    string
		crate   string
		wantVer string
		wantOk  bool
	}{
		{"found", "prost", "0.12", true},
		{"found serde", "serde", "1.0", true},
		{"not found", "tokio", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := reg.GetRustVersion(tt.crate)
			if v != tt.wantVer || ok != tt.wantOk {
				t.Errorf("GetRustVersion(%q) = (%q, %v), want (%q, %v)", tt.crate, v, ok, tt.wantVer, tt.wantOk)
			}
		})
	}
}

func TestPackageRegistry_GetPythonVersion(t *testing.T) {
	reg := &PackageRegistry{
		Python: LanguageConfig{
			Dependencies: map[string]string{
				"protobuf": "4.24",
			},
		},
	}

	v, ok := reg.GetPythonVersion("protobuf")
	if !ok || v != "4.24" {
		t.Errorf("GetPythonVersion(\"protobuf\") = (%q, %v), want (\"4.24\", true)", v, ok)
	}
	v, ok = reg.GetPythonVersion("nonexistent")
	if ok {
		t.Errorf("GetPythonVersion(\"nonexistent\") ok = true, want false")
	}
}

func TestPackageRegistry_GetJavaVersion(t *testing.T) {
	reg := &PackageRegistry{
		Java: LanguageConfig{
			Dependencies: map[string]string{
				"com.google.protobuf:protobuf-java": "3.24.0",
			},
		},
	}

	v, ok := reg.GetJavaVersion("com.google.protobuf:protobuf-java")
	if !ok || v != "3.24.0" {
		t.Errorf("GetJavaVersion() = (%q, %v), want (\"3.24.0\", true)", v, ok)
	}
	v, ok = reg.GetJavaVersion("nonexistent:artifact")
	if ok {
		t.Errorf("GetJavaVersion(\"nonexistent:artifact\") ok = true, want false")
	}
}

func TestPackageRegistry_GetTypeScriptVersion(t *testing.T) {
	reg := &PackageRegistry{
		TypeScript: LanguageConfig{
			Dependencies: map[string]string{
				"@bufbuild/protobuf": "1.4.0",
			},
		},
	}

	v, ok := reg.GetTypeScriptVersion("@bufbuild/protobuf")
	if !ok || v != "1.4.0" {
		t.Errorf("GetTypeScriptVersion() = (%q, %v), want (\"1.4.0\", true)", v, ok)
	}
	v, ok = reg.GetTypeScriptVersion("nonexistent")
	if ok {
		t.Errorf("GetTypeScriptVersion(\"nonexistent\") ok = true, want false")
	}
}

func TestPackageRegistry_GetToolVersion(t *testing.T) {
	reg := &PackageRegistry{
		Tools: map[string]string{
			"protoc": "25.1",
			"buf":    "1.28.0",
		},
	}

	tests := []struct {
		tool    string
		wantVer string
		wantOk  bool
	}{
		{"protoc", "25.1", true},
		{"buf", "1.28.0", true},
		{"nonexistent", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			v, ok := reg.GetToolVersion(tt.tool)
			if v != tt.wantVer || ok != tt.wantOk {
				t.Errorf("GetToolVersion(%q) = (%q, %v), want (%q, %v)", tt.tool, v, ok, tt.wantVer, tt.wantOk)
			}
		})
	}
}

func TestPackageRegistry_GetToolVersion_NilToolsMap(t *testing.T) {
	reg := &PackageRegistry{}
	v, ok := reg.GetToolVersion("protoc")
	if ok {
		t.Errorf("GetToolVersion on nil Tools map returned ok=true, v=%q", v)
	}
}

func TestPackageRegistry_GetAllRustDependencies(t *testing.T) {
	reg := &PackageRegistry{
		Rust: LanguageConfig{
			Dependencies: map[string]string{
				"prost": "0.12",
				"serde": "1.0",
			},
		},
	}

	deps := reg.GetAllRustDependencies()
	if len(deps) != 2 {
		t.Fatalf("GetAllRustDependencies() returned %d entries, want 2", len(deps))
	}
	if deps["prost"] != "0.12" {
		t.Errorf("prost = %q, want 0.12", deps["prost"])
	}

	// Verify copy
	deps["tokio"] = "1.0"
	if _, ok := reg.Rust.Dependencies["tokio"]; ok {
		t.Error("GetAllRustDependencies returned reference instead of copy")
	}
}

// ---------------------------------------------------------------------------
// KnownPackages consistency tests
// ---------------------------------------------------------------------------

func TestKnownPackages_AllHaveMappingsForListedLanguages(t *testing.T) {
	for _, pkg := range KnownPackages {
		for _, lang := range pkg.Languages {
			mappings, ok := pkg.Mappings[lang]
			if !ok {
				t.Errorf("package %q lists language %q but has no mapping entry", pkg.Name, lang)
				continue
			}
			if len(mappings) == 0 {
				t.Errorf("package %q has empty mapping for language %q", pkg.Name, lang)
			}
		}
	}
}

func TestKnownPackages_MappingLanguagesMatchLanguagesField(t *testing.T) {
	for _, pkg := range KnownPackages {
		langSet := make(map[string]bool)
		for _, lang := range pkg.Languages {
			langSet[lang] = true
		}
		for lang := range pkg.Mappings {
			if !langSet[lang] {
				t.Errorf("package %q has mapping for %q but it is not listed in Languages", pkg.Name, lang)
			}
		}
	}
}

func TestKnownPackages_UniqueNames(t *testing.T) {
	seen := make(map[string]bool)
	for _, pkg := range KnownPackages {
		if seen[pkg.Name] {
			t.Errorf("duplicate package name: %q", pkg.Name)
		}
		seen[pkg.Name] = true
	}
}

func TestKnownPackages_NonEmptyFields(t *testing.T) {
	for _, pkg := range KnownPackages {
		if pkg.Name == "" {
			t.Error("found KnownPackages entry with empty Name")
		}
		if pkg.Description == "" {
			t.Errorf("package %q has empty Description", pkg.Name)
		}
		if len(pkg.Languages) == 0 {
			t.Errorf("package %q has empty Languages", pkg.Name)
		}
		if len(pkg.Mappings) == 0 {
			t.Errorf("package %q has empty Mappings", pkg.Name)
		}
	}
}

func TestKnownPackages_ValidLanguageValues(t *testing.T) {
	validLangs := map[string]bool{
		"go": true, "java": true, "python": true, "rust": true, "typescript": true,
	}
	for _, pkg := range KnownPackages {
		for _, lang := range pkg.Languages {
			if !validLangs[lang] {
				t.Errorf("package %q has unknown language %q", pkg.Name, lang)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// LoadPackageRegistry: tools field
// ---------------------------------------------------------------------------

func TestLoadPackageRegistry_WithTools(t *testing.T) {
	yamlContent := `
go:
  dependencies:
    google.golang.org/grpc: v1.58.0
tools:
  protoc: "25.1"
  buf: "1.28.0"
`
	path := writeTempConfig(t, yamlContent)
	reg, err := LoadPackageRegistry(path)
	if err != nil {
		t.Fatalf("LoadPackageRegistry() error = %v", err)
	}

	if len(reg.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(reg.Tools))
	}
	if v := reg.Tools["protoc"]; v != "25.1" {
		t.Errorf("protoc tool version = %q, want 25.1", v)
	}
}

// ---------------------------------------------------------------------------
// LanguageConfig version/package fields
// ---------------------------------------------------------------------------

func TestLoadPackageRegistry_LanguageConfigFields(t *testing.T) {
	yamlContent := `
go:
  version: v1.0.0
  dependencies:
    google.golang.org/grpc: v1.58.0
`
	path := writeTempConfig(t, yamlContent)
	reg, err := LoadPackageRegistry(path)
	if err != nil {
		t.Fatalf("LoadPackageRegistry() error = %v", err)
	}

	if reg.Go.Version != "v1.0.0" {
		t.Errorf("Go.Version = %q, want v1.0.0", reg.Go.Version)
	}
}
