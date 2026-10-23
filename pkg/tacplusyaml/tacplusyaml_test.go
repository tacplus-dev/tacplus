// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestExists(t *testing.T) {
	tests := []struct {
		name       string
		createFile bool
		want       bool
	}{
		{name: "file exists", createFile: true, want: true},
		{name: "file does not exist", createFile: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.createFile {
				path := filepath.Join(dir, Filename)
				if err := os.WriteFile(path, []byte("language: go\nsrc: https://example.com\noutput-dir: tacplus\n"), 0644); err != nil {
					t.Fatalf("failed to create test file: %v", err)
				}
			}
			got := Exists(dir)
			if got != tt.want {
				t.Errorf("Exists(%q) = %v, want %v", dir, got, tt.want)
			}
		})
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantCfg   *Config
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "valid full project-local config",
			content: "language: go\nsrc: https://example.com/proto.git\npkg: myproject\noutput-dir: tacplus\nclients:\n  c2s:\n    enabled: true\n  w2s:\n    enabled: false\n",
			wantCfg: &Config{
				Language:  "go",
				Src:       "https://example.com/proto.git",
				Pkg:       "myproject",
				OutputDir: "tacplus",
				Clients: ClientsConfig{
					C2S: ClientSurfaceConfig{Enabled: boolPtr(true)},
					W2S: ClientSurfaceConfig{Enabled: boolPtr(false)},
				},
			},
		},
		{
			name:    "minimal valid config",
			content: "language: java\nsrc: /local/path\noutput-dir: tacplus\n",
			wantCfg: &Config{
				Language:  "java",
				Src:       "/local/path",
				OutputDir: "tacplus",
			},
		},
		{
			name:      "removed grpc selector field errors",
			content:   "language: go\noutput-dir: tacplus\ngrpc-s2s-clients:\n  - services/football.FootballService\n",
			wantErr:   true,
			errSubstr: "grpc-s2s-clients",
		},
		{
			name:      "removed container build field errors",
			content:   "language: go\noutput-dir: tacplus\ncontainer:\n  image: registry.example.com/svc\n",
			wantErr:   true,
			errSubstr: "container",
		},
		{
			name:      "removed mounts field errors",
			content:   "language: go\noutput-dir: tacplus\nmounts:\n  - /tmp/data:/data\n",
			wantErr:   true,
			errSubstr: "mounts",
		},
		{
			name:      "removed workspace field errors",
			content:   "language: go\noutput-dir: tacplus\nworkspace: ..\n",
			wantErr:   true,
			errSubstr: "workspace",
		},
		{
			name:      "removed post-gen field errors",
			content:   "language: go\noutput-dir: tacplus\npost-gen:\n  - cmd: make\n",
			wantErr:   true,
			errSubstr: "post-gen",
		},
		{
			name:    "old yaml with tag field still parses (ignored)",
			content: "language: go\nsrc: https://example.com\noutput-dir: tacplus\ntag: 1.0.0\n",
			wantCfg: &Config{
				Language:  "go",
				Src:       "https://example.com",
				OutputDir: "tacplus",
			},
		},
		{
			name:      "missing language",
			content:   "src: https://example.com\n",
			wantErr:   true,
			errSubstr: "language is required",
		},
		{
			name:      "missing output-dir",
			content:   "language: go\n",
			wantErr:   true,
			errSubstr: "output-dir is required",
		},
		{
			name:    "missing src stays empty (sys-only mode)",
			content: "language: go\noutput-dir: tacplus\n",
			wantCfg: &Config{
				Language:  "go",
				Src:       "",
				OutputDir: "tacplus",
			},
		},
		{
			name:      "invalid yaml",
			content:   ":\n  :\n    - [invalid",
			wantErr:   true,
			errSubstr: "failed to parse",
		},
		{
			name:      "file does not exist",
			content:   "", // special case: we won't create the file
			wantErr:   true,
			errSubstr: "failed to read",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Write the config file unless we're testing missing file
			if tt.name != "file does not exist" {
				path := filepath.Join(dir, Filename)
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
			}

			got, err := Read(dir)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Read(%q) expected error containing %q, got nil", dir, tt.errSubstr)
					return
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("Read(%q) error = %q, want substring %q", dir, err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("Read(%q) unexpected error: %v", dir, err)
				return
			}

			// Verify each field
			if got.Language != tt.wantCfg.Language {
				t.Errorf("Language = %q, want %q", got.Language, tt.wantCfg.Language)
			}
			if got.Src != tt.wantCfg.Src {
				t.Errorf("Src = %q, want %q", got.Src, tt.wantCfg.Src)
			}
			if got.Pkg != tt.wantCfg.Pkg {
				t.Errorf("Pkg = %q, want %q", got.Pkg, tt.wantCfg.Pkg)
			}
			if got.OutputDir != tt.wantCfg.OutputDir {
				t.Errorf("OutputDir = %q, want %q", got.OutputDir, tt.wantCfg.OutputDir)
			}
			if got.Clients.C2S.Enabled == nil != (tt.wantCfg.Clients.C2S.Enabled == nil) {
				t.Errorf("Clients.C2S.Enabled nil = %v, want %v", got.Clients.C2S.Enabled == nil, tt.wantCfg.Clients.C2S.Enabled == nil)
			} else if got.Clients.C2S.Enabled != nil && *got.Clients.C2S.Enabled != *tt.wantCfg.Clients.C2S.Enabled {
				t.Errorf("Clients.C2S.Enabled = %v, want %v", *got.Clients.C2S.Enabled, *tt.wantCfg.Clients.C2S.Enabled)
			}
			if got.Clients.W2S.Enabled == nil != (tt.wantCfg.Clients.W2S.Enabled == nil) {
				t.Errorf("Clients.W2S.Enabled nil = %v, want %v", got.Clients.W2S.Enabled == nil, tt.wantCfg.Clients.W2S.Enabled == nil)
			} else if got.Clients.W2S.Enabled != nil && *got.Clients.W2S.Enabled != *tt.wantCfg.Clients.W2S.Enabled {
				t.Errorf("Clients.W2S.Enabled = %v, want %v", *got.Clients.W2S.Enabled, *tt.wantCfg.Clients.W2S.Enabled)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{
			name: "full config",
			cfg: &Config{
				Language:  "go",
				Src:       "https://example.com/proto.git",
				Pkg:       "myproject",
				OutputDir: "tacplus",
			},
		},
		{
			name: "minimal config",
			cfg: &Config{
				Language: "typescript",
				Src:      "/local/path",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Write the config
			err := Write(dir, tt.cfg)
			if err != nil {
				t.Fatalf("Write(%q) unexpected error: %v", dir, err)
			}

			// Verify the file was created
			path := filepath.Join(dir, Filename)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read written file: %v", err)
			}

			// Parse the written YAML and verify fields match
			var got Config
			if err := yaml.Unmarshal(data, &got); err != nil {
				t.Fatalf("failed to parse written YAML: %v", err)
			}

			if got.Language != tt.cfg.Language {
				t.Errorf("Language = %q, want %q", got.Language, tt.cfg.Language)
			}
			if got.Src != tt.cfg.Src {
				t.Errorf("Src = %q, want %q", got.Src, tt.cfg.Src)
			}
			if got.Pkg != tt.cfg.Pkg {
				t.Errorf("Pkg = %q, want %q", got.Pkg, tt.cfg.Pkg)
			}
			if got.OutputDir != tt.cfg.OutputDir {
				t.Errorf("OutputDir = %q, want %q", got.OutputDir, tt.cfg.OutputDir)
			}
		})
	}
}

