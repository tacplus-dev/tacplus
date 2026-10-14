// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "test-token-abc"

// fakeGitHub serves the releases API and the release-asset downloads for one
// release tag holding checksums.txt and a single binary whose contents are body.
func fakeGitHub(t *testing.T, tag, fileName string, body []byte, sha256Hex string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+upgradeRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"tag_name":%q,"draft":false},{"tag_name":"v0.0.1","draft":true}]`, tag)
	})
	mux.HandleFunc("/"+upgradeRepo+"/releases/download/"+tag+"/"+checksumsAsset, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "deadbeef  other-file\n%s  %s\n", sha256Hex, fileName)
	})
	mux.HandleFunc("/"+upgradeRepo+"/releases/download/"+tag+"/"+fileName, func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withServer(t *testing.T, base string) {
	t.Helper()
	oldAPI, oldDL := upgradeAPIBase, upgradeDownloadBase
	upgradeAPIBase, upgradeDownloadBase = base, base
	t.Cleanup(func() { upgradeAPIBase, upgradeDownloadBase = oldAPI, oldDL })
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// leftoverTemps returns the names of files in dir left behind by a download attempt.
func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tacplus-upgrade-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestFetchReleases_SkipsDrafts(t *testing.T) {
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", []byte("x"), hashOf([]byte("x")))
	withServer(t, srv.URL)

	tags, err := fetchReleases("")
	if err != nil {
		t.Fatalf("fetchReleases: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1.1.3" {
		t.Fatalf("tags = %v, want [v1.1.3] (drafts excluded)", tags)
	}
}

func TestFetchChecksum(t *testing.T) {
	body := []byte("new binary contents")
	want := hashOf(body)
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", body, want)
	withServer(t, srv.URL)

	got, err := fetchChecksum("", "v1.1.3", "tacplus-1-1-3-linux-amd64")
	if err != nil {
		t.Fatalf("fetchChecksum: %v", err)
	}
	if got != want {
		t.Fatalf("sha256 = %q, want %q", got, want)
	}
}

func TestFetchChecksum_ReleaseNotFound(t *testing.T) {
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", []byte("x"), hashOf([]byte("x")))
	withServer(t, srv.URL)

	// v1.1.9 is not published: its checksums.txt does not exist.
	if _, err := fetchChecksum("", "v1.1.9", "tacplus-1-1-9-linux-amd64"); err == nil {
		t.Fatal("expected an error for a missing release")
	} else if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchChecksum_FileNotInRelease(t *testing.T) {
	body := []byte("x")
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", body, hashOf(body))
	withServer(t, srv.URL)

	if _, err := fetchChecksum("", "v1.1.3", "tacplus-1-1-3-darwin-arm64"); err == nil {
		t.Fatal("expected an error for a file absent from checksums.txt")
	} else if !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadReplace_ChecksumMatches(t *testing.T) {
	body := []byte("new binary contents")
	sum := hashOf(body)
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", body, sum)
	withServer(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := downloadReplace(assetURL("v1.1.3", "tacplus-1-1-3-linux-amd64"), testToken, dest, sum); err != nil {
		t.Fatalf("downloadReplace: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("dest contents = %q, want %q", got, body)
	}
	if temps := leftoverTemps(t, dir); len(temps) != 0 {
		t.Fatalf("temp files left behind: %v", temps)
	}
}

func TestDownloadReplace_ChecksumMismatch(t *testing.T) {
	body := []byte("tampered binary")
	// The server hands out a checksum for different content than it serves.
	srv := fakeGitHub(t, "v1.1.3", "tacplus-1-1-3-linux-amd64", body, hashOf([]byte("the real binary")))
	withServer(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	err := downloadReplace(assetURL("v1.1.3", "tacplus-1-1-3-linux-amd64"), testToken, dest, hashOf([]byte("the real binary")))
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Fatalf("dest was modified: %q", got)
	}
	if temps := leftoverTemps(t, dir); len(temps) != 0 {
		t.Fatalf("temp files left behind: %v", temps)
	}
}

func TestDownloadReplace_LookupFailureNeverDownloads(t *testing.T) {
	// The checksum lookup 404s, so runUpgrade's caller never reaches the download.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "-linux-amd64") {
			t.Errorf("download endpoint hit despite a failed checksum lookup: %s", r.URL.Path)
		}
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}))
	defer srv.Close()
	withServer(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	sum, err := fetchChecksum("", "v1.1.3", "tacplus-1-1-3-linux-amd64")
	if err == nil {
		t.Fatalf("expected lookup failure, got sum %q", sum)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != "old binary" {
		t.Fatalf("dest was modified: %q", got)
	}
	if temps := leftoverTemps(t, dir); len(temps) != 0 {
		t.Fatalf("temp files left behind: %v", temps)
	}
}

func TestRedirect_CrossHostStripsToken(t *testing.T) {
	var seen string
	var sawHeader bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, sawHeader = r.Header.Get("Authorization"), true
		w.Write([]byte("payload"))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/object-storage", http.StatusFound)
	}))
	defer origin.Close()

	resp, err := httpGet(origin.URL+"/download", testToken)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
	}
	resp.Body.Close()

	if !sawHeader {
		t.Fatal("redirect target was never reached")
	}
	if seen != "" {
		t.Fatalf("Authorization leaked to a different host: %q", seen)
	}
}

func TestRedirect_SameHostKeepsToken(t *testing.T) {
	var seen string
	var sawHeader bool
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/download-real", http.StatusFound)
	})
	mux.HandleFunc("/download-real", func(w http.ResponseWriter, r *http.Request) {
		seen, sawHeader = r.Header.Get("Authorization"), true
		w.Write([]byte("payload"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := httpGet(srv.URL+"/download", testToken)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
	}
	resp.Body.Close()

	if !sawHeader {
		t.Fatal("redirect target was never reached")
	}
	if seen != "Bearer "+testToken {
		t.Fatalf("Authorization = %q on a same-host redirect, want it preserved", seen)
	}
}

func TestAssetURL_EscapesPathSegments(t *testing.T) {
	got := assetURL("v1.1.3", "tacplus 1.1.3?linux")
	if strings.Contains(got, " ") || strings.Contains(got, "?") {
		t.Fatalf("asset name was not path-escaped: %s", got)
	}
}

func TestResolveReleaseTag(t *testing.T) {
	releases := []string{"v2.0.1", "v2.0.0", "v1.1.3", "v1.1.2"}
	cases := []struct{ version, want string }{
		{"1.1", "v1.1.3"},
		{"v1.1", "v1.1.3"},
		{"1.1.2", "v1.1.2"},
		{"2.0", "v2.0.1"},
	}
	for _, c := range cases {
		got, err := resolveReleaseTag(releases, c.version)
		if err != nil || got != c.want {
			t.Errorf("resolveReleaseTag(%q) = %q, %v; want %q", c.version, got, err, c.want)
		}
	}
	if _, err := resolveReleaseTag(releases, "3.0"); err == nil {
		t.Error("expected an error for a line with no release")
	}
	if _, err := resolveReleaseTag(releases, "1"); err == nil {
		t.Error("expected an error for a malformed version")
	}
}
