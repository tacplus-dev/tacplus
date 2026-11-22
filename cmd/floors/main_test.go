// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFloorOf(t *testing.T) {
	for _, c := range []struct {
		ecosystem, spec string
		want            []string
	}{
		{"crates.io", "0.14", []string{"0.14.0"}},
		{"crates.io", "1", []string{"1.0.0"}},
		{"crates.io", "=1.12.0", []string{"1.12.0"}},
		{"crates.io", "^1.2.3", []string{"1.2.3"}},
		{"crates.io", ">=0.9, <2", []string{"0.9.0"}},
		{"crates.io", "*", nil},
		{"npm", "^2.11.0", []string{"2.11.0"}},
		{"npm", "~4.3.2", []string{"4.3.2"}},
		{"npm", ">=22.0.0", []string{"22.0.0"}},
		{"npm", "^24.22.2 || >=26.0.0", []string{"24.22.2", "26.0.0"}},
		{"npm", "file:../key", nil},
		{"npm", "latest", nil},
		{"PyPI", ">=6.33.6", []string{"6.33.6"}},
		{"PyPI", "~=2.0", []string{"2.0"}},
		{"PyPI", ">=1.0,<2", []string{"1.0"}},
		{"PyPI", "<2", nil},
		{"Go", "v1.83.2", []string{"1.83.2"}},
		{"Go", "v0.0.0-20260526163538-3dc84a4a5aaa", []string{"0.0.0-20260526163538-3dc84a4a5aaa"}},
		{"Maven", "4.36.0", []string{"4.36.0"}},
		{"NuGet", "3.27.2", []string{"3.27.2"}},
		{"NuGet", "[2.65.0, 3.0)", []string{"2.65.0"}},
	} {
		if got := floorOf(c.ecosystem, c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("floorOf(%s, %q) = %q, want %q", c.ecosystem, c.spec, got, c.want)
		}
	}
}

func TestPythonRequirement(t *testing.T) {
	for _, c := range []struct{ req, name, spec string }{
		{"protobuf>=6.33.6", "protobuf", ">=6.33.6"},
		{"uvicorn[standard] >= 0.40.0 ; python_version >= '3.10'", "uvicorn", ">= 0.40.0"},
		{"tomli-w", "tomli-w", ""},
	} {
		name, spec := pythonRequirement(c.req)
		if name != c.name || spec != c.spec {
			t.Errorf("pythonRequirement(%q) = %q, %q; want %q, %q", c.req, name, spec, c.name, c.spec)
		}
	}
}

