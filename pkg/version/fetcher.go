// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/packages"
)

// VersionFetcher fetches latest stable versions from package registries
type VersionFetcher struct {
	client *http.Client
}

// NewVersionFetcher creates a new version fetcher
func NewVersionFetcher() *VersionFetcher {
	return &VersionFetcher{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// FetchLatestVersion fetches the latest stable version for a package
func (f *VersionFetcher) FetchLatestVersion(language, packageName string) (string, error) {
	switch language {
	case "go":
		return f.fetchGoVersion(packageName)
	case "rust":
		return f.fetchRustVersion(packageName)
	case "python":
		return f.fetchPythonVersion(packageName)
	case "java":
		return f.fetchJavaVersion(packageName)
	case "typescript":
		return f.fetchTypeScriptVersion(packageName)
	default:
		return "", fmt.Errorf("unsupported language: %s", language)
	}
}

// fetchGoVersion fetches latest Go module version from proxy.golang.org
func (f *VersionFetcher) fetchGoVersion(modulePath string) (string, error) {
	url := fmt.Sprintf("https://proxy.golang.org/%s/@latest", modulePath)

	resp, err := f.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Go version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch Go version: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Version string `json:"Version"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse Go version: %w", err)
	}

	return result.Version, nil
}

// fetchRustVersion fetches latest Rust crate version from crates.io
func (f *VersionFetcher) fetchRustVersion(crateName string) (string, error) {
	url := fmt.Sprintf("https://crates.io/api/v1/crates/%s", crateName)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tacplus")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Rust version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch Rust version: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Crate struct {
			MaxStableVersion string `json:"max_stable_version"`
		} `json:"crate"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse Rust version: %w", err)
	}

	return result.Crate.MaxStableVersion, nil
}

// fetchPythonVersion fetches latest Python package version from PyPI
func (f *VersionFetcher) fetchPythonVersion(packageName string) (string, error) {
	url := fmt.Sprintf("https://pypi.org/pypi/%s/json", packageName)

	resp, err := f.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Python version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch Python version: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse Python version: %w", err)
	}

	return result.Info.Version, nil
}

// fetchJavaVersion fetches latest Java artifact version from Maven Central.
// Uses Contains to parse HTML directory listing from Maven Central (no API available).
func (f *VersionFetcher) fetchJavaVersion(artifact string) (string, error) {
	// Parse groupId:artifactId
	parts := strings.Split(artifact, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid Java artifact format: %s (expected groupId:artifactId)", artifact)
	}

	groupId := parts[0]
	artifactId := parts[1]

	// Convert groupId to path (com.google.protobuf -> com/google/protobuf)
	groupPath := strings.ReplaceAll(groupId, ".", "/")

	// Query Maven Central repository directory listing
	url := fmt.Sprintf("https://repo1.maven.org/maven2/%s/%s/", groupPath, artifactId)

	resp, err := f.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Java version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch Java version: HTTP %d", resp.StatusCode)
	}

	// Parse HTML directory listing to extract version folders
	body := make([]byte, 0, 32*1024)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	html := string(body)

	// Extract version directories - look for patterns like href="1.2.3/"
	var versions []string
	for _, line := range strings.Split(html, "\n") {
		if strings.Contains(line, "href=") {
			// Find all href="X.Y.Z/" patterns
			start := strings.Index(line, "href=\"")
			if start == -1 {
				continue
			}
			start += 6
			end := strings.Index(line[start:], "\"")
			if end == -1 {
				continue
			}
			version := line[start : start+end]
			version = strings.TrimSuffix(version, "/")

			// Only consider stable versions (X.Y.Z format, no RC/alpha/beta/M)
			if isStableVersion(version) {
				versions = append(versions, version)
			}
		}
	}

	if len(versions) == 0 {
		return "", fmt.Errorf("no stable versions found for %s", artifact)
	}

	// Find the highest version
	latestVersion := versions[0]
	for _, v := range versions[1:] {
		if compareVersions(v, latestVersion) > 0 {
			latestVersion = v
		}
	}

	return latestVersion, nil
}

// isStableVersion checks if a version string represents a stable release
func isStableVersion(version string) bool {
	// Exclude pre-release markers
	preRelease := []string{"RC", "alpha", "beta", "M", "SNAPSHOT", "preview", "pre"}
	versionLower := strings.ToLower(version)
	for _, marker := range preRelease {
		if strings.Contains(versionLower, strings.ToLower(marker)) {
			return false
		}
	}

	// Must match basic version pattern (numbers and dots)
	for _, char := range version {
		if !((char >= '0' && char <= '9') || char == '.') {
			return false
		}
	}

	return true
}

// compareVersions compares two semantic versions
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func compareVersions(v1, v2 string) int {
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

// fetchTypeScriptVersion fetches latest npm package version
func (f *VersionFetcher) fetchTypeScriptVersion(packageName string) (string, error) {
	// Handle scoped packages
	packageName = strings.ReplaceAll(packageName, "@", "%40")
	packageName = strings.ReplaceAll(packageName, "/", "%2F")

	url := fmt.Sprintf("https://registry.npmjs.org/%s", packageName)

	resp, err := f.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch TypeScript version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch TypeScript version: HTTP %d", resp.StatusCode)
	}

	var result struct {
		DistTags struct {
			Latest string `json:"latest"`
		} `json:"dist-tags"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to parse TypeScript version: %w", err)
	}

	return result.DistTags.Latest, nil
}

// GeneratePackagesFromKnownList generates config.yaml from KnownPackages with latest versions
func GeneratePackagesFromKnownList() (map[string]map[string]string, error) {
	fetcher := NewVersionFetcher()

	versionMap := map[string]map[string]string{
		"go":         make(map[string]string),
		"rust":       make(map[string]string),
		"python":     make(map[string]string),
		"java":       make(map[string]string),
		"typescript": make(map[string]string),
	}

	for _, pkg := range packages.KnownPackages {
		for lang, pkgNames := range pkg.Mappings {
			for _, pkgName := range pkgNames {
				zap.L().Debug("fetching package version", zap.String("language", lang), zap.String("package", pkgName))

				version, err := fetcher.FetchLatestVersion(lang, pkgName)
				if err != nil {
					zap.L().Warn("failed to fetch version, skipping", zap.String("language", lang), zap.String("package", pkgName), zap.Error(err))
					continue
				}

				// Format version for language
				formattedVersion := formatVersionForLanguage(lang, version)
				versionMap[lang][pkgName] = formattedVersion

				zap.L().Debug("fetched version", zap.String("package", pkgName), zap.String("version", formattedVersion))
			}
		}
	}

	return versionMap, nil
}

// formatVersionForLanguage formats version string for each language
func formatVersionForLanguage(lang, version string) string {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	switch lang {
	case "go":
		// Go requires 'v' prefix
		if !strings.HasPrefix(version, "v") {
			return "v" + version
		}
		return version
	case "python":
		// Python uses >= comparisons
		return ">=" + version
	case "typescript":
		// TypeScript uses ^ for caret ranges
		return "^" + version
	default:
		// Rust, Java use plain version
		return version
	}
}
