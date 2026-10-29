// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A fixture service repo with several tags on several lines, pre-releases and
// a non-release tag among them.
var betaTags = []string{"v1.2.0", "v1.2.1", "v1.2.10-rc.1", "v1.2.9", "v1.3.0", "v1.4.0-rc.1", "notes"}

func TestResolveVersion(t *testing.T) {
	for _, tc := range []struct{ want, got, err string }{
		{"1.2", "1.2.9", ""},              // by number, not by name; pre-releases excluded
		{"1.3", "1.3.0", ""},              // another line
		{"1.4", "", "no release on line"}, // a line with only a pre-release
		{"2.0", "", "no release on line"}, // a line that does not exist
		{"1.2.10-rc.1", "1.2.10-rc.1", ""},
		{"1.4.0-rc.1", "1.4.0-rc.1", ""},
		{"1.4.0-rc.2", "", "no tag v1.4.0-rc.2"},
	} {
		got, err := resolveVersion(betaTags, tc.want)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("resolveVersion(%s) err = %v, want %q", tc.want, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.got {
			t.Errorf("resolveVersion(%s) = %q, %v; want %q", tc.want, got, err, tc.got)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	ordered := []string{"0.9.9", "1.2.0-alpha", "1.2.0-alpha.1", "1.2.0-alpha.beta", "1.2.0-beta.2", "1.2.0-beta.11", "1.2.0-rc.1", "1.2.0", "1.2.1", "1.10.0", "2.0.0"}
	for i := range ordered {
		for j := range ordered {
			a, _ := parseVersion(ordered[i])
			b, _ := parseVersion(ordered[j])
			got := a.compare(b)
			if (i < j && got >= 0) || (i > j && got <= 0) || (i == j && got != 0) {
				t.Errorf("compare(%s, %s) = %d", ordered[i], ordered[j], got)
			}
		}
	}
}

// twoServiceFleet is alpha (released v1.0.0) and beta (betaTags) under one
// registry, with beta at betaVersion.
func twoServiceFleet(t *testing.T, root, betaVersion string) (fleetDir, beta string) {
	t.Helper()
	reg := filepath.Join(root, "registry")
	alpha := releasedService(t, root, "alpha", 101, reg, []string{"beta@1.2"}, "v1.0.0")
	beta = releasedService(t, root, "beta", 102, reg, nil, betaTags...)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	return fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alpha+"}\n  - {name: beta, version: \""+betaVersion+"\", repo: "+beta+"}\n"), beta
}

// A pre-release runs only when the manifest names it, and the lock says so.
func TestBuildPinsAnExplicitPreRelease(t *testing.T) {
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2.10-rc.1")
	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	b := res.Lock.Services[1]
	if b.Version != "1.2.10-rc.1" || b.Commit != commitOfTag(t, beta, "v1.2.10-rc.1") {
		t.Fatalf("beta lock = %+v, want the pre-release v1.2.10-rc.1", b)
	}
	if want, _ := fakeImage(beta, "1.2.10-rc.1"); b.Image != want {
		t.Fatalf("beta image = %q, want %q", b.Image, want)
	}
}

// A line with no release, or a pre-release that was never tagged, is refused
// by name, never replaced by the default branch.
func TestBuildRefusesAnUnresolvableVersion(t *testing.T) {
	for _, v := range []string{"1.4", "1.5", "1.2.10-rc.2"} {
		root := t.TempDir()
		fl, _ := twoServiceFleet(t, root, v)
		_, err := Build(fl, filepath.Join(root, "out"))
		if err == nil || !strings.Contains(err.Error(), `service "beta"`) || !strings.Contains(err.Error(), v) {
			t.Errorf("version %s: err = %v, want a refusal naming beta and %s", v, err, v)
		}
		if _, statErr := os.Stat(filepath.Join(fl, LockFile)); !os.IsNotExist(statErr) {
			t.Errorf("version %s: a refused build must not write %s", v, LockFile)
		}
	}
}

// A release whose image was not published cannot run in a fleet.
func TestBuildRefusesAReleaseWithoutAnImage(t *testing.T) {
	releaseImage = func(repo, version string) (string, error) {
		if version == "1.2.9" {
			return "", errors.New("ghcr.io/test/beta:1.2.9 is not published")
		}
		return fakeImage(repo, version)
	}
	t.Cleanup(func() { releaseImage = fakeImage })
	root := t.TempDir()
	fl, _ := twoServiceFleet(t, root, "1.2")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), `service "beta": release v1.2.9`) || !strings.Contains(err.Error(), "not published") {
		t.Fatalf("err = %v", err)
	}
}

// A plain directory has no releases to resolve.
func TestBuildRefusesALocalDirectoryService(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := serviceRepoID(t, root, "alpha", 101, reg, nil)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alpha+"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v", err)
	}
}

