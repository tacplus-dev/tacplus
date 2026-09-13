// Copyright (c) 2026 Second Star Cavalier LLC

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/tacplusyaml"
)

const hashFileName = ".hash.json"

// buildHash represents the cached state of a tacplus generation run.
type buildHash struct {
	BinaryHash    string            `json:"binary_hash"`
	ConfigHash    string            `json:"config_hash"`
	StructureHash string            `json:"structure_hash"` // hash of the recursive file listing
	Files         map[string]string `json:"files"`          // relative path → content hash
	SrcHash       string            `json:"src_hash"`       // hash of project source files
}

// srcExcludes lists directory names that are never part of source.
// These are build outputs, dependency caches, and tacplus-generated dirs.
var srcExcludes = map[string]bool{
	"tacplus":      true, // default tacplus output dir
	"target":       true, // Rust / Maven / Gradle build output
	"bin":          true, // compiled binaries
	"dist":         true, // JS/TS dist output
	"build":        true, // generic build dir
	"node_modules": true, // JS dependencies
	".git":         true,
	".idea":        true,
	".vscode":      true,
}

// srcExtensions maps language name to the file extensions that constitute source.
var srcExtensions = map[string][]string{
	"go":         {".go", "go.mod", "go.sum"},
	"rust":       {".rs", "Cargo.toml", "Cargo.lock"},
	"java":       {".java", "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"},
	"python":     {".py", "pyproject.toml", "setup.py", "requirements.txt"},
	"typescript": {".ts", ".tsx", "package.json", "tsconfig.json"},
	"swift":      {".swift", "Package.swift"},
	"csharp":     {".cs", ".csproj"},
}

// isSrcFile reports whether path should be included in the source hash for lang.
func isSrcFile(path, lang string) bool {
	base := filepath.Base(path)
	exts, ok := srcExtensions[lang]
	if !ok {
		// Unknown language: hash everything non-hidden.
		return !strings.HasPrefix(base, ".")
	}
	for _, ext := range exts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// computeSrcHash returns a hex SHA-256 over all source files in projectDir for
// the given language, excluding known build-output directories and the tacplus
// output directory (outputDir).
func computeSrcHash(projectDir, outputDir, language string) (string, error) {
	absOutput, _ := filepath.Abs(outputDir)

	var listing []string
	err := filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Surface walk errors (e.g. permission denied) rather than
			// silently hashing a subset of the source tree.
			return err
		}
		base := filepath.Base(path)
		if info.IsDir() {
			// Skip excluded dirs and the tacplus output dir.
			if srcExcludes[base] {
				return filepath.SkipDir
			}
			if abs, _ := filepath.Abs(path); abs == absOutput {
				return filepath.SkipDir
			}
			return nil
		}
		if isSrcFile(path, language) {
			listing = append(listing, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk src dir: %w", err)
	}

	sort.Strings(listing)
	h := sha256.New()
	for _, path := range listing {
		rel, _ := filepath.Rel(projectDir, path)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		fh, err := hashFile(path)
		if err != nil {
			// A file we listed but cannot hash means the computed digest
			// would not reflect the true source tree — fail instead of
			// returning a hash that can spuriously match the cache.
			return "", fmt.Errorf("hash src file %s: %w", path, err)
		}
		h.Write([]byte(fh))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// computeBuildHash computes a buildHash from the running binary, the merged
// tacplus config (cfg), all .proto files in protoDir, and all source files in
// projectDir for language. cfg is the fully resolved config generation consumes
// (all -c overlays merged, import: bases resolved), so a change in any overlay or
// imported base invalidates the cache.
func computeBuildHash(protoDir, projectDir, outputDir, language string, cfg *tacplusyaml.Config) (*buildHash, error) {
	h := &buildHash{
		Files: make(map[string]string),
	}

	// Hash the tacplus binary itself.
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	binHash, err := hashFile(exe)
	if err != nil {
		return nil, fmt.Errorf("hash binary: %w", err)
	}
	h.BinaryHash = binHash

	// Hash the merged tacplus config (config changes require regeneration).
	//
	// Hash the SEMANTIC config, not the raw file bytes: the raw file embeds
	// build-local paths (absolute `src:` / `output-dir:`) that are explicit
	// non-inputs to generation. Hashing raw bytes would leak the absolute
	// project path into .hash.json and make two generations of the same proto
	// graph from different working directories non-identical. Normalize the
	// path-bearing fields to their basenames so a move/rename of the build dir
	// no longer changes the config hash, while genuine semantic config changes
	// (language, pkg, output-name, clients, import) still invalidate the cache.
	//
	// cfg is the MERGED config (the same one generation uses), so hashing it —
	// rather than re-reading cwd/tacplus.yaml — means an edit to a `-c` overlay
	// or an `import:`ed base config also invalidates the cache.
	cfgHash, err := hashConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("hash config: %w", err)
	}
	h.ConfigHash = cfgHash

	// Walk all files in the proto source directory recursively.
	// Track every file (not just .proto) so that directory structure and
	// non-proto file changes (e.g., dependencies.yaml) are detected.
	var listing []string
	err = filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Surface walk errors rather than silently hashing a subset
			// of the proto tree.
			return err
		}
		rel, err := filepath.Rel(protoDir, path)
		if err != nil {
			return nil
		}
		// Only include .proto files, dependencies.yaml, and registry files.
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(rel)
		if !strings.HasSuffix(base, ".proto") && base != "dependencies.yaml" &&
			base != "registry.yaml" {
			return nil
		}
		listing = append(listing, rel)
		fh, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash proto file %s: %w", path, err)
		}
		h.Files[rel] = fh
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk proto dir: %w", err)
	}

	// Hash the sorted listing so renames/moves/adds/deletes of any file
	// or directory invalidate the cache even if no content changed.
	sort.Strings(listing)
	structureHasher := sha256.New()
	for _, entry := range listing {
		structureHasher.Write([]byte(entry))
		structureHasher.Write([]byte{0})
	}
	h.StructureHash = hex.EncodeToString(structureHasher.Sum(nil))

	// Hash project source files so that code changes (without proto changes)
	// also invalidate the cache and trigger a rebuild.
	// An unhashable source tree must not degrade to an empty SrcHash: two
	// consecutive failing runs would both record "" and compare equal, which
	// is the same false-cache-hit this guards against. Fail so the caller
	// falls through to a full rebuild.
	srcHash, err := computeSrcHash(projectDir, outputDir, language)
	if err != nil {
		return nil, fmt.Errorf("hash project source: %w", err)
	}
	h.SrcHash = srcHash

	return h, nil
}

