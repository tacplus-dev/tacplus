// Copyright (c) 2026 Second Star Cavalier LLC

package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// ---------------------------------------------------------------------------
// clientLiteUnsupportedServerSurfaces (PAR-5)
// ---------------------------------------------------------------------------

func TestClientLiteUnsupportedServerSurfaces(t *testing.T) {
	// Full-service and full-client languages never trip this guard.
	for _, lang := range []string{"go", "rust", "java", "python", "typescript"} {
		cfg := &tactype.Config{GrpcServerAll: true, AllStates: true, GrpcS2SClientAll: true}
		if got := clientLiteUnsupportedServerSurfaces(cfg, lang); got != "" {
			t.Fatalf("%s must not trip the client-lite guard, got: %q", lang, got)
		}
	}

	// A lite target with no server-side surfaces passes (the C2D-client case).
	for _, lang := range []string{"swift", "csharp"} {
		if got := clientLiteUnsupportedServerSurfaces(&tactype.Config{}, lang); got != "" {
			t.Fatalf("%s with no server surfaces must pass, got: %q", lang, got)
		}
	}

	// Each server-side surface is rejected loudly with its named reason.
	cases := []struct {
		name   string
		cfg    *tactype.Config
		reason string
	}{
		{"server", &tactype.Config{GrpcServerAll: true}, "server generation"},
		{"s2s", &tactype.Config{GrpcS2SClientAll: true}, "S2S client generation"},
		{"state", &tactype.Config{AllStates: true}, "ServiceManager/state generation"},
		{"state-slice", &tactype.Config{States: []tactype.StateEntry{{}}}, "ServiceManager/state generation"},
	}
	for _, lang := range []string{"swift", "csharp"} {
		for _, tc := range cases {
			got := clientLiteUnsupportedServerSurfaces(tc.cfg, lang)
			if got == "" {
				t.Fatalf("%s/%s: expected a loud rejection, got none", lang, tc.name)
			}
			if !strings.Contains(got, tc.reason) || !strings.Contains(got, lang) {
				t.Fatalf("%s/%s: error should name the surface and language, got: %q", lang, tc.name, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// validatePaths
// ---------------------------------------------------------------------------

func TestValidatePaths(t *testing.T) {
	t.Run("both_exist", func(t *testing.T) {
		inputDir := t.TempDir()
		depsFile := filepath.Join(inputDir, "dependencies.yaml")
		if err := os.WriteFile(depsFile, []byte("tools: {}"), 0644); err != nil {
			t.Fatal(err)
		}

		b := &Builder{config: &tactype.Config{
			InputDir:     inputDir,
			DepsYAMLPath: depsFile,
		}}
		if err := b.validatePaths(); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("input_dir_missing", func(t *testing.T) {
		depsDir := t.TempDir()
		depsFile := filepath.Join(depsDir, "dependencies.yaml")
		if err := os.WriteFile(depsFile, []byte("tools: {}"), 0644); err != nil {
			t.Fatal(err)
		}

		b := &Builder{config: &tactype.Config{
			InputDir:     filepath.Join(t.TempDir(), "nonexistent"),
			DepsYAMLPath: depsFile,
		}}
		err := b.validatePaths()
		if err == nil {
			t.Fatal("expected error for missing input directory")
		}
		if !strings.Contains(err.Error(), "proto directory not found") {
			t.Errorf("error should mention proto directory: %v", err)
		}
	})

	t.Run("deps_yaml_missing", func(t *testing.T) {
		inputDir := t.TempDir()

		b := &Builder{config: &tactype.Config{
			InputDir:     inputDir,
			DepsYAMLPath: filepath.Join(t.TempDir(), "no-such-file.yaml"),
		}}
		err := b.validatePaths()
		if err == nil {
			t.Fatal("expected error for missing dependencies.yaml")
		}
		if !strings.Contains(err.Error(), "dependencies.yaml not found") {
			t.Errorf("error should mention dependencies.yaml: %v", err)
		}
	})

	t.Run("both_missing", func(t *testing.T) {
		b := &Builder{config: &tactype.Config{
			InputDir:     filepath.Join(t.TempDir(), "no-input"),
			DepsYAMLPath: filepath.Join(t.TempDir(), "no-deps.yaml"),
		}}
		err := b.validatePaths()
		if err == nil {
			t.Fatal("expected error when both paths are missing")
		}
		// InputDir is checked first, so the error should be about the proto directory
		if !strings.Contains(err.Error(), "proto directory not found") {
			t.Errorf("expected proto directory error first, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// EnsureFlatProtoDir
// ---------------------------------------------------------------------------

func TestEnsureFlatProtoDir(t *testing.T) {
	t.Run("already_set_is_noop", func(t *testing.T) {
		existingDir := "/some/already/set/dir"
		cfg := &tactype.Config{FlatProtoDir: existingDir}
		b := NewBuilder(cfg)
		defer b.Close()

		if err := b.EnsureFlatProtoDir(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.FlatProtoDir != existingDir {
			t.Errorf("FlatProtoDir changed: got %q, want %q", cfg.FlatProtoDir, existingDir)
		}
		// flatTempDir should remain empty since nothing was created
		if b.flatTempDir != "" {
			t.Errorf("flatTempDir should be empty for noop, got %q", b.flatTempDir)
		}
	})

	t.Run("creates_flat_dir_from_protos", func(t *testing.T) {
		// Set up an input directory with a minimal proto file
		inputDir := t.TempDir()
		svcDir := filepath.Join(inputDir, "services")
		if err := os.MkdirAll(svcDir, 0755); err != nil {
			t.Fatal(err)
		}
		protoContent := `syntax = "proto3";
package test;
message Ping { string msg = 1; }
`
		if err := os.WriteFile(filepath.Join(svcDir, "test.proto"), []byte(protoContent), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := &tactype.Config{InputDir: inputDir}
		b := NewBuilder(cfg)
		defer b.Close()

		if err := b.EnsureFlatProtoDir(); err != nil {
			t.Fatalf("EnsureFlatProtoDir() failed: %v", err)
		}

		// FlatProtoDir should now be set
		if cfg.FlatProtoDir == "" {
			t.Fatal("FlatProtoDir should be set after EnsureFlatProtoDir")
		}
		// The flat dir should exist on disk
		if _, err := os.Stat(cfg.FlatProtoDir); os.IsNotExist(err) {
			t.Fatalf("FlatProtoDir %q does not exist", cfg.FlatProtoDir)
		}
		// flatTempDir should track the temp directory for cleanup
		if b.flatTempDir == "" {
			t.Error("flatTempDir should be set for cleanup")
		}
	})

	t.Run("second_call_is_noop", func(t *testing.T) {
		// After a successful first call, FlatProtoDir is set, so the second call
		// should return immediately without doing extra work.
		inputDir := t.TempDir()
		svcDir := filepath.Join(inputDir, "services")
		if err := os.MkdirAll(svcDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(svcDir, "dummy.proto"), []byte(`syntax = "proto3";`), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := &tactype.Config{InputDir: inputDir}
		b := NewBuilder(cfg)
		defer b.Close()

		if err := b.EnsureFlatProtoDir(); err != nil {
			t.Fatalf("first call failed: %v", err)
		}
		firstDir := cfg.FlatProtoDir

		if err := b.EnsureFlatProtoDir(); err != nil {
			t.Fatalf("second call failed: %v", err)
		}
		if cfg.FlatProtoDir != firstDir {
			t.Errorf("FlatProtoDir changed on second call: %q -> %q", firstDir, cfg.FlatProtoDir)
		}
	})
}

// ---------------------------------------------------------------------------
// NewBuilder / Close lifecycle
// ---------------------------------------------------------------------------

func TestNewBuilder(t *testing.T) {
	t.Run("returns_non_nil", func(t *testing.T) {
		cfg := &tactype.Config{Language: "go"}
		b := NewBuilder(cfg)
		if b == nil {
			t.Fatal("NewBuilder returned nil")
		}
	})

	t.Run("close_on_fresh_builder", func(t *testing.T) {
		cfg := &tactype.Config{}
		b := NewBuilder(cfg)
		// Close should not panic when no temp dir was created
		b.Close()
	})

	t.Run("close_twice_no_panic", func(t *testing.T) {
		cfg := &tactype.Config{}
		b := NewBuilder(cfg)
		b.Close()
		b.Close() // second close must not panic
	})

	t.Run("close_cleans_temp_dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		innerDir := filepath.Join(tmpDir, "inner")
		if err := os.MkdirAll(innerDir, 0755); err != nil {
			t.Fatal(err)
		}

		b := &Builder{
			config:      &tactype.Config{},
			flatTempDir: innerDir,
		}
		b.Close()

		if _, err := os.Stat(innerDir); !os.IsNotExist(err) {
			t.Errorf("expected flatTempDir to be removed after Close, but it still exists")
		}
		if b.flatTempDir != "" {
			t.Errorf("flatTempDir field should be empty after Close, got %q", b.flatTempDir)
		}
	})
}

// ---------------------------------------------------------------------------
// getLanguagePackage
// ---------------------------------------------------------------------------

func TestGetLanguagePackage(t *testing.T) {
	cfg := &tactype.Config{
		ModuleBasePath:    "github.com/myorg/myproject",
		PythonPackageBase: "myorg.myproject",
		JavaPackageBase:   "com.myorg.myproject",
		TsPackageBase:     "@myorg/myproject",
		CrateBasePath:     "myorg_myproject",
	}
	b := NewBuilder(cfg)

	tests := []struct {
		lang    string
		want    string
		wantErr bool
	}{
		{lang: "go", want: "github.com/myorg/myproject"},
		{lang: "python", want: "myorg.myproject"},
		{lang: "java", want: "com.myorg.myproject"},
		{lang: "typescript", want: "@myorg/myproject"},
		{lang: "rust", want: "myorg_myproject"},
		{lang: "unknown_lang", want: "", wantErr: true},
		{lang: "", want: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got, err := b.getLanguagePackage(tt.lang)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for language %q, got nil", tt.lang)
				}
				if !strings.Contains(err.Error(), "unknown language") {
					t.Errorf("error should mention unknown language: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("getLanguagePackage(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

func TestValidateReservedNamespaces(t *testing.T) {
	// A clean proto tree passes.
	clean := t.TempDir()
	mustWriteProto(t, clean, "svc/ok.proto")
	mustWriteProto(t, clean, "common/types.proto")
	if err := validateReservedNamespaces(clean); err != nil {
		t.Fatalf("clean proto tree rejected: %v", err)
	}

	// Protos authored under the reserved sys/ or chassis/ namespaces are rejected.
	for _, reserved := range []string{"sys/custom.proto", "chassis/thing.proto"} {
		dir := t.TempDir()
		mustWriteProto(t, dir, "svc/ok.proto")
		mustWriteProto(t, dir, reserved)
		err := validateReservedNamespaces(dir)
		if err == nil {
			t.Fatalf("%s under reserved namespace was not rejected", reserved)
		}
		if !strings.Contains(err.Error(), reserved) {
			t.Fatalf("rejection for %s missing the offender: %v", reserved, err)
		}
	}
}

func mustWriteProto(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
