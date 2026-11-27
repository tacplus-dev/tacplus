// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// mainDigest is a fixture image digest.
var mainDigest = "sha256:" + strings.Repeat("ab", 32)

// mainImage is the image a main release of name at commit published, as the
// release record names it.
func mainImage(name, commit string) string {
	return "us-docker.pkg.dev/p/tacplus/" + name + ":main-" + commit[:12] + "@" + mainDigest
}

// commitOnMain commits a change to beta after its tags, as main moves on
// past a release: beta's contract gains a field, it depends on alpha, and it
// contributes a webapp. It returns the new, untagged commit.
func commitOnMain(t *testing.T, beta string) string {
	t.Helper()
	dir := strings.TrimPrefix(beta, "file://")
	write(t, filepath.Join(dir, "tacplus.yaml"), "language: go\nsrc: proto\noutput-dir: model\nkind: service\ndepends:\n  - alpha\ncontribute:\n  webapp: {dir: web, title: Beta}\n")
	write(t, filepath.Join(dir, "web", "package.json"), "{}\n")
	write(t, filepath.Join(dir, "web", "package-lock.json"), "{}\n")
	proto := filepath.Join(dir, "proto", "services", "beta.proto")
	data, err := os.ReadFile(proto)
	if err != nil {
		t.Fatal(err)
	}
	write(t, proto, strings.Replace(string(data), "string m = 1; }", "string m = 1; string on_main = 2; }", 1))
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "on main")
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// A service releases.yaml names is locked at its main release: everything is
// read at the commit, the image is the record's, and the others lock their
// lines as before.
func TestBuildLocksAServiceAtAMainRelease(t *testing.T) {
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2")
	before, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	tagged := before.Lock.Services[1]

	commit := commitOnMain(t, beta)
	image := mainImage("beta", commit)
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \""+image+"\"}\n")
	releaseImage = func(repo, version string) (string, error) {
		if repo == beta {
			t.Errorf("releaseImage(%s, %s) called for a service locked at a main release", repo, version)
		}
		return fakeImage(repo, version)
	}
	t.Cleanup(func() { releaseImage = fakeImage })

	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	// alpha stays on its line; its inputs digest moves with the contract of
	// beta, which it depends on.
	a, was := res.Lock.Services[0], before.Lock.Services[0]
	if a.InputsDigest == was.InputsDigest {
		t.Errorf("alpha's inputs digest %s must move with beta's contract", a.InputsDigest)
	}
	a.InputsDigest, was.InputsDigest = "", ""
	if !reflect.DeepEqual(a, was) {
		t.Errorf("alpha lock = %+v, want it on its line as before: %+v", a, was)
	}
	b := res.Lock.Services[1]
	if b.Version != "main-"+commit[:12] || b.Commit != commit || b.Image != image || !b.Pinned {
		t.Fatalf("beta lock = %+v, want main-%s at %s with %s, pinned", b, commit[:12], commit, image)
	}
	if b.ContractCommit != commit {
		t.Errorf("beta's contract commit = %s, want %s: its contract rides its commit", b.ContractCommit, commit)
	}
	if !reflect.DeepEqual(b.Depends, []string{"alpha"}) {
		t.Errorf("beta depends = %v, want [alpha], read at its commit", b.Depends)
	}
	wantTree := gitIn(t, strings.TrimPrefix(beta, "file://"), "rev-parse", commit+":web")
	if b.Contribute == nil || b.Contribute.Webapp == nil || b.Contribute.Webapp.Tree != wantTree {
		t.Errorf("beta contributes %+v, want its webapp at tree %s", b.Contribute, wantTree)
	}
	if len(b.InputsDigest) != 64 || b.InputsDigest == tagged.InputsDigest {
		t.Errorf("beta inputs digest = %q, want one from its commit's contract (the tag's was %q)", b.InputsDigest, tagged.InputsDigest)
	}
	merged, err := os.ReadFile(filepath.Join(res.ProtoDir, "services", "beta.proto"))
	if err != nil || !strings.Contains(string(merged), "on_main") {
		t.Fatalf("build/proto's beta.proto must be the commit's: %s, %v", merged, err)
	}

	lock, err := os.ReadFile(filepath.Join(fl, LockFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"version: main-" + commit[:12], "commit: " + commit, "image: " + image, "pinned: true"} {
		if !strings.Contains(string(lock), want) {
			t.Errorf("%s lacks %q:\n%s", LockFile, want, lock)
		}
	}
	if strings.Count(string(lock), "pinned:") != 1 {
		t.Errorf("only beta is pinned:\n%s", lock)
	}
}

