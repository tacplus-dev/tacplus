// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// A service's tacplus.yaml may declare what it contributes to the fleet's
// gateway (tacplusyaml.Contribute, #195). The fleet build reads it at the
// commit it locks for the service, so a contribution has no version of its
// own: a new one reaches a fleet only by a new release of the service and a
// re-lock. Everything a service contributes sits under its registry name,
// which the unioned registries already make unique in the fleet, and under
// the /-/ prefix no proto route can take (routes are /{package}/{Service}/{Rpc}
// and no proto identifier starts with -).
//
// The build checks here that each recipe's inputs are in the checkout and
// records the contribution in fleet.lock. Building the modules and clients
// and the gateway image from the lock is `tacplus fleet gateway`'s.

// AppsPrefix and ClientsPrefix are where the gateway serves contributions.
const (
	AppsPrefix    = "/-/apps/"
	ClientsPrefix = "/-/clients/"
	// AppsIndex is the merged webapp's index of modules.
	AppsIndex = "/-/apps.json"
)

// LockedContribution is what one service contributes, as locked.
type LockedContribution struct {
	Webapp  *LockedWebapp  `yaml:"webapp,omitempty"`
	Clients []LockedClient `yaml:"clients,omitempty"`
}

// LockedWebapp is a webapp module: its directory at the service's locked
// commit, that directory's git tree, and where the gateway serves it.
type LockedWebapp struct {
	Dir   string `yaml:"dir"`
	Title string `yaml:"title"`
	Tree  string `yaml:"tree"`
	Path  string `yaml:"path"`
}

// LockedClient is one contributed client: its recipe, what the recipe builds,
// the fixed targets, and where the gateway serves it (<path><os>/<arch>).
type LockedClient struct {
	Name    string   `yaml:"name"`
	Recipe  string   `yaml:"recipe"`
	Source  string   `yaml:"source"`
	Targets []string `yaml:"targets"`
	Path    string   `yaml:"path"`
}

// lockContribution checks a service's contribution against its checkout at
// the locked commit and returns its lock entry; nil when it contributes
// nothing.
func lockContribution(service, repoDir string, c *tacplusyaml.Contribute) (*LockedContribution, error) {
	if c == nil {
		return nil, nil
	}
	need := func(rel, why string) error {
		if _, err := os.Stat(filepath.Join(repoDir, filepath.FromSlash(rel))); err != nil {
			return fmt.Errorf("fleet: service %q contributes %s, but its release has no %s", service, why, rel)
		}
		return nil
	}
	out := &LockedContribution{}
	if w := c.Webapp; w != nil {
		dir := path.Clean(w.Dir)
		for _, f := range []string{"package.json", "package-lock.json"} {
			if err := need(dir+"/"+f, "a webapp module (npm ci builds it from its lockfile)"); err != nil {
				return nil, err
			}
		}
		out.Webapp = &LockedWebapp{Dir: dir, Title: w.Title, Tree: treeOf(repoDir, dir), Path: AppsPrefix + service + "/"}
	}
	for _, cl := range c.Clients {
		recipe, src := cl.Recipe()
		why := fmt.Sprintf("client %q (%s: %s)", cl.Name, recipe, src)
		var inputs []string
		switch recipe {
		case tacplusyaml.RecipeRust:
			inputs = []string{"Cargo.toml", "Cargo.lock"}
		case tacplusyaml.RecipeGo:
			inputs = []string{"go.mod", path.Clean(src)}
		case tacplusyaml.RecipeSwift:
			inputs = []string{"Package.swift"}
		case tacplusyaml.RecipeDotnet:
			inputs = []string{path.Clean(src), path.Join(path.Dir(path.Clean(src)), "packages.lock.json")}
		}
		for _, f := range inputs {
			if err := need(f, why); err != nil {
				return nil, err
			}
		}
		out.Clients = append(out.Clients, LockedClient{
			Name: cl.Name, Recipe: recipe, Source: src, Targets: tacplusyaml.RecipeTargets(recipe),
			Path: ClientsPrefix + service + "/" + cl.Name + "/",
		})
	}
	return out, nil
}

// treeOf is the git tree of rel at the checkout's HEAD, or "local" for a
// plain dir.
func treeOf(repoDir, rel string) string {
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD:"+rel).Output()
	if err != nil {
		return "local"
	}
	return strings.TrimSpace(string(out))
}
