// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package depfiles manages dependencies.yaml for tacplus projects.
package depfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/version"
)

// UpdateLevel represents the semver component that changed.
type UpdateLevel int

const (
	Patch UpdateLevel = iota
	Minor
	Major
)

func (l UpdateLevel) String() string {
	switch l {
	case Major:
		return "major"
	case Minor:
		return "minor"
	case Patch:
		return "patch"
	default:
		return "unknown"
	}
}

// LanguageDeps holds dependencies for a specific language
type LanguageDeps struct {
	Dependencies map[string]string `yaml:"dependencies,omitempty"`
}

// DepsConfig represents the structure of dependencies.yaml
type DepsConfig struct {
	Lang struct {
		Go         LanguageDeps `yaml:"go,omitempty"`
		Java       LanguageDeps `yaml:"java,omitempty"`
		TypeScript LanguageDeps `yaml:"typescript,omitempty"`
		Python     LanguageDeps `yaml:"python,omitempty"`
		Rust       LanguageDeps `yaml:"rust,omitempty"`
	} `yaml:"lang"`
}

// EnsureDependencyFiles checks that dependencies.yaml exists in the given directory.
// If it doesn't exist, it generates it from known packages.
func EnsureDependencyFiles(dir string) error {
	depsPath := filepath.Join(dir, "dependencies.yaml")

	if !fileExists(depsPath) {
		return GenerateDependencies(depsPath)
	}
	return nil
}

// updateInfo holds information about a single dependency update.
type updateInfo struct {
	pkg     string
	current string
	latest  string
	level   UpdateLevel
}

// CheckUpdates loads dependencies.yaml, checks for updates at the given level,
// and either reports, prints new YAML, or updates in place.
func CheckUpdates(depsPath string, level UpdateLevel, showNew bool, updateInPlace bool) error {
	deps, err := LoadDepsConfig(depsPath)
	if err != nil {
		return fmt.Errorf("failed to load dependencies.yaml: %w", err)
	}

	fetcher := version.NewVersionFetcher()
	updated := NewEmptyDepsConfig()

	// Copy current deps into updated config
	allLangs := GatherLanguageDeps(deps)
	for _, ld := range allLangs {
		destMap := GetDepsMapForLang(updated, ld.Lang)
		for pkg, ver := range ld.Deps {
			destMap[pkg] = ver
		}
	}

	// Track updates grouped by language
	type langUpdates struct {
		lang    string
		updates []updateInfo
	}
	var allUpdates []langUpdates

	for _, ld := range allLangs {
		var langUpds []updateInfo
		destMap := GetDepsMapForLang(updated, ld.Lang)

		// Sort packages for deterministic output
		pkgs := make([]string, 0, len(ld.Deps))
		for pkg := range ld.Deps {
			pkgs = append(pkgs, pkg)
		}
		sort.Strings(pkgs)

		for _, pkg := range pkgs {
			currentVer := ld.Deps[pkg]
			latest, err := fetcher.FetchLatestVersion(ld.Lang, pkg)
			if err != nil {
				zap.L().Warn("Failed to check package",
					zap.String("language", ld.Lang),
					zap.String("package", pkg),
					zap.Error(err))
				continue
			}

			latestFormatted := FormatVersionForLanguage(ld.Lang, latest)
			cl := ClassifyUpdate(currentVer, latestFormatted)
			if cl < 0 {
				// No update or can't determine
				continue
			}

			// Filter by requested level
			if cl > level {
				continue
			}

			langUpds = append(langUpds, updateInfo{
				pkg:     pkg,
				current: currentVer,
				latest:  latestFormatted,
				level:   cl,
			})
			destMap[pkg] = latestFormatted
		}

		if len(langUpds) > 0 {
			allUpdates = append(allUpdates, langUpdates{lang: ld.Lang, updates: langUpds})
		}
	}

	totalUpdates := 0
	for _, lu := range allUpdates {
		totalUpdates += len(lu.updates)
	}

	if totalUpdates == 0 {
		fmt.Println("All dependencies are up to date.")
		return nil
	}

	if showNew {
		// Apply updates and print full YAML to stdout
		data, err := yaml.Marshal(updated)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}
		fmt.Print(string(data))
		return nil
	}

	if updateInPlace {
		// Apply updates and write back
		if err := WriteDepsConfig(depsPath, updated); err != nil {
			return fmt.Errorf("failed to update dependencies.yaml: %w", err)
		}
	}

	// Print summary table
	for _, lu := range allUpdates {
		fmt.Printf("%s:\n", lu.lang)
		for _, u := range lu.updates {
			fmt.Printf("%-50s %s → %s (%s)\n", u.pkg, u.current, u.latest, u.level)
		}
	}
	fmt.Printf("%d updates available\n", totalUpdates)

	if updateInPlace {
		fmt.Printf("Updated %s\n", depsPath)
	}

	return nil
}

