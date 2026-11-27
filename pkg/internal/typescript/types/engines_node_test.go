// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// writeChassisWithDeps renders the TypeScript chassis against the given
// dependencies.yaml and returns every emitted package.json's engines.node,
// keyed by chassis directory.
func writeChassisWithDeps(t *testing.T, depsYAML string) map[string]string {
	t.Helper()
	out := t.TempDir()
	depsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(depsDir, "dependencies.yaml"), []byte(depsYAML), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(depsDir)
	t.Cleanup(func() { packages.SetProtoDir("") })
	cfg := &tactype.Config{
		OutputDir:         out,
		TsPackageBase:     "demo-tacplus",
		AnnotatedProtoDir: t.TempDir(),
	}
	if err := writeLibTemplates(cfg, nil, nil, []string{cfg.ServiceLabel()}); err != nil {
		t.Fatalf("writeLibTemplates: %v", err)
	}
	manifests, err := filepath.Glob(filepath.Join(out, "chassis", "*", "package.json"))
	if err != nil {
		t.Fatalf("glob chassis manifests: %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("no chassis package.json emitted")
	}
	engines := make(map[string]string, len(manifests))
	for _, path := range manifests {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var manifest struct {
			Engines map[string]string `json:"engines"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("%s is not valid JSON: %v\n%s", path, err, raw)
		}
		engines[filepath.Base(filepath.Dir(path))] = manifest.Engines["node"]
	}
	return engines
}

// TestTypeScriptChassisEnginesNodeIsToolsNode: every generated chassis
// package.json declares the Node floor dependencies.yaml's tools.node states,
// so a service on an unsupported Node fails at install under engine-strict
// (#318; tacplus-dev/fleet-dev#20).
func TestTypeScriptChassisEnginesNodeIsToolsNode(t *testing.T) {
	const fleetNode = "^24.22.2 || >=26.0.0"
	engines := writeChassisWithDeps(t, `tools:
  node: '`+fleetNode+`'
lang:
  typescript:
    dependencies:
      "@bufbuild/protobuf": "^2.11.0"
`)
	for dir, node := range engines {
		if node != fleetNode {
			t.Errorf("chassis/%s/package.json engines.node = %q, want tools.node %q", dir, node, fleetNode)
		}
	}
}

// TestTypeScriptChassisEnginesNodeDefault: without tools.node the floor is
// defaultNodeEngine, at or above what the TypeScript pins require
// (@bufbuild/protoc-gen-es 2.16 needs Node >=22), never an end-of-life line.
func TestTypeScriptChassisEnginesNodeDefault(t *testing.T) {
	engines := writeChassisWithDeps(t, `lang:
  typescript:
    dependencies:
      "@bufbuild/protobuf": "^2.11.0"
`)
	for dir, node := range engines {
		if node != defaultNodeEngine {
			t.Errorf("chassis/%s/package.json engines.node = %q, want default %q", dir, node, defaultNodeEngine)
		}
	}
}

// TestTypeScriptChassisTemplatesTakeEnginesNodeFromData covers the chassis
// manifests a client-only build does not emit too: none hardcodes a Node
// floor.
func TestTypeScriptChassisTemplatesTakeEnginesNodeFromData(t *testing.T) {
	const want = `"node": {{printf "%q" .NodeEngine}}`
	err := fs.WalkDir(templates.FS, "typescript", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "package.json.tmpl" {
			return err
		}
		content, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), `"engines"`) && !strings.Contains(string(content), want) {
			t.Errorf("%s: engines.node must be %s (dependencies.yaml tools.node)", path, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}