func TestReadFile(t *testing.T) {
	t.Run("simple file without import", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "tacplus.yaml")
		os.WriteFile(path, []byte("language: go\nsrc: https://example.com\npkg: myproject\noutput-dir: tacplus\n"), 0644)

		cfg, err := ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if cfg.Language != "go" {
			t.Errorf("Language = %q, want %q", cfg.Language, "go")
		}
		if cfg.Pkg != "myproject" {
			t.Errorf("Pkg = %q, want %q", cfg.Pkg, "myproject")
		}
	})

	t.Run("file with import", func(t *testing.T) {
		dir := t.TempDir()

		// Create base config
		basePath := filepath.Join(dir, "tacplus-base.yaml")
		os.WriteFile(basePath, []byte("language: go\nsrc: https://example.com\npkg: base-pkg\noutput-dir: tacplus\n"), 0644)

		// Create overlay that imports base
		overlayPath := filepath.Join(dir, "tacplus-ci.yaml")
		os.WriteFile(overlayPath, []byte("import: tacplus-base.yaml\npkg: ci-pkg\n"), 0644)

		cfg, err := ReadFile(overlayPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if cfg.Language != "go" {
			t.Errorf("Language = %q, want %q (should come from base)", cfg.Language, "go")
		}
		if cfg.Pkg != "ci-pkg" {
			t.Errorf("Pkg = %q, want %q (should be overridden by overlay)", cfg.Pkg, "ci-pkg")
		}
	})

	t.Run("chained imports", func(t *testing.T) {
		dir := t.TempDir()

		// grandparent
		os.WriteFile(filepath.Join(dir, "base.yaml"), []byte("language: java\nsrc: https://base.com\npkg: base\noutput-dir: tacplus\n"), 0644)
		// parent imports grandparent
		os.WriteFile(filepath.Join(dir, "mid.yaml"), []byte("import: base.yaml\npkg: mid\n"), 0644)
		// child imports parent
		os.WriteFile(filepath.Join(dir, "child.yaml"), []byte("import: mid.yaml\npkg: child\n"), 0644)

		cfg, err := ReadFile(filepath.Join(dir, "child.yaml"))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if cfg.Language != "java" {
			t.Errorf("Language = %q, want %q (from grandparent)", cfg.Language, "java")
		}
		if cfg.Pkg != "child" {
			t.Errorf("Pkg = %q, want %q (from child)", cfg.Pkg, "child")
		}
	})

	t.Run("removed selector in imported file errors", func(t *testing.T) {
		dir := t.TempDir()

		basePath := filepath.Join(dir, "base.yaml")
		os.WriteFile(basePath, []byte("language: go\noutput-dir: tacplus\ngrpc-servers:\n  - services/foo.FooService\n"), 0644)
		childPath := filepath.Join(dir, "child.yaml")
		os.WriteFile(childPath, []byte("import: base.yaml\npkg: child\n"), 0644)

		_, err := ReadFile(childPath)
		if err == nil {
			t.Fatal("expected removed selector error, got nil")
		}
		if !strings.Contains(err.Error(), "grpc-servers") {
			t.Errorf("error = %q, want grpc-servers", err.Error())
		}
	})

	t.Run("circular import detection", func(t *testing.T) {
		dir := t.TempDir()

		os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("import: b.yaml\nlanguage: go\n"), 0644)
		os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("import: a.yaml\nlanguage: go\n"), 0644)

		_, err := ReadFile(filepath.Join(dir, "a.yaml"))
		if err == nil {
			t.Fatal("expected circular import error, got nil")
		}
		if !strings.Contains(err.Error(), "circular import") {
			t.Errorf("error = %q, want it to contain 'circular import'", err.Error())
		}
	})

	t.Run("self-import detection", func(t *testing.T) {
		dir := t.TempDir()

		os.WriteFile(filepath.Join(dir, "self.yaml"), []byte("import: self.yaml\nlanguage: go\n"), 0644)

		_, err := ReadFile(filepath.Join(dir, "self.yaml"))
		if err == nil {
			t.Fatal("expected circular import error, got nil")
		}
		if !strings.Contains(err.Error(), "circular import") {
			t.Errorf("error = %q, want it to contain 'circular import'", err.Error())
		}
	})

	t.Run("missing import file", func(t *testing.T) {
		dir := t.TempDir()

		os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("import: nonexistent.yaml\nlanguage: go\n"), 0644)

		_, err := ReadFile(filepath.Join(dir, "a.yaml"))
		if err == nil {
			t.Fatal("expected error for missing import, got nil")
		}
	})
}

