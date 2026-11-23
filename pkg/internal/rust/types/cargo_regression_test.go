// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

func TestCreateWorkspaceCargoTomlFailsOnMissingWorkspaceDependency(t *testing.T) {
	depsDir := t.TempDir()
	deps := `rust:
  dependencies:
    axum: 0.8.8
    base64: 0.21.7
    bytes: 1.11.1
    futures: 0.3.31
    pbjson: 0.8.0
    pbjson-types: 0.8.0
    prost: 0.14.1
    prost-types: 0.14.1
    prost-wkt-types: 0.7.1
    regex: 1.12.3
    reqwest: 0.12.18
    rustls: 0.23.35
    serde: 1.0.228
    serde-saphyr: 2.1.1
    serde_json: 1.0.149
    tokio: 1.49.0
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
	grpcserverDir := filepath.Join(out, "sys", "grpcserver")
	if err := os.MkdirAll(grpcserverDir, 0755); err != nil {
		t.Fatalf("mkdir grpcserver: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(out, "types"), 0755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grpcserverDir, "Cargo.toml"), []byte(`[package]
name = "grpcserver"
version = "1.0.0"
edition = "2021"

[dependencies]
async-trait = { workspace = true }
`), 0644); err != nil {
		t.Fatalf("write grpcserver Cargo.toml: %v", err)
	}

	err := createWorkspaceCargoToml(&tactype.Config{
		CrateBasePath: "current-generation",
	}, out, true, false, false)
	if err == nil {
		t.Fatal("expected missing async-trait dependency error")
	}
	if !strings.Contains(err.Error(), "dependencies.yaml missing required workspace dependency versions") ||
		!strings.Contains(err.Error(), "async-trait") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Cargo 1.100 warns on a [workspace.dependencies] entry no member uses
// (cargo::unused_workspace_dependencies), so the generated workspace declares
// only the dependencies its crates reference, not every Rust pin in
// dependencies.yaml (tacplus#829).
func TestCreateWorkspaceCargoTomlDeclaresOnlyReferencedDependencies(t *testing.T) {
	depsDir := t.TempDir()
	deps := `rust:
  dependencies:
    anyhow: 1.0.101
    base64: 0.21.7
    bytes: 1.11.1
    pbjson: 0.8.0
    pbjson-types: 0.8.0
    prost: 0.14.1
    prost-build: 0.14.1
    prost-types: 0.14.1
    prost-wkt-types: 0.7.1
    regex: 1.12.3
    rustls: 0.23.35
    serde: 1.0.228
    serde-saphyr: 2.1.1
    serde_json: 1.0.149
    tokio-rustls: 0.26.4
    tokio-test: 0.4.4
    tokio-tungstenite: 0.30.0
    toml: 0.8.23
    tonic-health: 0.14.0
    tower: 0.5.3
    webpki-roots: 0.26.8
    x509-cert: 0.3.0
`
	if err := os.WriteFile(filepath.Join(depsDir, "dependencies.yaml"), []byte(deps), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(depsDir)
	defer packages.SetProtoDir("")

	out := t.TempDir()
	loggerDir := filepath.Join(out, "chassis", "logger")
	if err := os.MkdirAll(loggerDir, 0755); err != nil {
		t.Fatalf("mkdir logger: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(out, "types"), 0755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	if err := os.WriteFile(filepath.Join(loggerDir, "Cargo.toml"), []byte(`[package]
name = "logger"
version = "1.0.0"
edition = "2021"

[dependencies]
webpki-roots = { workspace = true }
`), 0644); err != nil {
		t.Fatalf("write logger Cargo.toml: %v", err)
	}

	// A state with no gRPC service: the root crate takes the cert crates
	// (tokio-rustls among them) without service wrappers.
	if err := createWorkspaceCargoToml(&tactype.Config{
		CrateBasePath: "current-generation",
		States:        []tactype.StateEntry{{}},
	}, out, false, false, false); err != nil {
		t.Fatalf("createWorkspaceCargoToml: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(data)
	start := strings.Index(manifest, "[workspace.dependencies]")
	end := strings.Index(manifest, "[package]")
	if start < 0 || end < start {
		t.Fatalf("no [workspace.dependencies] before [package]:\n%s", manifest)
	}
	declared := map[string]bool{}
	for _, line := range strings.Split(manifest[start:end], "\n")[1:] {
		if name, _, ok := strings.Cut(line, " = "); ok {
			declared[name] = true
		}
	}
	referenced := map[string]bool{}
	for _, member := range []string{".", "types", "chassis/logger"} {
		refs, err := workspaceDependencyRefsInCargo(filepath.Join(out, member, "Cargo.toml"))
		if err != nil {
			t.Fatal(err)
		}
		for name := range refs {
			referenced[name] = true
		}
	}
	for name := range declared {
		if !referenced[name] {
			t.Errorf("[workspace.dependencies] declares %s, which no member references", name)
		}
	}
	for name := range referenced {
		if !declared[name] {
			t.Errorf("a member references %s, which [workspace.dependencies] does not declare", name)
		}
	}
	for _, name := range []string{"webpki-roots", "tokio-rustls"} {
		if !declared[name] {
			t.Errorf("[workspace.dependencies] lacks %s", name)
		}
	}
}

// Each generated crate depends on what its source references; the rest are
// dev-dependencies, which still pin the lockfile and which tests placed in its
// tests/ still reach, so cargo 1.100 does not warn they are unused
// (cargo::unused_dependencies, tacplus#829).
func TestMoveUnusedCrateDependencies(t *testing.T) {
	depsDir := t.TempDir()
	deps := `rust:
  dependencies:
    async-trait: 0.1.89
    axum: 0.8.8
    base64: 0.21.7
    bytes: 1.11.1
    futures: 0.3.31
    pbjson: 0.8.0
    pbjson-types: 0.8.0
    prost: 0.14.1
    prost-types: 0.14.1
    prost-wkt-types: 0.7.1
    regex: 1.12.3
    reqwest: 0.12.18
    rustls: 0.23.35
    serde: 1.0.228
    serde-saphyr: 2.1.1
    serde_json: 1.0.149
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
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(out, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, crate := range []string{"am", "servicemanager", "tags", "x509"} {
		write("chassis/"+crate+"/Cargo.toml", "[package]\nname = \""+crate+"\"\n")
		// A chassis crate's own source naming another does not make the root use it.
		write("chassis/"+crate+"/src/lib.rs", "use x509::Cert;\n")
	}
	write("types/mod.rs", "pub fn f() -> ::prost_types::Timestamp { am::run() }\n")
	write("tests/behaviour_test.rs", "use tags::Tag;\n")
	write("lib.rs", "pub mod sm;\n")
	write("sm/service_manager.rs", "use servicemanager::auth::Caller;\nfn f() { ::am::run(); crate::types::sys::tags::Tag::default(); }\n")

	if err := createWorkspaceCargoToml(&tactype.Config{
		CrateBasePath: "current-generation",
	}, out, true, false, false); err != nil {
		t.Fatalf("createWorkspaceCargoToml: %v", err)
	}
	if err := moveUnusedCrateDependencies(out, "current-generation-types"); err != nil {
		t.Fatalf("moveUnusedCrateDependencies: %v", err)
	}
	var manifest string
	read := func(rel string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatal(err)
		}
		manifest = string(data)
	}
	section := func(header string) string {
		start := strings.Index(manifest, "\n"+header+"\n")
		if start < 0 {
			t.Fatalf("no %s:\n%s", header, manifest)
		}
		rest := manifest[start+len(header)+1:]
		if end := strings.Index(rest, "\n["); end >= 0 {
			rest = rest[:end]
		}
		return rest
	}
	read("Cargo.toml")
	dependencies := section("[dependencies]")
	devDependencies := section("[dev-dependencies]")
	for _, crate := range []string{"current-generation-types", "am", "servicemanager"} {
		if !strings.Contains(dependencies, "\n"+crate+" = { path") {
			t.Errorf("[dependencies] lacks %s, which the root crate uses:\n%s", crate, dependencies)
		}
	}
	// A pin the root crate's code does not use moves with the comment saying why.
	if !strings.Contains(devDependencies, "reads certificates through the x509-cert 0.3 API") {
		t.Errorf("x509-cert's comment did not move with it:\n%s", devDependencies)
	}
	for _, crate := range []string{"tags", "x509", "tonic-health", "tinyvec", "x509-cert"} {
		if strings.Contains(dependencies, "\n"+crate+" = ") {
			t.Errorf("[dependencies] has %s, which the root crate does not use:\n%s", crate, dependencies)
		}
		if !strings.Contains(devDependencies, "\n"+crate+" = ") {
			t.Errorf("[dev-dependencies] lacks %s:\n%s", crate, devDependencies)
		}
	}

	// The types crate: prost-types is used, hmac (redact fields only) is not.
	read("types/Cargo.toml")
	dependencies = section("[dependencies]")
	devDependencies = section("[dev-dependencies]")
	if !strings.Contains(dependencies, "\nprost-types = ") {
		t.Errorf("types [dependencies] lacks prost-types, which it uses:\n%s", dependencies)
	}
	if strings.Contains(dependencies, "\nhmac = ") || !strings.Contains(devDependencies, "\nhmac = ") {
		t.Errorf("types kept hmac, which it does not use, as a dependency:\n%s", manifest)
	}
}

