// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
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
    serde_json: 1.0.149
    serde_yaml: 0.9.34
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
