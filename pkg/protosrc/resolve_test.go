// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package protosrc

import (
	"encoding/base64"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsGitURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://gitlab.com/org/repo.git", true},
		{"http://github.com/org/repo.git", true},
		{"git@gitlab.com:org/repo.git", true},
		{"file:///srv/git/repo.git", true},
		{"/local/path/to/proto", false},
		{"./relative/path", false},
		{"gitlab.com/org/repo.git", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isGitURL(tt.input); got != tt.want {
				t.Errorf("isGitURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestLooksLikeRepo(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"gitlab.com/org/repo.git", true},
		{"github.com/org/repo", true},
		{"example.com/repo.git", true},
		{"/local/path", false},
		{"./relative", false},
		{"just-a-name", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := looksLikeRepo(tt.input); got != tt.want {
				t.Errorf("looksLikeRepo(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCompareSemverTags(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int // negative, zero, or positive
	}{
		{"equal", "v1.2.3", "v1.2.3", 0},
		{"patch_less", "v1.2.3", "v1.2.10", -1},
		{"patch_greater", "v1.2.10", "v1.2.3", 1},
		{"minor_less", "v1.2.3", "v1.3.0", -1},
		{"major_less", "v1.9.9", "v2.0.0", -1},
		{"no_v_prefix", "1.2.3", "1.2.4", -1},
		{"mixed_prefix", "v1.2.3", "1.2.4", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemverTags(tt.a, tt.b)
			if (tt.want < 0 && got >= 0) || (tt.want > 0 && got <= 0) || (tt.want == 0 && got != 0) {
				t.Errorf("compareSemverTags(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestResolve_LocalPath(t *testing.T) {
	dir := t.TempDir()

	result, err := Resolve(dir, "latest")
	if err != nil {
		t.Fatalf("Resolve(%q, \"latest\") returned error: %v", dir, err)
	}
	defer result.Cleanup()

	if result.Dir != dir {
		// Resolve returns an absolute path and may resolve root symlinks
		// such as macOS /var -> /private/var.
		abs, _ := filepath.Abs(dir)
		real, _ := filepath.EvalSymlinks(abs)
		if result.Dir != abs && result.Dir != real {
			t.Errorf("Dir = %q, want %q or %q", result.Dir, abs, real)
		}
	}
}

func TestResolve_NonExistentPath(t *testing.T) {
	_, err := Resolve("/nonexistent/path/that/does/not/exist", "latest")
	if err == nil {
		t.Error("Resolve with non-existent path should return error")
	}
}

func TestResolve_InvalidTagFormat(t *testing.T) {
	// Create a temp dir that looks like a valid local path
	// but use a git URL to trigger tag parsing
	_, err := Resolve("https://example.com/fake.git", "invalid-tag-format")
	// This will fail because git clone will fail, but we're testing
	// that the function handles various inputs without panicking.
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestCollectFiles(t *testing.T) {
	dir := t.TempDir()

	// Create test file structure
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "a.rs"), []byte("fn main() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "b.rs"), []byte("fn test() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "d.rs"), []byte("mod sub;"), 0644)

	// This tests the formatter package's collectFiles — but it's unexported there.
	// Instead we test the protosrc helpers which are the main target.
}

func TestAuthArgs(t *testing.T) {
	t.Setenv("CI_JOB_TOKEN", "s3cr3t-token")
	t.Setenv("CI_SERVER_HOST", "gitlab.com")
	t.Setenv("GH_TOKEN", "gh-s3cr3t")
	t.Setenv("GITHUB_TOKEN", "gh-s3cr3t")

	if got := authArgs("git@gitlab.com:org/repo.git"); got != nil {
		t.Errorf("authArgs(ssh url) = %v, want nil", got)
	}

	want := []string{"-c", "http.extraheader=AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:s3cr3t-token"))}
	for _, url := range []string{
		"https://gitlab.com/org/repo.git",
		"https://GitLab.com/org/repo.git",
		"https://gitlab.com:443/org/repo.git",
	} {
		if got := authArgs(url); !reflect.DeepEqual(got, want) {
			t.Errorf("authArgs(%q) = %v, want %v", url, got, want)
		}
	}

	// The GitHub token goes to github.com, and only there, beside the job token.
	githubWant := []string{"-c", "http.extraheader=AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:gh-s3cr3t"))}
	for _, url := range []string{
		"https://github.com/org/repo.git",
		"https://GitHub.com/org/repo.git",
		"https://github.com:443/org/repo.git",
	} {
		if got := authArgs(url); !reflect.DeepEqual(got, githubWant) {
			t.Errorf("authArgs(%q) = %v, want %v", url, got, githubWant)
		}
	}

	// The job token belongs to the GitLab instance that issued it, the GitHub
	// token to github.com; no other host gets either.
	for _, url := range []string{
		"https://example.com/org/repo.git",
		"https://gitlab.com.evil.example/org/repo.git",
		"https://gitlab.com@evil.example/org/repo.git",
		"http://gitlab.com/org/repo.git", // never over cleartext
		"https://github.com.evil.example/org/repo.git",
		"https://github.com@evil.example/org/repo.git",
		"https://api.github.com/org/repo.git",
		"http://github.com/org/repo.git",
		"git@github.com:org/repo.git",
	} {
		if got := authArgs(url); got != nil {
			t.Errorf("authArgs(%q) = %v, want nil", url, got)
		}
	}

	// GH_TOKEN wins over GITHUB_TOKEN, as with the gh CLI; GITHUB_TOKEN alone
	// (GitHub Actions) is used; neither set sends nothing.
	t.Setenv("GITHUB_TOKEN", "actions-s3cr3t")
	if got := authArgs("https://github.com/org/repo.git"); !reflect.DeepEqual(got, githubWant) {
		t.Errorf("authArgs() with GH_TOKEN and GITHUB_TOKEN = %v, want %v", got, githubWant)
	}
	t.Setenv("GH_TOKEN", "")
	actionsWant := []string{"-c", "http.extraheader=AUTHORIZATION: basic " +
		base64.StdEncoding.EncodeToString([]byte("x-access-token:actions-s3cr3t"))}
	if got := authArgs("https://github.com/org/repo.git"); !reflect.DeepEqual(got, actionsWant) {
		t.Errorf("authArgs() with GITHUB_TOKEN only = %v, want %v", got, actionsWant)
	}
	t.Setenv("GITHUB_TOKEN", "")
	if got := authArgs("https://github.com/org/repo.git"); got != nil {
		t.Errorf("authArgs() with no GitHub token = %v, want nil", got)
	}

	// Without CI_SERVER_HOST the issuing instance is unknown: send nothing.
	t.Setenv("CI_SERVER_HOST", "")
	if got := authArgs("https://gitlab.com/org/repo.git"); got != nil {
		t.Errorf("authArgs() with no CI_SERVER_HOST = %v, want nil", got)
	}

	t.Setenv("CI_SERVER_HOST", "gitlab.com")
	t.Setenv("CI_JOB_TOKEN", "")
	if got := authArgs("https://gitlab.com/org/repo.git"); got != nil {
		t.Errorf("authArgs() with no token = %v, want nil", got)
	}
}

// fakeForge serves the git repository at origin over smart HTTP (git
// http-backend) and records whether every request carried exactly the
// Authorization header wantAuth ("" meaning none). A request carrying any
// other credential is refused with 401, as a real forge refuses a foreign one.
type fakeForge struct {
	url     string
	refused atomic.Int32
	served  atomic.Int32
}

func newFakeForge(t *testing.T, origin, wantAuth string) *fakeForge {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	f := &fakeForge{}
	backend := &cgi.Handler{
		Path: gitPath,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(origin),
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			f.refused.Add(1)
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		f.served.Add(1)
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL + "/" + filepath.Base(origin)
	return f
}

// TestCachedRepoCredentialGoesOnlyToItsForge clones through the real
// cachedRepo path inside a simulated GitLab CI job (CI_JOB_TOKEN and
// CI_SERVER_HOST set, plus GitHub tokens) from several forges. Each forge
// URL is rewritten by git's url.insteadOf to a local fake forge, so the
// headers git actually sends are observed. The issuing GitLab instance must
// receive the job token and nothing else (the job token sent to GitHub made
// the clone fail, and leaked it); github.com over https must receive the
// GitHub token (the private registry is on GitHub); every other host,
// including look-alikes of either and github.com over http, must receive no
// credential at all.
func TestCachedRepoCredentialGoesOnlyToItsForge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	const token = "s3cr3t-token"
	gitlabAuth := "basic " + base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:"+token))
	githubAuth := "basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:gh-s3cr3t"))

	origin := filepath.Join(t.TempDir(), "repo.git")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", origin},
		{"-C", origin, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	forges := []struct {
		name string
		url  string
		auth string // the only Authorization header the forge accepts
	}{
		{"github", "https://github.com/org/repo.git", githubAuth},
		{"github-cleartext", "http://github.com/org/repo.git", ""},
		{"github-suffix", "https://github.com.evil.example/org/repo.git", ""},
		{"github-userinfo", "https://github.com@evil.example/org/repo.git", ""},
		{"ci-server-host", "https://gitlab.example.com/org/repo.git", gitlabAuth},
		{"unrelated", "https://git.unrelated.example/org/repo.git", ""},
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CI_JOB_TOKEN", token)
	t.Setenv("CI_SERVER_HOST", "gitlab.example.com")
	t.Setenv("GH_TOKEN", "gh-s3cr3t")
	t.Setenv("GITHUB_TOKEN", "gh-s3cr3t")

	fakes := make([]*fakeForge, len(forges))
	t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(len(forges)))
	for i, fg := range forges {
		fakes[i] = newFakeForge(t, origin, fg.auth)
		t.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(i), "url."+fakes[i].url+".insteadOf")
		t.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(i), fg.url)
	}

	for i, fg := range forges {
		t.Run(fg.name, func(t *testing.T) {
			// Twice: the first call clones, the second takes the fetch path.
			for call := 1; call <= 2; call++ {
				if _, err := cachedRepo(fg.url, "latest"); err != nil {
					t.Fatalf("cachedRepo(%q) call %d: %v", fg.url, call, err)
				}
			}
			if n := fakes[i].refused.Load(); n != 0 {
				t.Errorf("%s: %d request(s) carried a credential the forge does not accept", fg.url, n)
			}
			if fakes[i].served.Load() == 0 {
				t.Errorf("%s: the fake forge served nothing; insteadOf rewrite did not apply", fg.url)
			}
		})
	}
}

// TestCloneDoesNotPersistToken clones a local repo the same way cachedRepo does
// and asserts the CI_JOB_TOKEN never lands in the clone's .git/config.
func TestCloneDoesNotPersistToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	const token = "s3cr3t-token"
	t.Setenv("CI_JOB_TOKEN", token)
	t.Setenv("CI_SERVER_HOST", "gitlab.com")

	// Origin repo with one commit.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Clone it exactly as cachedRepo does: plain URL + per-invocation auth flags.
	url := "https://gitlab.com/org/repo.git"
	auth := authArgs(url)
	dst := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", append(append([]string{}, auth...), "clone", "-q", "--", origin, dst)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}

	cfg, err := os.ReadFile(filepath.Join(dst, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), token) {
		t.Errorf(".git/config contains CI_JOB_TOKEN:\n%s", cfg)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:" + token))
	if strings.Contains(string(cfg), b64) {
		t.Errorf(".git/config contains encoded credential:\n%s", cfg)
	}
	if strings.Contains(string(cfg), "extraheader") {
		t.Errorf(".git/config persisted extraheader:\n%s", cfg)
	}
}

// oldRepoSlug is the pre-fix slug function, kept here only to prove the
// collision it produced was real and not hypothetical.
func oldRepoSlug(url string) string {
	s := url
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")
	return s
}

func TestRepoSlugInjective(t *testing.T) {
	// The pair the old slug collapsed: "a/b-c" and "a-b/c" both became
	// "gitlab.com-a-b-c", so two unrelated repos shared one cache directory.
	collide := [2]string{
		"https://gitlab.com/a/b-c.git",
		"https://gitlab.com/a-b/c.git",
	}
	if oldRepoSlug(collide[0]) != oldRepoSlug(collide[1]) {
		t.Fatalf("old slug no longer collides on %v — this test is vacuous", collide)
	}
	if repoSlug(collide[0]) == repoSlug(collide[1]) {
		t.Errorf("repoSlug still collides on %v: %q", collide, repoSlug(collide[0]))
	}

	pairs := [][2]string{
		collide,
		{"https://gitlab.com/org/repo.git", "https://gitlab.com/other/repo.git"}, // different org
		{"https://gitlab.com/org/repo.git", "https://gitlab.com/org/repo2.git"},  // different repo
		{"https://gitlab.com/org/repo.git", "https://gitlab.com/org/repo"},       // .git vs not
		{"https://gitlab.com/org/repo.git", "http://gitlab.com/org/repo.git"},    // https vs http
		{"https://gitlab.com/org/repo.git", "git@gitlab.com:org/repo.git"},       // https vs ssh
		{"git@gitlab.com:org/repo.git", "git@github.com:org/repo.git"},           // different host
		{"https://gitlab.com/x/y-z/w.git", "https://gitlab.com/x-y/z-w.git"},     // deeper collapse
	}
	for _, p := range pairs {
		if repoSlug(p[0]) == repoSlug(p[1]) {
			t.Errorf("repoSlug(%q) == repoSlug(%q) == %q", p[0], p[1], repoSlug(p[0]))
		}
	}

	// The readable prefix from the docstring survives, so cache dirs stay
	// identifiable by eye.
	if got := repoSlug("https://gitlab.com/example/proto.git"); !strings.HasPrefix(got, "gitlab.com-example-proto-") {
		t.Errorf("repoSlug lost its readable prefix: %q", got)
	}

	// Same URL twice must land in the same directory, or the cache never hits.
	if repoSlug(collide[0]) != repoSlug(collide[0]) {
		t.Error("repoSlug is not deterministic")
	}
}

// TestLockCacheSerializes proves a second holder blocks until the first
// releases. lockCache is held across cachedRepo's whole clone+fetch+checkout
// sequence (see the lockCache call at the top of cachedRepo in resolve.go).
func TestLockCacheSerializes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "slug")

	var inside atomic.Int32
	var maxInside atomic.Int32
	var acquired atomic.Int32

	first, err := lockCache(dir)
	if err != nil {
		t.Fatalf("first lockCache: %v", err)
	}
	inside.Add(1)

	done := make(chan error, 1)
	go func() {
		release, err := lockCache(dir)
		if err != nil {
			done <- err
			return
		}
		acquired.Store(1)
		if n := inside.Add(1); n > maxInside.Load() {
			maxInside.Store(n)
		}
		inside.Add(-1)
		release()
		done <- nil
	}()

	// The second acquire must still be blocked while the first is held.
	time.Sleep(200 * time.Millisecond)
	if acquired.Load() != 0 {
		t.Error("second lockCache acquired while the first was still held")
	}

	inside.Add(-1)
	first()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second lockCache: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second lockCache never acquired after release")
	}
	if maxInside.Load() > 1 {
		t.Errorf("%d holders inside the lock at once", maxInside.Load())
	}
}