func TestReadFiles(t *testing.T) {
	t.Run("single file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "tacplus.yaml")
		os.WriteFile(path, []byte("language: go\nsrc: https://example.com\noutput-dir: tacplus\n"), 0644)

		cfg, err := ReadFiles([]string{path})
		if err != nil {
			t.Fatalf("ReadFiles: %v", err)
		}
		if cfg.Language != "go" {
			t.Errorf("Language = %q, want %q", cfg.Language, "go")
		}
	})

	t.Run("multiple files merged left-to-right", func(t *testing.T) {
		dir := t.TempDir()

		basePath := filepath.Join(dir, "base.yaml")
		os.WriteFile(basePath, []byte("language: go\nsrc: https://base.com\npkg: base-pkg\noutput-dir: tacplus\n"), 0644)

		ciPath := filepath.Join(dir, "ci.yaml")
		os.WriteFile(ciPath, []byte("src: https://ci.com\n"), 0644)

		cfg, err := ReadFiles([]string{basePath, ciPath})
		if err != nil {
			t.Fatalf("ReadFiles: %v", err)
		}
		if cfg.Language != "go" {
			t.Errorf("Language = %q, want %q (from base)", cfg.Language, "go")
		}
		if cfg.Src != "https://ci.com" {
			t.Errorf("Src = %q, want %q (from ci)", cfg.Src, "https://ci.com")
		}
		if cfg.Pkg != "base-pkg" {
			t.Errorf("Pkg = %q, want %q (from base, ci didn't set it)", cfg.Pkg, "base-pkg")
		}
	})

	t.Run("removed selector in layered config errors", func(t *testing.T) {
		dir := t.TempDir()

		basePath := filepath.Join(dir, "base.yaml")
		os.WriteFile(basePath, []byte("language: go\noutput-dir: tacplus\n"), 0644)
		ciPath := filepath.Join(dir, "ci.yaml")
		os.WriteFile(ciPath, []byte("states:\n  - services/foo.FooState:rw\n"), 0644)

		_, err := ReadFiles([]string{basePath, ciPath})
		if err == nil {
			t.Fatal("expected removed selector error, got nil")
		}
		if !strings.Contains(err.Error(), "states") {
			t.Errorf("error = %q, want states", err.Error())
		}
	})

	t.Run("no files", func(t *testing.T) {
		_, err := ReadFiles(nil)
		if err == nil {
			t.Fatal("expected error for no files, got nil")
		}
	})

	t.Run("missing language across all files", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "a.yaml")
		os.WriteFile(path, []byte("src: https://example.com\n"), 0644)

		_, err := ReadFiles([]string{path})
		if err == nil {
			t.Fatal("expected error for missing language, got nil")
		}
		if !strings.Contains(err.Error(), "language is required") {
			t.Errorf("error = %q, want 'language is required'", err.Error())
		}
	})
}

