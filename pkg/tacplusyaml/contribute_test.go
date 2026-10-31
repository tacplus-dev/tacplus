// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestContributeReadWithEveryRecipe(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, Filename), []byte(`language: go
output-dir: model
kind: service
contribute:
  webapp:
    dir: web
    title: Echo
  clients:
    - {name: echo, rust: echo}
    - {name: echo-go, go: ./cmd/echo}
    - {name: echo-mac, swift: EchoMac}
    - {name: echo-win, dotnet: win/EchoWin.csproj}
`), 0o644)
	cfg, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := cfg.Contribute
	if c == nil || c.Webapp == nil || c.Webapp.Dir != "web" || c.Webapp.Title != "Echo" || len(c.Clients) != 4 {
		t.Fatalf("contribute = %+v", c)
	}
	want := []struct {
		recipe, source string
		targets        []string
	}{
		{RecipeRust, "echo", []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}},
		{RecipeGo, "./cmd/echo", []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}},
		{RecipeSwift, "EchoMac", []string{"darwin/arm64"}},
		{RecipeDotnet, "win/EchoWin.csproj", []string{"windows/amd64"}},
	}
	for i, w := range want {
		r, s := c.Clients[i].Recipe()
		if r != w.recipe || s != w.source || !reflect.DeepEqual(RecipeTargets(r), w.targets) {
			t.Errorf("clients[%d]: recipe %q %q targets %v, want %q %q %v", i, r, s, RecipeTargets(r), w.recipe, w.source, w.targets)
		}
	}
}

func TestCheckContributeRefuses(t *testing.T) {
	web := &WebappContribution{Dir: "web", Title: "Echo"}
	for _, tc := range []struct {
		kind string
		c    *Contribute
		want string
	}{
		{"", &Contribute{Webapp: web}, "kind: service"},
		{KindClient, &Contribute{Webapp: web}, "kind: service"},
		{KindLibrary, &Contribute{Webapp: web}, "kind: service"},
		{KindService, &Contribute{}, "names nothing"},
		{KindService, &Contribute{Webapp: &WebappContribution{Title: "Echo"}}, "webapp.dir"},
		{KindService, &Contribute{Webapp: &WebappContribution{Dir: "../web", Title: "Echo"}}, "webapp.dir"},
		{KindService, &Contribute{Webapp: &WebappContribution{Dir: "/web", Title: "Echo"}}, "webapp.dir"},
		{KindService, &Contribute{Webapp: &WebappContribution{Dir: "web"}}, "webapp.title"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "Echo", Rust: "echo"}}}, "name"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "-echo", Rust: "echo"}}}, "name"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Rust: "echo"}, {Name: "echo", Go: "./cmd/echo"}}}, "twice"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo"}}}, "exactly one"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Rust: "echo", Go: "./cmd/echo"}}}, "exactly one"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Go: "cmd/echo"}}}, "./"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Go: "./../echo"}}}, "./"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Rust: "echo; rm -rf /"}}}, "rust"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Swift: "Echo Mac"}}}, "swift"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Dotnet: "win/EchoWin"}}}, ".csproj"},
		{KindService, &Contribute{Clients: []ClientContribution{{Name: "echo", Dotnet: "../EchoWin.csproj"}}}, ".csproj"},
	} {
		err := CheckContribute(tc.kind, tc.c)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckContribute(%q, %+v) = %v, want an error naming %q", tc.kind, tc.c, err, tc.want)
		}
	}
	if err := CheckContribute(KindService, &Contribute{Webapp: web, Clients: []ClientContribution{{Name: "echo", Rust: "echo"}}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckContribute(KindClient, nil); err != nil {
		t.Fatalf("no contribute is fine for any kind: %v", err)
	}
}

func TestContributeRefusedOnReadAndMerged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, Filename), []byte("language: go\noutput-dir: model\nkind: client\ncontribute:\n  webapp: {dir: web, title: Echo}\n"), 0o644)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "kind: service") {
		t.Fatalf("a client contributing must be refused, got %v", err)
	}
	base := &Config{Contribute: &Contribute{Webapp: &WebappContribution{Dir: "web", Title: "A"}}}
	if got := MergeConfigs(base, &Config{}); got.Contribute != base.Contribute {
		t.Fatal("an overlay without contribute keeps the base's")
	}
	over := &Config{Contribute: &Contribute{Clients: []ClientContribution{{Name: "x", Rust: "x"}}}}
	if got := MergeConfigs(base, over); got.Contribute != over.Contribute {
		t.Fatal("an overlay's contribute replaces the base's")
	}
}