// Re-locking takes a new patch of the stated line by itself, and a new minor
// only when the manifest names it; --check says which are waiting, and the
// lock written records versions a diff shows.
func TestRelockAndCheck(t *testing.T) {
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2")
	if _, err := Build(fl, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	lock, err := ReadLock(fl)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Services[1].Version != "1.2.9" {
		t.Fatalf("locked beta %s, want 1.2.9", lock.Services[1].Version)
	}
	data, _ := os.ReadFile(filepath.Join(fl, LockFile))
	for _, want := range []string{"version: 1.2.9", "image: ghcr.io/test/beta@sha256:", "commit: " + commitOfTag(t, beta, "v1.2.9"), "inputs_digest: "} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %q:\n%s", LockFile, want, data)
		}
	}

	report, err := Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	want := []Available{
		{Name: "alpha", Version: "1.0", Locked: "1.0.0"},
		{Name: "beta", Version: "1.2", Locked: "1.2.9", Minor: "1.3.0"},
	}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("check = %+v, want %+v", report, want)
	}

	// beta releases 1.2.11 and 1.5.0: check reports both, the patch as stale.
	release(t, strings.TrimPrefix(beta, "file://"), "v1.2.11", "v1.5.0")
	report, err = Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	if b := report[1]; b.Patch != "1.2.11" || b.Minor != "1.5.0" || !b.Stale() || report[0].Stale() {
		t.Fatalf("check after new releases = %+v", report)
	}

	// Re-locking moves beta to the new patch and not to the new minor.
	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if b := res.Lock.Services[1]; b.Version != "1.2.11" || b.Commit != commitOfTag(t, beta, "v1.2.11") {
		t.Fatalf("relocked beta = %+v, want 1.2.11", b)
	}
	if report, _ = Check(fl); report[1].Stale() || report[1].Minor != "1.5.0" {
		t.Fatalf("check after relock = %+v", report[1])
	}
}

// A pre-release pin is reported once its release (or a later patch) is out:
// the fleet should move back to the line. It is not stale: relocking keeps it.
func TestCheckReportsTheReleaseOfAPinnedPreRelease(t *testing.T) {
	root := t.TempDir()
	fl, beta := twoServiceFleet(t, root, "1.2.10-rc.1")
	if _, err := Build(fl, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	release(t, strings.TrimPrefix(beta, "file://"), "v1.2.10")
	report, err := Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	if b := report[1]; b.Patch != "1.2.10" || b.Stale() {
		t.Fatalf("check = %+v, want 1.2.10 reported and not stale", b)
	}
}

// With no fleet.lock yet, every service is stale: building would write one.
func TestCheckWithoutALock(t *testing.T) {
	root := t.TempDir()
	fl, _ := twoServiceFleet(t, root, "1.2")
	report, err := Check(fl)
	if err != nil {
		t.Fatal(err)
	}
	if b := report[1]; b.Locked != "" || b.Patch != "1.2.9" || !b.Stale() {
		t.Fatalf("check = %+v", b)
	}
}

func TestImageName(t *testing.T) {
	for _, tc := range []struct{ repo, want string }{
		{"https://github.com/tacplus-dev/kernel.git", "ghcr.io/tacplus-dev/kernel"},
		{"https://github.com/Acme/Our-Kernel", "ghcr.io/acme/our-kernel"},
		{"github.com/acme/kernel.git", "ghcr.io/acme/kernel"},
		{"git@github.com:acme/kernel.git", "ghcr.io/acme/kernel"},
		{"https://gitlab.com/acme/kernel.git", ""},
		{"https://github.com/acme", ""},
	} {
		got, err := imageName(tc.repo)
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("imageName(%s) = %q, %v; want %q", tc.repo, got, err, tc.want)
		}
	}
}

// The release image is looked up on the registry by the version's tag, with
// the GitHub token when one is set (a private package).
func TestGHCRReleaseImage(t *testing.T) {
	const digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if _, p, _ := r.BasicAuth(); p != "gh-secret" || r.URL.Query().Get("scope") != "repository:acme/kernel:pull" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "pull"})
	})
	mux.HandleFunc("/v2/acme/kernel/manifests/0.4.3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", digest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := imageRegistryBase
	imageRegistryBase = srv.URL
	t.Cleanup(func() { imageRegistryBase = old })
	t.Setenv("GH_TOKEN", "gh-secret")

	got, err := ghcrReleaseImage("https://github.com/acme/kernel.git", "0.4.3")
	if err != nil || got != "ghcr.io/acme/kernel@"+digest {
		t.Fatalf("ghcrReleaseImage = %q, %v", got, err)
	}
	_, err = ghcrReleaseImage("https://github.com/acme/kernel.git", "0.4.4")
	if err == nil || !strings.Contains(err.Error(), "ghcr.io/acme/kernel:0.4.4 is not published") {
		t.Fatalf("missing image err = %v", err)
	}
}