func TestMergeConfigs(t *testing.T) {
	t.Run("header: overlay wins when non-empty, base kept otherwise", func(t *testing.T) {
		base := &Config{Language: "go", Header: "Copyright (c) 2026 Base Inc\nSPDX-License-Identifier: Apache-2.0"}
		if got := MergeConfigs(base, &Config{}).Header; got != base.Header {
			t.Errorf("Header = %q, want base header", got)
		}
		if got := MergeConfigs(base, &Config{Header: "Copyright (c) 2026 Overlay Inc"}).Header; got != "Copyright (c) 2026 Overlay Inc" {
			t.Errorf("Header = %q, want overlay header", got)
		}
	})

	t.Run("strings: overlay wins when non-empty", func(t *testing.T) {
		base := &Config{Language: "go", Src: "base-src", Pkg: "base-pkg"}
		overlay := &Config{Src: "overlay-src"}
		result := MergeConfigs(base, overlay)

		if result.Language != "go" {
			t.Errorf("Language = %q, want %q", result.Language, "go")
		}
		if result.Src != "overlay-src" {
			t.Errorf("Src = %q, want %q", result.Src, "overlay-src")
		}
		if result.Pkg != "base-pkg" {
			t.Errorf("Pkg = %q, want %q", result.Pkg, "base-pkg")
		}
	})

	t.Run("clients: overlay can override true with false", func(t *testing.T) {
		base := &Config{
			Language: "go",
			Clients: ClientsConfig{
				C2S: ClientSurfaceConfig{Enabled: boolPtr(true)},
				W2S: ClientSurfaceConfig{Enabled: boolPtr(false)},
			},
		}
		overlay := &Config{
			Clients: ClientsConfig{
				C2S: ClientSurfaceConfig{Enabled: boolPtr(false)},
				W2S: ClientSurfaceConfig{Enabled: boolPtr(true)},
			},
		}
		result := MergeConfigs(base, overlay)

		if result.Clients.C2S.Enabled == nil || *result.Clients.C2S.Enabled {
			t.Errorf("Clients.C2S.Enabled = %v, want false", result.Clients.C2S.Enabled)
		}
		if result.Clients.W2S.Enabled == nil || !*result.Clients.W2S.Enabled {
			t.Errorf("Clients.W2S.Enabled = %v, want true", result.Clients.W2S.Enabled)
		}
	})

	t.Run("clients: empty overlay preserves base", func(t *testing.T) {
		base := &Config{
			Language: "go",
			Clients: ClientsConfig{
				C2S: ClientSurfaceConfig{Enabled: boolPtr(true)},
			},
		}
		result := MergeConfigs(base, &Config{})

		if result.Clients.C2S.Enabled == nil || !*result.Clients.C2S.Enabled {
			t.Errorf("Clients.C2S.Enabled = %v, want true", result.Clients.C2S.Enabled)
		}
		if result.Clients.W2S.Enabled != nil {
			t.Errorf("Clients.W2S.Enabled = %v, want nil", result.Clients.W2S.Enabled)
		}
	})

	t.Run("empty overlay changes nothing", func(t *testing.T) {
		base := &Config{Language: "go", Src: "base", Pkg: "pkg"}
		overlay := &Config{}
		result := MergeConfigs(base, overlay)

		if result.Language != "go" || result.Src != "base" || result.Pkg != "pkg" {
			t.Error("strings should be unchanged")
		}
	})
}

