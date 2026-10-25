// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package protosrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Disjoint files from two sources union into one tree; an identical shared file
// present in both is deduplicated (not a conflict).
func TestMergeSources_UnionAndDedup(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "services/kernel.proto", "package kernel;\n")
	writeFile(t, a, "sys/spec.proto", "package sys;\n") // shared base
	writeFile(t, b, "services/app.proto", "package app;\n")
	writeFile(t, b, "sys/spec.proto", "package sys;\n") // identical shared base

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources: %v", err)
	}
	for _, rel := range []string{"services/kernel.proto", "services/app.proto", "sys/spec.proto"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("expected merged file %s: %v", rel, err)
		}
	}
	if got := read(t, filepath.Join(dest, "sys/spec.proto")); got != "package sys;\n" {
		t.Errorf("deduped shared file content = %q", got)
	}
}

// Hidden paths (.git trees, dotfiles) and non-proto/yaml housekeeping files
// (README, scripts) are excluded from the merge — they differ per repo and must
// neither be copied nor treated as conflicts.
func TestMergeSources_SkipsHiddenAndHousekeepingFiles(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "a.proto", "package a;\n")
	writeFile(t, a, ".git/config", "[core]\nrepo = a\n")
	writeFile(t, a, ".gitlab-ci.yml", "a-ci\n")
	writeFile(t, a, "README.md", "base readme\n")
	writeFile(t, b, "b.proto", "package b;\n")
	writeFile(t, b, ".git/config", "[core]\nrepo = b\n") // differs — must not conflict
	writeFile(t, b, "README.md", "app readme\n")         // differs — must not conflict

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources should ignore hidden/housekeeping files: %v", err)
	}
	for _, rel := range []string{".git/config", ".gitlab-ci.yml", "README.md"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("%s should not be copied into the merged tree", rel)
		}
	}
	for _, rel := range []string{"a.proto", "b.proto"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("expected %s in merged tree: %v", rel, err)
		}
	}
}

// A shared path with DIFFERENT contents across sources is a hard error — this is
// what forces the shared base to be factored into a single source.
func TestMergeSources_CarriesTheFleetLock(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	writeFile(t, a, "services/x.proto", "syntax = \"proto3\";\n")
	writeFile(t, a, "fleet.lock", "services:\n    - {name: x, depends: [y]}\n")
	writeFile(t, a, "README.md", "not merged\n")
	writeFile(t, b, "services/y.proto", "syntax = \"proto3\";\n")
	dest := t.TempDir()
	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dest, "fleet.lock")); !strings.Contains(got, "depends: [y]") {
		t.Fatalf("fleet.lock not carried: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err == nil {
		t.Fatal("README.md must not be merged")
	}
}

func TestMergeSources_ConflictIsHardError(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "sys/spec.proto", "package sys;\n// variant A\n")
	writeFile(t, b, "sys/spec.proto", "package sys;\n// variant B\n")

	err := MergeSources([]string{a, b}, dest)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "sys/spec.proto") || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("error should name the conflicting file: %v", err)
	}
}

// dependencies.yaml is merged semantically: disjoint language deps union.
func TestMergeSources_DependenciesDeepMerge(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "a.proto", "package a;\n")
	writeFile(t, a, "dependencies.yaml", "lang:\n  go:\n    dependencies:\n      go.uber.org/zap: v1.27.1\n")
	writeFile(t, b, "b.proto", "package b;\n")
	writeFile(t, b, "dependencies.yaml", "lang:\n  go:\n    dependencies:\n      google.golang.org/grpc: v1.78.0\n")

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources: %v", err)
	}
	var doc struct {
		Lang struct {
			Go struct {
				Dependencies map[string]string `yaml:"dependencies"`
			} `yaml:"go"`
		} `yaml:"lang"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(dest, "dependencies.yaml"))), &doc); err != nil {
		t.Fatalf("parse merged deps: %v", err)
	}
	deps := doc.Lang.Go.Dependencies
	if deps["go.uber.org/zap"] != "v1.27.1" || deps["google.golang.org/grpc"] != "v1.78.0" {
		t.Errorf("merged go deps missing a source's entries: %v", deps)
	}
}

// registry.yaml's services/vservices lists union by name across a base and an
// app source, each owning its own IDs.
func TestMergeSources_RegistryListUnion(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "a.proto", "package a;\n")
	writeFile(t, a, "registry.yaml", "services:\n  - name: kernel\n    id: 8\n  - name: oauth\n    id: 21\nvservices:\n  - name: agentsensor\n    field_id: 42\n")
	writeFile(t, b, "b.proto", "package b;\n")
	writeFile(t, b, "registry.yaml", "services:\n  - name: anthropic\n    id: 22\n  - name: github\n    id: 23\nvservices:\n  - name: llmsensor\n    field_id: 44\n")

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources: %v", err)
	}
	var doc struct {
		Services []struct {
			Name string `yaml:"name"`
			ID   int    `yaml:"id"`
		} `yaml:"services"`
		VServices []struct {
			Name    string `yaml:"name"`
			FieldID int    `yaml:"field_id"`
		} `yaml:"vservices"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(dest, "registry.yaml"))), &doc); err != nil {
		t.Fatalf("parse merged registry: %v", err)
	}
	gotIDs := map[string]int{}
	for _, s := range doc.Services {
		gotIDs[s.Name] = s.ID
	}
	want := map[string]int{"kernel": 8, "oauth": 21, "anthropic": 22, "github": 23}
	for n, id := range want {
		if gotIDs[n] != id {
			t.Errorf("service %q id = %d, want %d (merged=%v)", n, gotIDs[n], id, gotIDs)
		}
	}
	// services emitted sorted by name for determinism.
	if len(doc.Services) != 4 || doc.Services[0].Name != "anthropic" {
		t.Errorf("services not unioned+sorted: %+v", doc.Services)
	}
	if len(doc.VServices) != 2 {
		t.Errorf("vservices not unioned: %+v", doc.VServices)
	}
}

