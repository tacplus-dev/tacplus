// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tacplus-dev/tacplus/pkg/ghcr"
	"github.com/tacplus-dev/tacplus/pkg/protosrc"
)

// A service's version in fleet.yaml is its release line, MAJOR.MINOR: the
// build runs that line's highest released vMAJOR.MINOR.PATCH tag, pre-releases
// excluded. A fleet that needs an unreleased fix names the pre-release
// explicitly (MAJOR.MINOR.PATCH-pre); it never silently takes a branch. The
// exact version, its commit and its release image land in fleet.lock.

var (
	lineRe       = regexp.MustCompile(`^(\d+)\.(\d+)$`)
	preReleaseRe = regexp.MustCompile(`^\d+\.\d+\.\d+-[0-9A-Za-z.-]+$`)
	// tagRe is a release tag, vMAJOR.MINOR.PATCH with an optional pre-release.
	tagRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)
)

// version is a parsed MAJOR.MINOR.PATCH[-pre].
type version struct {
	major, minor, patch int
	pre                 string
}

// parseVersion parses a version, with or without the tag's v.
func parseVersion(s string) (version, bool) {
	m := tagRe.FindStringSubmatch("v" + strings.TrimPrefix(s, "v"))
	if m == nil {
		return version{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return version{maj, min, pat, m[4]}, true
}

func (v version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if v.pre != "" {
		s += "-" + v.pre
	}
	return s
}

func (v version) line() string { return fmt.Sprintf("%d.%d", v.major, v.minor) }

// compare orders versions by semver precedence: numbers as numbers, and a
// pre-release before its release.
func (v version) compare(o version) int {
	for _, d := range []int{v.major - o.major, v.minor - o.minor, v.patch - o.patch} {
		if d != 0 {
			return d
		}
	}
	switch {
	case v.pre == o.pre:
		return 0
	case v.pre == "":
		return 1
	case o.pre == "":
		return -1
	}
	a, b := strings.Split(v.pre, "."), strings.Split(o.pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aErr := strconv.Atoi(a[i])
		bn, bErr := strconv.Atoi(b[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return an - bn
			}
		case aErr == nil:
			return -1 // numeric identifiers sort before alphanumeric ones
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return len(a) - len(b)
}

// releases returns the release tags (no pre-releases) among tags.
func releases(tags []string) []version {
	var out []version
	for _, t := range tags {
		if v, ok := parseVersion(t); ok && strings.HasPrefix(t, "v") && v.pre == "" {
			out = append(out, v)
		}
	}
	return out
}

// highest returns the highest of vs that keep returns true for.
func highest(vs []version, keep func(version) bool) (version, bool) {
	var best version
	found := false
	for _, v := range vs {
		if keep(v) && (!found || v.compare(best) > 0) {
			best, found = v, true
		}
	}
	return best, found
}

// resolveVersion returns the exact version a service's stated version names
// among its repo's tags: a line's highest release, or the pre-release itself.
func resolveVersion(tags []string, want string) (string, error) {
	if preReleaseRe.MatchString(want) {
		for _, t := range tags {
			if t == "v"+want {
				return want, nil
			}
		}
		return "", fmt.Errorf("no tag v%s", want)
	}
	if !lineRe.MatchString(want) {
		return "", fmt.Errorf("version %q is neither a MAJOR.MINOR line nor a MAJOR.MINOR.PATCH-pre pre-release", want)
	}
	best, ok := highest(releases(tags), func(v version) bool { return v.line() == want })
	if !ok {
		return "", fmt.Errorf("no release on line %s (no v%s.PATCH tag; pre-releases are taken only when named)", want, want)
	}
	return best.String(), nil
}

// lineOf is the MAJOR.MINOR of a stated version (a line or a pre-release).
func lineOf(stated string) string {
	if v, ok := parseVersion(stated); ok {
		return v.line()
	}
	return stated
}

// serviceTags lists a service repo's tags. A fleet runs releases, so the
// repo must be a git repository: a plain directory has no tags to resolve.
func serviceTags(repo string) ([]string, error) {
	if !protosrc.IsGitSource(repo) {
		return nil, fmt.Errorf("%s is not a git repository; a fleet runs a service's releases, which are its tags (name a local clone as file://<path>)", repo)
	}
	return protosrc.ListTags(repo)
}

// releaseImage returns the image a service's release published, by digest
// (ghcr.io/<owner>/<repo>@sha256:...). A var so tests stand in for the
// registry.
var releaseImage = ghcrReleaseImage

// imageRegistryBase is the registry ghcrReleaseImage asks; a var for tests.
var imageRegistryBase = "https://ghcr.io"

var imageHTTPClient = &http.Client{Timeout: 30 * time.Second}

// githubRepoRe takes owner and name from a github.com repository reference.
var githubRepoRe = regexp.MustCompile(`^(?:https?://|ssh://)?(?:git@)?github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// imageName is the image a repository's releases publish: a github.com
// repository publishes ghcr.io/<owner>/<repo>, lower-cased as the registry
// requires.
func imageName(repo string) (string, error) {
	m := githubRepoRe.FindStringSubmatch(strings.TrimSpace(repo))
	if m == nil {
		return "", fmt.Errorf("%s is not a github.com repository, so its release image (ghcr.io/<owner>/<repo>) cannot be named", repo)
	}
	return "ghcr.io/" + strings.ToLower(m[1]+"/"+m[2]), nil
}

// ghcrReleaseImage looks up the digest of <image>:<version> on ghcr.io, with
// GH_TOKEN (else GITHUB_TOKEN) when set: a private repository's package needs
// read:packages.
func ghcrReleaseImage(repo, version string) (string, error) {
	name, err := imageName(repo)
	if err != nil {
		return "", err
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	digest, err := ghcr.Digest(imageHTTPClient, imageRegistryBase, strings.TrimPrefix(name, "ghcr.io/"), version, token)
	if errors.Is(err, ghcr.ErrNotPublished) {
		return "", fmt.Errorf("%s:%s is not published: the service's release publishes its image from its tag", name, version)
	}
	if errors.Is(err, ghcr.ErrDenied) {
		return "", fmt.Errorf("pulling %s:%s was refused: a private package needs GH_TOKEN (or GITHUB_TOKEN) with read:packages", name, version)
	}
	if err != nil {
		return "", fmt.Errorf("resolving %s:%s: %w", name, version, err)
	}
	return name + "@" + digest, nil
}

// Available is what --check found for one service: the version fleet.yaml
// states, the one fleet.lock holds, and what is newer.
type Available struct {
	Name    string
	Version string // as fleet.yaml states it: a line, or a pre-release
	Locked  string // the version in fleet.lock; empty without one
	// Patch is what re-locking would take when it differs from Locked: the
	// line's highest release. For a pre-release pin it is the line's highest
	// release above the pin, reported only: re-locking keeps the pin.
	Patch string
	// Minor is the highest release of a later line than the stated one: a
	// one-line fleet.yaml change away.
	Minor string
}

// Stale reports whether re-locking would change the service's lock entry.
func (a Available) Stale() bool {
	if preReleaseRe.MatchString(a.Version) {
		return a.Locked != a.Version
	}
	return a.Locked == "" || a.Patch != ""
}

// Check reports, for every service in the manifest in manifestDir, the
// releases newer than its lock: a newer patch of its line, which re-locking
// takes, and a newer minor, which needs a fleet.yaml change. It reads the
// service repos' tags and changes nothing.
func Check(manifestDir string) ([]Available, error) {
	m, err := ReadManifest(manifestDir)
	if err != nil {
		return nil, err
	}
	locked := map[string]string{}
	if lock, err := ReadLock(manifestDir); err == nil {
		for _, s := range lock.Services {
			locked[s.Name] = s.Version
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	var out []Available
	for _, spec := range m.Services {
		tags, err := serviceTags(spec.Repo)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: %w", spec.Name, err)
		}
		rels := releases(tags)
		a := Available{Name: spec.Name, Version: spec.Version, Locked: locked[spec.Name]}
		line := lineOf(spec.Version)
		onLine, ok := highest(rels, func(v version) bool { return v.line() == line })
		if preReleaseRe.MatchString(spec.Version) {
			pin, _ := parseVersion(spec.Version)
			if ok && onLine.compare(pin) > 0 {
				a.Patch = onLine.String()
			}
		} else {
			if !ok {
				return nil, fmt.Errorf("fleet: service %q: no release on line %s", spec.Name, line)
			}
			if onLine.String() != a.Locked {
				a.Patch = onLine.String()
			}
		}
		lineV, _ := parseVersion(line + ".0")
		if newer, ok := highest(rels, func(v version) bool { return v.line() != line && v.compare(lineV) > 0 }); ok {
			a.Minor = newer.String()
		}
		out = append(out, a)
	}
	return out, nil
}
