// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package ghcr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// fakeRegistry serves repo's tags; when wantToken is set, a pull token is
// handed out only to a request carrying it as basic auth.
func fakeRegistry(t *testing.T, repo string, tags map[string]string, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "repository:"+repo+":pull" {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		if wantToken != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != "x-access-token" || p != wantToken {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "pull"})
	})
	mux.HandleFunc("/v2/"+repo+"/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "Bearer pull" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		d, ok := tags[r.URL.Path[len("/v2/"+repo+"/manifests/"):]]
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

func TestDigest(t *testing.T) {
	srv := fakeRegistry(t, "acme/kernel", map[string]string{"0.4.3": digest}, "")
	got, err := Digest(srv.Client(), srv.URL, "acme/kernel", "0.4.3", "")
	if err != nil || got != digest {
		t.Fatalf("Digest = %q, %v; want %q", got, err, digest)
	}
	if _, err := Digest(srv.Client(), srv.URL, "acme/kernel", "0.4.4", ""); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("missing tag err = %v, want ErrNotPublished", err)
	}
}

// A private package: the pull token is granted only for the GitHub token.
func TestDigestPrivatePackageSendsTheToken(t *testing.T) {
	srv := fakeRegistry(t, "acme/kernel", map[string]string{"0.4.3": digest}, "secret")
	if _, err := Digest(srv.Client(), srv.URL, "acme/kernel", "0.4.3", ""); err == nil {
		t.Fatal("an anonymous pull of a private package must fail")
	}
	// A token that may not read the package: ghcr.io grants a pull token, then
	// refuses the manifest.
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "pull"})
	})
	mux.HandleFunc("/v2/acme/kernel/manifests/0.4.3", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	denied := httptest.NewServer(mux)
	defer denied.Close()
	if _, err := Digest(denied.Client(), denied.URL, "acme/kernel", "0.4.3", "weak"); !errors.Is(err, ErrDenied) {
		t.Fatalf("refused pull err = %v, want ErrDenied", err)
	}
	got, err := Digest(srv.Client(), srv.URL, "acme/kernel", "0.4.3", "secret")
	if err != nil || got != digest {
		t.Fatalf("Digest with token = %q, %v; want %q", got, err, digest)
	}
}
