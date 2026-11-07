// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The build image's cache configuration (#315): images/cache.sh
// (tacplus-cache) and images/image-build.sh (tacplus-image-build) read the
// cache from the build's configuration, never their own, and build cold when
// it names none. images/cache-determinism.sh, in the image, proves a cold and
// a warm build give the same verdict and artifacts.

// runScript runs an images/ script with exactly env (plus PATH and HOME) and
// returns its stdout, its stderr and its exit code.
func runScript(t *testing.T, script, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "images", script))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func cacheEnv(t *testing.T, env ...string) map[string]string {
	t.Helper()
	out, errOut, code := runScript(t, "cache.sh", t.TempDir(), env, "env")
	if code != 0 {
		t.Fatalf("tacplus-cache env: exit %d\n%s", code, errOut)
	}
	vars := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		kv, ok := strings.CutPrefix(line, "export ")
		k, v, ok2 := strings.Cut(kv, "=")
		if !ok || !ok2 {
			t.Fatalf("not an export: %q", line)
		}
		vars[k] = v
	}
	return vars
}

func TestCacheEnvColdWithNoConfiguration(t *testing.T) {
	if vars := cacheEnv(t); len(vars) != 0 {
		t.Fatalf("nothing configured must change nothing, got %v", vars)
	}
	for _, cmd := range []string{"restore", "save"} {
		_, errOut, code := runScript(t, "cache.sh", t.TempDir(), nil, cmd)
		if code != 0 || !strings.Contains(errOut, "TACPLUS_CACHE_BUCKET") {
			t.Fatalf("tacplus-cache %s with nothing configured: exit %d, %q", cmd, code, errOut)
		}
	}
}

