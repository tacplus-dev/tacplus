// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package protosrc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// DefaultProtoSrc is the default proto source. Empty by default: the proto
// source must be supplied via --src or tacplus.yaml (no repository is baked in).
const DefaultProtoSrc = ""

// Result holds the resolved local directory and a cleanup function.
type Result struct {
	Dir     string
	Cleanup func()
}

// Resolve resolves a proto source string to a local directory.
// The source can be a local path or a git repository URL.
// For git repos, a persistent cache at ~/.tacplus/cache/proto/ is used
// to avoid redundant clones. The first run clones; subsequent runs fetch.
// The tag parameter controls which version to check out:
//   - "latest" or "" → fetch and checkout origin/HEAD
//   - "X.Y.Z" or "X.Y.Z-pre" → fetch tags and checkout vX.Y.Z(-pre)
//   - "X.Y" → fetch tags, find latest vX.Y.* tag, checkout
//
// The caller should call Result.Cleanup when done (currently a no-op for cached repos).
func Resolve(src string, tag string) (*Result, error) {
	if isGitURL(src) {
		return cachedRepo(src, tag)
	}

	// Check if local path exists
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path %q: %w", src, err)
	}

	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		// Resolve symlinks so filepath.Walk (which uses Lstat on the root) sees a real directory.
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		return &Result{Dir: abs, Cleanup: func() {}}, nil
	}

	// Path doesn't exist — maybe it's a repo reference without scheme
	if looksLikeRepo(src) {
		return cachedRepo("https://"+src, tag)
	}

	return nil, fmt.Errorf("proto source %q is not a local directory and does not look like a git URL", src)
}

// IsGitSource returns true if src looks like a git URL or repo reference
// (as opposed to a local directory path).
func IsGitSource(src string) bool {
	return isGitURL(src) || looksLikeRepo(src)
}

// isGitURL returns true if the string looks like a git URL.
func isGitURL(s string) bool {
	return strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "file://")
}

// ListTags returns the tag names of a git source, read from the remote
// (git ls-remote, with the source's forge credentials), sorted by name. A
// local directory is not a git source and has none.
func ListTags(src string) ([]string, error) {
	url := src
	if !isGitURL(src) {
		if !looksLikeRepo(src) {
			return nil, fmt.Errorf("%q is not a git repository URL; its tags cannot be listed", src)
		}
		url = "https://" + src
	}
	cmd := exec.Command("git", append(authArgs(url), "ls-remote", "--tags", "--refs", "--", url)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing the tags of %q: %w", url, err)
	}
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		if _, ref, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		}
	}
	sort.Strings(tags)
	return tags, nil
}

// looksLikeRepo returns true if the string looks like it could be a repo reference.
// Uses Contains to detect well-known git hosting domains in URL-like strings.
func looksLikeRepo(s string) bool {
	return strings.HasSuffix(s, ".git") ||
		strings.Contains(s, "gitlab.com") ||
		strings.Contains(s, "github.com")
}

// cacheDir returns the persistent cache directory for a given repo URL.
// The directory is keyed by a slug derived from the URL.
func cacheDir(url string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	slug := repoSlug(url)
	return filepath.Join(home, ".tacplus", "cache", "proto", slug), nil
}

// repoSlug converts a git URL to a filesystem-safe directory name.
// e.g. "https://gitlab.com/example/proto.git"
//
//	→ "gitlab.com-example-proto-2f8a1c0d9e4b"
//
// The readable part alone is not injective: ":" and "/" both collapse to "-",
// so "gitlab.com/a/b-c" and "gitlab.com/a-b/c" produce identical text. Two
// unrelated repos then shared one cache directory and the first one cloned
// there silently served its protos under the other's name. The suffix is a
// SHA-256 prefix over the full URL, so distinct URLs always get distinct
// directories while the readable part stays legible for debugging.
func repoSlug(url string) string {
	s := url
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")
	sum := sha256.Sum256([]byte(url))
	return s + "-" + hex.EncodeToString(sum[:])[:12]
}

