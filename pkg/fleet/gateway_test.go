// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBuilds stands in for the recipes' tools (npm, cargo, go, dotnet, swift,
// tacplus-image-build): each writes the output its real tool would, derived
// only from its inputs, and every step is recorded. salt, when set, makes the
// webapp builds differ, as a build that is not reproducible would.
type fakeBuilds struct {
	steps []Step
	salt  string
}

func (f *fakeBuilds) run(s Step) error {
	f.steps = append(f.steps, s)
	env := map[string]string{}
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	arg := func(flag string) string {
		for i, a := range s.Args {
			if a == flag && i+1 < len(s.Args) {
				return s.Args[i+1]
			}
		}
		return ""
	}
	put := func(p, content string) error {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o755)
	}
	cmd := strings.Join(s.Args, " ")
	switch {
	case strings.HasPrefix(cmd, "npm run build"):
		return put(filepath.Join(s.Dir, "dist", "index.html"), "base="+env["TACPLUS_WEB_BASE"]+f.salt+"\n")
	case strings.HasPrefix(cmd, "cargo build --release --locked --bin"):
		return put(filepath.Join(s.Dir, "target", arg("--target"), "release", arg("--bin")), "rust "+arg("--target")+"\n")
	case strings.HasPrefix(cmd, "cargo zigbuild"):
		bin := arg("--bin")
		if strings.Contains(arg("--target"), "windows") {
			bin += ".exe"
		}
		return put(filepath.Join(s.Dir, "target", arg("--target"), "release", bin), "rust "+arg("--target")+"\n")
	case strings.HasPrefix(cmd, "cargo build --release --locked"):
		return put(filepath.Join(s.Dir, "target", "release", "gateway"), "gateway binary\n")
	case strings.HasPrefix(cmd, "go build"):
		return put(arg("-o"), "go "+env["GOOS"]+"/"+env["GOARCH"]+"\n")
	case strings.HasPrefix(cmd, "swift build"):
		return put(filepath.Join(s.Dir, ".build", "arm64-apple-macosx", "release", arg("--product")), "swift\n")
	case strings.HasPrefix(cmd, "dotnet publish"):
		name := strings.TrimSuffix(filepath.Base(s.Args[2]), ".csproj")
		return put(filepath.Join(arg("-o"), name+".exe"), "dotnet\n")
	case strings.HasPrefix(cmd, "tacplus-image-build"):
		ctx := s.Args[len(s.Args)-1]
		sum, err := treeDigest(ctx)
		if err != nil {
			return err
		}
		dest := strings.TrimPrefix(strings.Split(arg("--output"), ",")[1], "dest=")
		if err := put(dest, "oci "+sum+"\n"); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]string{"containerimage.digest": "sha256:" + sum})
		return put(arg("--metadata-file"), string(meta))
	}
	return nil // tacplus build, npm ci: nothing the test reads
}

// stepsMatching are the recorded steps whose command starts with prefix.
func (f *fakeBuilds) stepsMatching(prefix string) []Step {
	var out []Step
	for _, s := range f.steps {
		if strings.HasPrefix(strings.Join(s.Args, " "), prefix) {
			out = append(out, s)
		}
	}
	return out
}

const gatewayDockerfile = `FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
COPY --from=ghcr.io/grpc-ecosystem/grpc-health-probe:v0.4.63@sha256:1d6cc5716f1c80cbcf97d5bc7eef3cd5bd4c7a7b942c0dc4f533dea70dcb5173 /ko-app/grpc-health-probe /usr/local/bin/grpc_health_probe
COPY target/release/gateway /usr/local/bin/gateway
COPY webapp-dist/ /opt/tacplus/webapp/
COPY web-dist/ /opt/tacplus/web/
COPY clients/ /opt/tacplus/clients/
ARG TARGETARCH
COPY launcher-linux-${TARGETARCH} /usr/local/bin/launcher
`

