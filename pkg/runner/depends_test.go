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
	writeFile(t, filepath.Join(depRepo, "proto", "registry.yaml"), "services:\n    - {name: llmproxy, id: 75}\n")
	writeFile(t, filepath.Join(depRepo, "proto", "services", "llmproxy.proto"), "syntax = \"proto3\";\npackage llmproxy;\n")
	global := filepath.Join(root, "registry")
	writeFile(t, filepath.Join(global, "registry.yaml"), "services:\n    - {name: llmproxy, id: 75, repo: "+depRepo+", visibility: public}\n    - {name: norepo, id: 102}\n")
	consumer := filepath.Join(root, "consumer", "proto")
	writeFile(t, filepath.Join(consumer, "dependencies.yaml"), "{}\n")
	writeFile(t, filepath.Join(consumer, "registry.yaml"), "global: "+global+"\nservices:\n    - {name: doppelganger, id: 101}\n")

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
	if _, err := os.Stat(filepath.Join(dirs[0], "registry.yaml")); err != nil {
		t.Fatalf("staged dependency lacks its registry lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirs[0], "dependencies.yaml")); !os.IsNotExist(err) {
		t.Fatal("a dependency's dependencies.yaml must not be merged; its pins are its own build's")
	}

	if _, _, err := resolveDependencies([]string{"nobody"}, consumer); err == nil || !strings.Contains(err.Error(), "not in the global registry") {
		t.Fatalf("unregistered dependency err = %v", err)
	}
	if _, _, err := resolveDependencies([]string{"norepo"}, consumer); err == nil || !strings.Contains(err.Error(), "without a repo") {
		t.Fatalf("repo-less dependency err = %v", err)
	}
}

// Without a global registry in the lock there is no authority to resolve
// names against, and the error says so.
func TestResolveDependenciesNeedsGlobalRegistry(t *testing.T) {
	consumer := filepath.Join(t.TempDir(), "proto")
	writeFile(t, filepath.Join(consumer, "registry.yaml"), "services:\n    - {name: x, id: 1}\n")
	if _, _, err := resolveDependencies([]string{"llmproxy"}, consumer); err == nil || !strings.Contains(err.Error(), "names no global registry") {
		t.Fatalf("err = %v", err)
	}
}
