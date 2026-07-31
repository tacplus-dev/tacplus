// Copyright (c) 2026 Second Star Cavalier LLC

package upgrade

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/buildinfo"
)

const (
	upgradeProjectID   = "81965435"
	upgradeAPIBase     = "https://gitlab.com/api/v4"
	upgradeHTTPTimeout = 30 * time.Second
)

var upgradeVersion string

var upgradeHTTPClient = &http.Client{Timeout: upgradeHTTPTimeout}

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
	url := fmt.Sprintf("%s/projects/%s/packages/generic/%s/%d/%s",
		upgradeAPIBase, upgradeProjectID, pkg, patch, binName)

	fmt.Printf("Installing tacplus %s (%s)...\n", tag, binName)

	current, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating current binary: %w", err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	if err := downloadReplace(url, token, current); err != nil {
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
	url := fmt.Sprintf("%s/projects/%s/releases?per_page=100", upgradeAPIBase, upgradeProjectID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := upgradeHTTPClient.Do(req)
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

// downloadReplace downloads url into a temp file then atomically replaces dest.
// On Windows (where running executables are locked) it writes dest+".new" instead.
func downloadReplace(url, token, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	fmt.Printf("Downloading from %s\n", url)
	resp, err := upgradeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("downloading binary: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	dir := filepath.Dir(dest)

	if runtime.GOOS == "windows" {
		newPath := strings.TrimSuffix(dest, ".exe") + ".new.exe"
		if err := writeFile(newPath, resp.Body, 0755); err != nil {
			return err
		}
		fmt.Printf("Downloaded to %s\n", newPath)
		fmt.Printf("To complete the upgrade, run:\n  move /Y %q %q\n", newPath, dest)
		return nil
	}

	tmp, err := os.CreateTemp(dir, ".tacplus-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("writing download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}
	return nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return f.Close()
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
