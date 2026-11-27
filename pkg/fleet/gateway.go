// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/fsroot"
	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// A fleet has one gateway, and its image is the one image the fleet builds
// (decision gateway-per-fleet, #195): the gateway's locked release, generated
// against the fleet's merged protos, with the gateway's own webapp at / and
// every contributed module and client (contribute.go) baked in. `tacplus fleet
// gateway` builds it from fleet.lock alone, never re-resolving: each part by
// its fixed recipe at its locked commit, the image with tacplus-image-build
// (the build image's rootless BuildKit) at the gateway commit's time with
// rewritten timestamps, so the image's digest depends only on its inputs. The
// lock's gateway: section records every input and output by digest, and
// --check rebuilds and fails on any that differs.
//
// Where each part builds: everything on Linux in the build image, but for the
// darwin/arm64 builds of the rust: and swift: recipes, which the macos runner
// builds natively (--clients-only there) and the Linux build takes from its
// output (--prebuilt).

// GatewayService is the registry name of a fleet's gateway.
const GatewayService = "gateway"

// LockedLauncher is the launcher every service image runs under
// (github.com/tacplus-dev/launcher), which the fleet builds into the gateway's
// image: fleet.yaml's launcher: line, resolved like a service's.
type LockedLauncher struct {
	Version string `yaml:"version"`
	Repo    string `yaml:"repo"`
	Commit  string `yaml:"commit"`
}

// LockedGateway is fleet.lock's gateway: section, what `tacplus fleet gateway`
// built: the gateway release it built from, the platform, the build image it
// ran in and the Dockerfile's base images (each by digest), the sha256 of
// every part, and the image's digest.
type LockedGateway struct {
	Version    string         `yaml:"version"`
	Commit     string         `yaml:"commit"`
	Platform   string         `yaml:"platform"`
	BuildImage string         `yaml:"build_image"`
	BaseImages []string       `yaml:"base_images"`
	Outputs    []LockedOutput `yaml:"outputs"`
	Image      string         `yaml:"image"`
}

// LockedOutput is one built part and its sha256 (a directory's is
// treeDigest's): binary (the gateway), launcher, webapp (the merged webapp),
// webapp/<service> (the gateway's own as webapp/gateway, each module), and
// client/<service>/<client>/<os>/<arch>.
type LockedOutput struct {
	Name   string `yaml:"name"`
	SHA256 string `yaml:"sha256"`
}

// GatewayOptions are tacplus fleet gateway's flags.
type GatewayOptions struct {
	// Check rebuilds and compares every digest with the lock's gateway:
	// section, changing nothing.
	Check bool
	// ClientsOnly builds only the clients this host builds into
	// <out>/gateway/clients (the macos runner's job), no image and no lock.
	ClientsOnly bool
	// Prebuilt is a clients directory another host built (the macos
	// runner's <out>/gateway/clients), for the targets this host does not.
	Prebuilt string
}

// Step is one command of a recipe: its directory, the environment it adds,
// and its arguments ("tacplus" is this binary).
type Step struct {
	Dir  string
	Env  []string
	Args []string
}

// runStep runs a recipe's step; a var so tests stand in for the toolchains.
var runStep = execStep

// hostOS and hostArch are the machine the recipes run on; vars for tests.
var hostOS, hostArch = runtime.GOOS, runtime.GOARCH

func execStep(s Step) error {
	name := s.Args[0]
	if name == "tacplus" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		name = self
	}
	cmd := exec.Command(name, s.Args[1:]...)
	cmd.Dir = s.Dir
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s (in %s): %w", strings.Join(s.Args, " "), s.Dir, err)
	}
	return nil
}

