// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// A service contributes to its fleet's gateway (#195; contract accepted by
// Jody on 2026-10-30): a webapp module, served on the gateway's origin, and
// clients, served for download beside it:
//
//	kind: service
//	contribute:
//	  webapp:
//	    dir: web                   # an npm package: package.json and package-lock.json
//	    title: "Echo"              # its entry in the merged webapp's index
//	  clients:
//	    - {name: echo, rust: echo}                     # a cargo bin of this repo
//	    - {name: echo-go, go: ./cmd/echo}              # a Go main package of this repo
//	    - {name: echo-mac, swift: EchoMac}             # a SwiftPM executable product
//	    - {name: echo-win, dotnet: win/EchoWin.csproj} # a .NET project
//
// Nothing here is a command: each client names one fixed recipe, and the
// fleet builder runs that recipe, at the commit fleet.lock records for the
// service, in the pinned build image (or on the macOS runner). The service
// picks no mount point either: everything it contributes sits under its
// registry name, /-/apps/<service>/ and /-/clients/<service>/<client>/.
type Contribute struct {
	Webapp  *WebappContribution  `yaml:"webapp,omitempty"`
	Clients []ClientContribution `yaml:"clients,omitempty"`
}

// WebappContribution is a webapp module: an npm package in the service's repo,
// built as its own bundle and served at /-/apps/<service>/.
type WebappContribution struct {
	Dir   string `yaml:"dir"`
	Title string `yaml:"title"`
}

// ClientContribution is one client binary; exactly one recipe key is set.
type ClientContribution struct {
	Name   string `yaml:"name"`
	Rust   string `yaml:"rust,omitempty"`   // a cargo bin target: cargo build --release --locked --bin <rust>
	Go     string `yaml:"go,omitempty"`     // a Go main package, ./-relative: go build -trimpath <go>
	Swift  string `yaml:"swift,omitempty"`  // a SwiftPM executable product, built on the macOS runner
	Dotnet string `yaml:"dotnet,omitempty"` // a .csproj path: dotnet publish -r win-x64
}

// The client recipes.
const (
	RecipeRust   = "rust"
	RecipeGo     = "go"
	RecipeSwift  = "swift"
	RecipeDotnet = "dotnet"
)

// RecipeTargets is the fixed os/arch set a recipe builds, sorted. macOS is
// Apple silicon only: there is no darwin/amd64, now or later.
func RecipeTargets(recipe string) []string {
	switch recipe {
	case RecipeRust, RecipeGo:
		return []string{"darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"}
	case RecipeSwift:
		return []string{"darwin/arm64"}
	case RecipeDotnet:
		return []string{"windows/amd64"}
	}
	return nil
}

// Recipe returns the client's one recipe key and its value, or "" when it
// sets none (CheckContribute refuses that, and more than one).
func (c ClientContribution) Recipe() (recipe, source string) {
	for _, r := range c.recipes() {
		if r[1] != "" {
			return r[0], r[1]
		}
	}
	return "", ""
}

func (c ClientContribution) recipes() [][2]string {
	return [][2]string{{RecipeRust, c.Rust}, {RecipeGo, c.Go}, {RecipeSwift, c.Swift}, {RecipeDotnet, c.Dotnet}}
}

var (
	clientNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// A cargo bin or a SwiftPM product: a name, never a path or a command.
	targetNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// CheckContribute rejects a contribute section that is not a service's, names
// nothing, or a malformed entry. nil is fine for any kind.
func CheckContribute(kind string, c *Contribute) error {
	if c == nil {
		return nil
	}
	if kind != KindService {
		stated := "states no kind"
		if kind != "" {
			stated = fmt.Sprintf("is kind: %s", kind)
		}
		return fmt.Errorf("%s: contribute: only a kind: service project contributes to its fleet's gateway; this one %s", Filename, stated)
	}
	if c.Webapp == nil && len(c.Clients) == 0 {
		return fmt.Errorf("%s: contribute: names nothing; declare a webapp, clients, or remove it", Filename)
	}
	if w := c.Webapp; w != nil {
		if !isRepoRelative(w.Dir) {
			return fmt.Errorf("%s: contribute: webapp.dir %q must be a directory of this repo, relative, without ..", Filename, w.Dir)
		}
		if strings.TrimSpace(w.Title) == "" {
			return fmt.Errorf("%s: contribute: webapp.title is required: it is the module's entry in the merged webapp's index", Filename)
		}
	}
	seen := map[string]bool{}
	for i, cl := range c.Clients {
		if !clientNameRe.MatchString(cl.Name) {
			return fmt.Errorf("%s: contribute: clients[%d]: name %q must match [a-z][a-z0-9-]*", Filename, i, cl.Name)
		}
		if seen[cl.Name] {
			return fmt.Errorf("%s: contribute: clients names %q twice", Filename, cl.Name)
		}
		seen[cl.Name] = true
		var set []string
		for _, r := range cl.recipes() {
			if r[1] != "" {
				set = append(set, r[0])
			}
		}
		if len(set) != 1 {
			return fmt.Errorf("%s: contribute: client %q must set exactly one recipe (%s, %s, %s or %s), got %d", Filename, cl.Name, RecipeRust, RecipeGo, RecipeSwift, RecipeDotnet, len(set))
		}
		switch recipe, src := cl.Recipe(); recipe {
		case RecipeRust, RecipeSwift:
			if !targetNameRe.MatchString(src) {
				return fmt.Errorf("%s: contribute: client %q: %s: %q must be a target name, not a path or a command", Filename, cl.Name, recipe, src)
			}
		case RecipeGo:
			if !strings.HasPrefix(src, "./") || !isRepoRelative(src) {
				return fmt.Errorf("%s: contribute: client %q: go: %q must be a main package of this repo, ./-relative, without ..", Filename, cl.Name, src)
			}
		case RecipeDotnet:
			if !strings.HasSuffix(src, ".csproj") || !isRepoRelative(src) {
				return fmt.Errorf("%s: contribute: client %q: dotnet: %q must be a .csproj of this repo, relative, without ..", Filename, cl.Name, src)
			}
		}
	}
	return nil
}

// isRepoRelative reports whether p is a non-empty relative path that stays
// inside the repo.
func isRepoRelative(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(path.Clean(p), "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
