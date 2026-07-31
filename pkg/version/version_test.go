// Copyright (c) 2026 Second Star Cavalier LLC

package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// isStableVersion tests
// ---------------------------------------------------------------------------

func TestIsStableVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{name: "simple semver", version: "1.2.3", want: true},
		{name: "major only", version: "1", want: true},
		{name: "major.minor", version: "1.2", want: true},
		{name: "four parts", version: "1.2.3.4", want: true},
		{name: "zero version", version: "0.0.0", want: true},

		// Pre-release markers
		{name: "RC suffix", version: "1.2.3-RC1", want: false},
		{name: "alpha suffix", version: "1.2.3-alpha", want: false},
		{name: "beta suffix", version: "1.2.3-beta1", want: false},
		{name: "M suffix", version: "1.2.3.M1", want: false},
		{name: "SNAPSHOT suffix", version: "1.0.0-SNAPSHOT", want: false},
		{name: "preview suffix", version: "1.0.0-preview", want: false},
		{name: "pre suffix", version: "1.0.0-pre1", want: false},
		{name: "case insensitive alpha", version: "1.0.0-ALPHA", want: false},
		{name: "case insensitive beta", version: "1.0.0-Beta", want: false},

		// Non-numeric chars
		{name: "has letters", version: "abc", want: false},
		{name: "v prefix", version: "v1.2.3", want: false},
		{name: "has hyphen", version: "1.2.3-4", want: false},
		{name: "has plus", version: "1.2.3+build", want: false},

		// Edge cases
		{name: "empty string", version: "", want: true},
		{name: "just dots", version: "...", want: true},
		{name: "single zero", version: "0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isStableVersion(tt.version)
			if got != tt.want {
				t.Errorf("isStableVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// compareVersions tests
// ---------------------------------------------------------------------------

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		v1   string
		v2   string
		want int
	}{
		{name: "equal versions", v1: "1.2.3", v2: "1.2.3", want: 0},
		{name: "v1 greater major", v1: "2.0.0", v2: "1.9.9", want: 1},
		{name: "v2 greater major", v1: "1.9.9", v2: "2.0.0", want: -1},
		{name: "v1 greater minor", v1: "1.3.0", v2: "1.2.9", want: 1},
		{name: "v2 greater minor", v1: "1.2.9", v2: "1.3.0", want: -1},
		{name: "v1 greater patch", v1: "1.2.4", v2: "1.2.3", want: 1},
		{name: "v2 greater patch", v1: "1.2.3", v2: "1.2.4", want: -1},
		{name: "different lengths v1 longer", v1: "1.2.3.4", v2: "1.2.3", want: 1},
		{name: "different lengths v2 longer", v1: "1.2.3", v2: "1.2.3.4", want: -1},
		{name: "shorter equal with zero pad", v1: "1.2", v2: "1.2.0", want: 0},
		{name: "zeros", v1: "0.0.0", v2: "0.0.0", want: 0},
		{name: "single component", v1: "5", v2: "3", want: 1},
		{name: "empty vs empty", v1: "", v2: "", want: 0},
		{name: "empty vs something", v1: "", v2: "1.0.0", want: -1},
		{name: "something vs empty", v1: "1.0.0", v2: "", want: 1},
		{name: "large numbers", v1: "100.200.300", v2: "100.200.299", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatVersionForLanguage tests
// ---------------------------------------------------------------------------

func TestFormatVersionForLanguage(t *testing.T) {
	tests := []struct {
		name    string
		lang    string
		version string
		want    string
	}{
		// Go always has v prefix
		{name: "go adds v prefix", lang: "go", version: "1.2.3", want: "v1.2.3"},
		{name: "go keeps v prefix", lang: "go", version: "v1.2.3", want: "v1.2.3"},

		// Python uses >= prefix
		{name: "python adds >= prefix", lang: "python", version: "1.2.3", want: ">=1.2.3"},
		{name: "python strips v then adds >=", lang: "python", version: "v1.2.3", want: ">=1.2.3"},

		// TypeScript uses ^ prefix
		{name: "typescript adds ^ prefix", lang: "typescript", version: "1.2.3", want: "^1.2.3"},
		{name: "typescript strips v then adds ^", lang: "typescript", version: "v1.2.3", want: "^1.2.3"},

		// Rust uses plain version
		{name: "rust plain version", lang: "rust", version: "1.2.3", want: "1.2.3"},
		{name: "rust strips v prefix", lang: "rust", version: "v1.2.3", want: "1.2.3"},

		// Java uses plain version
		{name: "java plain version", lang: "java", version: "1.2.3", want: "1.2.3"},
		{name: "java strips v prefix", lang: "java", version: "v1.2.3", want: "1.2.3"},

		// Unknown language uses default (plain)
		{name: "unknown language plain", lang: "unknown", version: "1.2.3", want: "1.2.3"},

		// Edge cases
		{name: "go empty version", lang: "go", version: "", want: "v"},
		{name: "python empty version", lang: "python", version: "", want: ">="},
		{name: "typescript empty version", lang: "typescript", version: "", want: "^"},
		{name: "rust empty version", lang: "rust", version: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatVersionForLanguage(tt.lang, tt.version)
			if got != tt.want {
				t.Errorf("formatVersionForLanguage(%q, %q) = %q, want %q", tt.lang, tt.version, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewVersionFetcher tests
// ---------------------------------------------------------------------------

func TestNewVersionFetcher(t *testing.T) {
	f := NewVersionFetcher()
	if f == nil {
		t.Fatal("NewVersionFetcher() returned nil")
	}
	if f.client == nil {
		t.Fatal("NewVersionFetcher().client is nil")
	}
	if f.client.Timeout != 10*1e9 { // 10 seconds in nanoseconds
		t.Errorf("NewVersionFetcher().client.Timeout = %v, want 10s", f.client.Timeout)
	}
}

// ---------------------------------------------------------------------------
// FetchLatestVersion tests (with mock HTTP server)
// ---------------------------------------------------------------------------

func TestFetchLatestVersion_UnsupportedLanguage(t *testing.T) {
	f := NewVersionFetcher()
	_, err := f.FetchLatestVersion("cobol", "mypackage")
	if err == nil {
		t.Fatal("expected error for unsupported language, got nil")
	}
	if got := err.Error(); got != "unsupported language: cobol" {
		t.Errorf("unexpected error message: %q", got)
	}
}

func TestFetchGoVersion_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"Version": "v1.42.0"})
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	// Override the module path to hit our test server
	version, err := fetchGoVersionFromURL(f.client, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "v1.42.0" {
		t.Errorf("got version %q, want %q", version, "v1.42.0")
	}
}

func TestFetchGoVersion_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	_, err := fetchGoVersionFromURL(f.client, server.URL)
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
}

func TestFetchRustVersion_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "tacplus" {
			t.Error("expected User-Agent header 'tacplus'")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"crate": map[string]string{"max_stable_version": "0.8.15"},
		})
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	version, err := fetchRustVersionFromURL(f.client, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "0.8.15" {
		t.Errorf("got version %q, want %q", version, "0.8.15")
	}
}

func TestFetchPythonVersion_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"info": map[string]string{"version": "3.1.4"},
		})
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	version, err := fetchPythonVersionFromURL(f.client, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "3.1.4" {
		t.Errorf("got version %q, want %q", version, "3.1.4")
	}
}

func TestFetchTypeScriptVersion_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"dist-tags": map[string]string{"latest": "5.0.0"},
		})
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	version, err := fetchTypeScriptVersionFromURL(f.client, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "5.0.0" {
		t.Errorf("got version %q, want %q", version, "5.0.0")
	}
}

func TestFetchJavaVersion_MockServer(t *testing.T) {
	html := `<html><body>
<a href="1.0.0/">1.0.0/</a>
<a href="1.1.0/">1.1.0/</a>
<a href="2.0.0-RC1/">2.0.0-RC1/</a>
<a href="1.2.0/">1.2.0/</a>
<a href="maven-metadata.xml">maven-metadata.xml</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	version, err := fetchJavaVersionFromURL(f.client, server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.0" {
		t.Errorf("got version %q, want %q", version, "1.2.0")
	}
}

func TestFetchJavaVersion_NoStableVersions(t *testing.T) {
	html := `<html><body>
<a href="1.0.0-SNAPSHOT/">1.0.0-SNAPSHOT/</a>
<a href="2.0.0-RC1/">2.0.0-RC1/</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, html)
	}))
	defer server.Close()

	f := &VersionFetcher{client: server.Client()}
	_, err := fetchJavaVersionFromURL(f.client, server.URL)
	if err == nil {
		t.Fatal("expected error when no stable versions exist, got nil")
	}
}