// gatewayFleet is a locked fleet of the gateway, echo (contribute, as given,
// with files) and the launcher; it returns the deployment repo and the out
// dir fleet build wrote.
func gatewayFleet(t *testing.T, contribute string, files map[string]string) (fl, out string) {
	t.Helper()
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	gwDir := serviceRepoID(t, root, "gateway", 1, reg, nil)
	write(t, filepath.Join(gwDir, "Dockerfile"), gatewayDockerfile)
	write(t, filepath.Join(gwDir, "Cargo.toml"), "[package]\nname = \"gateway\"\n")
	write(t, filepath.Join(gwDir, "webapp", "package.json"), "{}\n")
	write(t, filepath.Join(gwDir, "webapp", "package-lock.json"), "{}\n")
	write(t, filepath.Join(gwDir, "webapp", "tacplus.yaml"), "language: typescript\nsrc: proto\n")
	gw := release(t, gwDir, "v0.3.0")
	echo := contributingService(t, root, "echo", 101, reg, contribute, files)
	launcherDir := filepath.Join(root, "launcher")
	write(t, filepath.Join(launcherDir, "go.mod"), "module github.com/tacplus-dev/launcher\n")
	write(t, filepath.Join(launcherDir, "main.go"), "package main\n")
	launcher := release(t, launcherDir, "v0.1.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: gateway, id: 1, contract: "+gw+"}\n    - {name: echo, id: 101, contract: "+echo+"}\n")
	fl = fleetRepo(t, root, reg, "  - {name: gateway, version: \"0.3\", repo: "+gw+"}\n  - {name: echo, version: \"0.1\", repo: "+echo+"}\n")
	b, _ := os.ReadFile(filepath.Join(fl, ManifestFile))
	write(t, filepath.Join(fl, ManifestFile), string(b)+"launcher: {version: \"0.1\", repo: "+launcher+"}\n")
	out = filepath.Join(root, "out")
	if _, err := Build(fl, out); err != nil {
		t.Fatal(err)
	}
	return fl, out
}

// withFakeBuilds runs the recipes through f on a host of hostOS/arm64 that
// names its build image, for one test.
func withFakeBuilds(t *testing.T, f *fakeBuilds, os string) {
	t.Helper()
	oldRun, oldOS, oldArch := runStep, hostOS, hostArch
	runStep, hostOS, hostArch = f.run, os, "arm64"
	t.Setenv("TACPLUS_BUILD_IMAGE", "ghcr.io/tacplus-dev/tacplus/build@sha256:"+strings.Repeat("b", 64))
	t.Cleanup(func() { runStep, hostOS, hostArch = oldRun, oldOS, oldArch })
}

var echoWebapp = map[string]string{"web/package.json": "{}\n", "web/package-lock.json": "{}\n", "web/tacplus.yaml": "kind: client\n", "go.mod": "module echo\n", "cmd/echo/main.go": "package main\n"}

