// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package depfiles

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_splitVersionPrefix(t *testing.T) {
	tests := []struct {
		in         string
		wantPrefix string
		wantBare   string
	}{
		{"v1.34.2", "v", "1.34.2"},
		{"^1.10.1", "^", "1.10.1"},
		{">=4.25.5", ">=", "4.25.5"},
		{"~2.3.4", "~", "2.3.4"},
		{"0.12.6", "", "0.12.6"},
	}
	for _, tt := range tests {
		prefix, bare := splitVersionPrefix(tt.in)
		if prefix != tt.wantPrefix || bare != tt.wantBare {
			t.Errorf("splitVersionPrefix(%q) = (%q, %q), want (%q, %q)", tt.in, prefix, bare, tt.wantPrefix, tt.wantBare)
		}
	}
}

func TestCompareVersionStrings(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"1.34.2", "1.34.1", 1},
		{"1.34.1", "1.34.2", -1},
		{"1.34.2", "1.34.2", 0},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0", 0},
		{"1.1.0", "1.0.0", 1},
	}
	for _, tt := range tests {
		got := CompareVersionStrings(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("CompareVersionStrings(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestFormatVersionForLanguage(t *testing.T) {
	tests := []struct {
		lang, ver, want string
	}{
		{"go", "1.34.2", "v1.34.2"},
		{"python", "4.25.5", ">=4.25.5"},
		{"typescript", "1.10.1", "^1.10.1"},
		{"rust", "0.12.6", "0.12.6"},
		{"java", "4.28.3", "4.28.3"},
	}
	for _, tt := range tests {
		got := FormatVersionForLanguage(tt.lang, tt.ver)
		if got != tt.want {
			t.Errorf("FormatVersionForLanguage(%q, %q) = %q, want %q", tt.lang, tt.ver, got, tt.want)
		}
	}
}

// ============================================================================
// UpdateLevel tests
// ============================================================================

func TestUpdateLevelString(t *testing.T) {
	tests := []struct {
		level UpdateLevel
		want  string
	}{
		{Patch, "patch"},
		{Minor, "minor"},
		{Major, "major"},
		{UpdateLevel(-1), "unknown"},
		{UpdateLevel(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.want {
			t.Errorf("UpdateLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestUpdateLevelConstants(t *testing.T) {
	// Verify the ordering: Patch < Minor < Major
	if Patch >= Minor {
		t.Errorf("expected Patch (%d) < Minor (%d)", Patch, Minor)
	}
	if Minor >= Major {
		t.Errorf("expected Minor (%d) < Major (%d)", Minor, Major)
	}
}

// ============================================================================
// ClassifyUpdate tests
// ============================================================================

func TestClassifyUpdate(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    UpdateLevel
	}{
		{
			name:    "patch update",
			current: "1.0.0",
			latest:  "1.0.1",
			want:    Patch,
		},
		{
			name:    "minor update",
			current: "1.0.0",
			latest:  "1.1.0",
			want:    Minor,
		},
		{
			name:    "major update",
			current: "1.0.0",
			latest:  "2.0.0",
			want:    Major,
		},
		{
			name:    "no update same version",
			current: "1.0.0",
			latest:  "1.0.0",
			want:    UpdateLevel(-1),
		},
		{
			name:    "downgrade returns negative",
			current: "2.0.0",
			latest:  "1.0.0",
			want:    UpdateLevel(-1),
		},
		{
			name:    "with v prefix on current",
			current: "v1.0.0",
			latest:  "v1.0.2",
			want:    Patch,
		},
		{
			name:    "with caret prefix on current",
			current: "^1.0.0",
			latest:  "^1.1.0",
			want:    Minor,
		},
		{
			name:    "with >= prefix",
			current: ">=1.0.0",
			latest:  ">=2.0.0",
			want:    Major,
		},
		{
			name:    "mixed prefixes stripped for comparison",
			current: "v1.0.0",
			latest:  "^1.0.5",
			want:    Patch,
		},
		{
			name:    "large patch number",
			current: "1.0.0",
			latest:  "1.0.999",
			want:    Patch,
		},
		{
			name:    "minor with patch change too",
			current: "1.2.3",
			latest:  "1.5.0",
			want:    Minor,
		},
		{
			name:    "major with minor and patch change",
			current: "1.2.3",
			latest:  "3.0.0",
			want:    Major,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyUpdate(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("ClassifyUpdate(%q, %q) = %d (%s), want %d (%s)",
					tt.current, tt.latest, got, got.String(), tt.want, tt.want.String())
			}
		})
	}
}

// ============================================================================
// StripVersionPrefix tests
// ============================================================================

func TestStripVersionPrefix(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"v1.0.0", "1.0.0"},
		{"^2.3.4", "2.3.4"},
		{">=1.5.0", "1.5.0"},
		{"~3.2.1", "3.2.1"},
		{"1.0.0", "1.0.0"},
		{"", ""},
		{"v", ""},
		{"vv1.0.0", "v1.0.0"}, // only strips one prefix
	}
	for _, tt := range tests {
		got := StripVersionPrefix(tt.in)
		if got != tt.want {
			t.Errorf("StripVersionPrefix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ============================================================================
// splitVersionPrefix edge cases
// ============================================================================

func Test_splitVersionPrefix_EdgeCases(t *testing.T) {
	tests := []struct {
		in         string
		wantPrefix string
		wantBare   string
	}{
		{"", "", ""},
		{"v", "v", ""},
		{">=", ">=", ""},
		{"^", "^", ""},
		{"~", "~", ""},
		{"abc", "", "abc"},
		{"v0.0.0", "v", "0.0.0"},
		{">=0.0.1", ">=", "0.0.1"},
	}
	for _, tt := range tests {
		prefix, bare := splitVersionPrefix(tt.in)
		if prefix != tt.wantPrefix || bare != tt.wantBare {
			t.Errorf("splitVersionPrefix(%q) = (%q, %q), want (%q, %q)",
				tt.in, prefix, bare, tt.wantPrefix, tt.wantBare)
		}
	}
}

// ============================================================================
// CompareVersionStrings edge cases
// ============================================================================

func TestCompareVersionStrings_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		v1, v2 string
		want   int
	}{
		{"empty strings", "", "", 0},
		{"one empty v1", "", "1.0.0", -1},
		{"one empty v2", "1.0.0", "", 1},
		{"single component equal", "5", "5", 0},
		{"single component greater", "10", "5", 1},
		{"single component less", "5", "10", -1},
		{"different lengths padded with zero", "1.0", "1.0.0", 0},
		{"different lengths v1 shorter but greater", "2", "1.0.0", 1},
		{"different lengths v1 longer", "1.0.0.1", "1.0.0", 1},
		{"four components equal", "1.2.3.4", "1.2.3.4", 0},
		{"large version numbers", "100.200.300", "100.200.299", 1},
		{"zero versions", "0.0.0", "0.0.0", 0},
		{"zero vs non-zero", "0.0.0", "0.0.1", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareVersionStrings(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("CompareVersionStrings(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

// ============================================================================
// VersionToWildcard edge cases
// ============================================================================

// ============================================================================
// FormatVersionForLanguage edge cases
// ============================================================================

func TestFormatVersionForLanguage_EdgeCases(t *testing.T) {
	tests := []struct {
		lang, ver, want string
	}{
		{"go", "v1.0.0", "v1.0.0"},         // already has v prefix, strips then re-adds
		{"python", "v2.0.0", ">=2.0.0"},    // strips v, adds >=
		{"typescript", "v3.0.0", "^3.0.0"}, // strips v, adds ^
		{"rust", "v1.0.0", "1.0.0"},        // strips v, no prefix
		{"java", "v1.0.0", "1.0.0"},        // strips v, no prefix
		{"unknown_lang", "1.0.0", "1.0.0"}, // unknown language uses default
		{"go", "", "v"},                    // empty version
		{"python", "", ">="},               // empty version
		{"typescript", "", "^"},            // empty version
		{"", "1.0.0", "1.0.0"},             // empty language uses default
	}
	for _, tt := range tests {
		got := FormatVersionForLanguage(tt.lang, tt.ver)
		if got != tt.want {
			t.Errorf("FormatVersionForLanguage(%q, %q) = %q, want %q", tt.lang, tt.ver, got, tt.want)
		}
	}
}

// ============================================================================
// NewEmptyDepsConfig tests
// ============================================================================

func TestNewEmptyDepsConfig(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	if cfg == nil {
		t.Fatal("NewEmptyDepsConfig returned nil")
	}
	if cfg.Lang.Go.Dependencies == nil {
		t.Error("Go dependencies map is nil")
	}
	if cfg.Lang.Java.Dependencies == nil {
		t.Error("Java dependencies map is nil")
	}
	if cfg.Lang.TypeScript.Dependencies == nil {
		t.Error("TypeScript dependencies map is nil")
	}
	if cfg.Lang.Python.Dependencies == nil {
		t.Error("Python dependencies map is nil")
	}
	if cfg.Lang.Rust.Dependencies == nil {
		t.Error("Rust dependencies map is nil")
	}
	// All maps should be empty
	if len(cfg.Lang.Go.Dependencies) != 0 {
		t.Errorf("expected empty Go deps, got %d", len(cfg.Lang.Go.Dependencies))
	}
	if len(cfg.Lang.Java.Dependencies) != 0 {
		t.Errorf("expected empty Java deps, got %d", len(cfg.Lang.Java.Dependencies))
	}
}

// ============================================================================
// GetDepsMapForLang tests
// ============================================================================

func TestGetDepsMapForLang(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	cfg.Lang.Go.Dependencies["google.golang.org/protobuf"] = "v1.34.2"
	cfg.Lang.Java.Dependencies["com.google.protobuf:protobuf-java"] = "4.28.3"
	cfg.Lang.TypeScript.Dependencies["@bufbuild/protobuf"] = "^1.10.1"
	cfg.Lang.Python.Dependencies["protobuf"] = ">=4.25.5"
	cfg.Lang.Rust.Dependencies["prost"] = "0.12.6"

	tests := []struct {
		lang    string
		wantNil bool
		wantLen int
	}{
		{"go", false, 1},
		{"java", false, 1},
		{"typescript", false, 1},
		{"python", false, 1},
		{"rust", false, 1},
		{"unknown", true, 0},
		{"", true, 0},
		{"GO", true, 0},     // case sensitive
		{"Go", true, 0},     // case sensitive
		{"PYTHON", true, 0}, // case sensitive
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			m := GetDepsMapForLang(cfg, tt.lang)
			if tt.wantNil {
				if m != nil {
					t.Errorf("GetDepsMapForLang(%q) = %v, want nil", tt.lang, m)
				}
			} else {
				if m == nil {
					t.Fatalf("GetDepsMapForLang(%q) = nil, want non-nil", tt.lang)
				}
				if len(m) != tt.wantLen {
					t.Errorf("GetDepsMapForLang(%q) has %d entries, want %d", tt.lang, len(m), tt.wantLen)
				}
			}
		})
	}
}

// ============================================================================
// GatherLanguageDeps tests
// ============================================================================

func TestGatherLanguageDeps_AllEmpty(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	result := GatherLanguageDeps(cfg)
	if len(result) != 0 {
		t.Errorf("expected 0 results for empty config, got %d", len(result))
	}
}

func TestGatherLanguageDeps_SomePopulated(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	cfg.Lang.Go.Dependencies["pkg"] = "v1.0.0"
	cfg.Lang.Python.Dependencies["pkg"] = ">=1.0.0"

	result := GatherLanguageDeps(cfg)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	// Check that the correct languages are returned
	langs := make(map[string]bool)
	for _, ld := range result {
		langs[ld.Lang] = true
	}
	if !langs["go"] {
		t.Error("expected Go in results")
	}
	if !langs["python"] {
		t.Error("expected Python in results")
	}
	if langs["java"] {
		t.Error("did not expect Java in results")
	}
}

func TestGatherLanguageDeps_AllPopulated(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	cfg.Lang.Go.Dependencies["a"] = "v1"
	cfg.Lang.Java.Dependencies["b"] = "2"
	cfg.Lang.TypeScript.Dependencies["c"] = "^3"
	cfg.Lang.Python.Dependencies["d"] = ">=4"
	cfg.Lang.Rust.Dependencies["e"] = "5"

	result := GatherLanguageDeps(cfg)
	if len(result) != 5 {
		t.Fatalf("expected 5 results, got %d", len(result))
	}

	// Verify ordering matches the function's implementation:
	// go, java, typescript, python, rust
	expectedOrder := []string{"go", "java", "typescript", "python", "rust"}
	for i, ld := range result {
		if ld.Lang != expectedOrder[i] {
			t.Errorf("result[%d].Lang = %q, want %q", i, ld.Lang, expectedOrder[i])
		}
	}
}

func TestGatherLanguageDeps_NilMaps(t *testing.T) {
	cfg := &DepsConfig{} // All maps are nil
	result := GatherLanguageDeps(cfg)
	if len(result) != 0 {
		t.Errorf("expected 0 results for nil deps maps, got %d", len(result))
	}
}

// ============================================================================
// LoadDepsConfig and WriteDepsConfig tests
// ============================================================================

func TestLoadWriteDepsConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deps.yaml")

	cfg := NewEmptyDepsConfig()
	cfg.Lang.Go.Dependencies["google.golang.org/protobuf"] = "v1.34.2"
	cfg.Lang.Java.Dependencies["com.google.protobuf:protobuf-java"] = "4.28.3"
	cfg.Lang.TypeScript.Dependencies["@bufbuild/protobuf"] = "^1.10.1"

	// Write
	err := WriteDepsConfig(path, cfg)
	if err != nil {
		t.Fatalf("WriteDepsConfig failed: %v", err)
	}

	// Read back
	loaded, err := LoadDepsConfig(path)
	if err != nil {
		t.Fatalf("LoadDepsConfig failed: %v", err)
	}

	// Verify round-trip
	if loaded.Lang.Go.Dependencies["google.golang.org/protobuf"] != "v1.34.2" {
		t.Errorf("Go dep mismatch: got %q", loaded.Lang.Go.Dependencies["google.golang.org/protobuf"])
	}
	if loaded.Lang.Java.Dependencies["com.google.protobuf:protobuf-java"] != "4.28.3" {
		t.Errorf("Java dep mismatch: got %q", loaded.Lang.Java.Dependencies["com.google.protobuf:protobuf-java"])
	}
	if loaded.Lang.TypeScript.Dependencies["@bufbuild/protobuf"] != "^1.10.1" {
		t.Errorf("TypeScript dep mismatch: got %q", loaded.Lang.TypeScript.Dependencies["@bufbuild/protobuf"])
	}
}

func TestLoadDepsConfig_NonexistentFile(t *testing.T) {
	_, err := LoadDepsConfig("/nonexistent/path/deps.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadDepsConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")

	err := os.WriteFile(path, []byte("{{{{invalid yaml!!!!"), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err = LoadDepsConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadDepsConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")

	err := os.WriteFile(path, []byte(""), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadDepsConfig(path)
	if err != nil {
		t.Fatalf("LoadDepsConfig on empty file failed: %v", err)
	}
	// All maps should be nil for empty YAML
	if cfg.Lang.Go.Dependencies != nil && len(cfg.Lang.Go.Dependencies) > 0 {
		t.Error("expected nil or empty Go deps for empty file")
	}
}

func TestLoadDepsConfig_MissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.yaml")

	// YAML with only Go dependencies
	content := `lang:
  go:
    dependencies:
      google.golang.org/protobuf: "v1.34.2"
`
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadDepsConfig(path)
	if err != nil {
		t.Fatalf("LoadDepsConfig on partial file failed: %v", err)
	}
	if len(cfg.Lang.Go.Dependencies) != 1 {
		t.Errorf("expected 1 Go dep, got %d", len(cfg.Lang.Go.Dependencies))
	}
	// Other languages should be nil
	if cfg.Lang.Java.Dependencies != nil && len(cfg.Lang.Java.Dependencies) > 0 {
		t.Error("expected nil or empty Java deps for partial file")
	}
}

func TestWriteDepsConfig_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.yaml")

	cfg := NewEmptyDepsConfig()
	err := WriteDepsConfig(path, cfg)
	if err != nil {
		t.Fatalf("WriteDepsConfig failed: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected file to be created")
	}
}

func TestWriteDepsConfig_InvalidPath(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	err := WriteDepsConfig("/nonexistent/dir/deps.yaml", cfg)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// ============================================================================
// EnsureDependencyFiles tests (limited to file-existence logic)
// ============================================================================

func TestEnsureDependencyFiles_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	depsPath := filepath.Join(dir, "dependencies.yaml")

	// Create a valid deps file
	cfg := NewEmptyDepsConfig()
	err := WriteDepsConfig(depsPath, cfg)
	if err != nil {
		t.Fatalf("setup: WriteDepsConfig failed: %v", err)
	}

	// EnsureDependencyFiles should not error when file exists
	err = EnsureDependencyFiles(dir)
	if err != nil {
		t.Fatalf("EnsureDependencyFiles failed for existing file: %v", err)
	}
}

// ============================================================================
// GetDepsMapForLang returns the same map reference
// ============================================================================

func TestGetDepsMapForLang_ReturnsReference(t *testing.T) {
	cfg := NewEmptyDepsConfig()
	goMap := GetDepsMapForLang(cfg, "go")
	goMap["test-pkg"] = "v1.0.0"

	// Should be reflected in the original config
	if cfg.Lang.Go.Dependencies["test-pkg"] != "v1.0.0" {
		t.Error("changes to returned map should affect the original config")
	}
}

// ============================================================================
// splitSemver tests (via ClassifyUpdate behavior)
// ============================================================================

func TestClassifyUpdate_VersionEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    UpdateLevel
	}{
		{
			name:    "single component versions",
			current: "1",
			latest:  "2",
			want:    Major,
		},
		{
			name:    "two component versions minor update",
			current: "1.0",
			latest:  "1.1",
			want:    Minor,
		},
		{
			name:    "two component versions major update",
			current: "1.0",
			latest:  "2.0",
			want:    Major,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyUpdate(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("ClassifyUpdate(%q, %q) = %d (%s), want %d (%s)",
					tt.current, tt.latest, got, got.String(), tt.want, tt.want.String())
			}
		})
	}
}
