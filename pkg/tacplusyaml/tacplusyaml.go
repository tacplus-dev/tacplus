// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
)

// Filename is the config file name written to the project root.
const Filename = "tacplus.yaml"

// ClientSurfaceConfig controls whether tacplus emits one client transport surface.
type ClientSurfaceConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsZero lets yaml omit an unset client surface while still preserving
// explicit enabled: false when present.
func (c ClientSurfaceConfig) IsZero() bool {
	return c.Enabled == nil
}

// ClientsConfig controls client transport surfaces emitted for this package.
type ClientsConfig struct {
	C2S ClientSurfaceConfig `yaml:"c2s,omitempty"`
	W2S ClientSurfaceConfig `yaml:"w2s,omitempty"`
	C2D ClientSurfaceConfig `yaml:"c2d,omitempty"`
}

// IsZero lets yaml omit an unset clients section.
func (c ClientsConfig) IsZero() bool {
	return c.C2S.IsZero() && c.W2S.IsZero() && c.C2D.IsZero()
}

// Config represents the tacplus.yaml schema.
type Config struct {
	Import     string        `yaml:"import,omitempty"`      // path to base config to import (resolved relative to this file's directory)
	Language   string        `yaml:"language"`              // go, java, typescript, python, rust, swift
	Src        string        `yaml:"src"`                   // primary proto source (git URL or local path)
	Sources    []string      `yaml:"sources,omitempty"`     // additional proto sources merged with src into one generation (e.g. a shared base + a reusable service's protos)
	Pkg        string        `yaml:"pkg"`                   // canonical package name (Java-style reverse DNS, e.g. "com.example.services.identity")
	Clients    ClientsConfig `yaml:"clients,omitempty"`     // generated C2S/W2S/C2D client transport surfaces
	OutputDir  string        `yaml:"output-dir"`            // output subdirectory relative to project dir (required)
	OutputName string        `yaml:"output-name,omitempty"` // generated package/dir segment name; default "model" (compiler stays "tacplus")
	Tacplus    string        `yaml:"tacplus,omitempty"`     // minimum tacplus MAJOR.MINOR this project needs (a floor, see CheckFloor); unset = no floor
	Depends    []string      `yaml:"depends,omitempty"`     // services this one calls, by registry name ("llmproxy" or "llmproxy@1.2"); their protos are fetched through the registry and merged into this generation (see CheckDepends)
	Header     string        `yaml:"header,omitempty"`      // free-form text stamped as a comment at the top of every generated file (e.g. a copyright line and an SPDX identifier); its first line is also the author of generated package manifests. Unset: generated code carries no copyright line
}

// Exists returns true if tacplus.yaml exists in dir.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, Filename))
	return err == nil
}

// Read reads and parses tacplus.yaml from dir.
func Read(dir string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", Filename, err)
	}
	if err := rejectRemovedFields(data, Filename); err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", Filename, err)
	}

	if cfg.Language == "" {
		return nil, fmt.Errorf("%s: language is required", Filename)
	}
	if cfg.OutputDir == "" {
		return nil, fmt.Errorf("%s: output-dir is required", Filename)
	}
	if err := CheckFloor(cfg.Tacplus, buildinfo.Version); err != nil {
		return nil, err
	}
	if err := CheckDepends(cfg.Depends); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Write writes cfg to tacplus.yaml in dir.
func Write(dir string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal %s: %w", Filename, err)
	}

	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", Filename, err)
	}

	return nil
}

// ReadFile reads a specific yaml config file, resolving any import directives recursively.
// The returned Config has imports fully merged (base first, then overlay).
func ReadFile(path string) (*Config, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %s: %w", path, err)
	}
	return readFileChain(absPath, make(map[string]bool))
}

// readFileChain reads a config file and recursively resolves imports.
// The seen map tracks visited files to detect circular imports.
func readFileChain(absPath string, seen map[string]bool) (*Config, error) {
	if seen[absPath] {
		return nil, fmt.Errorf("circular import detected: %s", absPath)
	}
	seen[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", absPath, err)
	}
	if err := rejectRemovedFields(data, absPath); err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", absPath, err)
	}

	// If this file imports a base, load it first
	if cfg.Import != "" {
		importPath := cfg.Import
		if !filepath.IsAbs(importPath) {
			importPath = filepath.Join(filepath.Dir(absPath), importPath)
		}
		importPath, err = filepath.Abs(importPath)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve import path %s: %w", cfg.Import, err)
		}

		base, err := readFileChain(importPath, seen)
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", cfg.Import, err)
		}

		// Clear the import field before merging — it's been resolved
		cfg.Import = ""
		merged := MergeConfigs(base, &cfg)
		return merged, nil
	}

	return &cfg, nil
}

