// Copyright (c) 2026 Second Star Cavalier LLC

package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// NOTE: LanguageSection is used by PackageRegistry.Lang for backward-compat
// YAML loading (dependencies.yaml nests languages under "lang:").

// LanguageConfig represents the configuration for a specific language
type LanguageConfig struct {
	Package      string            `yaml:"package,omitempty"`
	Version      string            `yaml:"version,omitempty"`
	Dependencies map[string]string `yaml:"dependencies,omitempty"`
}

// LanguageSection wraps all language configs under a "lang" key
type LanguageSection struct {
	Go         LanguageConfig `yaml:"go"`
	Rust       LanguageConfig `yaml:"rust"`
	Python     LanguageConfig `yaml:"python"`
	Java       LanguageConfig `yaml:"java"`
	TypeScript LanguageConfig `yaml:"typescript"`
	Swift      LanguageConfig `yaml:"swift"`
	CSharp     LanguageConfig `yaml:"csharp"`
}

// PackageRegistry holds all external package dependencies for all languages
type PackageRegistry struct {
	// Support both flat structure and nested under "lang:"
	Go         LanguageConfig    `yaml:"go,omitempty"`
	Rust       LanguageConfig    `yaml:"rust,omitempty"`
	Python     LanguageConfig    `yaml:"python,omitempty"`
	Java       LanguageConfig    `yaml:"java,omitempty"`
	TypeScript LanguageConfig    `yaml:"typescript,omitempty"`
	Swift      LanguageConfig    `yaml:"swift,omitempty"`
	CSharp     LanguageConfig    `yaml:"csharp,omitempty"`
	Lang       LanguageSection   `yaml:"lang,omitempty"`
	Tools      map[string]string `yaml:"tools,omitempty"`

	mu       sync.RWMutex
	filePath string
}

var (
	protoDir                  string
	globalPackageRegistry     *PackageRegistry
	globalPackageRegistryOnce sync.Once
	globalPackageRegistryErr  error
)

// SetProtoDir sets the directory containing dependencies.yaml.
// Must be called before GetPackageRegistry().
// If the registry has already been loaded, it is reset so the next
// GetPackageRegistry call reloads from the new directory.
func SetProtoDir(dir string) {
	if protoDir != dir {
		// Reset the singleton so the next call picks up the new dir.
		globalPackageRegistryOnce = sync.Once{}
		globalPackageRegistry = nil
		globalPackageRegistryErr = nil
	}
	protoDir = dir
}

// GetPackageRegistry returns the singleton package registry, loading it if necessary.
// Loads all configuration from dependencies.yaml.
func GetPackageRegistry() (*PackageRegistry, error) {
	globalPackageRegistryOnce.Do(func() {
		dir := protoDir
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				globalPackageRegistryErr = fmt.Errorf("failed to get current working directory: %w", err)
				return
			}
		}

		depsPath := filepath.Join(dir, "dependencies.yaml")

		registry, err := LoadPackageRegistry(depsPath)
		if err != nil {
			globalPackageRegistryErr = fmt.Errorf("failed to load dependencies.yaml: %w", err)
			return
		}

		globalPackageRegistry = registry
	})

	return globalPackageRegistry, globalPackageRegistryErr
}

// LoadPackageRegistry loads the package registry from a YAML file
func LoadPackageRegistry(path string) (*PackageRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	var registry PackageRegistry
	if err := yaml.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	registry.filePath = path

	// If languages are nested under "lang:", move them to top level
	if len(registry.Go.Dependencies) == 0 && len(registry.Lang.Go.Dependencies) > 0 {
		registry.Go = registry.Lang.Go
	}
	if len(registry.Rust.Dependencies) == 0 && len(registry.Lang.Rust.Dependencies) > 0 {
		registry.Rust = registry.Lang.Rust
	}
	if len(registry.Python.Dependencies) == 0 && len(registry.Lang.Python.Dependencies) > 0 {
		registry.Python = registry.Lang.Python
	}
	if len(registry.Java.Dependencies) == 0 && len(registry.Lang.Java.Dependencies) > 0 {
		registry.Java = registry.Lang.Java
	}
	if len(registry.TypeScript.Dependencies) == 0 && len(registry.Lang.TypeScript.Dependencies) > 0 {
		registry.TypeScript = registry.Lang.TypeScript
	}
	if len(registry.Swift.Dependencies) == 0 && len(registry.Lang.Swift.Dependencies) > 0 {
		registry.Swift = registry.Lang.Swift
	}

	// Validate that we have at least some dependencies
	if len(registry.Go.Dependencies) == 0 && len(registry.Rust.Dependencies) == 0 &&
		len(registry.Python.Dependencies) == 0 && len(registry.Java.Dependencies) == 0 &&
		len(registry.TypeScript.Dependencies) == 0 && len(registry.Swift.Dependencies) == 0 {
		return nil, fmt.Errorf("no dependencies found in %s", path)
	}

	return &registry, nil
}

// GetGoVersion returns the version for a Go module path
// Handles subpackages by checking parent modules
func (r *PackageRegistry) GetGoVersion(modulePath string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try exact match first
	if version, ok := r.Go.Dependencies[modulePath]; ok {
		return version, true
	}

	// Handle subpackages by checking parent modules
	// e.g., "google.golang.org/grpc/codes" should match "google.golang.org/grpc"
	checkPath := modulePath
	for {
		lastSlash := strings.LastIndex(checkPath, "/")
		if lastSlash == -1 {
			break
		}
		checkPath = checkPath[:lastSlash]

		if version, ok := r.Go.Dependencies[checkPath]; ok {
			return version, true
		}
	}

	return "", false
}

// GetRustVersion returns the version for a Rust crate
func (r *PackageRegistry) GetRustVersion(crateName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.Rust.Dependencies[crateName]
	return version, ok
}

// GetPythonVersion returns the version for a Python package
func (r *PackageRegistry) GetPythonVersion(packageName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.Python.Dependencies[packageName]
	return version, ok
}

// GetJavaVersion returns the version for a Java artifact (format: "groupId:artifactId")
func (r *PackageRegistry) GetJavaVersion(artifact string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.Java.Dependencies[artifact]
	return version, ok
}

// GetTypeScriptVersion returns the version for a TypeScript/npm package
func (r *PackageRegistry) GetTypeScriptVersion(packageName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.TypeScript.Dependencies[packageName]
	return version, ok
}

// GetSwiftVersion returns the version for a Swift package.
func (r *PackageRegistry) GetSwiftVersion(packageName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.Swift.Dependencies[packageName]
	return version, ok
}

// GetToolVersion returns the version for a build tool or compiler
func (r *PackageRegistry) GetToolVersion(toolName string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	version, ok := r.Tools[toolName]
	return version, ok
}

// GetAllRustDependencies returns all Rust dependencies
func (r *PackageRegistry) GetAllRustDependencies() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]string, len(r.Rust.Dependencies))
	for k, v := range r.Rust.Dependencies {
		result[k] = v
	}
	return result
}
