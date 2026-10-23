// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/registry"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// resolveDependencies turns tacplus.yaml `depends` entries into resolved proto
// directories, one per dependency, ready to merge into this generation.
//
// The authorities are the registries named by `registries:` in the project's
// own registry lock, found in the first resolved source dir, unioned. Each name
// is looked up there (service or vservice); its CONTRACT repo (where its proto
// lives, which need not be the implementation a fleet runs) is fetched at the
// entry's version (MAJOR.MINOR -> latest patch tag; none -> latest) and the
// repo's proto/ subdir convention applies as for any source.
func resolveDependencies(depends []string, firstSourceDir string) ([]string, []func(), error) {
	if len(depends) == 0 {
		return nil, nil, nil
	}
	lock, err := registry.Load(firstSourceDir)
	if err != nil {
		return nil, nil, fmt.Errorf("depends: reading this project's registry lock: %w", err)
	}
	if !lock.IsLock() {
		return nil, nil, fmt.Errorf("depends: %s/registry.yaml names no registries (registries: [<url>, ...]); dependencies are resolved by registry name and need the authorities", firstSourceDir)
	}
	global, regCleanup, err := registry.LoadAuthorities(lock.Registries, func(ref string) (string, func(), error) {
		res, err := protosrc.Resolve(ref, "latest")
		if err != nil {
			return "", nil, err
		}
		return res.Dir, res.Cleanup, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("depends: %w", err)
	}
	var dirs []string
	cleanups := []func(){regCleanup}
	runCleanups := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for _, d := range depends {
		name, version := tacplusyaml.SplitDepend(d)
		repo, ok := global.ContractOf(name)
		if !ok {
			runCleanups()
			return nil, nil, fmt.Errorf("depends: %q is not in any registry this project names (%s)", name, strings.Join(lock.Registries, ", "))
		}
		if repo == "" {
			runCleanups()
			return nil, nil, fmt.Errorf("depends: %q is registered without a contract; the registry must name where its proto lives", name)
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
		staged, stagedCleanup, err := stageDependency(dir)
		if err != nil {
			cleanup()
			runCleanups()
			return nil, nil, fmt.Errorf("depends: staging %q: %w", name, err)
		}
		zap.L().Info("resolved dependency", zap.String("name", name), zap.String("repo", repo), zap.String("tag", tag), zap.String("dir", filepath.Clean(dir)))
		dirs = append(dirs, staged)
		cleanups = append(cleanups, cleanup, stagedCleanup)
	}
	return dirs, cleanups, nil
}

// stageDependency copies a dependency's proto tree into a temp dir without
// its dependencies.yaml. A dependency contributes its protos (and its
// registry lock, which must agree with ours); its language and tool pins are
// its own build's business and must not collide with this project's.
func stageDependency(dir string) (string, func(), error) {
	staged, err := os.MkdirTemp("", "tacplus-dep-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(staged) }
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if rel != "." && d.Name()[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "dependencies.yaml" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(staged, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return staged, cleanup, nil
}
