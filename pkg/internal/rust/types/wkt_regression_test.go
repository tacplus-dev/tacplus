// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// chrono 0.4 is unmaintained (RUSTSEC-2026-0543), and prost-wkt-types depends
// on it with no feature to turn it off. The generated Rust maps the
// google.protobuf well-known types to the wkt chassis crate, which serializes
// them without chrono (tacplus#862).
func TestWellKnownTypesMapToTheWktChassisCrate(t *testing.T) {
	args := strings.Join(wellKnownTypeExternPaths(), "\n")
	for _, name := range []string{"Any", "Timestamp", "Duration", "Struct", "Value", "ListValue"} {
		want := "--prost_opt=extern_path=.google.protobuf." + name + "=::wkt::" + name
		if !strings.Contains(args, want) {
			t.Errorf("protoc arguments lack %s:\n%s", want, args)
		}
	}
	if strings.Contains(args, "prost_wkt") {
		t.Errorf("protoc arguments still map a well-known type to prost-wkt-types:\n%s", args)
	}
	if _, err := fs.Stat(templates.FS, "rust/chassis/wkt/Cargo.toml.tmpl"); err != nil {
		t.Errorf("no wkt chassis crate: %v", err)
	}
}

// No generated Rust crate depends on chrono or prost-wkt-types, even when the
// consumer's dependencies.yaml still pins them, and no Rust template names
// either (tacplus#862). current-generation checks the generated Cargo.lock.
func TestGeneratedRustDoesNotDependOnChrono(t *testing.T) {
	depsDir := t.TempDir()
	deps := `rust:
  dependencies:
    async-trait: 0.1.89
    axum: 0.8.8
    base64: 0.21.7
    bytes: 1.11.1
    chrono: 0.4.43
    futures: 0.3.31
    pbjson: 0.8.0
    prost: 0.14.1
    prost-types: 0.14.1
    prost-wkt-types: 0.7.1
    regex: 1.12.3
    reqwest: 0.12.18
    rustls: 0.23.35
    rustls-pemfile: 2.2.0
    serde: 1.0.228
    serde-saphyr: 2.1.1
    serde_json: 1.0.149
    serde_yaml: 0.9.34
    tinyvec: "=1.12.0"
    tokio: 1.49.0
    tokio-rustls: 0.26.4
    tokio-stream: 0.1.17
    tokio-util: 0.7.15
    toml: 0.8.23
    tonic: 0.14.0
    tonic-health: 0.14.0
    tonic-prost: 0.14.0
    tracing: 0.1.44
`
	if err := os.WriteFile(filepath.Join(depsDir, "dependencies.yaml"), []byte(deps), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(depsDir)
	defer packages.SetProtoDir("")

	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "types"), 0755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	if err := createWorkspaceCargoToml(&tactype.Config{
		CrateBasePath: "current-generation",
	}, out, true, false, false); err != nil {
		t.Fatalf("createWorkspaceCargoToml: %v", err)
	}
	dependency := regexp.MustCompile(`(?m)^\s*(chrono|prost-wkt[-a-z]*)\s*=`)
	for _, manifest := range []string{"Cargo.toml", "types/Cargo.toml"} {
		data, err := os.ReadFile(filepath.Join(out, manifest))
		if err != nil {
			t.Fatal(err)
		}
		if dependency.Match(data) {
			t.Errorf("%s depends on chrono or prost-wkt-types:\n%s", manifest, data)
		}
	}
	types, err := os.ReadFile(filepath.Join(out, "types/Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(types), `wkt = { path = "../chassis/wkt" }`) {
		t.Errorf("the types crate does not depend on the wkt chassis crate:\n%s", types)
	}

	// Code and manifests, not their comments: chrono_now_ms (healthreporter)
	// and "synchronous" are not chrono either.
	named := regexp.MustCompile(`\bchrono\b|prost[-_]wkt`)
	err = fs.WalkDir(templates.FS, "rust", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "#") {
				continue
			}
			if named.MatchString(code) {
				t.Errorf("%s:%d names chrono or prost-wkt: %s", path, i+1, code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
