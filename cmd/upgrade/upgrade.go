// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitlab.com/seabeam/schematic/tacplus/pkg/buildinfo"
)

const (
	upgradeProjectID   = "81965435"
	upgradeHTTPTimeout = 30 * time.Second
)

// upgradeAPIBase is a var rather than a const so tests can point it at a fake server.
var upgradeAPIBase = "https://gitlab.com/api/v4"

var upgradeVersion string

var upgradeHTTPClient = &http.Client{
	Timeout:       upgradeHTTPTimeout,
	CheckRedirect: stripTokenOnCrossHostRedirect,
}

// stripTokenOnCrossHostRedirect removes the PRIVATE-TOKEN header when a redirect
// points at a host other than the one originally requested. Go strips Authorization
// and Cookie on cross-host redirects itself, but knows nothing about GitLab's custom
// auth header, so it would otherwise be forwarded verbatim to the redirect target.
// The comparison is an exact host match: GitLab redirects package downloads to signed
// object-storage URLs that need no token anyway, so there is nothing to gain from a
// looser "same registered domain" rule.
func stripTokenOnCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("PRIVATE-TOKEN")
	}
	return nil
}

// gitlabGet issues an authenticated GET against the GitLab API.
func gitlabGet(url, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	return upgradeHTTPClient.Do(req)
}

// GetCmd returns the upgrade cobra command.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade tacplus to the latest patch release",
		Long: `Download and replace the running tacplus binary.

By default, upgrades to the latest patch release on the current MAJOR.MINOR
version line (e.g. if running v1.1.2, installs the latest v1.1.x).
Patch releases contain bug fixes only and are safe to apply without risk of
destabilising existing workflows.

If a newer MAJOR.MINOR release line is available, a notice is printed to
stderr with the command to upgrade — run it when you are ready.

Version selection:
  (default)         Latest patch for the current MAJOR.MINOR
  --version 1.1     Latest patch for major.minor 1.1
  --version 1.1.3   Exact release v1.1.3

Authentication is read from (in order):
  1. GITLAB_TOKEN or PRIVATE_TOKEN environment variable
  2. ~/.netrc entry for gitlab.com:

       machine gitlab.com
       login <username>
       password <your-pat>`,
		RunE: runUpgrade,
	}
	cmd.Flags().StringVarP(&upgradeVersion, "version", "v", "", "version to install: MAJOR.MINOR or MAJOR.MINOR.PATCH")
	return cmd
}

func runUpgrade(_ *cobra.Command, _ []string) error {
	token := gitlabToken()
	if token == "" {
		return fmt.Errorf("no GitLab token found — set GITLAB_TOKEN or add a ~/.netrc entry for gitlab.com")
	}

	releases, err := fetchReleases(token)
	if err != nil {
		return fmt.Errorf("fetching releases: %w", err)
	}
	if len(releases) == 0 {
		return fmt.Errorf("no releases found")
	}

	tag, err := resolveReleaseTag(releases, upgradeVersion)
	if err != nil {
		return fmt.Errorf("resolving version: %w", err)
	}

	// Notify if a newer MAJOR.MINOR release line exists beyond the resolved tag.
	checkNewerLine(releases, tag)

	mm := parseMajorMinor(tag)
	if mm == nil {
		return fmt.Errorf("could not parse version from tag %q", tag)
	}
	patch, err := parsePatch(tag)
	if err != nil {
		return fmt.Errorf("could not parse patch from tag %q: %w", tag, err)
	}

	binName, err := platformBinary(mm[0], mm[1], patch)
	if err != nil {
		return err
	}

	pkg := fmt.Sprintf("tacplus-%d-%d", mm[0], mm[1])
	pkgVersion := strconv.Itoa(patch)
	downloadURL := fmt.Sprintf("%s/projects/%s/packages/generic/%s/%s/%s",
		upgradeAPIBase, upgradeProjectID, pkg, pkgVersion, binName)

	fmt.Printf("Installing tacplus %s (%s)...\n", tag, binName)

	wantSHA256, err := fetchPackageSHA256(token, pkg, pkgVersion, binName)
	if err != nil {
		return fmt.Errorf("looking up checksum for %s: %w", binName, err)
	}

	current, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current binary: %w", err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	if err := downloadReplace(downloadURL, token, current, wantSHA256); err != nil {
		return err
	}

	fmt.Printf("Successfully upgraded to %s.\n", tag)
	return nil
}