// ClassifyUpdate compares two version strings and returns the UpdateLevel.
// Returns -1 if there is no update (latest <= current).
func ClassifyUpdate(current, latest string) UpdateLevel {
	curBare := StripVersionPrefix(current)
	latBare := StripVersionPrefix(latest)

	if CompareVersionStrings(latBare, curBare) <= 0 {
		return -1
	}

	curParts := splitSemver(curBare)
	latParts := splitSemver(latBare)

	if latParts[0] != curParts[0] {
		return Major
	}
	if latParts[1] != curParts[1] {
		return Minor
	}
	return Patch
}

// splitSemver splits a bare version string into [major, minor, patch].
func splitSemver(ver string) [3]int {
	parts := strings.Split(ver, ".")
	var result [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		fmt.Sscanf(parts[i], "%d", &result[i])
	}
	return result
}

// GenerateDependencies fetches latest versions from registries and generates dependencies.yaml.
func GenerateDependencies(depsPath string) error {
	zap.L().Info("No dependencies.yaml found, generating from known packages")

	versionMap, err := version.GeneratePackagesFromKnownList()
	if err != nil {
		return fmt.Errorf("failed to fetch latest versions: %w", err)
	}

	deps := NewEmptyDepsConfig()

	for lang, pkgVersions := range versionMap {
		depMap := GetDepsMapForLang(deps, lang)
		for pkg, ver := range pkgVersions {
			depMap[pkg] = ver
		}
	}

	if err := WriteDepsConfig(depsPath, deps); err != nil {
		return fmt.Errorf("failed to write dependencies.yaml: %w", err)
	}
	zap.L().Info("Created dependencies.yaml", zap.String("path", depsPath))

	return nil
}

// ============================================================================
// Version helpers
// ============================================================================

// splitVersionPrefix extracts a prefix like "v", "^", ">=", "~" from a version string.
func splitVersionPrefix(ver string) (prefix, bare string) {
	for _, p := range []string{">=", "^", "~", "v"} {
		if strings.HasPrefix(ver, p) {
			return p, strings.TrimPrefix(ver, p)
		}
	}
	return "", ver
}

// StripVersionPrefix removes all known prefixes for comparison.
func StripVersionPrefix(ver string) string {
	_, bare := splitVersionPrefix(ver)
	return bare
}

// FormatVersionForLanguage formats a raw version for each language's convention.
func FormatVersionForLanguage(lang, ver string) string {
	ver = strings.TrimPrefix(ver, "v")
	switch lang {
	case "go":
		return "v" + ver
	case "python":
		return ">=" + ver
	case "typescript":
		return "^" + ver
	default:
		return ver
	}
}

// CompareVersionStrings compares two bare version strings (no prefixes).
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal.
func CompareVersionStrings(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 > n2 {
			return 1
		} else if n1 < n2 {
			return -1
		}
	}
	return 0
}

// ============================================================================
// YAML I/O and helpers
// ============================================================================

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// LoadDepsConfig reads and parses a dependency config YAML file.
func LoadDepsConfig(path string) (*DepsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg DepsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// WriteDepsConfig writes a DepsConfig to a YAML file.
func WriteDepsConfig(path string, cfg *DepsConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// NewEmptyDepsConfig creates a DepsConfig with initialized empty maps.
func NewEmptyDepsConfig() *DepsConfig {
	cfg := &DepsConfig{}
	cfg.Lang.Go.Dependencies = make(map[string]string)
	cfg.Lang.Java.Dependencies = make(map[string]string)
	cfg.Lang.TypeScript.Dependencies = make(map[string]string)
	cfg.Lang.Python.Dependencies = make(map[string]string)
	cfg.Lang.Rust.Dependencies = make(map[string]string)
	return cfg
}

// LangDeps holds language name and its dependencies map.
type LangDeps struct {
	Lang string
	Deps map[string]string
}

// GatherLanguageDeps collects non-empty dependency maps from all languages.
func GatherLanguageDeps(cfg *DepsConfig) []LangDeps {
	var result []LangDeps
	pairs := []struct {
		lang string
		deps map[string]string
	}{
		{"go", cfg.Lang.Go.Dependencies},
		{"java", cfg.Lang.Java.Dependencies},
		{"typescript", cfg.Lang.TypeScript.Dependencies},
		{"python", cfg.Lang.Python.Dependencies},
		{"rust", cfg.Lang.Rust.Dependencies},
	}
	for _, p := range pairs {
		if len(p.deps) > 0 {
			result = append(result, LangDeps{Lang: p.lang, Deps: p.deps})
		}
	}
	return result
}

// GetDepsMapForLang returns the dependency map for the given language.
func GetDepsMapForLang(cfg *DepsConfig, lang string) map[string]string {
	switch lang {
	case "go":
		return cfg.Lang.Go.Dependencies
	case "java":
		return cfg.Lang.Java.Dependencies
	case "typescript":
		return cfg.Lang.TypeScript.Dependencies
	case "python":
		return cfg.Lang.Python.Dependencies
	case "rust":
		return cfg.Lang.Rust.Dependencies
	default:
		return nil
	}
}
