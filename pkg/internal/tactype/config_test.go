// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tactype

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Config.TypesModulePath
// ---------------------------------------------------------------------------

func TestTypesModulePath(t *testing.T) {
	tests := []struct {
		name           string
		moduleBasePath string
		want           string
	}{
		{
			name:           "standard module path",
			moduleBasePath: "github.com/myorg/myproject",
			want:           "github.com/myorg/myproject",
		},
		{
			name:           "empty module path",
			moduleBasePath: "",
			want:           "",
		},
		{
			name:           "module path with trailing slash",
			moduleBasePath: "github.com/myorg/myproject/",
			want:           "github.com/myorg/myproject/",
		},
		{
			name:           "module path with subdirectory",
			moduleBasePath: "github.com/myorg/myproject/types",
			want:           "github.com/myorg/myproject/types",
		},
		{
			name:           "simple path",
			moduleBasePath: "myproject",
			want:           "myproject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{ModuleBasePath: tt.moduleBasePath}
			got := c.TypesModulePath()
			if got != tt.want {
				t.Errorf("TypesModulePath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.FormattedVersion
// ---------------------------------------------------------------------------

func TestFormattedVersion(t *testing.T) {
	tests := []struct {
		name       string
		versionTag string
		want       string
	}{
		{
			name:       "version without v prefix",
			versionTag: "1.0.1",
			want:       "v1.0.1",
		},
		{
			name:       "version already has v prefix",
			versionTag: "v1.0.1",
			want:       "v1.0.1",
		},
		{
			name:       "empty version tag",
			versionTag: "",
			want:       "v",
		},
		{
			name:       "version with capital V",
			versionTag: "V1.0.0",
			want:       "vV1.0.0",
		},
		{
			name:       "semver with pre-release",
			versionTag: "1.0.0-beta.1",
			want:       "v1.0.0-beta.1",
		},
		{
			name:       "semver with pre-release and v prefix",
			versionTag: "v1.0.0-beta.1",
			want:       "v1.0.0-beta.1",
		},
		{
			name:       "just v",
			versionTag: "v",
			want:       "v",
		},
		{
			name:       "double v prefix",
			versionTag: "vv1.0.0",
			want:       "vv1.0.0",
		},
		{
			name:       "major only",
			versionTag: "2",
			want:       "v2",
		},
		{
			name:       "major.minor",
			versionTag: "1.2",
			want:       "v1.2",
		},
		{
			name:       "zero version",
			versionTag: "0.0.0",
			want:       "v0.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{VersionTag: tt.versionTag}
			got := c.FormattedVersion()
			if got != tt.want {
				t.Errorf("FormattedVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.UtilsPath
// ---------------------------------------------------------------------------

func TestUtilsPath(t *testing.T) {
	tests := []struct {
		name           string
		moduleBasePath string
		want           string
	}{
		{
			name:           "derives from types module path",
			moduleBasePath: "github.com/myorg/myproject",
			want:           "github.com/myorg/myproject/utils",
		},
		{
			name:           "empty module base path",
			moduleBasePath: "",
			want:           "/utils",
		},
		{
			name:           "derives correctly from gitlab path",
			moduleBasePath: "gitlab.com/org/repo",
			want:           "gitlab.com/org/repo/utils",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				ModuleBasePath: tt.moduleBasePath,
			}
			got := c.UtilsPath()
			if got != tt.want {
				t.Errorf("UtilsPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUtilsPathConsistencyWithTypesModulePath verifies that UtilsPath uses
// TypesModulePath() when UtilsModulePath is empty.
func TestUtilsPathConsistencyWithTypesModulePath(t *testing.T) {
	c := &Config{
		ModuleBasePath: "github.com/myorg/myproject",
	}

	typesPath := c.TypesModulePath()
	utilsPath := c.UtilsPath()
	expected := typesPath + "/utils"

	if utilsPath != expected {
		t.Errorf("UtilsPath() = %q, expected TypesModulePath()+\"/utils\" = %q", utilsPath, expected)
	}
}

// ---------------------------------------------------------------------------
// Config.AccessModeForState
// ---------------------------------------------------------------------------

func TestAccessModeForStateScopesExactStateRoots(t *testing.T) {
	c := &Config{
		States: []StateEntry{
			{ProtoFile: "services/identity.proto", MessageName: "Identity", AccessMode: AccessRO},
			{ProtoFile: "services/gateway.proto", MessageName: "GatewayConfigState", AccessMode: AccessRW},
		},
	}

	mode, ok := c.AccessModeForState("services/identity.proto", "Identity")
	if !ok {
		t.Fatalf("AccessModeForState() did not match exact state root")
	}
	if mode != AccessRO {
		t.Fatalf("AccessModeForState() mode = %s, want %s", mode, AccessRO)
	}

	mode, ok = c.AccessModeForState("services/identity.proto", "Cluster")
	if ok {
		t.Fatalf("AccessModeForState() matched unrelated state root with mode %s", mode)
	}
	if mode != AccessRW {
		t.Fatalf("AccessModeForState() fallback mode = %s, want %s", mode, AccessRW)
	}

	mode, ok = c.AccessModeForState("services/gateway.proto", "Identity")
	if ok {
		t.Fatalf("AccessModeForState() matched same message name from wrong proto with mode %s", mode)
	}
	if mode != AccessRW {
		t.Fatalf("AccessModeForState() fallback mode = %s, want %s", mode, AccessRW)
	}
}

func TestAccessModeForStateAllStatesIncludesUnmatchedReadOnly(t *testing.T) {
	c := &Config{AllStates: true}

	mode, ok := c.AccessModeForState("services/identity.proto", "Cluster")
	if !ok {
		t.Fatalf("AccessModeForState() did not include unmatched root for all-state config")
	}
	if mode != AccessRO {
		t.Fatalf("AccessModeForState() all-state mode = %s, want %s", mode, AccessRO)
	}
}

// ---------------------------------------------------------------------------
// Config.resolveLanguagePlugin (via ResolveProtocPaths - indirect coverage)
// ---------------------------------------------------------------------------

// TestResolveLanguagePluginJavaAndPython verifies that Java and Python require
// no separate plugin (built into protoc) so resolveLanguagePlugin returns nil.
func TestResolveLanguagePluginJavaAndPython(t *testing.T) {
	for _, lang := range []string{"java", "python"} {
		t.Run(lang, func(t *testing.T) {
			c := &Config{Language: lang}
			err := c.resolveLanguagePlugin()
			if err != nil {
				t.Fatalf("resolveLanguagePlugin() for %s returned unexpected error: %v", lang, err)
			}
		})
	}
}

// TestResolveLanguagePluginUnknownLanguage ensures that an unknown language
// does not cause an error -- it simply does nothing.
func TestResolveLanguagePluginUnknownLanguage(t *testing.T) {
	c := &Config{Language: "cobol"}
	err := c.resolveLanguagePlugin()
	if err != nil {
		t.Fatalf("resolveLanguagePlugin() for unknown language returned unexpected error: %v", err)
	}
}

// TestResolveLanguagePluginGoAlreadySet verifies that if ProtocGenGo is
// already set, it is not overridden.
func TestResolveLanguagePluginGoAlreadySet(t *testing.T) {
	c := &Config{
		Language:    "go",
		ProtocGenGo: "/custom/path/protoc-gen-go",
	}
	err := c.resolveLanguagePlugin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProtocGenGo != "/custom/path/protoc-gen-go" {
		t.Fatalf("expected ProtocGenGo to remain %q, got %q", "/custom/path/protoc-gen-go", c.ProtocGenGo)
	}
}

// TestResolveLanguagePluginRustAlreadySet verifies that if ProtocGenRs is
// already set, it is not overridden.
func TestResolveLanguagePluginRustAlreadySet(t *testing.T) {
	c := &Config{
		Language:    "rust",
		ProtocGenRs: "/custom/path/protoc-gen-prost",
	}
	err := c.resolveLanguagePlugin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProtocGenRs != "/custom/path/protoc-gen-prost" {
		t.Fatalf("expected ProtocGenRs to remain %q, got %q", "/custom/path/protoc-gen-prost", c.ProtocGenRs)
	}
}

// TestResolveLanguagePluginTypescriptAlreadySet verifies that if ProtocGenTs
// is already set, it is not overridden.
func TestResolveLanguagePluginTypescriptAlreadySet(t *testing.T) {
	c := &Config{
		Language:    "typescript",
		ProtocGenTs: "/custom/path/protoc-gen-es",
	}
	err := c.resolveLanguagePlugin()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProtocGenTs != "/custom/path/protoc-gen-es" {
		t.Fatalf("expected ProtocGenTs to remain %q, got %q", "/custom/path/protoc-gen-es", c.ProtocGenTs)
	}
}

// TestResolveLanguagePluginEmptyLanguage verifies that an empty Language string
// does not cause any error.
func TestResolveLanguagePluginEmptyLanguage(t *testing.T) {
	c := &Config{Language: ""}
	err := c.resolveLanguagePlugin()
	if err != nil {
		t.Fatalf("resolveLanguagePlugin() with empty language returned unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config field defaults and zero values
// ---------------------------------------------------------------------------

func TestConfigZeroValue(t *testing.T) {
	var c Config

	// All string fields should be empty
	if c.ProtoDir != "" {
		t.Errorf("expected ProtoDir to be empty, got %q", c.ProtoDir)
	}
	if c.Language != "" {
		t.Errorf("expected Language to be empty, got %q", c.Language)
	}
	if c.InputDir != "" {
		t.Errorf("expected InputDir to be empty, got %q", c.InputDir)
	}
	if c.OutputDir != "" {
		t.Errorf("expected OutputDir to be empty, got %q", c.OutputDir)
	}
	if c.ModuleBasePath != "" {
		t.Errorf("expected ModuleBasePath to be empty, got %q", c.ModuleBasePath)
	}
	if c.VersionTag != "" {
		t.Errorf("expected VersionTag to be empty, got %q", c.VersionTag)
	}
	// Bool fields should be false
	if c.Verbose {
		t.Error("expected Verbose to be false")
	}
	if c.LocalMode {
		t.Error("expected LocalMode to be false")
	}

	// Map fields should be nil
	if c.Tools != nil {
		t.Errorf("expected Tools to be nil, got %v", c.Tools)
	}
}

// ---------------------------------------------------------------------------
// Config methods with various field combinations
// ---------------------------------------------------------------------------

func TestFormattedVersionDoesNotMutateConfig(t *testing.T) {
	c := &Config{VersionTag: "1.0.0"}
	_ = c.FormattedVersion()

	// The VersionTag field should NOT be modified by FormattedVersion.
	if c.VersionTag != "1.0.0" {
		t.Errorf("FormattedVersion mutated VersionTag: got %q, want %q", c.VersionTag, "1.0.0")
	}
}

func TestFormattedVersionIdempotent(t *testing.T) {
	c := &Config{VersionTag: "1.0.0"}
	first := c.FormattedVersion()
	second := c.FormattedVersion()

	if first != second {
		t.Errorf("FormattedVersion() not idempotent: first=%q, second=%q", first, second)
	}
}

// ---------------------------------------------------------------------------
// Config.ResolveProtocPaths -- tests that don't depend on system state
// ---------------------------------------------------------------------------

func TestResolveProtocPathsWithInvalidProtocPath(t *testing.T) {
	c := &Config{
		ProtocPath: "/nonexistent/path/to/protoc",
		Language:   "go",
	}
	err := c.ResolveProtocPaths()
	if err == nil {
		t.Fatal("expected error for nonexistent protoc path, got nil")
	}
}

func TestResolveProtocPathsWithValidProtocButMissingPlugin(t *testing.T) {
	// Use a real binary that exists on macOS/Linux as a stand-in for protoc.
	// /usr/bin/true exists on most Unix systems and will pass the os.Stat check.
	protocPath := "/usr/bin/true"

	c := &Config{
		ProtocPath: protocPath,
		Language:   "java",
	}
	err := c.ResolveProtocPaths()
	// Java is built into protoc, so no plugin resolution needed.
	if err != nil {
		t.Fatalf("expected no error for java language with valid protoc, got: %v", err)
	}
}

func TestResolveProtocPathsPythonNoPlugin(t *testing.T) {
	protocPath := "/usr/bin/true"
	c := &Config{
		ProtocPath: protocPath,
		Language:   "python",
	}
	err := c.ResolveProtocPaths()
	if err != nil {
		t.Fatalf("expected no error for python language with valid protoc, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config.PEP440Version
// ---------------------------------------------------------------------------

func TestPEP440Version(t *testing.T) {
	tests := []struct {
		name       string
		versionTag string
		want       string
	}{
		{
			name:       "standard version",
			versionTag: "1.0.0",
			want:       "1.0.0",
		},
		{
			name:       "version with v prefix stripped",
			versionTag: "v1.0.0",
			want:       "1.0.0",
		},
		{
			name:       "SNAPSHOT converted to dev0",
			versionTag: "0.0.0-SNAPSHOT",
			want:       "0.0.0.dev0",
		},
		{
			name:       "SNAPSHOT with v prefix",
			versionTag: "v0.0.0-SNAPSHOT",
			want:       "0.0.0.dev0",
		},
		{
			name:       "version without SNAPSHOT unchanged",
			versionTag: "2.3.4",
			want:       "2.3.4",
		},
		{
			name:       "version with hyphen but not SNAPSHOT",
			versionTag: "1.0.0-beta.1",
			want:       "1.0.0-beta.1",
		},
		{
			name:       "empty version",
			versionTag: "",
			want:       "",
		},
		{
			name:       "just v",
			versionTag: "v",
			want:       "",
		},
		{
			name:       "SNAPSHOT only replaces first occurrence",
			versionTag: "1.0.0-SNAPSHOT-SNAPSHOT",
			want:       "1.0.0.dev0-SNAPSHOT",
		},
		{
			name:       "large version with SNAPSHOT",
			versionTag: "v100.200.300-SNAPSHOT",
			want:       "100.200.300.dev0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{VersionTag: tt.versionTag}
			got := c.PEP440Version()
			if got != tt.want {
				t.Errorf("PEP440Version() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPEP440VersionDoesNotMutateConfig(t *testing.T) {
	c := &Config{VersionTag: "v1.0.0-SNAPSHOT"}
	_ = c.PEP440Version()
	if c.VersionTag != "v1.0.0-SNAPSHOT" {
		t.Errorf("PEP440Version mutated VersionTag: got %q", c.VersionTag)
	}
}

// ---------------------------------------------------------------------------
// Config.ErrorsPath
// ---------------------------------------------------------------------------

func TestErrorsPath(t *testing.T) {
	tests := []struct {
		name           string
		moduleBasePath string
		want           string
	}{
		{
			name:           "standard path",
			moduleBasePath: "github.com/myorg/myproject",
			want:           "github.com/myorg/myproject/types/sys/errors",
		},
		{
			name:           "empty base path",
			moduleBasePath: "",
			want:           "/types/sys/errors",
		},
		{
			name:           "simple base path",
			moduleBasePath: "myproject",
			want:           "myproject/types/sys/errors",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{ModuleBasePath: tt.moduleBasePath}
			got := c.ErrorsPath()
			if got != tt.want {
				t.Errorf("ErrorsPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.TypesImportPath
// ---------------------------------------------------------------------------

func TestTypesImportPath(t *testing.T) {
	tests := []struct {
		name           string
		moduleBasePath string
		parentDir      string
		packageName    string
		want           string
	}{
		{
			name:           "parent dir base matches package name",
			moduleBasePath: "github.com/myorg/myproject",
			parentDir:      "services/baseball",
			packageName:    "baseball",
			want:           "github.com/myorg/myproject/types/services/baseball",
		},
		{
			name:           "parent dir base does not match package name",
			moduleBasePath: "github.com/myorg/myproject",
			parentDir:      "services",
			packageName:    "baseball",
			want:           "github.com/myorg/myproject/types/services/baseball",
		},
		{
			name:           "empty parent dir",
			moduleBasePath: "github.com/myorg/myproject",
			parentDir:      "",
			packageName:    "pkg",
			want:           "github.com/myorg/myproject/types//pkg",
		},
		{
			name:           "single component parent dir matching package",
			moduleBasePath: "github.com/myorg/myproject",
			parentDir:      "common",
			packageName:    "common",
			want:           "github.com/myorg/myproject/types/common",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{ModuleBasePath: tt.moduleBasePath}
			got := c.TypesImportPath(tt.parentDir, tt.packageName)
			if got != tt.want {
				t.Errorf("TypesImportPath(%q, %q) = %q, want %q", tt.parentDir, tt.packageName, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.LangDir
// ---------------------------------------------------------------------------

func TestLangDir(t *testing.T) {
	c := &Config{OutputDir: "/some/path/go"}
	if c.LangDir() != "/some/path/go" {
		t.Errorf("LangDir() = %q, want %q", c.LangDir(), "/some/path/go")
	}

	c = &Config{OutputDir: ""}
	if c.LangDir() != "" {
		t.Errorf("LangDir() = %q, want empty", c.LangDir())
	}
}

// ---------------------------------------------------------------------------
// Language-specific config fields
// ---------------------------------------------------------------------------

func TestLanguageSpecificFields(t *testing.T) {
	c := Config{
		InputDir:          "/in",
		OutputDir:         "/out",
		ModuleBasePath:    "github.com/myorg/myproject",
		CrateBasePath:     "myorg_myproject",
		JavaPackageBase:   "com.myorg.myproject",
		PythonPackageBase: "myorg.myproject",
		TsPackageBase:     "@myorg/myproject",
	}

	if c.CrateBasePath != "myorg_myproject" {
		t.Errorf("CrateBasePath = %q", c.CrateBasePath)
	}
	if c.JavaPackageBase != "com.myorg.myproject" {
		t.Errorf("JavaPackageBase = %q", c.JavaPackageBase)
	}
	if c.PythonPackageBase != "myorg.myproject" {
		t.Errorf("PythonPackageBase = %q", c.PythonPackageBase)
	}
	if c.TsPackageBase != "@myorg/myproject" {
		t.Errorf("TsPackageBase = %q", c.TsPackageBase)
	}
}

// ---------------------------------------------------------------------------
// RegistryConfig
// ---------------------------------------------------------------------------

func TestRegistryConfig_GitLabPackagesURL(t *testing.T) {
	tests := []struct {
		name      string
		projectID string
		want      string
	}{
		{
			name:      "with project ID",
			projectID: "12345",
			want:      "https://gitlab.com/api/v4/projects/12345/packages",
		},
		{
			name:      "empty project ID",
			projectID: "",
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := RegistryConfig{GitLabProjectID: tt.projectID}
			got := r.GitLabPackagesURL()
			if got != tt.want {
				t.Errorf("GitLabPackagesURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegistryConfig_GitLabNpmURL(t *testing.T) {
	r := RegistryConfig{GitLabProjectID: "42"}
	got := r.GitLabNpmURL()
	want := "https://gitlab.com/api/v4/projects/42/packages/npm"
	if got != want {
		t.Errorf("GitLabNpmURL() = %q, want %q", got, want)
	}

	r = RegistryConfig{}
	if r.GitLabNpmURL() != "" {
		t.Error("expected empty string for empty project ID")
	}
}

func TestRegistryConfig_GitLabMavenURL(t *testing.T) {
	r := RegistryConfig{GitLabProjectID: "42"}
	got := r.GitLabMavenURL()
	want := "https://gitlab.com/api/v4/projects/42/packages/maven"
	if got != want {
		t.Errorf("GitLabMavenURL() = %q, want %q", got, want)
	}

	r = RegistryConfig{}
	if r.GitLabMavenURL() != "" {
		t.Error("expected empty string for empty project ID")
	}
}

func TestRegistryConfig_GitLabPyPIURL(t *testing.T) {
	r := RegistryConfig{GitLabProjectID: "42"}
	got := r.GitLabPyPIURL()
	want := "https://gitlab.com/api/v4/projects/42/packages/pypi/simple"
	if got != want {
		t.Errorf("GitLabPyPIURL() = %q, want %q", got, want)
	}

	r = RegistryConfig{}
	if r.GitLabPyPIURL() != "" {
		t.Error("expected empty string for empty project ID")
	}
}

func TestRegistryConfig_WithMaps(t *testing.T) {
	r := RegistryConfig{
		GitLabProjectID: "12345",
		Interfaces: map[string]string{
			"go":   "github.com/myorg/interfaces",
			"java": "com.myorg:interfaces",
		},
		Imports: map[string]string{
			"go":   "github.com/myorg/imports",
			"java": "com.myorg:imports",
		},
	}

	if r.Interfaces["go"] != "github.com/myorg/interfaces" {
		t.Errorf("Interfaces[go] = %q", r.Interfaces["go"])
	}
	if r.Imports["java"] != "com.myorg:imports" {
		t.Errorf("Imports[java] = %q", r.Imports["java"])
	}
}

// ---------------------------------------------------------------------------
// LanguagesConfig
// ---------------------------------------------------------------------------

func TestLanguagesConfig_Fields(t *testing.T) {
	lc := LanguagesConfig{
		Go: LanguageConfig{
			Interfaces: "github.com/myorg/interfaces",
			Version:    "v1.0.0",
			Dependencies: map[string]string{
				"google.golang.org/protobuf": "v1.34.2",
			},
		},
		Java: LanguageConfig{
			Interfaces: "com.myorg:interfaces",
			Version:    "1.0.0",
		},
		TypeScript: LanguageConfig{
			Interfaces: "@myorg/interfaces",
			Version:    "1.0.0",
		},
		Python: LanguageConfig{
			Interfaces: "myorg-interfaces",
			Version:    "1.0.0",
		},
		Rust: LanguageConfig{
			Interfaces: "myorg_interfaces",
			Version:    "1.0.0",
		},
	}

	if lc.Go.Interfaces != "github.com/myorg/interfaces" {
		t.Errorf("Go.Interfaces = %q", lc.Go.Interfaces)
	}
	if lc.Go.Dependencies["google.golang.org/protobuf"] != "v1.34.2" {
		t.Error("Go dependency missing")
	}
	if lc.Java.Interfaces != "com.myorg:interfaces" {
		t.Errorf("Java.Interfaces = %q", lc.Java.Interfaces)
	}
	if lc.TypeScript.Version != "1.0.0" {
		t.Errorf("TypeScript.Version = %q", lc.TypeScript.Version)
	}
}

// ---------------------------------------------------------------------------
// LanguageConfig.JavaImportBase
// ---------------------------------------------------------------------------

func TestLanguageConfig_JavaImportBase(t *testing.T) {
	tests := []struct {
		name             string
		interfaces       string
		interfacesImport string
		want             string
	}{
		{
			name:             "uses InterfacesImport when set",
			interfaces:       "com.myorg:interfaces",
			interfacesImport: "com.myorg.interfaces",
			want:             "com.myorg.interfaces",
		},
		{
			name:             "falls back to Interfaces when InterfacesImport empty",
			interfaces:       "com.myorg:interfaces",
			interfacesImport: "",
			want:             "com.myorg:interfaces",
		},
		{
			name:             "both empty",
			interfaces:       "",
			interfacesImport: "",
			want:             "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lc := LanguageConfig{
				Interfaces:       tt.interfaces,
				InterfacesImport: tt.interfacesImport,
			}
			got := lc.JavaImportBase()
			if got != tt.want {
				t.Errorf("JavaImportBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GrpcEntry and StateEntry structs
// ---------------------------------------------------------------------------

func TestGrpcEntryFields(t *testing.T) {
	fe := GrpcEntry{
		ProtoFile:   "services/baseball.proto",
		ServiceName: "BaseballService",
	}
	if fe.ProtoFile != "services/baseball.proto" {
		t.Errorf("ProtoFile = %q", fe.ProtoFile)
	}
	if fe.ServiceName != "BaseballService" {
		t.Errorf("ServiceName = %q", fe.ServiceName)
	}
}

func TestStateEntryFields(t *testing.T) {
	se := StateEntry{
		ProtoFile:   "services/baseball.proto",
		MessageName: "BaseballState",
		AccessMode:  AccessRW,
	}
	if se.ProtoFile != "services/baseball.proto" {
		t.Errorf("ProtoFile = %q", se.ProtoFile)
	}
	if se.MessageName != "BaseballState" {
		t.Errorf("MessageName = %q", se.MessageName)
	}
	if se.AccessMode != AccessRW {
		t.Error("expected AccessMode to be AccessRW")
	}
}

// ---------------------------------------------------------------------------
// Config with LanguagesConfig and RegistryConfig
// ---------------------------------------------------------------------------

func TestConfigWithLangAndRegistry(t *testing.T) {
	c := Config{
		InputDir:       "/in",
		OutputDir:      "/out",
		ModuleBasePath: "github.com/myorg/myproject",
		Lang: LanguagesConfig{
			Go: LanguageConfig{
				Interfaces: "github.com/myorg/interfaces",
				Version:    "v1.0.0",
			},
		},
		Registry: RegistryConfig{
			GitLabProjectID: "12345",
			Interfaces: map[string]string{
				"go": "github.com/myorg/interfaces",
			},
		},
	}

	if c.Lang.Go.Interfaces != "github.com/myorg/interfaces" {
		t.Errorf("Lang.Go.Interfaces = %q", c.Lang.Go.Interfaces)
	}
	if c.Registry.GitLabProjectID != "12345" {
		t.Errorf("Registry.GitLabProjectID = %q", c.Registry.GitLabProjectID)
	}
}

// ---------------------------------------------------------------------------
// Config defaults for zero-value struct
// ---------------------------------------------------------------------------

func TestConfigZeroValue_LanguageSpecificFields(t *testing.T) {
	var c Config
	if c.CrateBasePath != "" {
		t.Errorf("CrateBasePath should be empty, got %q", c.CrateBasePath)
	}
	if c.JavaPackageBase != "" {
		t.Errorf("JavaPackageBase should be empty, got %q", c.JavaPackageBase)
	}
	if c.PythonPackageBase != "" {
		t.Errorf("PythonPackageBase should be empty, got %q", c.PythonPackageBase)
	}
	if c.TsPackageBase != "" {
		t.Errorf("TsPackageBase should be empty, got %q", c.TsPackageBase)
	}
	if c.AllStates {
		t.Error("AllStates should be false")
	}
	if c.GrpcServers != nil {
		t.Error("GrpcServers should be nil")
	}
	if c.GrpcS2SClients != nil {
		t.Error("GrpcS2SClients should be nil")
	}
	if c.States != nil {
		t.Error("States should be nil")
	}
}

func TestConfigZeroValue_ProtocPaths(t *testing.T) {
	var c Config
	if c.ProtocPath != "" {
		t.Errorf("ProtocPath should be empty, got %q", c.ProtocPath)
	}
	if c.ProtocGenGo != "" {
		t.Errorf("ProtocGenGo should be empty, got %q", c.ProtocGenGo)
	}
	if c.ProtocGenTs != "" {
		t.Errorf("ProtocGenTs should be empty, got %q", c.ProtocGenTs)
	}
	if c.ProtocGenRs != "" {
		t.Errorf("ProtocGenRs should be empty, got %q", c.ProtocGenRs)
	}
}

// ---------------------------------------------------------------------------
// FormattedVersion and PEP440Version consistency
// ---------------------------------------------------------------------------

func TestFormattedVersionAndPEP440VersionConsistency(t *testing.T) {
	// For a standard version, FormattedVersion adds "v" and PEP440Version strips it
	c := &Config{VersionTag: "1.2.3"}

	formatted := c.FormattedVersion()
	pep := c.PEP440Version()

	if formatted != "v1.2.3" {
		t.Errorf("FormattedVersion() = %q, want %q", formatted, "v1.2.3")
	}
	if pep != "1.2.3" {
		t.Errorf("PEP440Version() = %q, want %q", pep, "1.2.3")
	}
}

func TestFormattedVersionAndPEP440VersionSNAPSHOT(t *testing.T) {
	c := &Config{VersionTag: "v0.1.0-SNAPSHOT"}

	formatted := c.FormattedVersion()
	pep := c.PEP440Version()

	if formatted != "v0.1.0-SNAPSHOT" {
		t.Errorf("FormattedVersion() = %q, want %q", formatted, "v0.1.0-SNAPSHOT")
	}
	if pep != "0.1.0.dev0" {
		t.Errorf("PEP440Version() = %q, want %q", pep, "0.1.0.dev0")
	}
}

// ---------------------------------------------------------------------------
// Config.ServiceLabel
// ---------------------------------------------------------------------------

// The label is the service's identity: kernel's service ids, the gateway's
// routes and every token audience use the proto package, so a project whose
// module directory is not named after its package (tenantecho generates into
// <module>/tacplus) must still be labelled by the package.
func TestServiceLabelIsTheServicePackage(t *testing.T) {
	c := &Config{ModuleBasePath: "github.com/tacplus-dev/tenantecho/tacplus", OutputName: "tacplus", SelfPackage: "tenantecho"}
	if got := c.ServiceLabel(); got != "tenantecho" {
		t.Fatalf("ServiceLabel() = %q, want the service's package", got)
	}
	// Without a package of its own, the module directory above the output is
	// the only name there is.
	c = &Config{ModuleBasePath: "gitlab.com/x/kernel/model"}
	if got := c.ServiceLabel(); got != "kernel" {
		t.Fatalf("ServiceLabel() = %q, want the module directory", got)
	}
}