// Every floor a generated manifest or an inventory declares, by ecosystem:
// path, workspace and file references carry none.
func TestCollect(t *testing.T) {
	root := t.TempDir()
	write(t, root, "proto/dependencies.yaml", `tools:
  typescript: ^5.3.3
lang:
  go:
    dependencies:
      google.golang.org/grpc: v1.83.2
  java:
    dependencies:
      com.google.protobuf:protobuf-java: 4.36.0
  rust:
    dependencies:
      tokio: 1.51.6
  python:
    dependencies:
      urllib3: '>=2.8.0'
  typescript:
    dependencies:
      axios: ^1.20.0
`)
	// A tree the walk skips.
	write(t, root, "target/x/dependencies.yaml", "lang:\n  go:\n    dependencies:\n      skipped: v1.0.0\n")
	gen := t.TempDir()
	write(t, gen, "base/rust/tacplus/model/Cargo.toml", `[workspace]
members = ["."]

[workspace.dependencies]
serde = { version = "1.0.228", features = ["derive"] }

[package]
name = "model"
version = "1.0.0"

[dependencies]
model-types = { path = "types" }
serde = { workspace = true }
rand = "0.8.6"

[dev-dependencies]
tempfile = "3"

[target.'cfg(unix)'.dependencies]
libc = { version = "0.2" }
`)
	write(t, gen, "base/typescript/tacplus/model/package.json", `{
  "dependencies": {"axios": "^1.20.0", "@x/chassis-key": "file:../key"},
  "devDependencies": {"typescript": "^5.3.3"},
  "engines": {"node": ">=22.0.0"}
}`)
	write(t, gen, "base/python/tacplus/model/pyproject.toml", `[build-system]
requires = ["setuptools>=61.0", "wheel"]

[project]
name = "model"
dependencies = ["protobuf>=6.33.6", "grpcio>=1.76.0"]

[project.optional-dependencies]
server = ["uvicorn>=0.40.0"]
`)
	write(t, gen, "c2d/csharp/tacplus/Model.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Google.Protobuf" Version="3.27.2" />
    <PackageReference Include="Grpc.Tools" Version="2.65.0" PrivateAssets="All" />
  </ItemGroup>
</Project>`)
	write(t, gen, "c2d/swift/tacplus/Package.swift", `// swift-tools-version: 6.1
let package = Package(
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf.git", from: "1.38.0"),
        .package(url: "https://github.com/grpc/grpc-swift-2.git", exact: "2.4.0"),
    ]
)`)

	floors, err := collect(root, gen)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range floors {
		got = append(got, f.Ecosystem+" "+f.Name+"@"+f.Version)
	}
	sort.Strings(got)
	want := []string{
		"Go google.golang.org/grpc@1.83.2",
		"Maven com.google.protobuf:protobuf-java@4.36.0",
		"NuGet Google.Protobuf@3.27.2",
		"NuGet Grpc.Tools@2.65.0",
		"PyPI grpcio@1.76.0",
		"PyPI protobuf@6.33.6",
		"PyPI setuptools@61.0",
		"PyPI urllib3@2.8.0",
		"PyPI uvicorn@0.40.0",
		"SwiftURL github.com/apple/swift-protobuf@1.38.0",
		"SwiftURL github.com/grpc/grpc-swift-2@2.4.0",
		"crates.io libc@0.2.0",
		"crates.io rand@0.8.6",
		"crates.io serde@1.0.228",
		"crates.io tempfile@3.0.0",
		"crates.io tokio@1.51.6",
		"npm axios@1.20.0",
		"npm typescript@5.3.3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("collect:\n got %q\nwant %q", got, want)
	}
	for _, f := range floors {
		if f.Name == "axios" && len(f.Sources) != 2 {
			t.Errorf("axios floor's sources = %v, want the inventory and the package.json", f.Sources)
		}
	}
}

// fakeOSV answers querybatch with vulns for the versions in vulnerable, and
// each vuln's record by id.
func fakeOSV(t *testing.T, vulnerable map[string][]string, records map[string]osvVuln) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
			var req osvBatchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var resp osvBatchResponse
			for _, q := range req.Queries {
				var res osvBatchResult
				for _, id := range vulnerable[q.Package.Ecosystem+" "+q.Package.Name+"@"+q.Version] {
					res.Vulns = append(res.Vulns, osvVulnRef{ID: id})
				}
				resp.Results = append(resp.Results, res)
			}
			json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
			rec, ok := records[strings.TrimPrefix(r.URL.Path, "/v1/vulns/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(rec)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCheck(t *testing.T) {
	floors := []floor{
		{Ecosystem: "crates.io", Name: "tokio", Version: "1.0.0", Sources: []string{"a/Cargo.toml"}},
		{Ecosystem: "npm", Name: "axios", Version: "1.20.0", Sources: []string{"b/package.json"}},
		{Ecosystem: "PyPI", Name: "setuptools", Version: "61.0", Sources: []string{"c/pyproject.toml"}},
		{Ecosystem: "Go", Name: "golang.org/x/net", Version: "0.1.0", Sources: []string{"d/dependencies.yaml"}},
		{Ecosystem: "crates.io", Name: "chrono", Version: "0.4.43", Sources: []string{"e/Cargo.toml"}},
	}
	srv := fakeOSV(t, map[string][]string{
		"crates.io tokio@1.0.0":     {"RUSTSEC-2021-0072"},
		"PyPI setuptools@61.0":      {"GHSA-r9hx-vwmv-q579"},
		"Go golang.org/x/net@0.1.0": {"GO-2023-0001"},
		"crates.io chrono@0.4.43":   {"RUSTSEC-2026-0543"},
	}, map[string]osvVuln{
		"RUSTSEC-2021-0072":   {ID: "RUSTSEC-2021-0072", Summary: "Task dropped", Aliases: []string{"CVE-2021-38191"}},
		"GHSA-r9hx-vwmv-q579": {ID: "GHSA-r9hx-vwmv-q579", Summary: "ReDoS", Aliases: []string{"CVE-2022-40897"}},
		"GO-2023-0001":        {ID: "GO-2023-0001", Summary: "withdrawn one", Withdrawn: "2023-02-01T00:00:00Z"},
		"RUSTSEC-2026-0543":   {ID: "RUSTSEC-2026-0543", Summary: "chrono is unmaintained", Affected: []osvAffected{informationalAffected("unmaintained")}},
	})
	defer srv.Close()
	today := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	accepted := []exception{
		{ID: "CVE-2022-40897", Reason: "accepted", Expires: "2026-12-31"},
		// An expired exception accepts nothing.
		{ID: "CVE-2021-38191", Reason: "lapsed", Expires: "2026-11-01"},
	}
	var out bytes.Buffer
	failing, err := check(&out, srv.URL, floors, accepted, today)
	if err != nil {
		t.Fatal(err)
	}
	if failing != 1 {
		t.Errorf("failing = %d, want 1 (tokio: the setuptools advisory is accepted, the Go one withdrawn, chrono's informational)\n%s", failing, out.String())
	}
	for _, want := range []string{
		"FAIL crates.io tokio@1.0.0: RUSTSEC-2021-0072 (CVE-2021-38191) Task dropped",
		"a/Cargo.toml",
		"accepted PyPI setuptools@61.0: GHSA-r9hx-vwmv-q579",
		"warning crates.io chrono@0.4.43: RUSTSEC-2026-0543 chrono is unmaintained [unmaintained]",
		"### Checked against",
		"- osv: db " + srv.URL + ", queried_at ",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "GO-2023-0001") {
		t.Errorf("a withdrawn advisory is reported:\n%s", out.String())
	}
}

func TestCheckFailsWhenOSVIsUnreachable(t *testing.T) {
	defer func(d time.Duration) { retryDelay = d }(retryDelay)
	retryDelay = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	var out bytes.Buffer
	if _, err := check(&out, srv.URL, []floor{{Ecosystem: "npm", Name: "axios", Version: "1.20.0"}}, nil, time.Now()); err == nil {
		t.Fatal("check passed with OSV answering 503: a floor nobody checked is no pass")
	}
}

// security-exceptions.yaml is read as the build's scanner reads it: a value
// runs to the end of its line, colons and all.
func TestReadExceptions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "security-exceptions.yaml", `# The findings tacplus accepts.
exceptions:
  # A comment between entries.
  - id: GHSA-r9hx-vwmv-q579
    reason: "setuptools: the build backend only: no runtime use"
    decided_by: looper, tacplus#949
    expires: 2026-12-31
  - id: G402
    reason: unquoted: with a colon
    expires: '2027-01-15'
`)
	got, err := readExceptions(filepath.Join(dir, "security-exceptions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []exception{
		{ID: "GHSA-r9hx-vwmv-q579", Reason: "setuptools: the build backend only: no runtime use", Expires: "2026-12-31"},
		{ID: "G402", Reason: "unquoted: with a colon", Expires: "2027-01-15"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readExceptions:\n got %+v\nwant %+v", got, want)
	}
}

func informationalAffected(kind string) osvAffected {
	var a osvAffected
	a.DatabaseSpecific.Informational = kind
	return a
}
