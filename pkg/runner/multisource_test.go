// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

func TestEffectiveSources(t *testing.T) {
	tests := []struct {
		name string
		p    Params
		cfg  *tacplusyaml.Config
		want []tacplusyaml.Source
	}{
		{
			name: "explicit --src overrides yaml entirely",
			p:    Params{ProtoSrcs: []string{"/flag/a", "/flag/b"}, SrcExplicit: true},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary", Sources: []tacplusyaml.Source{{Src: "/yaml/extra"}}},
			want: []tacplusyaml.Source{{Src: "/flag/a"}, {Src: "/flag/b"}},
		},
		{
			name: "yaml src + sources union when not explicit",
			p:    Params{},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary", Sources: []tacplusyaml.Source{{Src: "/yaml/kernel"}, {Src: "/yaml/base"}}},
			want: []tacplusyaml.Source{{Src: "/yaml/primary"}, {Src: "/yaml/kernel"}, {Src: "/yaml/base"}},
		},
		{
			name: "yaml layers ride with their sources",
			p:    Params{},
			cfg:  &tacplusyaml.Config{Src: "/yaml/app", Layer: "app", Sources: []tacplusyaml.Source{{Src: "/yaml/platform", Layer: "platform"}, {Src: "/yaml/base"}}},
			want: []tacplusyaml.Source{{Src: "/yaml/app", Layer: "app"}, {Src: "/yaml/platform", Layer: "platform"}, {Src: "/yaml/base"}},
		},
		{
			name: "yaml src only (back-compat single source)",
			p:    Params{},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary"},
			want: []tacplusyaml.Source{{Src: "/yaml/primary"}},
		},
		{
			name: "empty everything -> nil (sys-only)",
			p:    Params{},
			cfg:  &tacplusyaml.Config{},
			want: nil,
		},
		{
			name: "empty flag entries filtered out",
			p:    Params{ProtoSrcs: []string{"", "/flag/a", ""}, SrcExplicit: true},
			cfg:  nil,
			want: []tacplusyaml.Source{{Src: "/flag/a"}},
		},
		{
			name: "explicit but no flag values -> nil (sys-only), yaml ignored",
			p:    Params{SrcExplicit: true},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary"},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveSources(tt.p, tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("effectiveSources() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResolveConfigCarriesSourceLayers pins that the layer named on src and on a
// sources entry reaches the generator config as a per-file map, for both the
// merged (several sources) and single-source paths.
func TestResolveConfigCarriesSourceLayers(t *testing.T) {
	root := t.TempDir()
	mk := func(dir, rel string) string {
		p := filepath.Join(root, dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(root, dir)
	}
	plat := mk("plat", "kernel/kernel.proto")
	app := mk("app", "fleet/fleet.proto")
	for _, d := range []string{plat, app} {
		if err := os.WriteFile(filepath.Join(d, "dependencies.yaml"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := t.TempDir()
	t.Chdir(project)

	cfg := &tacplusyaml.Config{
		Language: "go", Pkg: "com.example.fleet", OutputDir: ".",
		Src: app, Layer: "app",
		Sources: []tacplusyaml.Source{{Src: plat, Layer: "platform"}},
	}
	rc, err := resolveConfig(Params{Config: cfg})
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	defer rc.cleanup()
	want := map[string]string{"fleet/fleet.proto": "app", "kernel/kernel.proto": "platform"}
	if !reflect.DeepEqual(rc.config.SourceLayers, want) {
		t.Errorf("SourceLayers = %v, want %v", rc.config.SourceLayers, want)
	}

	one, err := resolveConfig(Params{Config: &tacplusyaml.Config{
		Language: "go", Pkg: "com.example.fleet", OutputDir: ".", Src: app, Layer: "app",
	}})
	if err != nil {
		t.Fatalf("resolveConfig single: %v", err)
	}
	defer one.cleanup()
	if got := one.config.SourceLayers["fleet/fleet.proto"]; got != "app" {
		t.Errorf("single-source layer = %q, want app", got)
	}
}

// TestBuildCarriesSourceLayersToRegistry runs a whole Go build over two layered
// sources and reads the layer back out of registry.json. The unit tests above
// stop at the config; this one fails if any hop between the runner and the
// registry generator drops the layer.
func TestBuildCarriesSourceLayersToRegistry(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	fixture := filepath.Join("..", "..", "cross-test", "proto-c2sonly")
	deps, err := os.ReadFile(filepath.Join(fixture, "dependencies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	copyTree := func(sub, dst string) string {
		d := filepath.Join(root, dst)
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Join(fixture, sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(fixture, sub, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, sub, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(d, "dependencies.yaml"), deps, 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	plat := copyTree("platform", "plat")
	app := copyTree("svc", "app")

	project := t.TempDir()
	t.Chdir(project)
	err = Build(Params{ProtoTag: "latest", Config: &tacplusyaml.Config{
		Language: "go", Pkg: "com.example.fleet", OutputDir: "model",
		Src: app, Layer: "app",
		Sources: []tacplusyaml.Source{{Src: plat, Layer: "platform"}},
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(project, "model", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if g, ok := x["grpc_service"].(string); ok {
				s, _ := x["source"].(string)
				got[g] = s
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc)
	if got["c2sonly.C2SOnlyApi"] != "app" || got["c2sonlyhealth.ServiceHealthStateService"] != "platform" {
		t.Errorf("service sources = %v, want app for C2SOnlyApi and platform for the health service", got)
	}
}
