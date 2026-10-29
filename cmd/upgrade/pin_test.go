// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

const (
	digest260 = "sha256:2600000000000000000000000000000000000000000000000000000000000000"
	digest261 = "sha256:2610000000000000000000000000000000000000000000000000000000000000"
	digest270 = "sha256:2700000000000000000000000000000000000000000000000000000000000000"
)

// fakeForge serves the GitHub releases API and release downloads for tags, and
// a ghcr-style registry holding a build image per tag in images (tag → digest).
// A release in noBinaries has no checksums.txt.
func fakeForge(t *testing.T, tags []string, images map[string]string, noBinaries ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+upgradeRepo+"/releases", func(w http.ResponseWriter, _ *http.Request) {
		var items []map[string]any
		for _, tag := range tags {
			items = append(items, map[string]any{"tag_name": tag, "draft": false})
		}
		_ = json.NewEncoder(w).Encode(items)
	})
	for _, tag := range tags {
		tag := tag
		skip := false
		for _, n := range noBinaries {
			skip = skip || n == tag
		}
		if skip {
			continue
		}
		mux.HandleFunc("/"+upgradeRepo+"/releases/download/"+tag+"/"+checksumsAsset, func(w http.ResponseWriter, _ *http.Request) {
			v := strings.Split(strings.TrimPrefix(tag, "v"), ".")
			fmt.Fprintf(w, "%s  tacplus-%s-%s-%s-linux-amd64\n", strings.Repeat("a", 64), v[0], v[1], v[2])
		})
	}
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "repository:"+pinImageRepo+":pull" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon"})
	})
	mux.HandleFunc("/v2/"+pinImageRepo+"/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer anon" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		d, ok := images[strings.TrimPrefix(r.URL.Path, "/v2/"+pinImageRepo+"/manifests/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// withForge points the release and registry lookups at srv.
func withForge(t *testing.T, srv *httptest.Server) {
	t.Helper()
	withServer(t, srv.URL)
	old := pinRegistryBase
	pinRegistryBase = srv.URL
	t.Cleanup(func() { pinRegistryBase = old })
}

// project writes a tacplus.yaml with the given floor ("" = none) and returns its directory.
func project(t *testing.T, floor string) string {
	t.Helper()
	dir := t.TempDir()
	yml := "language: go\n"
	if floor != "" {
		yml += "tacplus: \"" + floor + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(dir, tacplusyaml.Filename), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func image(tag, digest string) string {
	return pinImageName + ":" + tag + "@" + digest
}

func TestPin_FloorLineNewestPatch(t *testing.T) {
	// Newest first by date, as the API orders them: a 2.6 backport (2.6.1) was
	// cut after 2.7.0, and the highest patch wins over the API order.
	withForge(t, fakeForge(t, []string{"v2.6.1", "v2.7.0", "v2.6.0"},
		map[string]string{"2.6.0": digest260, "2.6.1": digest261, "2.7.0": digest270}))
	dir := project(t, "2.6")

	if err := pin(dir, ""); err != nil {
		t.Fatal(err)
	}
	got, err := tacplusyaml.ReadLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := tacplusyaml.Lock{Tacplus: "2.6.1", BuildImage: image("2.6.1", digest261)}
	if *got != want {
		t.Fatalf("lock = %+v, want %+v", *got, want)
	}
}

func TestPin_ExistingLockKeepsItsLine(t *testing.T) {
	withForge(t, fakeForge(t, []string{"v2.7.0", "v2.6.1", "v2.6.0"},
		map[string]string{"2.6.0": digest260, "2.6.1": digest261, "2.7.0": digest270}))
	// Floor 2.4: the pin is on 2.6 and moves within 2.6, not to 2.7.
	dir := project(t, "2.4")
	if err := tacplusyaml.WriteLock(dir, tacplusyaml.Lock{Tacplus: "2.6.0", BuildImage: image("2.6.0", digest260)}); err != nil {
		t.Fatal(err)
	}
	if err := pin(dir, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := tacplusyaml.ReadLock(dir)
	if got.Tacplus != "2.6.1" {
		t.Fatalf("pin moved to %s, want 2.6.1", got.Tacplus)
	}
}

func TestPin_VersionFlag(t *testing.T) {
	withForge(t, fakeForge(t, []string{"v2.7.0", "v2.6.1", "v2.6.0"},
		map[string]string{"2.6.0": digest260, "2.6.1": digest261, "2.7.0": digest270}))
	dir := project(t, "2.6")

	if err := pin(dir, "2.6.0"); err != nil {
		t.Fatal(err)
	}
	if got, _ := tacplusyaml.ReadLock(dir); got.Tacplus != "2.6.0" || got.BuildImage != image("2.6.0", digest260) {
		t.Fatalf("--version 2.6.0 pinned %+v", got)
	}
	if err := pin(dir, "2.7"); err != nil {
		t.Fatal(err)
	}
	if got, _ := tacplusyaml.ReadLock(dir); got.Tacplus != "2.7.0" {
		t.Fatalf("--version 2.7 pinned %s", got.Tacplus)
	}
}

func TestPin_Refusals(t *testing.T) {
	withForge(t, fakeForge(t, []string{"v2.6.2", "v2.6.1", "v2.6.0", "v2.5.0"},
		map[string]string{"2.6.0": digest260, "2.6.1": digest261, "2.5.0": digest260}, "v2.6.2"))
	cases := []struct {
		name, floor, version, wantErr string
	}{
		{"no floor, no lock, no version", "", "", "--version"},
		{"below the floor", "2.6", "2.5", "needs tacplus 2.6"},
		{"no such line", "2.6", "2.9", "no release"},
		{"no such release", "2.6", "2.6.7", "no release v2.6.7"},
		{"release without binaries", "2.6", "2.6.2", "checksums.txt"},
		{"no build image for the release", "", "2.5.0", ""}, // has one: control
		{"bad version", "2.6", "2", "MAJOR.MINOR"},
	}
	for _, c := range cases {
		dir := project(t, c.floor)
		err := pin(dir, c.version)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: pin = %v, want success", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: pin = %v, want error containing %q", c.name, err, c.wantErr)
		}
		if _, statErr := os.Stat(filepath.Join(dir, tacplusyaml.LockFilename)); statErr == nil {
			t.Errorf("%s: a refused pin wrote %s", c.name, tacplusyaml.LockFilename)
		}
	}
}

func TestPin_BuildImageNotPublished(t *testing.T) {
	withForge(t, fakeForge(t, []string{"v2.6.1", "v2.6.0"}, map[string]string{"2.6.0": digest260}))
	dir := project(t, "2.6")
	err := pin(dir, "")
	if err == nil || !strings.Contains(err.Error(), "build:2.6.1") {
		t.Fatalf("pin with no build:2.6.1 = %v, want an error naming it", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, tacplusyaml.LockFilename)); statErr == nil {
		t.Fatal("pin wrote a lock without a build image digest")
	}
}

func TestPin_NoProject(t *testing.T) {
	err := pin(t.TempDir(), "2.6")
	if err == nil || !strings.Contains(err.Error(), tacplusyaml.Filename) {
		t.Fatalf("pin outside a project = %v, want an error naming %s", err, tacplusyaml.Filename)
	}
}