func TestCacheEnvLocalDirectory(t *testing.T) {
	vars := cacheEnv(t, "TACPLUS_CACHE_DIR=/cache")
	want := map[string]string{
		"GOCACHE":           "/cache/go-build",
		"GOMODCACHE":        "/cache/go-mod",
		"MAVEN_ARGS":        `-Dmaven.repo.local=/cache/m2`,
		"RUSTC_WRAPPER":     "sccache",
		"CARGO_INCREMENTAL": "0",
		"SCCACHE_DIR":       "/cache/sccache",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	if _, ok := vars["SCCACHE_GCS_BUCKET"]; ok {
		t.Errorf("no bucket configured, but SCCACHE_GCS_BUCKET is set")
	}
}

func TestCacheEnvBucket(t *testing.T) {
	vars := cacheEnv(t, "TACPLUS_CACHE_BUCKET=gs://ci-cache/tacplus/", "TACPLUS_CACHE_DIR=/cache")
	want := map[string]string{
		"SCCACHE_GCS_BUCKET":     "ci-cache",
		"SCCACHE_GCS_KEY_PREFIX": "tacplus/sccache",
		"SCCACHE_GCS_RW_MODE":    "READ_WRITE",
		"RUSTC_WRAPPER":          "sccache",
		"GOCACHE":                "/cache/go-build",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	if _, ok := vars["SCCACHE_DIR"]; ok {
		t.Errorf("a gs:// bucket is sccache's storage, but SCCACHE_DIR is set")
	}
	// A bucket alone still gets a local directory, under HOME.
	if vars := cacheEnv(t, "TACPLUS_CACHE_BUCKET=gs://ci-cache"); !strings.HasSuffix(vars["GOCACHE"], "/.cache/tacplus-build/go-build") ||
		vars["SCCACHE_GCS_KEY_PREFIX"] != "sccache" {
		t.Errorf("bucket without a directory: %v", vars)
	}
}

func TestCacheRefusesBadConfiguration(t *testing.T) {
	for _, env := range []string{"TACPLUS_CACHE_BUCKET=s3://x", "TACPLUS_CACHE_BUCKET=gs://", "TACPLUS_CACHE_DIR=relative"} {
		if _, _, code := runScript(t, "cache.sh", t.TempDir(), []string{env}, "env"); code != 2 {
			t.Errorf("%s: exit %d, want 2", env, code)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cacheKey(t *testing.T, dir, kind string) string {
	t.Helper()
	out, errOut, code := runScript(t, "cache.sh", dir, nil, "key", kind)
	if code != 0 {
		t.Fatalf("tacplus-cache key %s: exit %d\n%s", kind, code, errOut)
	}
	return strings.TrimSpace(out)
}

func TestCacheKeyIsContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.sum"), "a v1 h1:x\n")
	writeFile(t, filepath.Join(dir, "sub", "go.sum"), "b v1 h1:y\n")
	writeFile(t, filepath.Join(dir, "sub", "pom.xml"), "<project/>\n")
	goKey, mavenKey := cacheKey(t, dir, "go"), cacheKey(t, dir, "maven")
	if !strings.HasPrefix(goKey, "go-") || !strings.HasPrefix(mavenKey, "maven-") {
		t.Fatalf("keys %q, %q", goKey, mavenKey)
	}
	if k := cacheKey(t, dir, "go"); k != goKey {
		t.Fatalf("the same content keyed twice: %q then %q", goKey, k)
	}
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	if k := cacheKey(t, dir, "go"); k != goKey {
		t.Fatalf("a source file changed the go key: %q then %q", goKey, k)
	}
	writeFile(t, filepath.Join(dir, "sub", "go.sum"), "b v2 h1:z\n")
	if k := cacheKey(t, dir, "go"); k == goKey {
		t.Fatalf("a nested go.sum changed and the go key did not")
	}
	if k := cacheKey(t, dir, "maven"); k != mavenKey {
		t.Fatalf("a go.sum changed the maven key")
	}
	if _, _, code := runScript(t, "cache.sh", dir, nil, "key", "npm"); code != 2 {
		t.Fatalf("an unknown kind: exit %d, want 2", code)
	}
}

func TestCacheSaveRestoreRoundTrip(t *testing.T) {
	src, bucket := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "go.sum"), "a v1 h1:x\n")
	from := t.TempDir()
	writeFile(t, filepath.Join(from, "go-build", "ab", "entry"), "compiled")
	writeFile(t, filepath.Join(from, "go-mod", "cache", "download", "a", "v1.zip"), "module")
	env := []string{"TACPLUS_CACHE_BUCKET=file://" + bucket, "TACPLUS_CACHE_DIR=" + from}
	if _, errOut, code := runScript(t, "cache.sh", src, env, "save"); code != 0 || !strings.Contains(errOut, "go: saved go-") {
		t.Fatalf("save: exit %d\n%s", code, errOut)
	}
	if !strings.Contains(func() string { _, e, _ := runScript(t, "cache.sh", src, env, "save"); return e }(), "already stored") {
		t.Fatalf("a second save must leave the stored entry as it is")
	}

	to := t.TempDir()
	env[1] = "TACPLUS_CACHE_DIR=" + to
	if _, errOut, code := runScript(t, "cache.sh", src, env, "restore"); code != 0 || !strings.Contains(errOut, "go: restored go-") ||
		!strings.Contains(errOut, "maven: no snapshot") {
		t.Fatalf("restore: exit %d\n%s", code, errOut)
	}
	for _, f := range []string{"go-build/ab/entry", "go-mod/cache/download/a/v1.zip"} {
		if _, err := os.Stat(filepath.Join(to, f)); err != nil {
			t.Errorf("not restored: %s", f)
		}
	}

	// Another lock file is another key: nothing to restore, and still no failure.
	writeFile(t, filepath.Join(src, "go.sum"), "a v2 h1:y\n")
	if _, errOut, code := runScript(t, "cache.sh", src, env, "restore"); code != 0 || !strings.Contains(errOut, "go: no snapshot") {
		t.Fatalf("restore of another key: exit %d\n%s", code, errOut)
	}
}

// imageBuildArgs runs tacplus-image-build against a fake buildctl and returns
// the arguments it passed, one per element.
func imageBuildArgs(t *testing.T, env []string, args ...string) []string {
	t.Helper()
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "args")
	writeFile(t, filepath.Join(bin, "buildctl"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+record+"\n")
	if err := os.Chmod(filepath.Join(bin, "buildctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := t.TempDir()
	writeFile(t, filepath.Join(ctx, "images", "base.Dockerfile"), "FROM scratch\n")
	path, err := filepath.Abs(filepath.Join("..", "images", "image-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Dir = ctx
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "BUILDKIT_HOST=unix:///fake.sock"}, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tacplus-image-build %v: %v\n%s", args, err, out)
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(got)), "\n")
}

func hasArg(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestImageBuildCold(t *testing.T) {
	args := imageBuildArgs(t, []string{"SOURCE_DATE_EPOCH=1767225600"},
		"-f", "images/base.Dockerfile", "--build-arg", "A=b", "--output", "type=oci,dest=/tmp/x.tar", ".")
	for _, want := range [][2]string{
		{"--frontend", "dockerfile.v0"},
		{"--opt", "filename=base.Dockerfile"},
		{"--opt", "build-arg:A=b"},
		{"--opt", "build-arg:SOURCE_DATE_EPOCH=1767225600"},
		{"--output", "type=oci,dest=/tmp/x.tar,rewrite-timestamp=true"},
	} {
		if !hasArg(args, want[0], want[1]) {
			t.Errorf("missing %s %s in %q", want[0], want[1], args)
		}
	}
	for _, a := range args {
		if a == "--import-cache" || a == "--export-cache" {
			t.Fatalf("no cache configured, but buildctl got %s: %q", a, args)
		}
	}
}

func TestImageBuildRegistryCache(t *testing.T) {
	args := imageBuildArgs(t, []string{"TACPLUS_CACHE_REGISTRY=registry.example/ci/build-cache/", "SOURCE_DATE_EPOCH=1"},
		"-f", "images/base.Dockerfile", "--platform", "linux/amd64,linux/arm64", ".")
	ref := "registry.example/ci/build-cache/base:buildcache-linux-amd64-linux-arm64"
	if !hasArg(args, "--import-cache", "type=registry,ref="+ref) {
		t.Errorf("no registry import of %s: %q", ref, args)
	}
	if !hasArg(args, "--export-cache", "type=registry,ref="+ref+",mode=max,image-manifest=true,oci-mediatypes=true,ignore-error=true") {
		t.Errorf("no registry export of %s: %q", ref, args)
	}
	if !hasArg(args, "--opt", "platform=linux/amd64,linux/arm64") {
		t.Errorf("platforms not passed: %q", args)
	}
}

func TestImageBuildLocalCache(t *testing.T) {
	dir := t.TempDir()
	env := []string{"TACPLUS_CACHE_DIR=" + dir, "SOURCE_DATE_EPOCH=1"}
	local := filepath.Join(dir, "buildkit", "fixture-buildcache")
	args := imageBuildArgs(t, env, "-f", "images/base.Dockerfile", "--cache", "fixture", ".")
	if !hasArg(args, "--export-cache", "type=local,dest="+local+",mode=max,ignore-error=true") {
		t.Errorf("no local export to %s: %q", local, args)
	}
	if hasArg(args, "--import-cache", "type=local,src="+local) {
		t.Errorf("imported a local cache that does not exist yet: %q", args)
	}
	writeFile(t, filepath.Join(local, "index.json"), "{}")
	if args := imageBuildArgs(t, env, "-f", "images/base.Dockerfile", "--cache", "fixture", "."); !hasArg(args, "--import-cache", "type=local,src="+local) {
		t.Errorf("no local import from %s: %q", local, args)
	}
}
