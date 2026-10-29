// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

const (
	// pinImageRepo is the build image's repository on the registry; the lock
	// names it as pinImageName.
	pinImageRepo = "tacplus-dev/tacplus/build"
	pinImageName = "ghcr.io/" + pinImageRepo
	// pinProbeAsset is the binary whose checksum entry shows a release's
	// binaries were published.
	pinProbeAsset = "linux-amd64"
)

// A var so tests can point it at a fake registry.
var pinRegistryBase = "https://ghcr.io"

var pinVersion string

var pinVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?$`)

// GetPinCmd returns the pin cobra command.
func GetPinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pin",
		Short: "Pin this repository's tacplus release and build image (tacplus.lock)",
		Long: `Write tacplus.lock beside tacplus.yaml: the exact tacplus release every build
of this repository runs, and the build image it runs in, by digest:

  tacplus: 2.6.1
  build_image: ghcr.io/` + pinImageRepo + `:2.6.1@sha256:...

A build runs exactly the pin and never resolves "newest": the build image's
tacplus refuses a repository without one. Moving the pin is this command, and
then a commit of tacplus.lock; a build never moves it.

Version selection:
  (default)         Newest patch of the pin's line (of the tacplus.yaml
                    floor when there is no pin yet)
  --version 2.7     Newest patch of 2.7
  --version 2.7.0   Exactly v2.7.0

The release must have published its binaries (checksums.txt) and its build
image (build:X.Y.Z). GITHUB_TOKEN is used for GitHub when set.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return pin(cwd, pinVersion)
		},
	}
	cmd.Flags().StringVarP(&pinVersion, "version", "v", "", "version to pin: MAJOR.MINOR (its newest patch) or MAJOR.MINOR.PATCH")
	return cmd
}

// pin resolves the release and build image to pin and writes dir/tacplus.lock.
func pin(dir, version string) error {
	floor, err := projectFloor(dir)
	if err != nil {
		return err
	}
	old, err := tacplusyaml.ReadLock(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	want := version
	switch {
	case want != "":
	case old != nil:
		want = old.Line()
	case floor != "":
		want = floor
	default:
		return fmt.Errorf("no pin and no floor (tacplus: in %s) to take the line from: run tacplus pin --version MAJOR.MINOR", tacplusyaml.Filename)
	}
	vm := pinVersionRe.FindStringSubmatch(strings.TrimPrefix(want, "v"))
	if vm == nil {
		return fmt.Errorf("invalid version %q: use MAJOR.MINOR or MAJOR.MINOR.PATCH", want)
	}

	token := os.Getenv("GITHUB_TOKEN")
	releases, err := fetchReleases(token)
	if err != nil {
		return fmt.Errorf("fetching releases: %w", err)
	}
	var tag string
	if vm[3] != "" {
		tag = "v" + strings.TrimPrefix(want, "v")
		if !contains(releases, tag) {
			return fmt.Errorf("no release %s", tag)
		}
	} else if tag, err = highestPatchFor(releases, vm[1]+"."+vm[2]); err != nil {
		return err
	}
	resolved := strings.TrimPrefix(tag, "v")
	if err := tacplusyaml.CheckFloor(floor, resolved); err != nil {
		return err
	}

	// The release's binaries, which the build image's tacplus downloads and
	// checks against this file.
	parts := strings.Split(resolved, ".")
	if _, err := fetchChecksum(token, tag, fmt.Sprintf("tacplus-%s-%s-%s-%s", parts[0], parts[1], parts[2], pinProbeAsset)); err != nil {
		return fmt.Errorf("release %s has no published binaries (%s): %w", tag, checksumsAsset, err)
	}
	digest, err := buildImageDigest(resolved)
	if err != nil {
		return err
	}

	lock := tacplusyaml.Lock{Tacplus: resolved, BuildImage: pinImageName + ":" + resolved + "@" + digest}
	if err := tacplusyaml.WriteLock(dir, lock); err != nil {
		return err
	}
	if old != nil && *old == lock {
		fmt.Printf("%s already pins tacplus %s (%s)\n", tacplusyaml.LockFilename, lock.Tacplus, lock.BuildImage)
		return nil
	}
	from := ""
	if old != nil {
		from = " (was " + old.Tacplus + ")"
	}
	fmt.Printf("Pinned tacplus %s%s, build image %s.\nCommit %s.\n", lock.Tacplus, from, lock.BuildImage, tacplusyaml.LockFilename)
	return nil
}

// projectFloor returns the tacplus: floor of dir's tacplus.yaml (or
// fleet.yaml), "" when it declares none. Only the one key is read: the full
// config's own floor check is against the running binary, and pin may move a
// repository to another line than its own.
func projectFloor(dir string) (string, error) {
	for _, name := range []string{tacplusyaml.Filename, "fleet.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		var cfg struct {
			Tacplus string `yaml:"tacplus"`
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return cfg.Tacplus, nil
	}
	return "", fmt.Errorf("no %s (or fleet.yaml) in %s: tacplus pin writes %s beside it", tacplusyaml.Filename, dir, tacplusyaml.LockFilename)
}

// highestPatchFor returns the release tag with the highest patch on the
// MAJOR.MINOR line, by number: the API orders by date, and a line's backport
// can be cut after a newer line's release. Pre-releases are not candidates.
func highestPatchFor(releases []string, majorMinor string) (string, error) {
	best, bestPatch := "", -1
	prefix := "v" + majorMinor + "."
	for _, tag := range releases {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		p, err := strconv.Atoi(strings.TrimPrefix(tag, prefix))
		if err != nil {
			continue
		}
		if p > bestPatch {
			best, bestPatch = tag, p
		}
	}
	if best == "" {
		return "", fmt.Errorf("no release found matching %s.x", majorMinor)
	}
	return best, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// buildImageDigest returns the digest of build:<version> on the registry,
// asking anonymously as a public pull does.
func buildImageDigest(version string) (string, error) {
	ref := pinImageName + ":" + version
	q := url.Values{"scope": {"repository:" + pinImageRepo + ":pull"}, "service": {"ghcr.io"}}
	resp, err := upgradeHTTPClient.Get(pinRegistryBase + "/token?" + q.Encode())
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", ref, err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || tok.Token == "" {
		return "", fmt.Errorf("resolving %s: no registry token (HTTP %d)", ref, resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodHead, pinRegistryBase+"/v2/"+pinImageRepo+"/manifests/"+url.PathEscape(version), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err = upgradeHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", ref, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s is not published (yet): the release's build image is pushed by .github/workflows/images.yml from its tag", ref)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolving %s: HTTP %d", ref, resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("resolving %s: the registry returned no digest", ref)
	}
	return digest, nil
}