// resolveReleaseTag selects the exact release tag to download.
// version may be "" (current line's latest patch), "MAJOR.MINOR", or "MAJOR.MINOR.PATCH".
func resolveReleaseTag(releases []string, version string) (string, error) {
	if version != "" {
		bare := strings.TrimPrefix(version, "v")
		parts := strings.Split(bare, ".")
		switch len(parts) {
		case 3:
			return "v" + bare, nil
		case 2:
			return latestPatchFor(releases, bare)
		default:
			return "", fmt.Errorf("invalid version %q: use MAJOR.MINOR or MAJOR.MINOR.PATCH", version)
		}
	}

	// Default: latest patch for the running binary's MAJOR.MINOR.
	mm := currentMajorMinor()
	if mm == "" {
		// Dev build — upgrade to absolute latest.
		return releases[0], nil
	}
	return latestPatchFor(releases, mm)
}

// latestPatchFor returns the newest release whose tag starts with vMAJOR.MINOR.
func latestPatchFor(releases []string, majorMinor string) (string, error) {
	prefix := "v" + strings.TrimPrefix(majorMinor, "v") + "."
	for _, tag := range releases {
		if strings.HasPrefix(tag, prefix) {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no release found matching %s.x", majorMinor)
}

// currentMajorMinor parses buildinfo.Version and returns "MAJOR.MINOR", or "" if unparseable.
func currentMajorMinor() string {
	bare := strings.TrimPrefix(buildinfo.Version, "v")
	parts := strings.Split(bare, ".")
	if len(parts) < 2 {
		return ""
	}
	if _, err := strconv.Atoi(parts[0]); err != nil {
		return ""
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// checkNewerLine prints a stderr notice if any release has a higher MAJOR.MINOR than target.
func checkNewerLine(releases []string, target string) {
	targetMM := parseMajorMinor(target)
	if targetMM == nil {
		return
	}
	for _, tag := range releases {
		mm := parseMajorMinor(tag)
		if mm == nil {
			continue
		}
		if mm[0] > targetMM[0] || (mm[0] == targetMM[0] && mm[1] > targetMM[1]) {
			bin := filepath.Base(os.Args[0])
			fmt.Fprintf(os.Stderr,
				"notice: a newer release line v%d.%d is available — to upgrade, run: %s upgrade --version %d.%d\n",
				mm[0], mm[1], bin, mm[0], mm[1])
			return // report only the newest newer line
		}
	}
}

// parseMajorMinor parses a tag like "v1.1.3" -> [major, minor]. Returns nil if unparseable.
func parseMajorMinor(tag string) []int {
	bare := strings.TrimPrefix(tag, "v")
	parts := strings.Split(bare, ".")
	if len(parts) < 2 {
		return nil
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	return []int{major, minor}
}

// parsePatch parses the patch number from a tag like "v1.1.3".
func parsePatch(tag string) (int, error) {
	bare := strings.TrimPrefix(tag, "v")
	parts := strings.Split(bare, ".")
	if len(parts) < 3 {
		return 0, fmt.Errorf("not enough version components")
	}
	return strconv.Atoi(parts[2])
}

// fetchReleases returns all release tag names sorted newest-first.
func fetchReleases(token string) ([]string, error) {
	resp, err := gitlabGet(fmt.Sprintf("%s/projects/%s/releases?per_page=100", upgradeAPIBase, upgradeProjectID), token)
	if err != nil {
		return nil, fmt.Errorf("fetching releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetching releases: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var items []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("parsing releases: %w", err)
	}

	tags := make([]string, len(items))
	for i, item := range items {
		tags[i] = item.TagName
	}
	return tags, nil
}

// fetchPackageSHA256 returns the SHA-256 GitLab computed for the named file of a
// generic package, using the Packages API:
//
//	GET /projects/:id/packages?package_type=generic&package_name=&package_version=
//	GET /projects/:id/packages/:package_id/package_files
//
// A failure to obtain the checksum is a hard error — an unverified binary is never
// installed.
func fetchPackageSHA256(token, pkg, version, fileName string) (string, error) {
	listURL := fmt.Sprintf("%s/projects/%s/packages?package_type=generic&package_name=%s&package_version=%s",
		upgradeAPIBase, upgradeProjectID, url.QueryEscape(pkg), url.QueryEscape(version))

	var packages []struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := getJSON(listURL, token, &packages); err != nil {
		return "", err
	}

	pkgID := 0
	for _, p := range packages {
		if p.Name == pkg && p.Version == version {
			pkgID = p.ID
			break
		}
	}
	if pkgID == 0 {
		return "", fmt.Errorf("generic package %s version %s not found", pkg, version)
	}

	filesURL := fmt.Sprintf("%s/projects/%s/packages/%d/package_files", upgradeAPIBase, upgradeProjectID, pkgID)
	var files []struct {
		FileName   string `json:"file_name"`
		FileSHA256 string `json:"file_sha256"`
	}
	if err := getJSON(filesURL, token, &files); err != nil {
		return "", err
	}

	for _, f := range files {
		if f.FileName != fileName {
			continue
		}
		if f.FileSHA256 == "" {
			return "", fmt.Errorf("GitLab reported no sha256 for %s", fileName)
		}
		return f.FileSHA256, nil
	}
	return "", fmt.Errorf("file %s not found in package %s version %s", fileName, pkg, version)
}

// getJSON performs an authenticated GET and decodes a JSON response body into out.
func getJSON(url, token string, out any) error {
	resp, err := gitlabGet(url, token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("parsing response from %s: %w", url, err)
	}
	return nil
}

// platformBinary returns the binary filename for the current OS/arch.
func platformBinary(major, minor, patch int) (string, error) {
	var suffix string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		suffix = "darwin-arm64"
	case "linux/amd64":
		suffix = "linux-amd64"
	case "linux/arm64":
		suffix = "linux-arm64"
	case "windows/amd64":
		suffix = "windows-amd64.exe"
	case "windows/arm64":
		suffix = "windows-arm64.exe"
	default:
		return "", fmt.Errorf("unsupported platform %s/%s — download manually from the releases page", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("tacplus-%d-%d-%d-%s", major, minor, patch, suffix), nil
}

// downloadReplace downloads url into a temp file, verifies its SHA-256 against
// wantSHA256, then atomically replaces dest. On Windows (where running executables
// are locked) the verified file is left at dest+".new.exe" instead.
func downloadReplace(downloadURL, token, dest, wantSHA256 string) error {
	fmt.Printf("Downloading from %s\n", downloadURL)
	resp, err := gitlabGet(downloadURL, token)
	if err != nil {
		return fmt.Errorf("downloading binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("downloading binary: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".tacplus-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, wantSHA256) {
		return fmt.Errorf("checksum mismatch: expected sha256 %s, got %s — refusing to install", wantSHA256, got)
	}

	if err := os.Chmod(tmpName, 0755); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}

	if runtime.GOOS == "windows" {
		newPath := strings.TrimSuffix(dest, ".exe") + ".new.exe"
		if err := os.Rename(tmpName, newPath); err != nil {
			return fmt.Errorf("writing %s: %w", newPath, err)
		}
		fmt.Printf("Downloaded to %s\n", newPath)
		fmt.Printf("To complete the upgrade, run:\n  move /Y %q %q\n", newPath, dest)
		return nil
	}

	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}
	return nil
}

// gitlabToken returns a GitLab PAT from env or ~/.netrc.
func gitlabToken() string {
	if t := os.Getenv("GITLAB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("PRIVATE_TOKEN"); t != "" {
		return t
	}
	return netrcPassword("gitlab.com")
}

// netrcPassword parses ~/.netrc and returns the password for the given machine.
func netrcPassword(machine string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".netrc"))
	if err != nil {
		return ""
	}
	toks := strings.Fields(string(data))
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == "machine" && toks[i+1] == machine {
			for j := i + 2; j+1 < len(toks); j++ {
				if toks[j] == "machine" {
					break
				}
				if toks[j] == "password" {
					return toks[j+1]
				}
			}
		}
	}
	return ""
}