// The gateway step builds the gateway's own webapp at / and each contributed
// module under /-/apps/<service>/ with the fixed recipe, writes the index,
// builds the clients and the launcher, and builds the image from them,
// recording every output and the image's digest in fleet.lock.
func TestGatewayBuildsTheMergedWebappClientsAndImage(t *testing.T) {
	fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n  clients:\n    - {name: echo, go: ./cmd/echo}\n", echoWebapp)
	f := &fakeBuilds{}
	withFakeBuilds(t, f, "linux")

	g, err := Gateway(fl, out, GatewayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := filepath.Join(out, "gateway", "context")
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(ctx, rel))
		if err != nil {
			t.Fatalf("the image context lacks %s: %v", rel, err)
		}
		return string(b)
	}
	if got := read("webapp-dist/index.html"); got != "base=/\n" {
		t.Errorf("the gateway's own webapp is built at /: %q", got)
	}
	if got := read("webapp-dist/-/apps/echo/index.html"); got != "base=/-/apps/echo/\n" {
		t.Errorf("echo's module is built at its mount: %q", got)
	}
	if got := read("webapp-dist/-/apps.json"); got != `[{"service":"echo","title":"Echo","version":"0.1.0","path":"/-/apps/echo/"}]`+"\n" {
		t.Errorf("apps.json = %s", got)
	}
	for _, target := range []string{"linux-amd64", "linux-arm64", "darwin-arm64", "windows-amd64.exe"} {
		read("clients/echo/echo/echo-" + target)
	}
	read("launcher-linux-arm64")
	read("target/release/gateway")
	read("Dockerfile")
	if fi, err := os.Stat(filepath.Join(ctx, "web-dist")); err != nil || !fi.IsDir() {
		t.Errorf("the image context needs web-dist/ (no customer web app: empty): %v", err)
	}

	// The fixed recipe, in the module's dir, with its mount and its tag's time.
	ci := f.stepsMatching("npm ci")
	if len(ci) != 2 || strings.Join(ci[1].Args, " ") != "npm ci --ignore-scripts" || filepath.Base(ci[1].Dir) != "web" {
		t.Fatalf("npm ci steps = %+v", ci)
	}
	builds := f.stepsMatching("npm run build")
	if !hasEnv(builds[1], "TACPLUS_WEB_BASE=/-/apps/echo/") || !hasEnvPrefix(builds[1], "SOURCE_DATE_EPOCH=") {
		t.Errorf("echo's build env = %v", builds[1].Env)
	}
	gens := f.stepsMatching("tacplus build --src")
	if len(gens) < 3 || gens[0].Args[3] != filepath.Join(out, "proto") {
		t.Errorf("each part is generated from the fleet's merged protos first: %+v", gens)
	}
	gob := f.stepsMatching("go build")
	if len(gob) != 5 || !hasEnv(gob[0], "CGO_ENABLED=0") || !strings.Contains(strings.Join(gob[0].Args, " "), "-trimpath -buildvcs=false -ldflags=-s -w -buildid=") {
		t.Errorf("go recipe (4 targets and the launcher) = %+v", gob)
	}
	img := f.stepsMatching("tacplus-image-build")
	if len(img) != 1 || !strings.Contains(strings.Join(img[0].Args, " "), "--platform linux/arm64") || !strings.Contains(strings.Join(img[0].Args, " "), "rewrite-timestamp=true") || !hasEnvPrefix(img[0], "SOURCE_DATE_EPOCH=") {
		t.Fatalf("image step = %+v", img)
	}

	lock, err := ReadLock(fl)
	if err != nil {
		t.Fatal(err)
	}
	lg := lock.Gateway
	if lg == nil || lg.Version != "0.3.0" || lg.Commit != lock.Services[0].Commit || lg.Platform != "linux/arm64" {
		t.Fatalf("lock's gateway section = %+v", lg)
	}
	if !strings.HasPrefix(lg.Image, "sha256:") || lg.Image != g.Image {
		t.Errorf("image digest = %q (built %q)", lg.Image, g.Image)
	}
	if !strings.HasPrefix(lg.BuildImage, "ghcr.io/tacplus-dev/tacplus/build@sha256:") || len(lg.BaseImages) != 2 || !strings.Contains(lg.BaseImages[0], "@sha256:") {
		t.Errorf("build and base images = %q, %q", lg.BuildImage, lg.BaseImages)
	}
	names := map[string]string{}
	for _, o := range lg.Outputs {
		names[o.Name] = o.SHA256
	}
	for _, want := range []string{"binary", "launcher", "webapp", "webapp/gateway", "webapp/echo", "client/echo/echo/linux/amd64", "client/echo/echo/darwin/arm64", "client/echo/echo/windows/amd64"} {
		if len(names[want]) != 64 {
			t.Errorf("lock lacks the sha256 of %s: %+v", want, lg.Outputs)
		}
	}
}

func hasEnv(s Step, kv string) bool {
	for _, e := range s.Env {
		if e == kv {
			return true
		}
	}
	return false
}

func hasEnvPrefix(s Step, p string) bool {
	for _, e := range s.Env {
		if strings.HasPrefix(e, p) {
			return true
		}
	}
	return false
}

// --check rebuilds from the lock and compares every recorded digest: the same
// inputs pass and change nothing; an output that differs fails, named.
func TestGatewayCheckRebuildsAndComparesEveryDigest(t *testing.T) {
	fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n", echoWebapp)
	f := &fakeBuilds{}
	withFakeBuilds(t, f, "linux")
	if _, err := Gateway(fl, out, GatewayOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(fl, LockFile))
	if _, err := Gateway(fl, out, GatewayOptions{Check: true}); err != nil {
		t.Fatalf("a rebuild from the same inputs must give the same digests: %v", err)
	}
	f.salt = " (another build)"
	_, err := Gateway(fl, out, GatewayOptions{Check: true})
	if err == nil || !strings.Contains(err.Error(), "webapp/echo") || !strings.Contains(err.Error(), "image") {
		t.Fatalf("got %v, want the differing module and image named", err)
	}
	if after, _ := os.ReadFile(filepath.Join(fl, LockFile)); string(after) != string(before) {
		t.Fatal("--check must not change fleet.lock")
	}
}

