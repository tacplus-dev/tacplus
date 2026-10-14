// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"bufio"
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
	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
)

const (
	upgradeRepo        = "tacplus-dev/tacplus"
	checksumsAsset     = "checksums.txt"
	upgradeHTTPTimeout = 30 * time.Second
)

// Vars rather than consts so tests can point them at a fake server.
var (
	upgradeAPIBase      = "https://api.github.com"
	upgradeDownloadBase = "https://github.com"
)

var upgradeVersion string

var upgradeHTTPClient = &http.Client{
	Timeout:       upgradeHTTPTimeout,
	CheckRedirect: stripTokenOnCrossHostRedirect,
}

// stripTokenOnCrossHostRedirect removes the Authorization header when a
// redirect points at a host other than the one originally requested. GitHub
// redirects release downloads to object storage, which needs no token. Go
// strips Authorization on cross-domain redirects itself; this makes the rule
// an exact host match and keeps it in one visible place.
func stripTokenOnCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
	}
	return nil
}

// httpGet issues a GET, with a bearer token when one is configured. Public
// releases need none; GITHUB_TOKEN raises the API rate limit and is required
// while the repository is private.
func httpGet(url, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return upgradeHTTPClient.Do(req)
}

// GetCmd returns the upgrade cobra command.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade tacplus to the latest patch release",
		Long: `Download and replace the running tacplus binary from the GitHub releases of
` + upgradeRepo + `.

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

Every download is verified against the release's checksums.txt before the
running binary is replaced. GITHUB_TOKEN is used when set.`,
		RunE: runUpgrade,
	}
	cmd.Flags().StringVarP(&upgradeVersion, "version", "v", "", "version to install: MAJOR.MINOR or MAJOR.MINOR.PATCH")
	return cmd
}

func runUpgrade(_ *cobra.Command, _ []string) error {
	token := os.Getenv("GITHUB_TOKEN")

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

	fmt.Printf("Installing tacplus %s (%s)...\n", tag, binName)

	wantSHA256, err := fetchChecksum(token, tag, binName)
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

	if err := downloadReplace(assetURL(tag, binName), token, current, wantSHA256); err != nil {
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
	mm := currentMajorMinor()
	if mm == "" {
		return releases[0], nil
	}
	return latestPatchFor(releases, mm)
}

// latestPatchFor returns the first (newest) release tag on the given MAJOR.MINOR line.
func latestPatchFor(releases []string, majorMinor string) (string, error) {
	prefix := "v" + strings.TrimPrefix(majorMinor, "v") + "."
	for _, tag := range releases {
		if strings.HasPrefix(tag, prefix) {
			return tag, nil
		}
	}
	return "", fmt.Errorf("no release found matching %s.x", majorMinor)
}

// currentMajorMinor returns the MAJOR.MINOR of the running binary, or "" when
// it is a local build with no version.
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

// checkNewerLine prints a notice when a newer MAJOR.MINOR line exists than target's.
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

func parsePatch(tag string) (int, error) {
	bare := strings.TrimPrefix(tag, "v")
	parts := strings.Split(bare, ".")
	if len(parts) < 3 {
		return 0, fmt.Errorf("not enough version components")
	}
	return strconv.Atoi(parts[2])
}

// fetchReleases returns the published (non-draft) release tags, newest first,
// as the GitHub releases API orders them.
func fetchReleases(token string) ([]string, error) {
	resp, err := httpGet(fmt.Sprintf("%s/repos/%s/releases?per_page=100", upgradeAPIBase, upgradeRepo), token)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var items []struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("parsing releases: %w", err)
	}

	tags := make([]string, 0, len(items))
	for _, item := range items {
		if !item.Draft {
			tags = append(tags, item.TagName)
		}
	}
	return tags, nil
}

// assetURL is the download URL of one asset of a release.
func assetURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s",
		upgradeDownloadBase, upgradeRepo, url.PathEscape(tag), url.PathEscape(name))
}

// fetchChecksum returns the SHA-256 recorded for fileName in the release's
// checksums.txt (sha256sum format: "<hex>  <name>" per line). A failure to
// obtain the checksum is a hard error — an unverified binary is never installed.
func fetchChecksum(token, tag, fileName string) (string, error) {
	resp, err := httpGet(assetURL(tag, checksumsAsset), token)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fileName {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", checksumsAsset, err)
	}
	return "", fmt.Errorf("%s has no entry for %s", checksumsAsset, fileName)
}

// platformBinary returns the release asset name for the running OS/arch.
func platformBinary(major, minor, patch int) (string, error) {
	var suffix string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		suffix = "darwin-arm64"
	case "darwin/amd64":
		suffix = "darwin-amd64"
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
	resp, err := httpGet(downloadURL, token)
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
