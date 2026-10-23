// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/registry"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// resolveDependencies turns tacplus.yaml `depends` entries into resolved proto
// directories, one per dependency, ready to merge into this generation.
//
// The authority is the global registry named by `global:` in the project's
// own registry lock, found in the first resolved source dir. Each name is
// looked up there (service or vservice); its repo is fetched at the entry's
// version (MAJOR.MINOR -> latest patch tag; none -> latest) and the repo's
// proto/ subdir convention applies as for any source.
func resolveDependencies(depends []string, firstSourceDir string) ([]string, []func(), error) {
	if len(depends) == 0 {
		return nil, nil, nil
	}
	lock, err := registry.Load(firstSourceDir)
	if err != nil {
		return nil, nil, fmt.Errorf("depends: reading this project's registry lock: %w", err)
	}
	if lock.Global == "" {
		return nil, nil, fmt.Errorf("depends: %s/registry.yaml names no global registry (global: <url>); dependencies are resolved by registry name and need the authority", firstSourceDir)
	}
	regRes, err := protosrc.Resolve(lock.Global, "latest")
	if err != nil {
		return nil, nil, fmt.Errorf("depends: resolving global registry %s: %w", lock.Global, err)
	}
	global, err := registry.Load(regRes.Dir)
	if err != nil {
		regRes.Cleanup()
		return nil, nil, fmt.Errorf("depends: loading global registry %s: %w", lock.Global, err)
	}
	var dirs []string
	cleanups := []func(){regRes.Cleanup}
	runCleanups := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, d := range depends {
		name, version := tacplusyaml.SplitDepend(d)
		repo, ok := global.RepoOf(name)
		if !ok {
			runCleanups()
			return nil, nil, fmt.Errorf("depends: %q is not in the global registry %s", name, lock.Global)
		}
		if repo == "" {
			runCleanups()
			return nil, nil, fmt.Errorf("depends: %q is registered without a repo; the registry must name where its proto lives", name)
		}
		tag := "latest"
		if version != "" {
			tag = version + ".latest"
		}
		dir, cleanup, err := resolveOneSource(repo, tag)
		if err != nil {
			runCleanups()
			return nil, nil, fmt.Errorf("depends: resolving %q from %s: %w", name, repo, err)
		}
		zap.L().Info("resolved dependency", zap.String("name", name), zap.String("repo", repo), zap.String("tag", tag), zap.String("dir", filepath.Clean(dir)))
		dirs = append(dirs, dir)
		cleanups = append(cleanups, cleanup)
	}
	return dirs, cleanups, nil
}
