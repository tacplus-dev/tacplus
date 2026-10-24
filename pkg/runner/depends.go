// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

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
// its dependencies.yaml, and with its registry lock reduced to wire facts
// (names, ids, slice fields). A dependency contributes its protos and the
// ids they are generated against; its language and tool pins, and its lock's
// metadata (which registries it names, where contracts live, visibility),
// are its own repo's business and must not collide with this project's.
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
		if rel == "registry.yaml" {
			data, err = wireOnlyLock(data)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
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

// wireOnlyLock reduces a registry.yaml to the facts generation needs from a
// dependency: each entry's name, id or field_id, and slice fields. The
// `registries:`, `scope:`, `id_min:` keys and the contract/visibility notes
// are dropped so two locks that agree on the wire never collide on metadata.
func wireOnlyLock(data []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]any{}
	keep := map[string]bool{"name": true, "id": true, "field_id": true, "tenant_slice_field": true, "project_slice_field": true}
	for _, section := range []string{"services", "vservices"} {
		list, ok := doc[section].([]any)
		if !ok {
			continue
		}
		var entries []any
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			kept := map[string]any{}
			for k, v := range m {
				if keep[k] {
					kept[k] = v
				}
			}
			entries = append(entries, kept)
		}
		out[section] = entries
	}
	return yaml.Marshal(out)
}

// ProtoDirWithDepends returns a directory holding protoDir's protos together
// with the protos of the depends declared in the tacplus.yaml beside it (the
// project root is protoDir's parent). With no depends it returns protoDir
// itself. Used wherever a proto tree is scanned outside generation, so a
// proto that imports its dependencies' protos resolves.
func ProtoDirWithDepends(protoDir string) (string, func(), error) {
	noop := func() {}
	root := filepath.Dir(protoDir)
	if !tacplusyaml.Exists(root) {
		return protoDir, noop, nil
	}
	cfg, err := tacplusyaml.Read(root)
	if err != nil {
		return "", nil, err
	}
	if len(cfg.Depends) == 0 {
		return protoDir, noop, nil
	}
	depDirs, cleanups, err := resolveDependencies(cfg.Depends, protoDir)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	// A dependency contributes its protos to the scan, not its lock: the lock
	// being written is this tree's own.
	for _, d := range depDirs {
		_ = os.Remove(filepath.Join(d, "registry.yaml"))
	}
	merged, err := os.MkdirTemp("", "tacplus-scan-*")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	all := append([]string{protoDir}, depDirs...)
	if err := protosrc.MergeSources(all, merged); err != nil {
		os.RemoveAll(merged)
		cleanup()
		return "", nil, fmt.Errorf("merging depends for scan: %w", err)
	}
	return merged, func() { os.RemoveAll(merged); cleanup() }, nil
}

// OwnDeclarations keeps, of the scanned names, those declared by files that
// live in protoDir itself (not in a merged dependency).
func OwnDeclarations(protoDir string, services, vservices []string) ([]string, []string) {
	own := map[string]bool{}
	_ = filepath.WalkDir(protoDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		src := string(data)
		for _, re := range []*regexp.Regexp{ownServiceNameRe, ownVServiceNameRe} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				own[m[1]] = true
			}
		}
		// A (spec.service) option without a name, or no option at all on a
		// file that declares a gRPC service or a grpc_config, identifies the
		// service by its package, as the generator does.
		if ownServiceNoNameRe.MatchString(src) || ownServiceBlockRe.MatchString(src) || ownGrpcConfigRe.MatchString(src) {
			if pm := ownPackageRe.FindStringSubmatch(src); pm != nil {
				own[pm[1]] = true
			}
		}
		return nil
	})
	keep := func(in []string) []string {
		var out []string
		for _, n := range in {
			if own[n] {
				out = append(out, n)
			}
		}
		return out
	}
	return keep(services), keep(vservices)
}

var (
	ownServiceNameRe   = regexp.MustCompile(`(?s)\(\s*spec\.service\s*\)\s*=\s*\{[^}]*\bname\s*:\s*"([^"]+)"`)
	ownVServiceNameRe  = regexp.MustCompile(`(?s)\(\s*spec\.vservice\s*\)\s*=\s*\{[^}]*\bname\s*:\s*"([^"]+)"`)
	ownServiceNoNameRe = regexp.MustCompile(`(?s)\(\s*spec\.service\s*\)\s*=\s*\{[^}]*\}`)
	ownPackageRe       = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][\w.]*)\s*;`)
	ownServiceBlockRe  = regexp.MustCompile(`(?m)^\s*service\s+[A-Za-z_][A-Za-z0-9_]*\s*\{`)
	ownGrpcConfigRe    = regexp.MustCompile(`\(\s*spec\.grpc_config\s*\)`)
)