// ReadFiles reads multiple config files and merges them left-to-right.
// Later files override earlier ones. Each file may contain import directives.
func ReadFiles(paths []string) (*Config, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no config files specified")
	}

	result, err := ReadFile(paths[0])
	if err != nil {
		return nil, err
	}

	for _, path := range paths[1:] {
		overlay, err := ReadFile(path)
		if err != nil {
			return nil, err
		}
		result = MergeConfigs(result, overlay)
	}

	// Apply defaults and validate
	if result.Language == "" {
		return nil, fmt.Errorf("language is required (not set in any config file)")
	}
	if result.OutputDir == "" {
		return nil, fmt.Errorf("output-dir is required (not set in any config file)")
	}
	if err := CheckFloor(result.Tacplus, buildinfo.Version); err != nil {
		return nil, err
	}
	if err := CheckDepends(result.Depends); err != nil {
		return nil, err
	}
	return result, nil
}

// MergeConfigs merges overlay on top of base using the following semantics:
//   - Strings: overlay wins if non-empty
func MergeConfigs(base, overlay *Config) *Config {
	result := *base // shallow copy

	// Strings: overlay wins if non-empty
	if overlay.Language != "" {
		result.Language = overlay.Language
	}
	if overlay.Src != "" {
		result.Src = overlay.Src
	}
	// Sources: overlay wins if it declares any (layered -c override semantics).
	if len(overlay.Sources) > 0 {
		result.Sources = overlay.Sources
	}
	if overlay.Pkg != "" {
		result.Pkg = overlay.Pkg
	}

	result.Clients = MergeClientsConfig(result.Clients, overlay.Clients)

	// OutputDir: overlay wins if non-empty
	if overlay.OutputDir != "" {
		result.OutputDir = overlay.OutputDir
	}

	// OutputName: overlay wins if non-empty
	if overlay.OutputName != "" {
		result.OutputName = overlay.OutputName
	}

	// Tacplus floor: overlay wins if non-empty
	if overlay.Tacplus != "" {
		result.Tacplus = overlay.Tacplus
	}

	// Depends: overlay wins if it declares any (same semantics as Sources)
	if len(overlay.Depends) > 0 {
		result.Depends = overlay.Depends
	}

	// Header: overlay wins if non-empty
	if overlay.Header != "" {
		result.Header = overlay.Header
	}

	return &result
}

// MergeClientsConfig merges overlay client-surface settings onto base.
func MergeClientsConfig(base, overlay ClientsConfig) ClientsConfig {
	return ClientsConfig{
		C2S: MergeClientSurfaceConfig(base.C2S, overlay.C2S),
		W2S: MergeClientSurfaceConfig(base.W2S, overlay.W2S),
		C2D: MergeClientSurfaceConfig(base.C2D, overlay.C2D),
	}
}

// MergeClientSurfaceConfig merges overlay client-surface settings onto base.
func MergeClientSurfaceConfig(base, overlay ClientSurfaceConfig) ClientSurfaceConfig {
	result := base
	if overlay.Enabled != nil {
		result.Enabled = overlay.Enabled
	}
	return result
}

func rejectRemovedFields(data []byte, source string) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse %s: %w", source, err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}

	removed := map[string]string{
		"grpc-servers":         "server wrappers are inferred from service annotations",
		"grpc-s2s-clients":     "use (spec.grpc_s2s_client) file options",
		"grpc-c2s-clients":     "use (spec.grpc_c2s_client) file options",
		"grpc-w2s-clients":     "use (spec.grpc_w2s_client) file options",
		"rest-clients":         "use service annotations plus rest_method/rest_path annotations",
		"all-grpc-servers":     "generation is annotation-driven",
		"all-grpc-s2s-clients": "generation is annotation-driven",
		"all-grpc-c2s-clients": "generation is annotation-driven",
		"all-grpc-w2s-clients": "generation is annotation-driven",
		"all-grpc":             "generation is annotation-driven",
		"all-rest-clients":     "generation is annotation-driven",
		"states":               "use field-level state_import and service_state export annotations",
		"all-states":           "state access is derived from proto annotations",
		"container":            "container image builds are no longer performed by tacplus",
		"mounts":               "tacplus no longer re-runs itself in a container",
		"workspace":            "tacplus no longer re-runs itself in a container",
		"post-gen":             "downstream build commands must run in the invoking workflow",
	}
	for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
		key := root.Content[0].Content[i].Value
		if msg, ok := removed[key]; ok {
			return fmt.Errorf("%s: %q has been removed; %s", source, key, msg)
		}
	}
	return nil
}