// hashConfig returns a deterministic hash of the SEMANTIC merged tacplus config.
// cfg must be the fully merged config generation consumes (all -c overlays
// merged, import: bases resolved) so any overlay/base change is reflected.
// Path-bearing fields (Src, OutputDir, Import) are normalized to their basenames
// so the hash is independent of the absolute build location — those paths are
// explicit non-inputs to generation. Returns "" when cfg is nil.
func hashConfig(cfg *tacplusyaml.Config) (string, error) {
	if cfg == nil {
		return "", nil
	}
	// Copy before normalizing so we don't mutate the caller's config.
	normalized := *cfg
	// Normalize build-local paths to basenames so a moved/renamed build
	// directory does not change the config hash.
	normalized.Src = filepath.Base(filepath.ToSlash(normalized.Src))
	normalized.OutputDir = filepath.Base(filepath.ToSlash(normalized.OutputDir))
	normalized.Import = filepath.Base(filepath.ToSlash(normalized.Import))

	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal config for hashing: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// equal returns true if two buildHash values are identical.
func (h *buildHash) equal(other *buildHash) bool {
	if h.BinaryHash != other.BinaryHash {
		return false
	}
	if h.ConfigHash != other.ConfigHash {
		return false
	}
	if h.StructureHash != other.StructureHash {
		return false
	}
	if h.SrcHash != other.SrcHash {
		return false
	}
	if len(h.Files) != len(other.Files) {
		return false
	}
	for k, v := range h.Files {
		if other.Files[k] != v {
			return false
		}
	}
	return true
}

// diff logs what changed between two buildHash values.
func (h *buildHash) diff(other *buildHash) {
	if h.BinaryHash != other.BinaryHash {
		zap.L().Info("tacplus binary changed")
	}
	if h.ConfigHash != other.ConfigHash {
		zap.L().Info("tacplus.yaml changed")
	}
	if h.StructureHash != other.StructureHash {
		zap.L().Info("proto directory structure changed")
	}
	if h.SrcHash != other.SrcHash {
		zap.L().Info("project source changed")
	}

	// Collect all keys.
	allKeys := make(map[string]bool)
	for k := range h.Files {
		allKeys[k] = true
	}
	for k := range other.Files {
		allKeys[k] = true
	}
	sorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		oldHash, inOld := h.Files[k]
		newHash, inNew := other.Files[k]
		if !inOld {
			zap.L().Info("proto added", zap.String("file", k))
		} else if !inNew {
			zap.L().Info("proto removed", zap.String("file", k))
		} else if oldHash != newHash {
			zap.L().Info("proto changed", zap.String("file", k))
		}
	}
}

// readHashFile reads a buildHash from the .hash.json file in outputDir.
func readHashFile(outputDir string) (*buildHash, error) {
	data, err := os.ReadFile(filepath.Join(outputDir, hashFileName))
	if err != nil {
		return nil, err
	}
	var h buildHash
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// writeHashFile writes a buildHash to .hash.json in outputDir.
func writeHashFile(outputDir string, h *buildHash) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, hashFileName), data, 0644)
}

// hashFile returns the hex-encoded SHA-256 hash of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