// pbjson-types names chrono as a non-optional dependency, and chrono 0.4 is
// unmaintained (RUSTSEC-2026-0543): no generated source names pbjson_types, so
// no generated crate depends on it, even with dependencies.yaml pinning it, and
// the logger chassis, which never names chrono, does not depend on chrono
// (tacplus#855).
func TestGeneratedCratesDoNotDependOnPbjsonTypes(t *testing.T) {
	depsDir := t.TempDir()
	deps := `rust:
  dependencies:
    async-trait: 0.1.89
    axum: 0.8.8
    base64: 0.21.7
    bytes: 1.11.1
    futures: 0.3.31
    pbjson: 0.8.0
    pbjson-types: 0.8.0
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
	for _, manifest := range []string{"Cargo.toml", "types/Cargo.toml"} {
		data, err := os.ReadFile(filepath.Join(out, manifest))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "pbjson-types") {
			t.Errorf("%s depends on pbjson-types:\n%s", manifest, data)
		}
	}

	logger, err := fs.ReadFile(templates.FS, "rust/chassis/logger/Cargo.toml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logger), "chrono") {
		t.Errorf("the logger chassis depends on chrono:\n%s", logger)
	}
}

// Issue #320: the model and the grpcserver config read and write YAML with
// serde-saphyr. serde_yaml is deprecated and locks unsafe-libyaml
// (RUSTSEC-2024-0449), so an inventory that pins only serde_yaml is refused
// with an error naming serde-saphyr, and the workspace pins serde-saphyr's
// serde features only.
const issue320Deps = `rust:
  dependencies:
    base64: 0.21.7
    bytes: 1.11.1
    pbjson: 0.8.0
    pbjson-types: 0.8.0
    prost: 0.14.1
    prost-types: 0.14.1
    prost-wkt-types: 0.7.1
    regex: 1.12.3
    rustls: 0.23.35
    serde: 1.0.228
    serde_json: 1.0.149
    toml: 0.8.23
    tonic-health: 0.14.0
`

func createIssue320Workspace(t *testing.T, yamlDep string) (string, error) {
	t.Helper()
	depsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(depsDir, "dependencies.yaml"), []byte(issue320Deps+yamlDep), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(depsDir)
	t.Cleanup(func() { packages.SetProtoDir("") })

	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "types"), 0755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	return out, createWorkspaceCargoToml(&tactype.Config{CrateBasePath: "current-generation"}, out, false, false, false)
}

func TestCreateWorkspaceCargoTomlRequiresSerdeSaphyr(t *testing.T) {
	_, err := createIssue320Workspace(t, "    serde_yaml: 0.9.34\n")
	if err == nil {
		t.Fatal("expected an inventory without serde-saphyr to be refused")
	}
	if !strings.Contains(err.Error(), "dependencies.yaml missing required workspace dependency versions: serde-saphyr") {
		t.Fatalf("error must name serde-saphyr: %v", err)
	}
}

func TestCreateWorkspaceCargoTomlPinsSerdeSaphyr(t *testing.T) {
	out, err := createIssue320Workspace(t, "    serde-saphyr: 2.1.1\n")
	if err != nil {
		t.Fatalf("createWorkspaceCargoToml: %v", err)
	}
	workspace, err := os.ReadFile(filepath.Join(out, "Cargo.toml"))
	if err != nil {
		t.Fatalf("read workspace Cargo.toml: %v", err)
	}
	want := `serde-saphyr = { version = "2.1.1", default-features = false, features = ["serialize", "deserialize"] }`
	if !strings.Contains(string(workspace), want) {
		t.Fatalf("workspace Cargo.toml must pin %s:\n%s", want, workspace)
	}
	types, err := os.ReadFile(filepath.Join(out, "types", "Cargo.toml"))
	if err != nil {
		t.Fatalf("read types Cargo.toml: %v", err)
	}
	if !strings.Contains(string(types), "serde-saphyr = { workspace = true }") || strings.Contains(string(types), "serde_yaml") {
		t.Fatalf("types Cargo.toml must use serde-saphyr, not serde_yaml:\n%s", types)
	}
}
