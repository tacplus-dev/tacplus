// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSoundnessFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A chassis manifest names no version for a package the inventory declares
// (tacplus#949): Rust takes it from the workspace, TypeScript from template
// data, so dependencies.yaml is the one place a floor moves.
func TestChassisManifestVersionViolations(t *testing.T) {
	root := t.TempDir()
	inventory := `tools:
  typescript: ^5.3.3
lang:
  rust:
    dependencies:
      serde: 1.0.228
      tonic: 0.14.6
  typescript:
    dependencies:
      axios: ^1.20.0
`
	for _, rel := range chassisInventoryFiles {
		writeSoundnessFile(t, root, rel, inventory)
	}
	writeSoundnessFile(t, root, "pkg/templates/rust/chassis/a/Cargo.toml.tmpl", `[package]
name = "a"
version = "0.1.0"

[dependencies]
{{.CrateBasePath}}-types = { path = "../../types" }
serde = { version = "1", features = ["derive"] }
tonic = { workspace = true, features = ["tls-ring"] }
rand = "0.8.6"

[dev-dependencies]
tonic = "0.14"
`)
	writeSoundnessFile(t, root, "pkg/templates/typescript/chassis/b/package.json.tmpl", `{
  "name": "{{.TsPackagePrefix}}chassis-b",
  "version": "1.0.0",
  "dependencies": {
    "axios": "^1.6.0",
    "jose": "^5.2.0"
  },
  "devDependencies": {
    "typescript": "^5.3.3"
  }
}
`)
	writeSoundnessFile(t, root, "pkg/templates/typescript/chassis/c/package.json.tmpl", `{
  "devDependencies": {
    "typescript": {{printf "%q" .TypeScript}}
  }
}
`)
	got, err := chassisManifestVersionViolations(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []chassisManifestViolation{
		{file: "pkg/templates/rust/chassis/a/Cargo.toml.tmpl", line: 7, pkg: "serde", version: "1"},
		{file: "pkg/templates/rust/chassis/a/Cargo.toml.tmpl", line: 12, pkg: "tonic", version: "0.14"},
		{file: "pkg/templates/typescript/chassis/b/package.json.tmpl", line: 5, pkg: "axios", version: "^1.6.0"},
		{file: "pkg/templates/typescript/chassis/b/package.json.tmpl", line: 9, pkg: "typescript", version: "^5.3.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("violations:\n got %+v\nwant %+v", got, want)
	}
}

// The repository's own chassis templates hold the rule.
func TestChassisManifestsNameNoInventoryVersion(t *testing.T) {
	got, err := chassisManifestVersionViolations(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		t.Errorf("%s:%d: %s = %q: the inventory declares %s; take it from there", v.file, v.line, v.pkg, v.version, v.pkg)
	}
}
