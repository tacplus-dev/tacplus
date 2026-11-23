// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// generateRustManifests writes every Cargo.toml the Rust generator emits (the
// chassis crates, the root crate and the types crate) under a fresh directory,
// with the header configured, and returns their paths.
func generateRustManifests(t *testing.T, headerText string) []string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	packages.SetProtoDir(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "cross-test", "proto"))
	t.Cleanup(func() { packages.SetProtoDir("") })
	header.SetCopyright(headerText)
	t.Cleanup(func() { header.SetCopyright("") })

	out := t.TempDir()
	cfg := &tactype.Config{OutputDir: out, ModuleBasePath: "example.com/consumer/tacplus", CrateBasePath: "consumer-model"}
	if err := writeLibTemplates(cfg, nil, []string{cfg.ServiceLabel()}); err != nil {
		t.Fatalf("writeLibTemplates: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(out, "types"), 0755); err != nil {
		t.Fatalf("mkdir types: %v", err)
	}
	if err := createWorkspaceCargoToml(cfg, out, true, false, false); err != nil {
		t.Fatalf("createWorkspaceCargoToml: %v", err)
	}

	var manifests []string
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "Cargo.toml" {
			manifests = append(manifests, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", out, err)
	}

	// Every Cargo manifest template is covered: a crate added later is held
	// to the same rule without touching this test.
	var tmpls int
	err = fs.WalkDir(templates.FS, "rust", func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(strings.ToLower(d.Name()), "cargo.toml.tmpl") {
			tmpls++
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if len(manifests) != tmpls {
		t.Fatalf("generated %d Cargo.toml, want one per manifest template (%d): %v", len(manifests), tmpls, manifests)
	}
	return manifests
}

// packageSection returns the [package] table of a Cargo manifest: the lines
// from [package] up to the next table header.
func packageSection(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`(?ms)^\[package\]\n(.*?)(?:^\[|\z)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s has no [package] table:\n%s", path, b)
	}
	return m[1]
}

// tacplus#368: cargo-deny refuses a crate that declares no license, so every
// crate tacplus generates declares the header's SPDX license and publish =
// false (they are the consumer's private crates, never published).
func TestRustGeneratedCratesDeclareLicenseAndPublishFalse(t *testing.T) {
	for _, path := range generateRustManifests(t, "Copyright (c) 2026 Example Inc\nSPDX-License-Identifier: Apache-2.0") {
		pkg := packageSection(t, path)
		for _, want := range []string{"\nlicense = \"Apache-2.0\"\n", "\npublish = false\n"} {
			if !strings.Contains("\n"+pkg, want) {
				t.Errorf("%s [package] lacks %q:\n%s", path, strings.TrimSpace(want), pkg)
			}
		}
	}
}

// A header naming no license claims none: the manifests still say publish =
// false, and carry no license field rather than an invented one.
func TestRustGeneratedCratesWithoutSPDXHeaderClaimNoLicense(t *testing.T) {
	for _, path := range generateRustManifests(t, "Copyright (c) 2026 Example Inc") {
		pkg := packageSection(t, path)
		if strings.Contains(pkg, "license") {
			t.Errorf("%s declares a license the header does not name:\n%s", path, pkg)
		}
		if !strings.Contains("\n"+pkg, "\npublish = false\n") {
			t.Errorf("%s [package] lacks publish = false:\n%s", path, pkg)
		}
	}
}
