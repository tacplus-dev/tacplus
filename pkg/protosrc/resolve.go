// Copyright (c) 2026 Second Star Cavalier LLC

package protosrc

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
//   - "X.Y.Z" → fetch tags and checkout vX.Y.Z
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
		strings.HasPrefix(s, "http://")
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
//	→ "gitlab.com-example-proto"
func repoSlug(url string) string {
	s := url
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

// authArgs returns per-invocation git flags carrying the GitLab CI credential
// when running in GitLab CI. The credential is passed as an http.extraheader
// config override on the command line, which is process-scoped: git never
// writes it to any .git/config, unlike a credential embedded in the remote URL.
// If CI_JOB_TOKEN is not set, no flags are returned.
func authArgs(url string) []string {
	token := os.Getenv("CI_JOB_TOKEN")
	if token == "" || !strings.HasPrefix(url, "https://") {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:" + token))
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
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create cache parent: %w", err)
		}
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
		// Fetch and checkout origin/HEAD
		if err := gitIn(dir, auth, "fetch", "origin"); err != nil {
			return fmt.Errorf("git fetch failed: %w", err)
		}
		if err := gitIn(dir, auth, "checkout", "FETCH_HEAD"); err != nil {
			return fmt.Errorf("git checkout FETCH_HEAD failed: %w", err)
		}
		return nil
	}

	if strings.Count(tag, ".") == 2 {
		// Exact version X.Y.Z
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

	return fmt.Errorf("invalid tag format %q: expected 'latest', 'X.Y', 'X.Y.Z', or 'X.Y.latest'", tag)
}

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