// ---------------------------------------------------------------------------
// Helper: fetchXxxVersionFromURL wrappers for testing without hitting real APIs
// These avoid needing to mock the entire URL construction.
// ---------------------------------------------------------------------------

func fetchGoVersionFromURL(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
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

func fetchRustVersionFromURL(client *http.Client, url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tacplus")

	resp, err := client.Do(req)
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

func fetchPythonVersionFromURL(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
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

func fetchTypeScriptVersionFromURL(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
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

func fetchJavaVersionFromURL(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to fetch Java version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch Java version: HTTP %d", resp.StatusCode)
	}

	body := make([]byte, 0, 32*1024)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}

	html := string(body)
	var versions []string
	for _, line := range splitLines(html) {
		if containsStr(line, "href=") {
			start := indexStr(line, "href=\"")
			if start == -1 {
				continue
			}
			start += 6
			end := indexStr(line[start:], "\"")
			if end == -1 {
				continue
			}
			version := line[start : start+end]
			version = trimSuffix(version, "/")
			if isStableVersion(version) {
				versions = append(versions, version)
			}
		}
	}

	if len(versions) == 0 {
		return "", fmt.Errorf("no stable versions found")
	}

	latestVersion := versions[0]
	for _, v := range versions[1:] {
		if compareVersions(v, latestVersion) > 0 {
			latestVersion = v
		}
	}
	return latestVersion, nil
}

// Simple helpers used by fetchJavaVersionFromURL
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func indexStr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