// lockCache takes an exclusive advisory lock covering a cache directory and
// returns the function that releases it. Two concurrent builds against the
// same proto source would otherwise both find no .git and both clone into the
// same path, or run a fetch while the other is mid-checkout. The lock file
// sits beside the cache directory rather than inside it, so it is unaffected
// by git clone creating (or a failed clone removing) the directory itself.
// The lock primitive is per platform (lock_unix.go, lock_windows.go).
func lockCache(dir string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create cache parent: %w", err)
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open cache lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to lock cache %q: %w", dir, err)
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}

// authArgs returns per-invocation git flags carrying the credential of the
// forge rawURL is on: the GitLab CI job token for the GitLab instance that
// issued it, and a GitHub token for github.com (the private registry `depends`
// resolve through lives there). The credential is passed as an
// http.extraheader config override on the command line, which is
// process-scoped: git never writes it to any .git/config, unlike a credential
// embedded in the remote URL.
//
// Each token goes only to its own forge, over https. Sent to any other host it
// is a leaked credential, and a forge such as GitHub refuses the clone outright
// when handed one it does not recognise, so every other host, and any URL
// whose forge has no token set, gets no flags.
func authArgs(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil
	}
	host := u.Hostname()

	// The job token goes only to CI_SERVER_HOST, and only in a job that has both.
	token, server := os.Getenv("CI_JOB_TOKEN"), os.Getenv("CI_SERVER_HOST")
	if token != "" && server != "" && strings.EqualFold(host, server) {
		return basicAuthArgs("gitlab-ci-token", token)
	}

	// A GitHub token (GH_TOKEN first, as the gh CLI reads it; GITHUB_TOKEN in
	// GitHub Actions) goes only to github.com.
	if strings.EqualFold(host, "github.com") {
		token = os.Getenv("GH_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		if token != "" {
			return basicAuthArgs("x-access-token", token)
		}
	}
	return nil
}

// basicAuthArgs returns the git flags sending user:token as HTTP basic auth.
func basicAuthArgs(user, token string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	return []string{"-c", "http.extraheader=AUTHORIZATION: basic " + basic}
}

// cachedRepo uses a persistent cache to avoid redundant clones.
// First run clones into ~/.tacplus/cache/proto/<slug>.
// Subsequent runs fetch and checkout the requested ref.
func cachedRepo(url string, tag string) (*Result, error) {
	dir, err := cacheDir(url)
	if err != nil {
		return nil, err
	}

	// Held across clone+fetch+checkout: the whole sequence mutates this one
	// directory, so a concurrent build must queue behind it, not interleave.
	unlock, err := lockCache(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Normalize X.Y.latest → X.Y for resolution
	resolvedTag := tag
	if strings.HasSuffix(tag, ".latest") {
		resolvedTag = strings.TrimSuffix(tag, ".latest")
	}

	// Credentials are supplied per git invocation, never baked into the remote
	// URL: the cache directory is persistent, and git would write an
	// authenticated remote URL into its .git/config.
	auth := authArgs(url)

	// Check if cache exists (has a .git directory)
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		// First run: clone with the plain, token-free URL as the remote.
		zap.L().Info("cloning proto source (first run)", zap.String("url", url), zap.String("cache", dir))
		cmd := exec.Command("git", append(append([]string{}, auth...), "clone", "--", url, dir)...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("failed to clone %q: %w", url, err)
		}
	} else {
		// NOTE: a cache directory created by an older version of tacplus may
		// still have a token embedded in its stored remote URL
		// (git remote get-url origin); such a cache must be deleted manually.
		zap.L().Info("using cached proto source", zap.String("url", url), zap.String("cache", dir))
	}

	// Fetch and checkout the requested ref
	if err := fetchAndCheckout(dir, resolvedTag, auth); err != nil {
		return nil, err
	}

	zap.L().Info("proto source ready")
	return &Result{Dir: dir, Cleanup: func() {}}, nil
}