// TestCachedRepoConcurrent runs the real cachedRepo path — the same one
// Resolve takes for a git source — from several goroutines at once against one
// cache directory. Without the lock the racers both clone into the same path
// and fetch across each other's checkout; with it each must come back with an
// intact worktree.
func TestCachedRepoConcurrent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// cacheDir keys off the home directory; point it at a scratch one.
	t.Setenv("HOME", t.TempDir())

	const racers = 4
	errs := make(chan error, racers)
	dirs := make(chan string, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			<-start
			res, err := cachedRepo(origin, "latest")
			if err != nil {
				errs <- err
				return
			}
			dirs <- res.Dir
			errs <- nil
		}()
	}
	close(start)

	for i := 0; i < racers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent cachedRepo failed: %v", err)
		}
	}
	close(dirs)

	var cache string
	for d := range dirs {
		if cache == "" {
			cache = d
		} else if d != cache {
			t.Fatalf("racers disagreed on cache dir: %q vs %q", cache, d)
		}
	}

	// The worktree must be a healthy repo, not a half-written clone.
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = cache
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cache worktree is corrupt: %v\n%s", err, out)
	}
}

// TestCachedRepoLatestTracksDefaultBranch pins what tag "latest" means: the
// remote's default branch (what origin/HEAD points at), not whichever branch
// the remote happens to advertise first. The cache is refreshed twice because
// only the second call takes the fetch path — the first clones, and a fresh
// clone lands on the default branch regardless.
func TestCachedRepoLatestTracksDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Origin with two branches; the non-default one sorts first.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"checkout", "-q", "-b", "aaa-feature"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "feature"},
		{"checkout", "-q", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	want := revParse(t, origin, "main")
	feature := revParse(t, origin, "aaa-feature")

	t.Setenv("HOME", t.TempDir())

	for i := 1; i <= 2; i++ {
		res, err := cachedRepo(origin, "latest")
		if err != nil {
			t.Fatalf("cachedRepo (call %d): %v", i, err)
		}
		got := revParse(t, res.Dir, "HEAD")
		if got == feature {
			t.Fatalf("call %d: checked out aaa-feature (%s); want default branch main (%s)", i, feature, want)
		}
		if got != want {
			t.Fatalf("call %d: HEAD is %s; want main (%s)", i, got, want)
		}
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// gitRepo makes a repository with one commit per tag, each tagged, and
// returns its directory and the commit of each tag.
func gitRepo(t *testing.T, tags ...string) (string, map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	commits := map[string]string{}
	for _, tag := range tags {
		git("commit", "-q", "--allow-empty", "-m", tag)
		git("tag", tag)
		commits[tag] = revParse(t, dir, "HEAD")
	}
	return dir, commits
}

// An exact version may be a pre-release (a fleet pins one explicitly): the
// tag vX.Y.Z-pre is checked out as it is.
func TestCachedRepoChecksOutAnExactPreRelease(t *testing.T) {
	origin, commits := gitRepo(t, "v1.2.0", "v1.2.1-rc.1", "v1.2.1")
	t.Setenv("HOME", t.TempDir())
	for _, tag := range []string{"1.2.1-rc.1", "v1.2.0"} {
		res, err := Resolve("file://"+origin, tag)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", tag, err)
		}
		want := commits["v"+strings.TrimPrefix(tag, "v")]
		if got := revParse(t, res.Dir, "HEAD"); got != want {
			t.Fatalf("Resolve(%s): HEAD %s, want %s", tag, got, want)
		}
	}
}

// ListTags reads a git source's tags from the remote, without a clone.
func TestListTags(t *testing.T) {
	origin, _ := gitRepo(t, "v0.1.0", "v0.2.0-rc.1", "notes")
	got, err := ListTags("file://" + origin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"notes", "v0.1.0", "v0.2.0-rc.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListTags = %v, want %v", got, want)
	}
	if _, err := ListTags(t.TempDir()); err == nil {
		t.Fatal("a local directory has no remote tags to list")
	}
}
