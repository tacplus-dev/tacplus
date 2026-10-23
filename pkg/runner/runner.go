// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/builder"
	"github.com/tacplus-dev/tacplus/pkg/depfiles"
	"github.com/tacplus-dev/tacplus/pkg/gittag"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/pkg/projectfile"
	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// Params holds the inputs for Build.
// Zero values are valid — config is read from tacplus.yaml when fields are empty.
type Params struct {
	ProtoTag string
	// ProtoSrcs is the list of proto source specs (each a local dir or git URL)
	// passed via repeatable --src. When SrcExplicit is true this list overrides
	// the tacplus.yaml src/sources; otherwise the yaml provides the sources.
	ProtoSrcs   []string
	SrcExplicit bool
	Verbose     bool
	Config      *tacplusyaml.Config
}

// depsYAML represents the structure of dependencies.yaml
type depsYAML struct {
	Registry tactype.RegistryConfig  `yaml:"registry,omitempty"`
	Tools    map[string]string       `yaml:"tools,omitempty"`
	Lang     tactype.LanguagesConfig `yaml:"lang,omitempty"`
}

// resolvedConfig holds the resolved configuration and proto source cleanup function.
type resolvedConfig struct {
	config  *tactype.Config
	tag     string // resolved proto tag (for logging)
	cleanup func()

	// yamlConfig is the fully merged tacplus.yaml config that drove this
	// resolution (all -c overlays merged, import: bases resolved). The build
	// hash is derived from THIS so it tracks the same config generation uses,
	// not a fresh single-file re-read of cwd/tacplus.yaml.
	yamlConfig *tacplusyaml.Config
}

// Build resolves config and generates types for the detected language.
func Build(p Params) error {
	rc, err := resolveConfig(p)
	if err != nil {
		return err
	}
	defer rc.cleanup()

	// Compute hash of inputs (binary + config + proto files + project source).
	// The config input is the fully merged yamlConfig (rc.yamlConfig), the same
	// config generation consumes — NOT a fresh re-read of cwd/tacplus.yaml.
	currentHash, err := computeBuildHash(rc.config.ProtoDir, rc.config.ProjectDir, rc.config.OutputDir, rc.config.Language, rc.yamlConfig)
	if err != nil {
		zap.L().Warn("Could not compute build hash, proceeding with full build", zap.Error(err))
	}

	// Check cache: if .hash.json matches, skip generation entirely.
	if currentHash != nil {
		if existing, err := readHashFile(rc.config.OutputDir); err == nil {
			if existing.equal(currentHash) {
				zap.L().Info("Build cache hit — inputs unchanged, skipping generation",
					zap.String("output", rc.config.OutputDir))
				return nil
			}
			existing.diff(currentHash)
		}

		// Inputs changed (or no cache): the builder clears the previous output
		// once validation has passed.
		rc.config.ClearOutput = true
	}

	zap.L().Info("Building", zap.String("language", rc.config.Language),
		zap.String("proto-dir", rc.config.ProtoDir), zap.String("proto-tag", rc.tag))

	b := builder.NewBuilder(rc.config)
	defer b.Close()
	if err := b.BuildLanguage(context.Background()); err != nil {
		return err
	}

	// Write .hash.json after successful generation.
	if currentHash != nil {
		if err := writeHashFile(rc.config.OutputDir, currentHash); err != nil {
			zap.L().Warn("Failed to write build hash", zap.Error(err))
		}
	}

	return nil
}

// effectiveSources computes the ordered proto source list from params + config.
// An explicit --src (repeatable) overrides the yaml entirely; otherwise the yaml
// contributes src (primary) followed by sources (additional). Empty entries are
// dropped; an empty result means sys-only mode.
func effectiveSources(p Params, cfg *tacplusyaml.Config) []string {
	if p.SrcExplicit {
		return filterNonEmpty(p.ProtoSrcs)
	}
	var srcs []string
	if cfg != nil {
		if cfg.Src != "" {
			srcs = append(srcs, cfg.Src)
		}
		srcs = append(srcs, filterNonEmpty(cfg.Sources)...)
	}
	return srcs
}