// A Rust client's darwin/arm64 build and a Swift client are the macos
// runner's: on Linux they come from its clients directory (--prebuilt), and
// without it the step says which runner builds them.
func TestGatewayDarwinClientsComeFromTheMacRunner(t *testing.T) {
	files := map[string]string{"Cargo.toml": "[workspace]\n", "Cargo.lock": "version = 4\n", "Package.swift": "// swift\n"}
	fl, out := gatewayFleet(t, "  clients:\n    - {name: echo, rust: echo}\n    - {name: echo-mac, swift: EchoMac}\n", files)

	mac := &fakeBuilds{}
	withFakeBuilds(t, mac, "darwin")
	if _, err := Gateway(fl, out, GatewayOptions{ClientsOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := len(mac.stepsMatching("cargo build --release --locked --bin echo --target aarch64-apple-darwin")); got != 1 {
		t.Errorf("the Mac builds the rust client's darwin/arm64 natively: %+v", mac.steps)
	}
	if len(mac.stepsMatching("cargo zigbuild")) != 0 || len(mac.stepsMatching("tacplus-image-build")) != 0 {
		t.Errorf("the Mac builds only its targets, and no image: %+v", mac.steps)
	}
	prebuilt := filepath.Join(out, "gateway", "clients")
	for _, rel := range []string{"echo/echo/echo-darwin-arm64", "echo/echo-mac/echo-mac-darwin-arm64"} {
		if _, err := os.Stat(filepath.Join(prebuilt, rel)); err != nil {
			t.Fatalf("the Mac's output lacks %s: %v", rel, err)
		}
	}
	keep := t.TempDir()
	if err := os.Rename(prebuilt, filepath.Join(keep, "clients")); err != nil {
		t.Fatal(err)
	}

	linux := &fakeBuilds{}
	withFakeBuilds(t, linux, "linux")
	_, err := Gateway(fl, out, GatewayOptions{})
	if err == nil || !strings.Contains(err.Error(), "macos") || !strings.Contains(err.Error(), "echo-darwin-arm64") {
		t.Fatalf("got %v, want the macos runner named for the darwin client", err)
	}
	if _, err := Gateway(fl, out, GatewayOptions{Prebuilt: filepath.Join(keep, "clients")}); err != nil {
		t.Fatal(err)
	}
	if got := len(linux.stepsMatching("cargo zigbuild --release --locked --bin echo --target")); got != 3 {
		t.Errorf("Linux cross-builds linux/amd64, linux/arm64 and windows/amd64 with cargo-zigbuild: %d", got)
	}
	lock, _ := ReadLock(fl)
	var found bool
	for _, o := range lock.Gateway.Outputs {
		found = found || o.Name == "client/echo/echo-mac/darwin/arm64"
	}
	if !found {
		t.Errorf("the Mac's outputs are locked like any other: %+v", lock.Gateway.Outputs)
	}
}

func TestGatewayRefuses(t *testing.T) {
	t.Run("no gateway in the fleet", func(t *testing.T) {
		root := t.TempDir()
		reg := filepath.Join(root, "registry")
		plain := releasedService(t, root, "plain", 102, reg, nil, "v0.1.0")
		write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: plain, id: 102, contract: "+plain+"}\n")
		fl := fleetRepo(t, root, reg, "  - {name: plain, version: \"0.1\", repo: "+plain+"}\n")
		if _, err := Build(fl, filepath.Join(root, "out")); err != nil {
			t.Fatal(err)
		}
		withFakeBuilds(t, &fakeBuilds{}, "linux")
		_, err := Gateway(fl, filepath.Join(root, "out"), GatewayOptions{})
		if err == nil || !strings.Contains(err.Error(), "runs no gateway") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a stale merged tree", func(t *testing.T) {
		fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n", echoWebapp)
		b, _ := os.ReadFile(filepath.Join(fl, LockFile))
		write(t, filepath.Join(fl, LockFile), string(b)+"# edited\n")
		withFakeBuilds(t, &fakeBuilds{}, "linux")
		_, err := Gateway(fl, out, GatewayOptions{})
		if err == nil || !strings.Contains(err.Error(), "tacplus fleet build") {
			t.Fatalf("got %v, want the merged tree refused as not this lock's", err)
		}
	})
	t.Run("no build image named", func(t *testing.T) {
		fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n", echoWebapp)
		withFakeBuilds(t, &fakeBuilds{}, "linux")
		t.Setenv("TACPLUS_BUILD_IMAGE", "")
		t.Setenv("CI_JOB_IMAGE", "ghcr.io/tacplus-dev/tacplus/build:2.6")
		_, err := Gateway(fl, out, GatewayOptions{})
		if err == nil || !strings.Contains(err.Error(), "TACPLUS_BUILD_IMAGE") {
			t.Fatalf("got %v, want the build image asked for by digest", err)
		}
	})
}

// A base image by tag is not an input the lock can hold: refused.
func TestBaseImagesMustBePinnedByDigest(t *testing.T) {
	got, err := baseImages("FROM rust:1 AS build\nFROM debian:trixie-slim@sha256:" + strings.Repeat("a", 64) + "\nCOPY --from=build /x /y\n")
	if err == nil || !strings.Contains(err.Error(), "rust:1") {
		t.Fatalf("got %v, %v; want rust:1 refused", got, err)
	}
	got, err = baseImages(gatewayDockerfile)
	if err != nil || len(got) != 2 || !strings.HasPrefix(got[0], "debian:") || !strings.HasPrefix(got[1], "ghcr.io/grpc-ecosystem/") {
		t.Fatalf("got %v, %v", got, err)
	}
}

// A tree's digest is its files' paths, modes and contents, nothing else.
func TestTreeDigestIsContentOnly(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		write(t, filepath.Join(d, "x", "y.txt"), "y\n")
		write(t, filepath.Join(d, "z.txt"), "z\n")
	}
	da, _ := treeDigest(a)
	db, _ := treeDigest(b)
	if da != db || len(da) != 64 {
		t.Fatalf("%s != %s", da, db)
	}
	write(t, filepath.Join(b, "z.txt"), "Z\n")
	if db, _ = treeDigest(b); da == db {
		t.Fatal("a changed file must change the digest")
	}
}

