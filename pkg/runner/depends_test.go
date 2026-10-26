// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A dependency is resolved by registry name: the project's lock names the
// global registry, the registry names the repo, the repo's proto/ is merged.
func TestResolveDependenciesByRegistryName(t *testing.T) {
	root := t.TempDir()
	depRepo := filepath.Join(root, "llmproxy-repo")
	writeFile(t, filepath.Join(depRepo, "proto", "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(depRepo, "proto", "registry.yaml"), "registries: [somewhere-else]\nservices:\n    - {name: llmproxy, id: 75, contract: https://elsewhere/x.git, visibility: private}\n")
	writeFile(t, filepath.Join(depRepo, "proto", "services", "llmproxy.proto"), "syntax = \"proto3\";\npackage llmproxy;\n")
	global := filepath.Join(root, "registry")
	writeFile(t, filepath.Join(global, "registry.yaml"), "scope: global\nservices:\n    - {name: llmproxy, id: 75, contract: "+depRepo+", visibility: public}\n    - {name: norepo, id: 102}\n")
	consumer := filepath.Join(root, "consumer", "proto")
	writeFile(t, filepath.Join(consumer, "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(consumer, "registry.yaml"), "registries: ["+global+"]\nservices:\n    - {name: doppelganger, id: 101}\n")

	dirs, cleanups, err := resolveDependencies([]string{"llmproxy"}, consumer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
	if len(dirs) != 1 {
		t.Fatalf("dirs = %v, want one staged dependency dir", dirs)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "services", "llmproxy.proto")); err != nil {
		t.Fatalf("staged dependency lacks its proto: %v", err)
	}
	lock, err := os.ReadFile(filepath.Join(dirs[0], "registry.yaml"))
	if err != nil {
		t.Fatalf("staged dependency lacks its registry lock: %v", err)
	}
	if !strings.Contains(string(lock), "llmproxy") || !strings.Contains(string(lock), "75") {
		t.Fatalf("staged lock must keep the wire facts: %s", lock)
	}
	for _, meta := range []string{"registries", "contract", "visibility", "elsewhere"} {
		if strings.Contains(string(lock), meta) {
			t.Fatalf("staged lock must drop %q (metadata that may legitimately differ from ours): %s", meta, lock)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "dependencies.yaml")); !os.IsNotExist(err) {
		t.Fatal("a dependency's dependencies.yaml must not be merged; its pins are its own build's")
	}

	if _, _, err := resolveDependencies([]string{"nobody"}, consumer); err == nil || !strings.Contains(err.Error(), "not in any registry") {
		t.Fatalf("unregistered dependency err = %v", err)
	}
	if _, _, err := resolveDependencies([]string{"norepo"}, consumer); err == nil || !strings.Contains(err.Error(), "without a contract") {
		t.Fatalf("repo-less dependency err = %v", err)
	}
}

// Without a global registry in the lock there is no authority to resolve
// names against, and the error says so.
func TestResolveDependenciesNeedsGlobalRegistry(t *testing.T) {
	consumer := filepath.Join(t.TempDir(), "proto")
	writeFile(t, filepath.Join(consumer, "registry.yaml"), "services:\n    - {name: x, id: 1}\n")
	if _, _, err := resolveDependencies([]string{"llmproxy"}, consumer); err == nil || !strings.Contains(err.Error(), "names no registries") {
		t.Fatalf("err = %v", err)
	}
}

// A dependency's own tacplus.yaml depends are followed, each name once: kernel's
// contract names opa (its opa_import provider), so depending on kernel brings opa.
func TestResolveDependenciesIsTransitive(t *testing.T) {
	root := t.TempDir()
	opaRepo := filepath.Join(root, "opa-repo")
	writeFile(t, filepath.Join(opaRepo, "proto", "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(opaRepo, "proto", "registry.yaml"), "services:\n    - {name: opa, id: 29}\n")
	writeFile(t, filepath.Join(opaRepo, "proto", "services", "opa.proto"), "syntax = \"proto3\";\npackage opa;\n")
	kernelRepo := filepath.Join(root, "kernel-repo")
	writeFile(t, filepath.Join(kernelRepo, "tacplus.yaml"), "language: java\noutput-dir: out\ndepends: [opa, kernel]\n")
	writeFile(t, filepath.Join(kernelRepo, "proto", "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(kernelRepo, "proto", "registry.yaml"), "services:\n    - {name: kernel, id: 8}\n")
	writeFile(t, filepath.Join(kernelRepo, "proto", "services", "kernel.proto"), "syntax = \"proto3\";\npackage kernel;\n")
	global := filepath.Join(root, "registry")
	writeFile(t, filepath.Join(global, "registry.yaml"), "scope: global\nservices:\n    - {name: kernel, id: 8, contract: "+kernelRepo+", visibility: public}\n    - {name: opa, id: 29, contract: "+opaRepo+", visibility: public}\n")
	consumer := filepath.Join(root, "consumer", "proto")
	writeFile(t, filepath.Join(consumer, "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(consumer, "registry.yaml"), "registries: ["+global+"]\nservices:\n    - {name: operator, id: 47}\n")

	dirs, cleanups, err := resolveDependencies([]string{"kernel"}, consumer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
	if len(dirs) != 2 {
		t.Fatalf("dirs = %v, want kernel and, through it, opa — each once", dirs)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "services", "kernel.proto")); err != nil {
		t.Fatalf("kernel not staged first: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs[1], "services", "opa.proto")); err != nil {
		t.Fatalf("kernel's own dependency opa not staged: %v", err)
	}
}

// With pkg omitted from tacplus.yaml, the project's own proto dir says which
// service is its own: one declaring package, or nothing when ambiguous.
func TestOwnServicePackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proto")
	writeFile(t, filepath.Join(dir, "services", "doppelganger.proto"), "syntax = \"proto3\";\npackage doppelganger;\noption (spec.service) = {name: \"doppelganger\"};\nservice Doppelganger {}\n")
	writeFile(t, filepath.Join(dir, "common", "shapes.proto"), "syntax = \"proto3\";\npackage shapes;\nmessage Shape {}\n")
	if got := ownServicePackage(dir); got != "doppelganger" {
		t.Fatalf("ownServicePackage = %q, want doppelganger (shapes declares no service)", got)
	}
	writeFile(t, filepath.Join(dir, "services", "other.proto"), "syntax = \"proto3\";\npackage other;\nservice Other {}\n")
	if got := ownServicePackage(dir); got != "" {
		t.Fatalf("ownServicePackage = %q, want \"\" when two packages declare services", got)
	}
}

func TestDeclaresPackage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proto")
	writeFile(t, filepath.Join(dir, "services", "kernel.proto"), "syntax = \"proto3\";\npackage kernel;\nservice Mint {}\n")
	writeFile(t, filepath.Join(dir, "common", "shapes.proto"), "syntax = \"proto3\";\npackage operator;\nmessage Shape {}\n")
	if !declaresPackage(dir, "kernel") {
		t.Fatal("kernel declares a service and must be found")
	}
	if declaresPackage(dir, "operator") {
		t.Fatal("a package with only messages declares no service; a library's own proto must still be merged")
	}
}