// filterNonEmpty returns the input with empty strings removed.
func filterNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// anyGitSource reports whether any source spec looks like a git source (vs a
// local directory), which determines whether a resolvable tag is required.
func anyGitSource(srcs []string) bool {
	for _, s := range srcs {
		if protosrc.IsGitSource(s) {
			return true
		}
	}
	return false
}

// resolveOneSource resolves a single proto source spec to a local directory,
// applying the proto/-subdir convention (a repo whose protos live under proto/
// with their own dependencies.yaml). Git sources resolve against tag's
// MAJOR.MINOR; local sources ignore the tag.
func resolveOneSource(src, tag string) (string, func(), error) {
	protoResolveTag := tag
	if protosrc.IsGitSource(src) {
		protoResolveTag = gittag.ToMinorLatest(tag)
	}
	result, err := protosrc.Resolve(src, protoResolveTag)
	if err != nil {
		return "", nil, err
	}
	dir := result.Dir
	protoSubdir := filepath.Join(dir, "proto")
	if _, err := os.Stat(filepath.Join(protoSubdir, "dependencies.yaml")); err == nil {
		dir = protoSubdir
	}
	return dir, result.Cleanup, nil
}

// resolveConfig reads tacplus.yaml, resolves proto source, detects language,
// and builds the unified tactype.Config. The caller must call rc.cleanup() when done.
func resolveConfig(p Params) (*resolvedConfig, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	tag := p.ProtoTag

	// Load config: use pre-loaded config if provided, otherwise auto-discover tacplus.yaml
	var yamlPkg string
	var cfg *tacplusyaml.Config
	if p.Config != nil {
		cfg = p.Config
	} else if tacplusyaml.Exists(cwd) {
		var err error
		cfg, err = tacplusyaml.Read(cwd)
		if err != nil {
			return nil, err
		}
	}
	headerText := ""
	if cfg != nil {
		headerText = cfg.Header
	}
	header.SetCopyright(headerText)
	if cfg != nil {
		yamlPkg = cfg.Pkg
		// clients.*.enabled is obsolete. Generation is a function of the proto
		// graph, not YAML flags: declare (spec.grpc_c2s_client)/(spec.grpc_w2s_client)
		// for C2S/W2S client surfaces and (spec.service_c2d) for C2D.
		if !cfg.Clients.IsZero() {
			return nil, fmt.Errorf("clients.c2s/w2s/c2d.enabled is obsolete; declare client dependencies in proto — (spec.grpc_c2s_client)/(spec.grpc_w2s_client) for C2S/W2S and (spec.service_c2d) for C2D. Tacplus generates legal client surfaces from proto declarations and target-language policy, not YAML flags")
		}
	}

	// Effective proto source list. Explicit --src (repeatable) overrides the yaml
	// entirely; otherwise the yaml contributes src (primary) + sources
	// (additional). Multiple sources are merged into one proto tree below.
	srcs := effectiveSources(p, cfg)

	// If no proto source was specified (by flag or yaml), use sys-only mode:
	// skip proto resolution and just generate standard library wrappers.
	sysOnly := len(srcs) == 0

	// Derive tag: CLI --tag > git tag > fallback
	if tag == "" {
		detected, err := gittag.Detect(cwd)
		if err != nil {
			if sysOnly || !anyGitSource(srcs) {
				// Local or sys-only — tag only affects version stamping, use "latest"
				tag = "latest"
				zap.L().Info("No git tag found; using tag \"latest\"")
			} else {
				return nil, fmt.Errorf("no git tag found and --tag not specified; tag a release (git tag v1.0.0) or pass --tag explicitly")
			}
		} else {
			tag = detected
			zap.L().Info("Auto-detected tag from git", zap.String("tag", tag))
		}
	}
	resolvedVersion, localMode := resolveProtoTag(tag)

	// Resolve proto source. In sys-only mode use an empty temp dir so the
	// builder has a valid (empty) input; all lib wrappers come from embedded protos.
	var absProtoDir string
	var protoCleanup func()
	if sysOnly {
		tempDir, err := os.MkdirTemp("", "tacplus-sys-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp proto dir: %w", err)
		}
		// Write an empty dependencies.yaml so the builder's path validation passes.
		if err := os.WriteFile(filepath.Join(tempDir, "dependencies.yaml"), []byte("{}\n"), 0644); err != nil {
			os.RemoveAll(tempDir)
			return nil, fmt.Errorf("failed to write temp dependencies.yaml: %w", err)
		}
		absProtoDir = tempDir
		protoCleanup = func() { os.RemoveAll(tempDir) }
		zap.L().Info("No proto source configured — generating standard library wrappers only")
	} else if len(srcs) == 1 && (cfg == nil || len(cfg.Depends) == 0) {
		// Single source: resolve directly (no merge), preserving the original
		// fast path exactly.
		dir, cleanup, err := resolveOneSource(srcs[0], tag)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve proto source: %w", err)
		}
		absProtoDir = dir
		protoCleanup = cleanup
	} else {
		// Multiple sources: resolve each (each with its own proto/-subdir probe)
		// and merge them into one working proto root. Everything downstream still
		// sees a single dir, so the generator blast radius is nil.
		var dirs []string
		var cleanups []func()
		runCleanups := func() {
			for _, c := range cleanups {
				c()
			}
		}
		for _, s := range srcs {
			dir, cleanup, err := resolveOneSource(s, tag)
			if err != nil {
				runCleanups()
				return nil, fmt.Errorf("failed to resolve proto source %q: %w", s, err)
			}
			dirs = append(dirs, dir)
			cleanups = append(cleanups, cleanup)
		}
		// Declared dependencies (tacplus.yaml depends) join the merge: their
		// protos are what the declared S2S clients generate from.
		if cfg != nil && len(cfg.Depends) > 0 {
			depDirs, depCleanups, err := resolveDependencies(cfg.Depends, dirs[0])
			if err != nil {
				runCleanups()
				return nil, err
			}
			dirs = append(dirs, depDirs...)
			cleanups = append(cleanups, depCleanups...)
		}
		mergedDir, err := os.MkdirTemp("", "tacplus-merged-*")
		if err != nil {
			runCleanups()
			return nil, fmt.Errorf("failed to create merged proto dir: %w", err)
		}
		if err := protosrc.MergeSources(dirs, mergedDir); err != nil {
			os.RemoveAll(mergedDir)
			runCleanups()
			return nil, fmt.Errorf("failed to merge proto sources: %w", err)
		}
		absProtoDir = mergedDir
		protoCleanup = func() {
			os.RemoveAll(mergedDir)
			runCleanups()
		}
		zap.L().Info("merged multiple proto sources into one generation",
			zap.Int("sources", len(srcs)), zap.String("merged-dir", mergedDir))
	}

	// Language must be declared in tacplus.yaml
	if cfg == nil || cfg.Language == "" {
		protoCleanup()
		return nil, fmt.Errorf("tacplus.yaml must declare a language")
	}
	pt := languageToProjectType(cfg.Language)
	if pt == projectfile.ProjectUnknown {
		protoCleanup()
		return nil, fmt.Errorf("unsupported language %q in tacplus.yaml", cfg.Language)
	}
	lang := pt.Language()

	// Load dependencies.yaml from proto dir. In sys-only mode skip this entirely
	// since there is no proto repo and no deps to load.
	depsYAMLPath := filepath.Join(absProtoDir, "dependencies.yaml")
	var depsCfg *depsYAML
	if sysOnly {
		depsCfg = &depsYAML{}
	} else {
		var err error
		depsCfg, err = loadDepsYAML(depsYAMLPath)
		if err != nil {
			protoCleanup()
			return nil, fmt.Errorf("failed to load dependencies.yaml: %w", err)
		}

		// Populate Lang.*.Interfaces from Registry.Interfaces map
		if depsCfg.Registry.Interfaces != nil {
			if v, ok := depsCfg.Registry.Interfaces["go"]; ok {
				depsCfg.Lang.Go.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["java"]; ok {
				depsCfg.Lang.Java.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["typescript"]; ok {
				depsCfg.Lang.TypeScript.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["python"]; ok {
				depsCfg.Lang.Python.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["rust"]; ok {
				depsCfg.Lang.Rust.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["swift"]; ok {
				depsCfg.Lang.Swift.Interfaces = v
			}
			if v, ok := depsCfg.Registry.Interfaces["csharp"]; ok {
				depsCfg.Lang.CSharp.Interfaces = v
			}
		}

		// Populate Lang.*.InterfacesImport from Registry.Imports map
		if depsCfg.Registry.Imports != nil {
			if v, ok := depsCfg.Registry.Imports["java"]; ok {
				depsCfg.Lang.Java.InterfacesImport = v
			}
		}

		// Ensure dependencies.yaml exists
		if err := depfiles.EnsureDependencyFiles(absProtoDir); err != nil {
			protoCleanup()
			return nil, fmt.Errorf("dependency files: %w", err)
		}
	}

	// Resolve the generated-output segment name (default "model"), overridable
	// via tacplus.yaml output-name. The compiler is "tacplus"; only the generated
	// model package/dir is named by this.
	outputName := tactype.DefaultOutputName
	if cfg != nil && cfg.OutputName != "" {
		outputName = cfg.OutputName
	}

	// Compute output directory based on language/project type
	outDir := resolveOutputDir(cwd, pt, outputName)

	// Apply output-dir override from tacplus.yaml.
	// resolveOutputDir always appends the output-name segment, so strip a trailing
	// output-name component from the explicit output-dir to avoid double nesting
	// (e.g. output-dir "model" would otherwise become "model/model").
	if cfg != nil && cfg.OutputDir != "" {
		base := cfg.OutputDir
		if filepath.Base(base) == outputName {
			base = filepath.Dir(base)
		}
		outDir = resolveOutputDir(filepath.Join(cwd, base), pt, outputName)
	}

	// The computed output directory must stay inside the project directory.
	// output-dir/output-name come from tacplus.yaml and the resolved tree is
	// removed with os.RemoveAll before regeneration, so a value like
	// "../sibling" would delete an unrelated tree outside the project.
	contained, err := outputDirWithinProject(cwd, outDir)
	if err != nil {
		protoCleanup()
		return nil, err
	}
	if !contained {
		protoCleanup()
		spec := fmt.Sprintf("output-name %q", outputName)
		if cfg != nil {
			spec = fmt.Sprintf("output-dir %q / output-name %q", cfg.OutputDir, outputName)
		}
		return nil, fmt.Errorf("tacplus.yaml %s resolves the output directory to %q, which is outside the project directory %q; tacplus deletes and regenerates the output directory, so it must be contained within the project", spec, outDir, cwd)
	}

	// Derive package base. The pkg field in tacplus.yaml uses canonical Java-style
	// reverse DNS format (e.g. "com.example.services.identity").
	// canonicalPkgToNative converts it to the native format for the target language.
	var pkg string
	if yamlPkg != "" {
		// Fail fast: a hyphen (or other non-identifier character) in a pkg
		// segment is invalid for every target's derived package/module/crate
		// identifier and selfPackage. Catching it here avoids partial generation
		// that emits broken artifacts (e.g. `crate::types::gitlab-connector::…`).
		if err := validateCanonicalPkg(yamlPkg, lang); err != nil {
			protoCleanup()
			return nil, err
		}
		pkg = canonicalPkgToNative(yamlPkg, pt.Language(), outputName)
	} else {
		pkg = projectfile.DerivePackage(pt, cwd)
	}

	// Create unified tactype.Config
	config := &tactype.Config{
		ProtoDir:     absProtoDir,
		Language:     lang,
		Lang:         depsCfg.Lang,
		Tools:        depsCfg.Tools,
		Registry:     depsCfg.Registry,
		Verbose:      p.Verbose,
		InputDir:     absProtoDir,
		OutputDir:    outDir,
		OutputName:   outputName,
		ProjectDir:   cwd,
		DepsYAMLPath: depsYAMLPath,
		VersionTag:   resolvedVersion,
		LocalMode:    localMode,
		SelfPackage:  selfPackageFromCanonical(yamlPkg),
		SysOnly:      sysOnly,
	}

	// Set language-specific package base
	switch lang {
	case "go":
		config.ModuleBasePath = goModuleBasePath(pkg, cwd, outDir, outputName)
	case "java":
		config.JavaPackageBase = pkg
	case "typescript":
		config.TsPackageBase = pkg
	case "python":
		config.PythonPackageBase = pkg
	case "rust":
		config.CrateBasePath = pkg
	case "swift":
		config.SwiftPackageBase = pkg
	case "csharp":
		config.CSharpPackageBase = pkg
	}

	if lang == "java" && config.Lang.Java.InterfacesImport == "" {
		protoCleanup()
		return nil, fmt.Errorf("Java import base not configured; set registry.imports.java in dependencies.yaml (the Java import package for interfaces, e.g. com.example.proto.interfaces)")
	}

	return &resolvedConfig{config: config, tag: tag, cleanup: protoCleanup, yamlConfig: cfg}, nil
}

// languageToProjectType maps a tacplus.yaml language string to a ProjectType.
func languageToProjectType(lang string) projectfile.ProjectType {
	switch lang {
	case "go":
		return projectfile.ProjectGo
	case "java":
		return projectfile.ProjectJava
	case "typescript":
		return projectfile.ProjectTypeScript
	case "python":
		return projectfile.ProjectPython
	case "rust":
		return projectfile.ProjectRust
	case "swift":
		return projectfile.ProjectSwift
	case "csharp":
		return projectfile.ProjectCSharp
	default:
		return projectfile.ProjectUnknown
	}
}

// resolveOutputDir returns the absolute output directory based on project type.
// The conventional subdir is the configured output name (default "model").
func resolveOutputDir(cwd string, projectType projectfile.ProjectType, outputName string) string {
	return filepath.Join(cwd, outputName)
}

// outputDirWithinProject reports whether outDir resolves to a path inside cwd.
// Both are resolved to absolute, cleaned paths and compared on path boundaries
// via filepath.Rel, so a sibling such as "/foo/barbaz" is not mistaken for a
// path contained in "/foo/bar".
func outputDirWithinProject(cwd, outDir string) (bool, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false, fmt.Errorf("failed to resolve project directory %q: %w", cwd, err)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return false, fmt.Errorf("failed to resolve output directory %q: %w", outDir, err)
	}
	rel, err := filepath.Rel(absCwd, absOut)
	if err != nil {
		// No relative path exists (e.g. different volumes) — not contained.
		return false, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// goModuleBasePath derives the Go import root for the generated tree. The
// canonical pkg maps to the cwd Go module root, and canonicalPkgToNative appends
// the output-name segment to give <module>/<output-name>. That is only the
// correct import root when the generated tree sits directly under the module
// root (the default layout, where outDir == cwd/<output-name>).
//
// When output-dir nests the tree below an extra directory not represented in the
// canonical pkg (e.g. output-dir: "tacplus" → files at cwd/tacplus/<output-name>),
// the files are reachable as <module>/tacplus/<output-name>, so the import root
// must carry that prefix too. Otherwise the generated imports resolve to a path
// that does not exist on disk and Go tries to fetch them as remote modules
// (CNA-001). We rebuild the import root from the module root plus the actual
// relative path from cwd to outDir.
func goModuleBasePath(pkg, cwd, outDir, outputName string) string {
	rel, err := filepath.Rel(cwd, outDir)
	if err != nil {
		return pkg
	}
	rel = filepath.ToSlash(rel)
	// Default layout (outDir == cwd/<output-name>): pkg already correct.
	if rel == "" || rel == "." || rel == outputName {
		return pkg
	}
	// Replace the trailing output-name segment with the full relative path so the
	// import root matches the on-disk layout.
	if strings.HasSuffix(pkg, "/"+outputName) {
		return strings.TrimSuffix(pkg, "/"+outputName) + "/" + rel
	}
	return pkg
}

// pkgSegmentRe matches a single valid canonical package segment: an identifier
// that starts with a letter or underscore and contains only letters, digits, and
// underscores. The canonical pkg is documented as Java-style reverse DNS, whose
// segments are identifiers — a hyphen (or any other character) is invalid for the
// derived package/module/crate identifier and selfPackage in every target.
var pkgSegmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// javaReservedWords are the Java keywords, boolean/null literals, and the lone
// underscore — none of which may appear as a package-name segment.
//
// Java is the only target that needs this check: canonicalPkgToNative emits the
// canonical pkg VERBATIM as the Java package root (`com.example.foo` + `.model`),
// so every segment lands in a `package …;` declaration in every generated file.
// The other targets use only the LAST segment and always suffix it
// (`foo-model`, `foo_model`, `FooModel`), or — for Go — use the segments as
// import-path elements, which are plain strings; none of those can collide with
// a reserved word. Do not add speculative per-language lists here for paths that
// cannot produce a bare reserved identifier.
var javaReservedWords = map[string]bool{
	"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true,
	"case": true, "catch": true, "char": true, "class": true, "const": true,
	"continue": true, "default": true, "do": true, "double": true, "else": true,
	"enum": true, "extends": true, "final": true, "finally": true, "float": true,
	"for": true, "goto": true, "if": true, "implements": true, "import": true,
	"instanceof": true, "int": true, "interface": true, "long": true, "native": true,
	"new": true, "package": true, "private": true, "protected": true, "public": true,
	"return": true, "short": true, "static": true, "strictfp": true, "super": true,
	"switch": true, "synchronized": true, "this": true, "throw": true, "throws": true,
	"transient": true, "try": true, "void": true, "volatile": true, "while": true,
	"true": true, "false": true, "null": true, "_": true,
}

// validateCanonicalPkg checks that the canonical pkg from tacplus.yaml produces
// valid identifiers for the target language before generation begins. It rejects
// empty segments (leading/trailing/consecutive dots) and any segment that is not
// a valid identifier, so a bad pkg fails fast with a clear message instead of
// partially generating broken output (e.g. a Rust crate path
// `crate::types::gitlab-connector::…` from a `gitlab-connector` segment).
func validateCanonicalPkg(canonical, lang string) error {
	for _, seg := range strings.Split(canonical, ".") {
		if seg == "" {
			return fmt.Errorf("tacplus.yaml pkg %q has an empty segment (no leading/trailing dots or consecutive dots allowed)", canonical)
		}
		if !pkgSegmentRe.MatchString(seg) {
			return fmt.Errorf("tacplus.yaml pkg %q has invalid segment %q for %s generation: each segment must be a valid identifier (letters, digits, underscores; not starting with a digit). Hyphens are not allowed — e.g. use %q instead of %q",
				canonical, seg, lang, strings.ReplaceAll(seg, "-", ""), seg)
		}
		// A segment that is a valid identifier can still be a Java reserved word.
		// Java emits the canonical pkg verbatim into `package …;`, so e.g.
		// `com.example.class` compiles to nothing — javac rejects every generated
		// file. Fail here rather than after the whole tree has been written.
		if lang == "java" && javaReservedWords[seg] {
			return fmt.Errorf("tacplus.yaml pkg %q has segment %q, which is a Java reserved word and cannot appear in a package declaration; rename the segment (e.g. %q)",
				canonical, seg, seg+"_")
		}
	}
	return nil
}

// canonicalPkgToNative converts a canonical Java-style reverse DNS package name
// (e.g. "com.example.services.identity") to the native
// package format for the given language, with the generated-output segment
// (outputName, e.g. "model") appended. The compiler is still "tacplus"; only the
// generated model package is named by outputName.
func canonicalPkgToNative(canonical, lang, outputName string) string {
	parts := strings.Split(canonical, ".")
	pascalOut := pascalCaseWord(outputName) // e.g. "model" → "Model"
	switch lang {
	case "java":
		return canonical + "." + outputName
	case "go":
		// Reverse the TLD prefix: com.gitlab → gitlab.com
		// Then join the rest with "/"
		if len(parts) >= 2 {
			host := parts[1] + "." + parts[0] // e.g. gitlab.com
			// A two-segment canonical pkg (e.g. "com.example") has no path
			// remainder, so joining host + "/" + "" + "/" + outputName would emit
			// an empty path element ("example.com//model"). That is not a legal Go
			// import path, and every generated import would carry it. Build the
			// segments and join, so the empty remainder simply drops out.
			segs := append([]string{host}, parts[2:]...)
			segs = append(segs, outputName)
			return strings.Join(segs, "/")
		}
		return canonical + "/" + outputName
	case "rust", "typescript":
		return parts[len(parts)-1] + "-" + outputName
	case "python":
		return parts[len(parts)-1] + "_" + outputName
	case "swift":
		return swiftModuleName(parts[len(parts)-1]) + pascalOut
	case "csharp":
		// C# assembly/namespace uses the same PascalCase transform as Swift.
		return swiftModuleName(parts[len(parts)-1]) + pascalOut
	default:
		return canonical
	}
}

// pascalCaseWord upper-cases the first letter of a single lowercase word
// (e.g. "model" → "Model") for Swift/C# module-name suffixes.
func pascalCaseWord(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func swiftModuleName(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func resolveProtoTag(tag string) (string, bool) {
	if tag == "latest" {
		zap.L().Info("using --tag 'latest' — building from HEAD, version set to 0.0.0-SNAPSHOT")
		return "0.0.0-SNAPSHOT", true
	}

	// X.Y.latest → treat as local/snapshot mode for the minor version
	if strings.HasSuffix(tag, ".latest") {
		base := strings.TrimSuffix(tag, ".latest")
		base = strings.TrimPrefix(base, "v")
		zap.L().Info("using latest patch for minor version", zap.String("base", base))
		return base, true
	}

	semverFull := regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	semverMinor := regexp.MustCompile(`^v?\d+\.\d+$`)

	if semverFull.MatchString(tag) {
		return strings.TrimPrefix(tag, "v"), false
	}
	if semverMinor.MatchString(tag) {
		return strings.TrimPrefix(tag, "v"), false
	}

	zap.L().Warn("--tag format not recognized, using as-is", zap.String("tag", tag))
	return tag, false
}

// selfPackageFromCanonical extracts the proto package name (last segment) from
// a canonical Java-style package (e.g. "com.gitlab...services.gateway" → "gateway").
func selfPackageFromCanonical(canonical string) string {
	if canonical == "" {
		return ""
	}
	if idx := strings.LastIndex(canonical, "."); idx >= 0 {
		return canonical[idx+1:]
	}
	return canonical
}

func loadDepsYAML(path string) (*depsYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg depsYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