// fetchAndCheckout fetches from origin and checks out the requested tag/ref.
func fetchAndCheckout(dir string, tag string, auth []string) error {
	if tag == "" || tag == "latest" {
		// Fetch exactly the ref the remote's HEAD points at — its default
		// branch, which is what a plain "git clone" checks out and what
		// "latest" means here. A bare "git fetch origin" instead writes every
		// advertised branch into FETCH_HEAD; once the cache sits on a detached
		// HEAD (which it does from the first checkout onwards) none of those
		// lines is marked for-merge, so "checkout FETCH_HEAD" takes the first
		// one listed — an arbitrary branch, not the default. Fetching the one
		// symbolic ref leaves FETCH_HEAD unambiguous.
		if err := gitIn(dir, auth, "fetch", "origin", "HEAD"); err != nil {
			return fmt.Errorf("git fetch origin HEAD failed: %w", err)
		}
		if err := gitIn(dir, auth, "checkout", "FETCH_HEAD"); err != nil {
			return fmt.Errorf("git checkout FETCH_HEAD failed: %w", err)
		}
		return nil
	}

	if exactVersionRe.MatchString(tag) {
		// Exact version X.Y.Z, or a pre-release X.Y.Z-pre
		gitTag := "v" + tag
		if strings.HasPrefix(tag, "v") {
			gitTag = tag
		}
		if err := gitIn(dir, auth, "fetch", "origin", "--tags"); err != nil {
			return fmt.Errorf("git fetch --tags failed: %w", err)
		}
		if err := gitIn(dir, auth, "checkout", gitTag); err != nil {
			return fmt.Errorf("git checkout %s failed: %w", gitTag, err)
		}
		return nil
	}

	if strings.Count(tag, ".") == 1 {
		// Major.Minor — find latest patch
		if err := gitIn(dir, auth, "fetch", "origin", "--tags"); err != nil {
			return fmt.Errorf("git fetch --tags failed: %w", err)
		}

		prefix := "v" + tag
		if strings.HasPrefix(tag, "v") {
			prefix = tag
		}

		latestTag, err := findLatestPatchTag(dir, prefix)
		if err != nil {
			return fmt.Errorf("failed to find latest tag for %s.*: %w", prefix, err)
		}

		if err := gitIn(dir, auth, "checkout", latestTag); err != nil {
			return fmt.Errorf("git checkout %s failed: %w", latestTag, err)
		}

		zap.L().Info("resolved to latest patch", zap.String("tag", latestTag))
		return nil
	}

	return fmt.Errorf("invalid tag format %q: expected 'latest', 'X.Y', 'X.Y.Z', 'X.Y.Z-pre', or 'X.Y.latest'", tag)
}

// exactVersionRe is an exact version: vX.Y.Z, optionally with a semver
// pre-release suffix.
var exactVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// gitIn runs a git command in the given directory. Any auth flags are passed
// before the subcommand so they apply to this invocation only.
func gitIn(dir string, auth []string, args ...string) error {
	cmd := exec.Command("git", append(append([]string{}, auth...), args...)...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// findLatestPatchTag finds the latest vX.Y.* tag in a git repo.
func findLatestPatchTag(repoDir, prefix string) (string, error) {
	cmd := exec.Command("git", "tag", "-l", prefix+".*")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git tag -l failed: %w", err)
	}

	tags := strings.Fields(string(output))
	if len(tags) == 0 {
		return "", fmt.Errorf("no tags found matching %s.*", prefix)
	}

	// Sort tags by semver (simple string sort works for vX.Y.Z with same prefix)
	sort.Slice(tags, func(i, j int) bool {
		return compareSemverTags(tags[i], tags[j]) < 0
	})

	return tags[len(tags)-1], nil
}

// compareSemverTags compares two semver tags like "v1.2.3" and "v1.2.10".
func compareSemverTags(a, b string) int {
	aParts := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bParts := strings.Split(strings.TrimPrefix(b, "v"), ".")

	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		var ai, bi int
		fmt.Sscanf(aParts[i], "%d", &ai)
		fmt.Sscanf(bParts[i], "%d", &bi)
		if ai != bi {
			return ai - bi
		}
	}
	return len(aParts) - len(bParts)
}