// Under images: source the fleet builds every image but a main release's,
// whose image the record gives.
func TestBuildRecordsAMainReleaseImageInASourceBuiltFleet(t *testing.T) {
	releaseImage = func(repo, version string) (string, error) {
		t.Errorf("releaseImage(%s, %s) called for a source-built fleet", repo, version)
		return "", errors.New("not published")
	}
	t.Cleanup(func() { releaseImage = fakeImage })
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2")
	manifest := filepath.Join(fl, ManifestFile)
	data, _ := os.ReadFile(manifest)
	write(t, manifest, "images: source\n"+string(data))
	commit := commitOnMain(t, beta)
	image := mainImage("beta", commit)
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - name: beta\n    commit: "+commit+"\n    image: "+image+"\n")

	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := res.Lock.Services[0], res.Lock.Services[1]; a.Image != "" || a.Pinned || b.Image != image || !b.Pinned {
		t.Fatalf("alpha %+v, beta %+v: want alpha source-built, beta at its record's image", a, b)
	}
}

// A dependent's floor on a service at a main release is checked against the
// line fleet.yaml states for it.
func TestBuildChecksAFloorOnAMainReleaseAgainstItsStatedLine(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := releasedService(t, root, "alpha", 101, reg, []string{"beta@1.3"}, "v1.0.0")
	beta := releasedService(t, root, "beta", 102, reg, nil, betaTags...)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alpha+"}\n  - {name: beta, version: \"1.2\", repo: "+beta+"}\n")
	commit := commitOfTag(t, beta, "v1.2.9")
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \"r/beta@"+mainDigest+"\"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), `needs "beta" at 1.3`) {
		t.Fatalf("err = %v, want beta's stated line 1.2 below alpha's floor 1.3", err)
	}

	manifest := filepath.Join(fl, ManifestFile)
	data, _ := os.ReadFile(manifest)
	write(t, manifest, strings.Replace(string(data), `version: "1.2"`, `version: "1.3"`, 1))
	if _, err := Build(fl, filepath.Join(root, "out")); err != nil {
		t.Fatalf("beta stated at 1.3 meets the floor: %v", err)
	}
}

// A commit the service's repo does not have is refused by name.
func TestBuildRefusesAMainReleaseAtAnUnknownCommit(t *testing.T) {
	root := t.TempDir()
	fl, _ := twoServiceFleet(t, root, "1.2")
	commit := strings.Repeat("0123456789", 4)
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \"r/beta@"+mainDigest+"\"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), `service "beta"`) || !strings.Contains(err.Error(), commit) {
		t.Fatalf("err = %v, want a refusal naming beta and %s", err, commit)
	}
	if _, statErr := os.Stat(filepath.Join(fl, LockFile)); !os.IsNotExist(statErr) {
		t.Errorf("a refused build must not write %s", LockFile)
	}
}

// releases.yaml takes a service the fleet runs, once, at a full commit and
// an image by digest whose tag, when it has one, is that commit's.
func TestReadReleases(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ManifestFile), "registries: [r]\nservices:\n  - {name: alpha, repo: x, version: \"0.4\"}\n  - {name: beta, repo: y, version: \"1.2\"}\n")
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ReadReleases(dir, m); err != nil || got != nil {
		t.Fatalf("no %s: %v, %v; want none", ReleasesFile, got, err)
	}

	commit := strings.Repeat("0123456789abcdef", 3)[:40]
	for _, image := range []string{
		"us-docker.pkg.dev/p/tacplus/beta:main-" + commit[:12] + "@" + mainDigest,
		"us-docker.pkg.dev/p/tacplus/beta@" + mainDigest,
		"localhost:5000/beta@" + mainDigest,
	} {
		write(t, filepath.Join(dir, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \""+image+"\"}\n")
		got, err := ReadReleases(dir, m)
		if err != nil {
			t.Errorf("%s: %v", image, err)
			continue
		}
		if want := map[string]Release{"beta": {Name: "beta", Commit: commit, Image: image}}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: releases = %+v, want %+v", image, got, want)
		}
	}

	good := "  - {name: beta, commit: " + commit + ", image: \"r/beta@" + mainDigest + "\"}\n"
	for _, tc := range []struct{ releases, want string }{
		{"  - {name: gamma, commit: " + commit + ", image: \"r/gamma@" + mainDigest + "\"}\n", `"gamma", which fleet.yaml does not run`},
		{good + good, `names "beta" twice`},
		{"  - {name: beta, commit: " + commit[:12] + ", image: \"r/beta@" + mainDigest + "\"}\n", "40 hex"},
		{"  - {name: beta, commit: " + strings.ToUpper(commit) + ", image: \"r/beta@" + mainDigest + "\"}\n", "40 hex"},
		{"  - {name: beta, image: \"r/beta@" + mainDigest + "\"}\n", "40 hex"},
		{"  - {name: beta, commit: " + commit + ", image: \"r/beta:main-" + commit[:12] + "\"}\n", "by digest"},
		{"  - {name: beta, commit: " + commit + ", image: \"r/beta@sha256:abc\"}\n", "by digest"},
		{"  - {name: beta, commit: " + commit + "}\n", "by digest"},
		{"  - {name: beta, commit: " + commit + ", image: \"r/beta:main-0000000000ab@" + mainDigest + "\"}\n", "main-" + commit[:12]},
		{"  - {name: beta, commit: " + commit + ", image: \"r/beta:v1.2.9@" + mainDigest + "\"}\n", "main-" + commit[:12]},
	} {
		write(t, filepath.Join(dir, ReleasesFile), "releases:\n"+tc.releases)
		if _, err := ReadReleases(dir, m); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ReleasesFile) {
			t.Errorf("%s: err = %v, want %q", tc.releases, err, tc.want)
		}
	}
}

