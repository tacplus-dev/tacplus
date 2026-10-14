// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// createRootPyprojectToml creates a root pyproject.toml file for all generated Python packages
func createRootPyprojectToml(config *tactype.Config, outputDir string) error {
	pyprojectPath := filepath.Join(outputDir, "pyproject.toml")

	packageName := config.PythonPackageBase
	zap.L().Info("creating root pyproject.toml", zap.String("package", packageName))

	// Get version from config (no fallbacks - must be set)
	version := config.PEP440Version()
	if version == "" {
		return fmt.Errorf("VersionTag is required for Python generation; set --tag")
	}

	// Load package registry for dependency versions
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return fmt.Errorf("failed to load package registry: %w", err)
	}

	// Helper to get version from registry (no fallbacks - must be in dependencies.yaml)
	getPythonVersion := func(pkgName string) (string, error) {
		if ver, ok := registry.GetPythonVersion(pkgName); ok {
			return ver, nil
		}
		return "", fmt.Errorf("%s version not found in dependencies.yaml", pkgName)
	}

	protobufVersion, err := getPythonVersion("protobuf")
	if err != nil {
		return err
	}
	pyYAMLVersion, err := getPythonVersion("PyYAML")
	if err != nil {
		return err
	}
	tomliVersion, err := getPythonVersion("tomli")
	if err != nil {
		return err
	}
	tomliWVersion, err := getPythonVersion("tomli-w")
	if err != nil {
		return err
	}
	grpcioVersion, err := getPythonVersion("grpcio")
	if err != nil {
		return err
	}
	extraProjectDeps, err := pythonRootProjectDependencies(config, getPythonVersion)
	if err != nil {
		return err
	}

	// If pyproject.toml already exists, update dependencies in-place
	if _, err := os.Stat(pyprojectPath); err == nil {
		return updatePyprojectToml(pyprojectPath, version, protobufVersion, pyYAMLVersion, tomliVersion, tomliWVersion, grpcioVersion, extraProjectDeps)
	}

	// Ensure __init__.py exists in all directories that are Python packages.
	// Walk the entire output directory and add __init__.py to any directory
	// that contains .py files or subdirectories (making them proper Python packages).
	err = filepath.Walk(outputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), ".") || filepath.Base(path) == "__pycache__" {
			return nil
		}
		// Skip the proto directory (reference protos, not Python packages)
		relPath, _ := filepath.Rel(outputDir, path)
		if relPath == "." {
			return nil
		}
		if relPath == "proto" || strings.HasPrefix(relPath, "proto/") {
			return nil
		}
		// Create __init__.py if it doesn't exist and the dir has package content
		if hasPackageContent(path) {
			initPath := filepath.Join(path, "__init__.py")
			if _, err := os.Stat(initPath); os.IsNotExist(err) {
				if err := os.WriteFile(initPath, []byte("# Auto-generated package\n"), 0644); err != nil {
					return fmt.Errorf("failed to create __init__.py in %s: %w", path, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to create __init__.py files: %w", err)
	}

	// Build pip extra-index-url config if registry is configured
	pipConfig := ""
	if pypiURL := config.Registry.GitLabPyPIURL(); pypiURL != "" {
		pipConfig = fmt.Sprintf(`
[tool.pip]
extra-index-url = ["%s"]
`, pypiURL)
	}

	deps := []string{
		fmt.Sprintf(`    "protobuf%s"`, protobufVersion),
		fmt.Sprintf(`    "grpcio%s"`, grpcioVersion),
	}
	for _, dep := range extraProjectDeps {
		deps = append(deps, fmt.Sprintf(`    "%s%s"`, dep.Name, dep.Version))
	}

	pyprojectContent := fmt.Sprintf(`[build-system]
requires = ["setuptools>=61.0", "wheel"]
build-backend = "setuptools.build_meta"

[project]
name = "%s"
version = "%s"
description = "Generated Protocol Buffer types"
requires-python = ">=3.8"
dependencies = [
%s
]

[project.optional-dependencies]
yaml = ["PyYAML%s"]
toml = ["tomli%s; python_version<'3.11'", "tomli-w%s"]
all = ["PyYAML%s", "tomli%s; python_version<'3.11'", "tomli-w%s"]

[tool.setuptools.packages.find]
where = ["."]
%s`, packageName, version, strings.Join(deps, ",\n"), pyYAMLVersion, tomliVersion, tomliWVersion, pyYAMLVersion, tomliVersion, tomliWVersion, pipConfig)

	if err := os.WriteFile(pyprojectPath, []byte(pyprojectContent), 0644); err != nil {
		return fmt.Errorf("failed to create pyproject.toml: %w", err)
	}

	return nil
}

// updatePyprojectToml updates dependency versions and project version in an
// existing pyproject.toml using regex replacement, preserving all other content
// (user-added dependencies, tool sections, comments, formatting).
func updatePyprojectToml(pyprojectPath, version, protobufVersion, pyYAMLVersion, tomliVersion, tomliWVersion, grpcioVersion string, extraProjectDeps []managedPythonDependency) error {
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", pyprojectPath, err)
	}
	content := string(data)

	// Update project version
	versionRe := regexp.MustCompile(`(?m)^version\s*=\s*"[^"]*"`)
	content = versionRe.ReplaceAllString(content, fmt.Sprintf(`version = "%s"`, version))

	// Update managed dependency versions.
	// Pattern matches "depname>=X.Y.Z" (or ~= or ==) with optional trailing markers.
	updateDep := func(depName, newVersion string) {
		// Escape dots in dependency name for regex
		escaped := regexp.QuoteMeta(depName)
		re := regexp.MustCompile(`"` + escaped + `(>=|~=|==)[^"]*"`)
		content = re.ReplaceAllStringFunc(content, func(match string) string {
			// Preserve any environment markers (e.g., "; python_version<'3.11'")
			inner := match[1 : len(match)-1] // strip surrounding quotes
			markerIdx := strings.Index(inner, ";")
			marker := ""
			if markerIdx >= 0 {
				marker = inner[markerIdx:]
			}
			return fmt.Sprintf(`"%s%s%s"`, depName, newVersion, marker)
		})
	}

	updateDep("protobuf", protobufVersion)
	updateDep("PyYAML", pyYAMLVersion)
	updateDep("tomli", tomliVersion)
	updateDep("tomli-w", tomliWVersion)
	updateDep("grpcio", grpcioVersion)
	ensureProjectDep := func(depName, newVersion string) {
		escaped := regexp.QuoteMeta(depName)
		re := regexp.MustCompile(`"` + escaped + `(>=|~=|==)[^"]*"`)
		if re.MatchString(content) {
			return
		}
		headerRe := regexp.MustCompile(`(?m)^dependencies\s*=\s*\[\s*$`)
		loc := headerRe.FindStringIndex(content)
		if loc == nil {
			return
		}
		line := fmt.Sprintf(`    "%s%s",`+"\n", depName, newVersion)
		content = content[:loc[1]] + "\n" + line + content[loc[1]:]
	}
	ensureProjectDep("grpcio", grpcioVersion)
	for _, dep := range extraProjectDeps {
		ensureProjectDep(dep.Name, dep.Version)
	}

	if err := os.WriteFile(pyprojectPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to update %s: %w", pyprojectPath, err)
	}

	zap.L().Info("Updated existing pyproject.toml", zap.String("path", pyprojectPath))
	return nil
}

type managedPythonDependency struct {
	Name    string
	Version string
}

func pythonRootProjectDependencies(config *tactype.Config, getPythonVersion func(string) (string, error)) ([]managedPythonDependency, error) {
	var deps []managedPythonDependency
	add := func(name string) error {
		version, err := getPythonVersion(name)
		if err != nil {
			return err
		}
		deps = append(deps, managedPythonDependency{Name: name, Version: version})
		return nil
	}

	// The generated Python runtime library set is written unconditionally today,
	// so root dependencies must cover direct imports from those libraries.
	for _, name := range []string{"cryptography", "httpx", "requests", "urllib3"} {
		if err := add(name); err != nil {
			return nil, err
		}
	}
	if len(config.GrpcW2SClients) > 0 {
		if err := add("websockets"); err != nil {
			return nil, err
		}
	}

	return deps, nil
}

// hasPackageContent checks if a directory contains Python package content
func hasPackageContent(dirPath string) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		// Has .py files or subdirectories (except __pycache__)
		if strings.HasSuffix(name, ".py") || (entry.IsDir() && name != "__pycache__") {
			return true
		}
	}
	return false
}