// The gateway's image is the one image the fleet builds, so fleet build asks
// no registry for it; it needs the launcher pinned, and keeps the gateway
// section of a lock whose inputs did not move.
func TestBuildLocksTheGatewayWithoutAReleaseImage(t *testing.T) {
	old := releaseImage
	releaseImage = func(repo, version string) (string, error) {
		if strings.Contains(repo, "gateway") {
			return "", fmt.Errorf("ghcr.io/test/gateway:%s is not published", version)
		}
		return fakeImage(repo, version)
	}
	t.Cleanup(func() { releaseImage = old })
	fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n", echoWebapp)
	lock, err := ReadLock(fl)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Services[0].Name != "gateway" || lock.Services[0].Image != "" {
		t.Fatalf("gateway lock entry = %+v", lock.Services[0])
	}
	if lock.Launcher == nil || lock.Launcher.Version != "0.1.0" || len(lock.Launcher.Commit) != 40 {
		t.Fatalf("launcher = %+v", lock.Launcher)
	}
	report, err := Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	if l := report[len(report)-1]; l.Name != launcherName || l.Locked != "0.1.0" || l.Stale() {
		t.Fatalf("--check reports the launcher's line too: %+v", report)
	}

	withFakeBuilds(t, &fakeBuilds{}, "linux")
	if _, err := Gateway(fl, out, GatewayOptions{}); err != nil {
		t.Fatal(err)
	}
	built, _ := os.ReadFile(filepath.Join(fl, LockFile))
	if _, err := Build(fl, out); err != nil {
		t.Fatal(err)
	}
	if relocked, _ := os.ReadFile(filepath.Join(fl, LockFile)); string(relocked) != string(built) {
		t.Fatalf("a re-lock that moves nothing keeps the gateway section:\n%s\nvs\n%s", built, relocked)
	}

	m, _ := os.ReadFile(filepath.Join(fl, ManifestFile))
	write(t, filepath.Join(fl, ManifestFile), strings.Split(string(m), "launcher:")[0])
	if _, err := Build(fl, out); err == nil || !strings.Contains(err.Error(), "launcher") {
		t.Fatalf("got %v, want a fleet with a gateway to pin its launcher", err)
	}
}

// The gateway's own webapp is the merged webapp's base, never a module.
func TestBuildRefusesAWebappContributionFromTheGateway(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	gw := contributingService(t, root, "gateway", 1, reg, "  webapp: {dir: webapp, title: Gateway}\n",
		map[string]string{"webapp/package.json": "{}\n", "webapp/package-lock.json": "{}\n"})
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: gateway, id: 1, contract: "+gw+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: gateway, version: \"0.1\", repo: "+gw+"}\n")
	if _, err := Build(fl, filepath.Join(root, "out")); err == nil || !strings.Contains(err.Error(), "base at /") {
		t.Fatalf("got %v, want the gateway's webapp contribution refused", err)
	}
}