// buildImageRe is an image by digest.
var buildImageRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// appEntry is one module in the merged webapp's index, /-/apps.json.
type appEntry struct {
	Service string `json:"service"`
	Title   string `json:"title"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

// gatewayBuild is one run of the gateway step.
type gatewayBuild struct {
	lock     *Lock
	opts     GatewayOptions
	protoDir string // the fleet's merged protos
	work     string // <out>/gateway
	ctx      string // the image's build context
	clients  string // where the clients land
	outputs  []LockedOutput
}

// Gateway runs `tacplus fleet gateway` for the deployment repo in manifestDir,
// whose merged protos `tacplus fleet build` wrote under outDir/proto. It
// builds in outDir/gateway (the image as outDir/gateway/image.tar, an OCI
// layout) and writes the lock's gateway: section, or with Check compares it.
func Gateway(manifestDir, outDir string, opts GatewayOptions) (*LockedGateway, error) {
	lock, err := ReadLock(manifestDir)
	if err != nil {
		return nil, fmt.Errorf("fleet: %w; run tacplus fleet build first", err)
	}
	var gw *LockedService
	for i := range lock.Services {
		if lock.Services[i].Name == GatewayService {
			gw = &lock.Services[i]
		}
	}
	if gw == nil {
		return nil, fmt.Errorf("fleet: %s runs no gateway: list the %q service in %s; its image is the one the fleet builds", LockFile, GatewayService, ManifestFile)
	}
	out, err := filepath.Abs(outDir)
	if err != nil {
		return nil, err
	}
	b := &gatewayBuild{lock: lock, opts: opts, protoDir: filepath.Join(out, "proto"), work: filepath.Join(out, "gateway")}
	b.ctx = filepath.Join(b.work, "context")
	b.clients = filepath.Join(b.ctx, "clients")
	if opts.ClientsOnly {
		b.clients = filepath.Join(b.work, "clients")
	}

	// The merged tree must be the one this lock was written with: the
	// gateway and every part are generated from it.
	mine, err := os.ReadFile(filepath.Join(manifestDir, LockFile))
	if err != nil {
		return nil, err
	}
	if theirs, err := os.ReadFile(filepath.Join(b.protoDir, LockFile)); err != nil || !bytes.Equal(mine, theirs) {
		return nil, fmt.Errorf("fleet: %s is not the merged tree of %s: run tacplus fleet build (--out %s) first", b.protoDir, filepath.Join(manifestDir, LockFile), outDir)
	}

	var buildImage string
	if !opts.ClientsOnly {
		if hostOS != "linux" {
			return nil, fmt.Errorf("fleet: the gateway image builds on Linux, in the build image; on %s, build its clients only (--clients-only)", hostOS)
		}
		if lock.Launcher == nil {
			return nil, fmt.Errorf("fleet: %s pins no launcher: add launcher: to %s and run tacplus fleet build", LockFile, ManifestFile)
		}
		buildImage = os.Getenv("TACPLUS_BUILD_IMAGE")
		if buildImage == "" {
			buildImage = os.Getenv("CI_JOB_IMAGE")
		}
		if !buildImageRe.MatchString(buildImage) {
			return nil, fmt.Errorf("fleet: name the build image this runs in, by digest, as TACPLUS_BUILD_IMAGE (ghcr.io/tacplus-dev/tacplus/build@sha256:...): the lock records it as an input (got %q)", buildImage)
		}
	}
	if opts.Prebuilt != "" {
		if opts.Prebuilt, err = filepath.Abs(opts.Prebuilt); err != nil {
			return nil, err
		}
		if strings.HasPrefix(opts.Prebuilt+string(filepath.Separator), b.work+string(filepath.Separator)) {
			return nil, fmt.Errorf("fleet: --prebuilt %s is inside %s, which the build replaces", opts.Prebuilt, b.work)
		}
		b.opts.Prebuilt = opts.Prebuilt
	}
	if err := os.RemoveAll(b.work); err != nil {
		return nil, err
	}
	for _, d := range []string{b.ctx, b.clients} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	if opts.ClientsOnly {
		return nil, b.contributions(false)
	}
	res, err := b.image(gw, buildImage)
	if err != nil {
		return nil, err
	}
	if opts.Check {
		if lock.Gateway == nil {
			return nil, fmt.Errorf("fleet: %s has no gateway: section to check: run tacplus fleet gateway and commit it", LockFile)
		}
		if diffs := diffGateway(lock.Gateway, res); len(diffs) > 0 {
			return nil, fmt.Errorf("fleet: the gateway rebuilt differently from %s:\n  %s", LockFile, strings.Join(diffs, "\n  "))
		}
		return res, nil
	}
	lock.Gateway = res
	for _, p := range []string{filepath.Join(manifestDir, LockFile), filepath.Join(b.protoDir, LockFile)} {
		if err := writeLock(lock, p); err != nil {
			return nil, err
		}
	}
	zap.L().Info("fleet gateway built", zap.String("image", res.Image), zap.String("oci", filepath.Join(b.work, "image.tar")))
	return res, nil
}

// image builds every part into the context, then the image from it.
func (b *gatewayBuild) image(gw *LockedService, buildImage string) (*LockedGateway, error) {
	gwDir := filepath.Join(b.work, "src", GatewayService)
	if err := checkout(gw.Repo, gw.ref(), gw.Commit, gwDir); err != nil {
		return nil, fmt.Errorf("fleet: gateway: %w", err)
	}
	epoch, err := commitTime(gwDir)
	if err != nil {
		return nil, err
	}
	dockerfile, err := os.ReadFile(filepath.Join(gwDir, "Dockerfile"))
	if err != nil {
		return nil, fmt.Errorf("fleet: gateway v%s: %w", gw.Version, err)
	}
	bases, err := baseImages(string(dockerfile))
	if err != nil {
		return nil, err
	}

	// The gateway, generated against the fleet's protos.
	if err := runStep(b.generate(gwDir)); err != nil {
		return nil, err
	}
	if err := runStep(Step{Dir: gwDir, Env: append(rustEnv(gwDir, GatewayService), epoch), Args: []string{"cargo", "build", "--release", "--locked"}}); err != nil {
		return nil, err
	}
	if err := b.place(filepath.Join(gwDir, "target", "release", "gateway"), "target/release/gateway", "binary"); err != nil {
		return nil, err
	}

	// The merged webapp: the gateway's own at /, each module under
	// /-/apps/<service>/, and their index.
	dist, err := b.webapp(filepath.Join(gwDir, "webapp"), "/", epoch)
	if err != nil {
		return nil, fmt.Errorf("fleet: the gateway's webapp: %w", err)
	}
	if err := b.place(dist, "webapp-dist", "webapp/"+GatewayService); err != nil {
		return nil, err
	}
	if err := b.contributions(true); err != nil {
		return nil, err
	}
	sum, err := treeDigest(filepath.Join(b.ctx, "webapp-dist"))
	if err != nil {
		return nil, err
	}
	b.outputs = append(b.outputs, LockedOutput{Name: "webapp", SHA256: sum})

	// The launcher, and the customer web app the fleet does not have.
	l := b.lock.Launcher
	lDir := filepath.Join(b.work, "src", "launcher")
	if err := checkout(l.Repo, l.Version, l.Commit, lDir); err != nil {
		return nil, fmt.Errorf("fleet: launcher: %w", err)
	}
	launcher := filepath.Join(b.ctx, "launcher-linux-"+hostArch)
	if err := runStep(goStep(lDir, ".", "linux", hostArch, launcher)); err != nil {
		return nil, err
	}
	if sum, err = fileDigest(launcher); err != nil {
		return nil, err
	}
	b.outputs = append(b.outputs, LockedOutput{Name: "launcher", SHA256: sum})
	if err := os.MkdirAll(filepath.Join(b.ctx, "web-dist"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(b.ctx, "Dockerfile"), dockerfile, 0o644); err != nil {
		return nil, err
	}

	platform := "linux/" + hostArch
	meta := filepath.Join(b.work, "image.json")
	if err := runStep(Step{Dir: b.work, Env: []string{epoch}, Args: []string{"tacplus-image-build",
		"--platform", platform,
		"--build-arg", "VERSION=" + gw.Version, "--build-arg", "VCS_REF=" + gw.Commit,
		"--output", "type=oci,dest=" + filepath.Join(b.work, "image.tar") + ",rewrite-timestamp=true",
		"--metadata-file", meta, b.ctx}}); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(meta)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("fleet: %s: %w", meta, err)
	}
	digest, _ := m["containerimage.digest"].(string)
	if !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("fleet: %s records no image digest", meta)
	}
	sort.Slice(b.outputs, func(i, j int) bool { return b.outputs[i].Name < b.outputs[j].Name })
	return &LockedGateway{Version: gw.Version, Commit: gw.Commit, Platform: platform, BuildImage: buildImage,
		BaseImages: bases, Outputs: b.outputs, Image: digest}, nil
}

// contributions builds every service's module (with webapps) and the clients
// this host builds, taking the others from --prebuilt (with webapps, the
// image's build, which needs every one).
func (b *gatewayBuild) contributions(webapps bool) error {
	services := append([]LockedService(nil), b.lock.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	index := []appEntry{}
	for _, s := range services {
		c := s.Contribute
		if c == nil || (!webapps && len(c.Clients) == 0) {
			continue
		}
		dir := filepath.Join(b.work, "src", s.Name)
		if err := checkout(s.Repo, s.ref(), s.Commit, dir); err != nil {
			return fmt.Errorf("fleet: service %q: %w", s.Name, err)
		}
		epoch, err := commitTime(dir)
		if err != nil {
			return err
		}
		if w := c.Webapp; w != nil && webapps {
			if tree := treeOf(dir, w.Dir); tree != w.Tree {
				return fmt.Errorf("fleet: service %q: %s is tree %s at %s, but %s locked %s", s.Name, w.Dir, tree, s.Commit, LockFile, w.Tree)
			}
			dist, err := b.webapp(filepath.Join(dir, filepath.FromSlash(w.Dir)), w.Path, epoch)
			if err != nil {
				return fmt.Errorf("fleet: service %q's webapp module: %w", s.Name, err)
			}
			if err := b.place(dist, "webapp-dist"+w.Path, "webapp/"+s.Name); err != nil {
				return err
			}
			index = append(index, appEntry{Service: s.Name, Title: w.Title, Version: s.Version, Path: w.Path})
		}
		generated := false
		for _, cl := range c.Clients {
			for _, target := range cl.Targets {
				goos, goarch, _ := strings.Cut(target, "/")
				file := cl.Name + "-" + goos + "-" + goarch
				if goos == "windows" {
					file += ".exe"
				}
				dst := filepath.Join(b.clients, s.Name, cl.Name, file)
				name := "client/" + s.Name + "/" + cl.Name + "/" + target
				if builder := builderOS(cl.Recipe, goos); builder != hostOS {
					if !webapps {
						continue
					}
					if err := b.prebuilt(s.Name, cl.Name, file, dst, name, builder); err != nil {
						return err
					}
					continue
				}
				if !generated {
					if err := runStep(b.generate(dir)); err != nil {
						return err
					}
					generated = true
				}
				if err := b.client(dir, s.Name, cl, goos, goarch, epoch, dst); err != nil {
					return fmt.Errorf("fleet: service %q client %q (%s): %w", s.Name, cl.Name, target, err)
				}
				sum, err := fileDigest(dst)
				if err != nil {
					return err
				}
				b.outputs = append(b.outputs, LockedOutput{Name: name, SHA256: sum})
			}
		}
	}
	if !webapps {
		return nil
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	idx := filepath.Join(b.ctx, "webapp-dist", filepath.FromSlash(strings.TrimPrefix(AppsIndex, "/")))
	if err := os.MkdirAll(filepath.Dir(idx), 0o755); err != nil {
		return err
	}
	return os.WriteFile(idx, append(data, '\n'), 0o644)
}

// prebuilt takes a client another host built from --prebuilt.
func (b *gatewayBuild) prebuilt(service, client, file, dst, name, builder string) error {
	runner := map[string]string{"darwin": "the macos runner"}[builder]
	if runner == "" {
		runner = "a " + builder + " host"
	}
	src := filepath.Join(b.opts.Prebuilt, service, client, file)
	if b.opts.Prebuilt == "" {
		return fmt.Errorf("fleet: service %q client %q: %s is built on %s (tacplus fleet gateway --clients-only there); pass its clients directory as --prebuilt", service, client, file, runner)
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("fleet: service %q client %q: --prebuilt has no %s, which %s builds (tacplus fleet gateway --clients-only)", service, client, path.Join(service, client, file), runner)
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	sum, err := fileDigest(dst)
	if err != nil {
		return err
	}
	b.outputs = append(b.outputs, LockedOutput{Name: name, SHA256: sum})
	return nil
}

// builderOS is the host a recipe's target builds on: darwin's rust: and
// swift: builds natively on the macos runner, everything else on Linux (Go
// and .NET cross-compile; Rust through cargo-zigbuild).
func builderOS(recipe, goos string) string {
	if goos == "darwin" && (recipe == tacplusyaml.RecipeRust || recipe == tacplusyaml.RecipeSwift) {
		return "darwin"
	}
	return "linux"
}

// rustTriples are the Rust targets of the fixed os/arch set: static musl for
// Linux, mingw for Windows.
var rustTriples = map[string]string{
	"linux/amd64":   "x86_64-unknown-linux-musl",
	"linux/arm64":   "aarch64-unknown-linux-musl",
	"windows/amd64": "x86_64-pc-windows-gnu",
	"darwin/arm64":  "aarch64-apple-darwin",
}

// client builds one client target by its recipe into dst.
func (b *gatewayBuild) client(dir, service string, cl LockedClient, goos, goarch, epoch, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var built string
	switch cl.Recipe {
	case tacplusyaml.RecipeGo:
		return runStep(goStep(dir, cl.Source, goos, goarch, dst))
	case tacplusyaml.RecipeRust:
		triple := rustTriples[goos+"/"+goarch]
		args := []string{"cargo", "zigbuild", "--release", "--locked", "--bin", cl.Source, "--target", triple}
		if hostOS == "darwin" {
			args = []string{"cargo", "build", "--release", "--locked", "--bin", cl.Source, "--target", triple}
		}
		if err := runStep(Step{Dir: dir, Env: append(rustEnv(dir, service), epoch), Args: args}); err != nil {
			return err
		}
		built = filepath.Join(dir, "target", triple, "release", cl.Source)
		if goos == "windows" {
			built += ".exe"
		}
	case tacplusyaml.RecipeSwift:
		if err := runStep(Step{Dir: dir, Env: []string{epoch}, Args: []string{"swift", "build", "-c", "release", "--arch", "arm64", "--product", cl.Source,
			"-Xswiftc", "-file-prefix-map=" + dir + "=/build/" + service}}); err != nil {
			return err
		}
		built = filepath.Join(dir, ".build", "arm64-apple-macosx", "release", cl.Source)
	case tacplusyaml.RecipeDotnet:
		pub := filepath.Join(b.work, "dotnet", service, cl.Name)
		if err := runStep(Step{Dir: dir, Env: []string{epoch, "DOTNET_CLI_TELEMETRY_OPTOUT=1"}, Args: []string{"dotnet", "publish", cl.Source,
			"-c", "Release", "-r", "win-x64", "--self-contained", "-p:PublishSingleFile=true", "-p:RestoreLockedMode=true",
			"-p:Deterministic=true", "-p:ContinuousIntegrationBuild=true", "-p:PathMap=" + dir + "=/build/" + service, "-o", pub}}); err != nil {
			return err
		}
		built = filepath.Join(pub, strings.TrimSuffix(path.Base(cl.Source), ".csproj")+".exe")
	default:
		return fmt.Errorf("no recipe %q", cl.Recipe)
	}
	return copyFile(built, dst)
}

// goStep is the go: recipe for one target.
func goStep(dir, pkg, goos, goarch, dst string) Step {
	return Step{Dir: dir, Env: []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch, "GOFLAGS=-mod=readonly"},
		Args: []string{"go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", dst, pkg}}
}

// rustEnv keeps a Rust build's paths out of its output: the checkout and the
// cargo home are remapped to fixed ones.
func rustEnv(dir, service string) []string {
	home := os.Getenv("CARGO_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".cargo")
	}
	return []string{"RUSTFLAGS=--remap-path-prefix=" + dir + "=/build/" + service + " --remap-path-prefix=" + home + "=/cargo"}
}

// generate is the step every part starts with: tacplus build against the
// fleet's merged protos.
func (b *gatewayBuild) generate(dir string) Step {
	return Step{Dir: dir, Args: []string{"tacplus", "build", "--src", b.protoDir}}
}

// webapp builds a webapp by the fixed recipe at base and returns its dist/.
func (b *gatewayBuild) webapp(dir, base, epoch string) (string, error) {
	if tacplusyaml.Exists(dir) {
		if err := runStep(b.generate(dir)); err != nil {
			return "", err
		}
	}
	if err := runStep(Step{Dir: dir, Env: []string{epoch}, Args: []string{"npm", "ci", "--ignore-scripts"}}); err != nil {
		return "", err
	}
	if err := runStep(Step{Dir: dir, Env: []string{"TACPLUS_WEB_BASE=" + base, epoch}, Args: []string{"npm", "run", "build"}}); err != nil {
		return "", err
	}
	dist := filepath.Join(dir, "dist")
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		return "", fmt.Errorf("npm run build wrote no dist/index.html in %s", dir)
	}
	return dist, nil
}

// place copies a built file or directory into the context at rel and records
// its digest as name.
func (b *gatewayBuild) place(src, rel, name string) error {
	dst := filepath.Join(b.ctx, filepath.FromSlash(rel))
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("fleet: %s: %w", name, err)
	}
	var sum string
	if fi.IsDir() {
		if err := copyTree(src, dst); err != nil {
			return err
		}
		sum, err = treeDigest(dst)
	} else {
		if err := copyFile(src, dst); err != nil {
			return err
		}
		sum, err = fileDigest(dst)
	}
	if err != nil {
		return err
	}
	b.outputs = append(b.outputs, LockedOutput{Name: name, SHA256: sum})
	return nil
}

// diffGateway lists what a rebuild changed against the lock.
func diffGateway(locked, built *LockedGateway) []string {
	var diffs []string
	field := func(name, a, b string) {
		if a != b {
			diffs = append(diffs, fmt.Sprintf("%s: locked %s, built %s", name, a, b))
		}
	}
	field("version", locked.Version, built.Version)
	field("commit", locked.Commit, built.Commit)
	field("platform", locked.Platform, built.Platform)
	field("build_image", locked.BuildImage, built.BuildImage)
	field("base_images", strings.Join(locked.BaseImages, " "), strings.Join(built.BaseImages, " "))
	was := map[string]string{}
	for _, o := range locked.Outputs {
		was[o.Name] = o.SHA256
	}
	for _, o := range built.Outputs {
		field(o.Name, was[o.Name], o.SHA256)
		delete(was, o.Name)
	}
	for name, sum := range was {
		field(name, sum, "(none)")
	}
	field("image", locked.Image, built.Image)
	sort.Strings(diffs)
	return diffs
}

// checkout clones repo at the locked commit into dst, resolving ref (a
// release's version, or a main release's commit: LockedService.ref) and
// refusing a tag that no longer points at it.
func checkout(repo, ref, commit, dst string) error {
	res, err := protosrc.Resolve(repo, ref)
	if err != nil {
		return err
	}
	defer res.Cleanup()
	if got := commitOf(res.Dir); got != commit {
		return fmt.Errorf("%s v%s is commit %s, but %s locked %s: the tag moved; re-lock with tacplus fleet build", repo, ref, got, LockFile, commit)
	}
	for _, args := range [][]string{{"clone", "-q", "--no-hardlinks", res.Dir, dst}, {"-C", dst, "checkout", "-q", "--detach", commit}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

// commitTime is SOURCE_DATE_EPOCH=<the checkout's commit time>.
func commitTime(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%ct").Output()
	if err != nil {
		return "", fmt.Errorf("fleet: the commit time of %s: %w", dir, err)
	}
	return "SOURCE_DATE_EPOCH=" + strings.TrimSpace(string(out)), nil
}

// baseImages lists a Dockerfile's base images (FROM, COPY --from), sorted,
// refusing one not pinned by digest: a tag is not a deterministic input.
func baseImages(dockerfile string) ([]string, error) {
	stages := map[string]bool{}
	seen := map[string]bool{}
	var out []string
	add := func(ref string) error {
		if ref == "scratch" || stages[strings.ToLower(ref)] || seen[ref] {
			return nil
		}
		if strings.Contains(ref, "$") || !strings.Contains(ref, "@sha256:") {
			return fmt.Errorf("fleet: the gateway's Dockerfile names base image %s by tag: pin it by digest (a tag is not a deterministic input)", ref)
		}
		seen[ref] = true
		out = append(out, ref)
		return nil
	}
	for _, line := range strings.Split(dockerfile, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "FROM":
			var ref string
			for _, a := range f[1:] {
				if !strings.HasPrefix(a, "--") {
					ref = a
					break
				}
			}
			if err := add(ref); err != nil {
				return nil, err
			}
			for i := 1; i+1 < len(f); i++ {
				if strings.EqualFold(f[i], "AS") {
					stages[strings.ToLower(f[i+1])] = true
				}
			}
		case "COPY", "ADD":
			for _, a := range f[1:] {
				if from, ok := strings.CutPrefix(a, "--from="); ok {
					if _, isIndex := parseIndex(from); !isIndex {
						if err := add(from); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func parseIndex(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, s != ""
}

// treeDigest is a directory's sha256 over its files' paths, kinds and
// contents, in path order: nothing else (no times, no owners).
func treeDigest(dir string) (string, error) {
	h := sha256.New()
	err := fsroot.Walk(dir, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := r.Readlink(rel)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l %s %s\n", target, name)
		default:
			f, err := r.Open(rel)
			if err != nil {
				return err
			}
			sum, err := readerDigest(f)
			f.Close()
			if err != nil {
				return err
			}
			kind := "f"
			if fi.Mode()&0o111 != 0 {
				kind = "x"
			}
			fmt.Fprintf(h, "%s %s %s\n", kind, sum, name)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// fileDigest is a file's sha256.
func fileDigest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return readerDigest(f)
}

// readerDigest is the sha256 of what r reads.
func readerDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies a file, keeping its execute bit.
func copyFile(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeCopy(dst, data, fi.Mode())
}

// writeCopy writes a copied file's data to dst, executable when mode is.
func writeCopy(dst string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	if err := os.WriteFile(dst, data, perm); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// copyTree copies a directory's files and symlinks into dst, reading the
// source through an os.Root so no symlink leads the copy out of it.
func copyTree(src, dst string) error {
	return fsroot.Walk(src, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			target, err := r.Readlink(rel)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		default:
			fi, err := r.Stat(rel)
			if err != nil {
				return err
			}
			data, err := r.ReadFile(rel)
			if err != nil {
				return err
			}
			return writeCopy(to, data, fi.Mode())
		}
	})
}