// Same service name with a different ID across sources is a conflict.
func TestMergeSources_RegistryNameConflict(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()
	writeFile(t, a, "registry.yaml", "services:\n  - name: kernel\n    id: 8\n")
	writeFile(t, b, "registry.yaml", "services:\n  - name: kernel\n    id: 99\n")
	err := MergeSources([]string{a, b}, dest)
	if err == nil {
		t.Fatal("expected name/id conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "kernel") {
		t.Errorf("error should name the differing entry: %v", err)
	}
}

// Two different service names sharing the same wire ID across sources is an ID
// collision — the exact thing a split registry must catch.
func TestMergeSources_RegistryIDCollision(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()
	writeFile(t, a, "registry.yaml", "services:\n  - name: kernel\n    id: 8\n")
	writeFile(t, b, "registry.yaml", "services:\n  - name: anthropic\n    id: 8\n")
	err := MergeSources([]string{a, b}, dest)
	if err == nil {
		t.Fatal("expected id-collision error, got nil")
	}
	if !strings.Contains(err.Error(), "8") || !strings.Contains(err.Error(), "kernel") || !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error should name the colliding id and both services: %v", err)
	}
}

// dependencies.yaml with the same dep pinned to different versions is a conflict.
func TestMergeSources_DependenciesConflict(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "dependencies.yaml", "lang:\n  go:\n    dependencies:\n      google.golang.org/grpc: v1.78.0\n")
	writeFile(t, b, "dependencies.yaml", "lang:\n  go:\n    dependencies:\n      google.golang.org/grpc: v1.70.0\n")

	err := MergeSources([]string{a, b}, dest)
	if err == nil {
		t.Fatal("expected version-conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "grpc") {
		t.Errorf("error should name the conflicting dependency: %v", err)
	}
}

// id_min is an allocation FLOOR, not a value the sources must agree on. Two app
// proto repos legitimately carry different floors (testapp 60, scotty 70) so
// their services allocate clear of each other; a build merging both must keep
// the higher one rather than refuse. Nothing already named is renumbered — every
// entry in services/vservices carries an explicit id.
func TestMergeSources_RegistryIDMinTakesMax(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "a.proto", "package a;\n")
	writeFile(t, a, "registry.yaml", "id_min: 60\nservices:\n  - name: echo\n    id: 60\n")
	writeFile(t, b, "b.proto", "package b;\n")
	writeFile(t, b, "registry.yaml", "id_min: 70\nservices:\n  - name: scotty\n    id: 70\n")

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources: %v", err)
	}
	var doc struct {
		IDMin    int `yaml:"id_min"`
		Services []struct {
			Name string `yaml:"name"`
			ID   int    `yaml:"id"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(dest, "registry.yaml"))), &doc); err != nil {
		t.Fatalf("parse merged registry: %v", err)
	}
	if doc.IDMin != 70 {
		t.Errorf("id_min = %d, want 70 (the higher floor)", doc.IDMin)
	}
	if len(doc.Services) != 2 {
		t.Errorf("services not unioned: %+v", doc.Services)
	}
	for _, s := range doc.Services {
		if (s.Name == "echo" && s.ID != 60) || (s.Name == "scotty" && s.ID != 70) {
			t.Errorf("raising the floor must not renumber: %+v", s)
		}
	}
}

// Order must not matter: the higher floor wins whichever source carries it.
func TestMergeSources_RegistryIDMinMaxIsOrderIndependent(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dest := t.TempDir()

	writeFile(t, a, "a.proto", "package a;\n")
	writeFile(t, a, "registry.yaml", "id_min: 70\n")
	writeFile(t, b, "b.proto", "package b;\n")
	writeFile(t, b, "registry.yaml", "id_min: 60\n")

	if err := MergeSources([]string{a, b}, dest); err != nil {
		t.Fatalf("MergeSources: %v", err)
	}
	var doc struct {
		IDMin int `yaml:"id_min"`
	}
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(dest, "registry.yaml"))), &doc); err != nil {
		t.Fatalf("parse merged registry: %v", err)
	}
	if doc.IDMin != 70 {
		t.Errorf("id_min = %d, want 70 (the higher floor)", doc.IDMin)
	}
}
