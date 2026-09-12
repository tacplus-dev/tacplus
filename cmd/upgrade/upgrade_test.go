// Copyright (c) 2026 Second Star Cavalier LLC

package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "test-token-abc"

// fakeGitLab serves the Packages API endpoints and the generic package download
// for a single file whose contents are body.
func fakeGitLab(t *testing.T, pkg, version, fileName string, body []byte, sha256Hex string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/projects/"+upgradeProjectID+"/packages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		if q.Get("package_type") != "generic" || q.Get("package_name") != pkg || q.Get("package_version") != version {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"id":42,"name":%q,"version":%q,"package_type":"generic"}]`, pkg, version)
	})

	mux.HandleFunc("/projects/"+upgradeProjectID+"/packages/42/package_files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `[{"file_name":"other-file","file_sha256":"deadbeef"},{"file_name":%q,"file_sha256":%q}]`, fileName, sha256Hex)
	})

	mux.HandleFunc("/projects/"+upgradeProjectID+"/packages/generic/"+pkg+"/"+version+"/"+fileName,
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != testToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write(body)
		})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withAPIBase(t *testing.T, base string) {
	t.Helper()
	old := upgradeAPIBase
	upgradeAPIBase = base
	t.Cleanup(func() { upgradeAPIBase = old })
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tempFiles returns the names of files in dir left behind by a download attempt.
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

func TestFetchPackageSHA256(t *testing.T) {
	body := []byte("new binary contents")
	want := hashOf(body)
	srv := fakeGitLab(t, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64", body, want)
	withAPIBase(t, srv.URL)

	got, err := fetchPackageSHA256(testToken, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64")
	if err != nil {
		t.Fatalf("fetchPackageSHA256: %v", err)
	}
	if got != want {
		t.Fatalf("sha256 = %q, want %q", got, want)
	}
}

func TestFetchPackageSHA256_PackageNotFound(t *testing.T) {
	srv := fakeGitLab(t, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64", []byte("x"), hashOf([]byte("x")))
	withAPIBase(t, srv.URL)

	// Version 9 is not published: the list endpoint returns an empty array.
	if _, err := fetchPackageSHA256(testToken, "tacplus-1-1", "9", "tacplus-1-1-9-linux-amd64"); err == nil {
		t.Fatal("expected an error for a missing package version")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFetchPackageSHA256_FileNotInPackage(t *testing.T) {
	body := []byte("x")
	srv := fakeGitLab(t, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64", body, hashOf(body))
	withAPIBase(t, srv.URL)

	if _, err := fetchPackageSHA256(testToken, "tacplus-1-1", "3", "tacplus-1-1-3-darwin-arm64"); err == nil {
		t.Fatal("expected an error for a file absent from the package")
	}
}

func TestFetchPackageSHA256_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "404 Project Not Found", http.StatusNotFound)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	if _, err := fetchPackageSHA256(testToken, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64"); err == nil {
		t.Fatal("expected an error when the API returns 404")
	} else if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadReplace_ChecksumMatches(t *testing.T) {
	body := []byte("new binary contents")
	sum := hashOf(body)
	srv := fakeGitLab(t, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64", body, sum)
	withAPIBase(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	dl := srv.URL + "/projects/" + upgradeProjectID + "/packages/generic/tacplus-1-1/3/tacplus-1-1-3-linux-amd64"
	if err := downloadReplace(dl, testToken, dest, sum); err != nil {
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
	srv := fakeGitLab(t, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64", body, hashOf([]byte("the real binary")))
	withAPIBase(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	dl := srv.URL + "/projects/" + upgradeProjectID + "/packages/generic/tacplus-1-1/3/tacplus-1-1-3-linux-amd64"
	err := downloadReplace(dl, testToken, dest, hashOf([]byte("the real binary")))
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
		if strings.Contains(r.URL.Path, "/packages/generic/") {
			t.Errorf("download endpoint hit despite a failed checksum lookup: %s", r.URL.Path)
		}
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}))
	defer srv.Close()
	withAPIBase(t, srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "tacplus")
	if err := os.WriteFile(dest, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	sum, err := fetchPackageSHA256(testToken, "tacplus-1-1", "3", "tacplus-1-1-3-linux-amd64")
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

func TestRedirect_CrossHostStripsPrivateToken(t *testing.T) {
	var seen string
	var sawHeader bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, sawHeader = r.Header.Get("PRIVATE-TOKEN"), true
		w.Write([]byte("payload"))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/object-storage", http.StatusFound)
	}))
	defer origin.Close()

	resp, err := gitlabGet(origin.URL+"/download", testToken)
	if err != nil {
		t.Fatalf("gitlabGet: %v", err)
	}
	resp.Body.Close()

	if !sawHeader {
		t.Fatal("redirect target was never reached")
	}
	if seen != "" {
		t.Fatalf("PRIVATE-TOKEN leaked to a different host: %q", seen)
	}
}

func TestRedirect_SameHostKeepsPrivateToken(t *testing.T) {
	var seen string
	var sawHeader bool
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/download-real", http.StatusFound)
	})
	mux.HandleFunc("/download-real", func(w http.ResponseWriter, r *http.Request) {
		seen, sawHeader = r.Header.Get("PRIVATE-TOKEN"), true
		w.Write([]byte("payload"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := gitlabGet(srv.URL+"/download", testToken)
	if err != nil {
		t.Fatalf("gitlabGet: %v", err)
	}
	resp.Body.Close()

	if !sawHeader {
		t.Fatal("redirect target was never reached")
	}
	if seen != testToken {
		t.Fatalf("PRIVATE-TOKEN = %q on a same-host redirect, want it preserved", seen)
	}
}

func TestFetchPackageSHA256_EscapesQueryParams(t *testing.T) {
	// Guard against a package name containing characters that would otherwise
	// corrupt the query string.
	var gotName string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/"+upgradeProjectID+"/packages", func(w http.ResponseWriter, r *http.Request) {
		gotName = r.URL.Query().Get("package_name")
		fmt.Fprint(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withAPIBase(t, srv.URL)

	name := "tacplus&evil=1"
	_, _ = fetchPackageSHA256(testToken, name, "3", "f")
	if gotName != name {
		t.Fatalf("package_name = %q, want %q (escaped as %q)", gotName, name, url.QueryEscape(name))
	}
}