func TestWriteThenRead(t *testing.T) {
	// Round-trip test: Write then Read should return the same config.
	dir := t.TempDir()

	original := &Config{
		Language:  "rust",
		Src:       "https://gitlab.com/example/proto.git",
		Pkg:       "my-crate",
		OutputDir: "target/generated-sources/tacplus",
	}

	if err := Write(dir, original); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if got.Language != original.Language {
		t.Errorf("Language = %q, want %q", got.Language, original.Language)
	}
	if got.Src != original.Src {
		t.Errorf("Src = %q, want %q", got.Src, original.Src)
	}
	if got.Pkg != original.Pkg {
		t.Errorf("Pkg = %q, want %q", got.Pkg, original.Pkg)
	}
	if got.OutputDir != original.OutputDir {
		t.Errorf("OutputDir = %q, want %q", got.OutputDir, original.OutputDir)
	}
}

func TestSourcesAcceptStringOrLayeredMapping(t *testing.T) {
	dir := t.TempDir()
	yml := `language: go
output-dir: model
src: ../app/proto
layer: app
sources:
  - ../base/proto
  - src: ../platform/proto
    layer: platform
`
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cfg.Layer != "app" {
		t.Errorf("Layer = %q, want app", cfg.Layer)
	}
	want := []Source{{Src: "../base/proto"}, {Src: "../platform/proto", Layer: "platform"}}
	if !reflect.DeepEqual(cfg.Sources, want) {
		t.Errorf("Sources = %#v, want %#v", cfg.Sources, want)
	}

	// Round-trip: a layerless source is written back as a plain string.
	if err := Write(dir, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := Read(dir)
	if err != nil {
		t.Fatalf("re-Read: %v", err)
	}
	if !reflect.DeepEqual(again, cfg) {
		t.Errorf("round trip changed config: %#v vs %#v", again, cfg)
	}
}

func TestMergeConfigsLayer(t *testing.T) {
	got := MergeConfigs(&Config{Src: "a", Layer: "base"}, &Config{Layer: "app"})
	if got.Layer != "app" {
		t.Errorf("overlay layer lost: %q", got.Layer)
	}
	got = MergeConfigs(&Config{Src: "a", Layer: "base"}, &Config{})
	if got.Layer != "base" {
		t.Errorf("base layer lost: %q", got.Layer)
	}
}
