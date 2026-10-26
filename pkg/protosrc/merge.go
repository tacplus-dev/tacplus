// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package protosrc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// mergedMetaFiles are proto-root metadata files merged SEMANTICALLY (deep YAML
// union) across sources rather than required to be byte-identical. They live at
// the proto root and legitimately differ per source (each source contributes its
// own language deps), so a plain byte-compare would wrongly reject them.
var mergedMetaFiles = map[string]bool{
	"dependencies.yaml": true,
	"registry.yaml":     true,
}

// MergeSources unions the proto trees of multiple resolved source directories
// into destDir (created if needed). Rules:
//
//   - Every regular file is copied to destDir at its source-relative path.
//   - A file already written at that path by an earlier source must be
//     byte-identical (it is deduplicated); a genuine content conflict is a hard
//     error. This is deliberate: it is what forces a shared base (sys/, common/)
//     to be factored into ONE source instead of vendored differently in several,
//     which is exactly the collision the multi-source model must not paper over.
//   - dependencies.yaml / registry.yaml at the proto root are merged with a deep
//     YAML union; conflicting scalar values for the same key are a hard error.
//
// The merge is order-independent: the resulting file set and bytes are identical
// regardless of source order, so a build hash computed over destDir is stable.
func MergeSources(sourceDirs []string, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create merge dir: %w", err)
	}
	// origin records which source first wrote each relative path, for clear
	// conflict messages.
	origin := map[string]string{}
	// metaDocs accumulates parsed YAML for the semantically-merged meta files,
	// keyed by relative path.
	metaDocs := map[string]map[string]any{}

	for _, src := range sourceDirs {
		walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Skip hidden paths entirely (.git, .gitlab-ci.yml, .DS_Store, ...).
			if base := filepath.Base(path); strings.HasPrefix(base, ".") && path != src {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			// Merge only what generation consumes: proto files, yaml metadata,
			// and a fleet build's lock at the root (the compiler reads each
			// service's depends from it when it scopes a generation). Everything
			// else (READMEs, release scripts, CI config) is per-repo housekeeping
			// that must not cause cross-source conflicts.
			switch filepath.Ext(path) {
			case ".proto", ".yaml", ".yml":
			default:
				if rel != "fleet.lock" {
					return nil
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			// Root-level meta files merge semantically.
			if filepath.Dir(rel) == "." && mergedMetaFiles[filepath.Base(rel)] {
				doc := map[string]any{}
				if err := yaml.Unmarshal(data, &doc); err != nil {
					return fmt.Errorf("parse %s from %q: %w", rel, src, err)
				}
				if existing, ok := metaDocs[rel]; ok {
					merged, err := mergeYAML(existing, doc, filepath.Base(rel))
					if err != nil {
						return fmt.Errorf("merging %s across sources %q and %q: %w", rel, origin[rel], src, err)
					}
					metaDocs[rel] = merged
				} else {
					metaDocs[rel] = doc
					origin[rel] = src
				}
				return nil
			}

			// Regular files: dedup-identical, error on real conflict.
			destPath := filepath.Join(destDir, rel)
			if prevSrc, ok := origin[rel]; ok {
				existing, err := os.ReadFile(destPath)
				if err != nil {
					return err
				}
				if !bytes.Equal(existing, data) {
					return fmt.Errorf("proto file %q conflicts between sources %q and %q: same path, different contents — factor the shared file into a single source", rel, prevSrc, src)
				}
				return nil // identical: deduplicated
			}
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return err
			}
			origin[rel] = src
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}

	// Write the semantically-merged meta files.
	for rel, doc := range metaDocs {
		out, err := yaml.Marshal(doc)
		if err != nil {
			return fmt.Errorf("marshal merged %s: %w", rel, err)
		}
		destPath := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destPath, out, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// mergeYAML deep-merges overlay into base. Maps merge key-by-key; a value present
// in both with differing (non-map) content is a conflict. ctx is the dotted key
// path for error messages.
func mergeYAML(base, overlay map[string]any, ctx string) (map[string]any, error) {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, ov := range overlay {
		bv, exists := out[k]
		if !exists {
			out[k] = ov
			continue
		}
		bm, bIsMap := bv.(map[string]any)
		om, oIsMap := ov.(map[string]any)
		if bIsMap && oIsMap {
			m, err := mergeYAML(bm, om, ctx+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = m
			continue
		}
		// registry.yaml's id_min is an ALLOCATION FLOOR ("never auto-assign
		// below this"), not a value two sources have to agree on. Each app
		// proto repo sets its own so its services land clear of the platform's
		// and of every other app's, which means a build merging two app sources
		// legitimately sees two different floors. The merged allocator must
		// respect both, so the merge is max() — and because every already-named
		// service carries an explicit id in the lists below, raising the floor
		// renumbers nothing. Demanding equality here is what made
		// platform+testapp+scotty (60 vs 70) unbuildable.
		if ctx+"."+k == "registry.yaml.id_min" {
			bn, bOK := asInt64(bv)
			on, oOK := asInt64(ov)
			if bOK && oOK {
				if on > bn {
					out[k] = ov
				}
				continue
			}
		}
		// Sequences of named entries (registry.yaml services/vservices, each a
		// {name, id|field_id, ...} map) union by name across sources, so a base
		// and an app registry can each own their own service IDs.
		bs, bIsSeq := bv.([]any)
		os, oIsSeq := ov.([]any)
		if bIsSeq && oIsSeq && isNamedList(bs) && isNamedList(os) {
			merged, err := mergeNamedList(bs, os, ctx+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = merged
			continue
		}
		if !reflect.DeepEqual(bv, ov) {
			return nil, fmt.Errorf("conflicting values for %q: %v vs %v", ctx+"."+k, bv, ov)
		}
	}
	return out, nil
}

// isNamedList reports whether every element of the sequence is a map carrying a
// "name" key — the shape of registry.yaml's services/vservices lists.
func isNamedList(seq []any) bool {
	if len(seq) == 0 {
		return false
	}
	for _, v := range seq {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := m["name"].(string); !ok {
			return false
		}
	}
	return true
}

// idFields are the numeric-ID keys whose values must be globally unique across a
// merged named list (a wire field ID assigned to two different names is a bug).
var idFields = []string{"id", "field_id"}

// mergeNamedList unions two sequences of {name, ...} maps by their name. An entry
// present in both sources must agree on every key both carry (same name → same
// definition), else it is a conflict; the keys only one side carries are kept,
// so a project's full entry (contract, visibility) and a dependency's wire-only
// mention of the same service (name, id — see wireOnlyLock) merge into the
// full one. After the union, each id/field_id must map to exactly one name,
// else it is an ID collision. Output is sorted by name for determinism.
func mergeNamedList(base, overlay []any, ctx string) ([]any, error) {
	byName := map[string]map[string]any{}
	add := func(v any) error {
		e := v.(map[string]any) // isNamedList guaranteed map-with-name
		name := e["name"].(string)
		if prev, ok := byName[name]; ok {
			merged := map[string]any{}
			for k, pv := range prev {
				merged[k] = pv
			}
			for k, ev := range e {
				if pv, both := prev[k]; both && !reflect.DeepEqual(pv, ev) {
					return fmt.Errorf("%s entry %q differs between sources: %v vs %v", ctx, name, prev, e)
				}
				merged[k] = ev
			}
			byName[name] = merged
			return nil
		}
		byName[name] = e
		return nil
	}
	for _, v := range base {
		if err := add(v); err != nil {
			return nil, err
		}
	}
	for _, v := range overlay {
		if err := add(v); err != nil {
			return nil, err
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, idKey := range idFields {
		seen := map[int64]string{}
		for _, name := range names {
			id, ok := asInt64(byName[name][idKey])
			if !ok {
				continue
			}
			if other, dup := seen[id]; dup {
				return nil, fmt.Errorf("%s: %s %d assigned to both %q and %q — "+
					"each proto repo allocates from its own registry, so set id_min "+
					"in the app registry.yaml (e.g. id_min: 60) and re-run tacplus "+
					"registry there so its services allocate above the platform's",
					ctx, idKey, id, other, name)
			}
			seen[id] = name
		}
	}

	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out, nil
}

// asInt64 coerces a YAML-decoded numeric value to int64.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// FileLayers maps each proto file's source-relative path to the layer name of
// the source directory it lives in. layers is parallel to sourceDirs; a file
// found in several sources (a deduplicated identical copy) belongs to the first
// source that carries it, whether or not that source names a layer. Files from
// unlayered sources are left out, so a build that names no layers yields nil.
func FileLayers(sourceDirs, layers []string) map[string]string {
	claimed := map[string]bool{}
	var out map[string]string
	for i, src := range sourceDirs {
		layer := ""
		if i < len(layers) {
			layer = layers[i]
		}
		_ = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if base := filepath.Base(path); strings.HasPrefix(base, ".") && path != src {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() || filepath.Ext(path) != ".proto" {
				return nil
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if claimed[rel] {
				return nil
			}
			claimed[rel] = true
			if layer != "" {
				if out == nil {
					out = map[string]string{}
				}
				out[rel] = layer
			}
			return nil
		})
	}
	return out
}