// --check reports a service at a main release as pinned, never as behind its
// line, and stale only when fleet.lock holds another release than the record.
func TestCheckReportsAMainReleaseAsPinned(t *testing.T) {
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2")
	commit := commitOnMain(t, beta)
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \""+mainImage("beta", commit)+"\"}\n")
	if _, err := Build(fl, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	release(t, strings.TrimPrefix(beta, "file://"), "v1.2.11")
	report, err := Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	if b := report[1]; b.Pinned != "main-"+commit[:12] || b.Locked != b.Pinned || b.Patch != "" || b.Minor != "" || b.Stale() {
		t.Fatalf("check = %+v, want beta pinned at main-%s and up to date", b, commit[:12])
	}
	if report[0].Pinned != "" || report[0].Stale() {
		t.Fatalf("alpha = %+v, want it on its line and up to date", report[0])
	}

	// The record moves on: the lock is stale until the fleet re-locks.
	next := gitIn(t, strings.TrimPrefix(beta, "file://"), "rev-parse", "HEAD")
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+next+", image: \""+mainImage("beta", next)+"\"}\n")
	if report, err = Check(fl); err != nil || !report[1].Stale() {
		t.Fatalf("check after the record moved = %+v, %v; want beta stale", report, err)
	}
	// So is a new image of the same commit.
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n  - {name: beta, commit: "+commit+", image: \"r/beta@sha256:"+strings.Repeat("cd", 32)+"\"}\n")
	if report, err = Check(fl); err != nil || !report[1].Stale() {
		t.Fatalf("check after the image moved = %+v, %v; want beta stale", report, err)
	}
	// Dropping beta from releases.yaml returns it to its line.
	if err := os.Remove(filepath.Join(fl, ReleasesFile)); err != nil {
		t.Fatal(err)
	}
	if report, err = Check(fl); err != nil || report[1].Pinned != "" || report[1].Patch != "1.2.11" || !report[1].Stale() {
		t.Fatalf("check without the record = %+v, %v; want beta back on 1.2, stale", report, err)
	}
}

// The gateway step checks out a service at a main release, and the gateway
// itself, at the commit the lock records: no tag names it. The gateway's
// image stays the one the fleet builds, never the record's.
func TestGatewayBuildsMainReleasesAtTheirCommits(t *testing.T) {
	fl, out := gatewayFleet(t, "  webapp: {dir: web, title: Echo}\n", echoWebapp)
	lock, err := ReadLock(fl)
	if err != nil {
		t.Fatal(err)
	}
	commits := map[string]string{}
	var releases string
	for _, s := range lock.Services {
		dir := strings.TrimPrefix(s.Repo, "file://")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "on main")
		commits[s.Name] = gitIn(t, dir, "rev-parse", "HEAD")
		releases += "  - {name: " + s.Name + ", commit: " + commits[s.Name] + ", image: \"" + mainImage(s.Name, commits[s.Name]) + "\"}\n"
	}
	write(t, filepath.Join(fl, ReleasesFile), "releases:\n"+releases)
	res, err := Build(fl, out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Lock.Services {
		want := ""
		if s.Name != GatewayService {
			want = mainImage(s.Name, commits[s.Name])
		}
		if !s.Pinned || s.Commit != commits[s.Name] || s.Image != want {
			t.Fatalf("%s lock = %+v, want pinned at %s with image %q", s.Name, s, commits[s.Name], want)
		}
	}
	if report, err := Check(fl); err != nil || report[0].Stale() || report[1].Stale() {
		t.Fatalf("check = %+v, %v; want both pinned and up to date", report, err)
	}

	withFakeBuilds(t, &fakeBuilds{}, "linux")
	if _, err := Gateway(fl, out, GatewayOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, want := range commits {
		if got := gitIn(t, filepath.Join(out, "gateway", "src", name), "rev-parse", "HEAD"); got != want {
			t.Errorf("%s built at %s, want its main release %s", name, got, want)
		}
	}
	lock, err = ReadLock(fl)
	if err != nil {
		t.Fatal(err)
	}
	if lg := lock.Gateway; lg == nil || lg.Version != "main-"+commits[GatewayService][:12] || lg.Commit != commits[GatewayService] {
		t.Fatalf("lock's gateway section = %+v", lg)
	}
}
