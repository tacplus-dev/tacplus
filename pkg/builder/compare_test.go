// Copyright (c) 2026 Second Star Cavalier LLC

//go:build integration

package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// depsConfig mirrors the structure of dependencies.yaml for test loading.
type depsConfig struct {
	Registry tactype.RegistryConfig  `yaml:"registry,omitempty"`
	Tools    map[string]string       `yaml:"tools,omitempty"`
	Lang     tactype.LanguagesConfig `yaml:"lang,omitempty"`
}

// loadTestDepsYAML reads and parses a dependencies.yaml file, populating
// Lang.*.Interfaces from Registry.Interfaces (same logic as runner.go).
func loadTestDepsYAML(path string) (*depsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg depsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Registry.Interfaces != nil {
		if v, ok := cfg.Registry.Interfaces["go"]; ok {
			cfg.Lang.Go.Interfaces = v
		}
		if v, ok := cfg.Registry.Interfaces["java"]; ok {
			cfg.Lang.Java.Interfaces = v
		}
		if v, ok := cfg.Registry.Interfaces["typescript"]; ok {
			cfg.Lang.TypeScript.Interfaces = v
		}
		if v, ok := cfg.Registry.Interfaces["python"]; ok {
			cfg.Lang.Python.Interfaces = v
		}
		if v, ok := cfg.Registry.Interfaces["rust"]; ok {
			cfg.Lang.Rust.Interfaces = v
		}
	}
	if cfg.Registry.Imports != nil {
		if v, ok := cfg.Registry.Imports["java"]; ok {
			cfg.Lang.Java.InterfacesImport = v
		}
	}
	return &cfg, nil
}

// langSpec describes how to build and compare one language variant.
type langSpec struct {
	name       string // human label (e.g. "java")
	lang       string // builder language key
	compareSub string // subdirectory of OutputDir to compare (empty = root)
	configure  func(cfg *tactype.Config)
}

func TestCrossLanguageCompare(t *testing.T) {
	protoDir := os.Getenv("TACPLUS_COMPARE_PROTO_DIR")
	if protoDir == "" {
		t.Skip("set TACPLUS_COMPARE_PROTO_DIR to run cross-language generated-output comparison")
	}
	depsPath := filepath.Join(protoDir, "dependencies.yaml")

	deps, err := loadTestDepsYAML(depsPath)
	if err != nil {
		t.Fatalf("failed to load dependencies.yaml: %v", err)
	}

	// Common config fields shared across all languages.
	baseConfig := func(lang string) *tactype.Config {
		return &tactype.Config{
			Language:         lang,
			InputDir:         protoDir,
			ProtoDir:         protoDir,
			DepsYAMLPath:     depsPath,
			GrpcServerAll:    true,
			GrpcS2SClientAll: true,
			AllStates:        true,
			LocalMode:        true,
			VersionTag:       "0.0.0-SNAPSHOT",
			Lang:             deps.Lang,
			Tools:            deps.Tools,
			Registry:         deps.Registry,
		}
	}

	specs := []langSpec{
		{
			name: "go",
			lang: "go",
			configure: func(cfg *tactype.Config) {
				cfg.ModuleBasePath = "example.com/myproj/tacplus"
			},
		},
		{
			name:       "java",
			lang:       "java",
			compareSub: filepath.Join("src", "main", "java", "com", "example", "myproj", "tacplus"),
			configure: func(cfg *tactype.Config) {
				cfg.JavaPackageBase = "com.example.myproj.tacplus"
			},
		},
		{
			name: "typescript",
			lang: "typescript",
			configure: func(cfg *tactype.Config) {
				cfg.TsPackageBase = "@myclient-proto-tacplus"
			},
		},
		{
			name: "python",
			lang: "python",
			configure: func(cfg *tactype.Config) {
				cfg.PythonPackageBase = "proto-tacplus"
			},
		},
		{
			name: "rust",
			lang: "rust",
			configure: func(cfg *tactype.Config) {
				cfg.CrateBasePath = "proto-types-tacplus"
			},
		},
	}

	// Build all languages into temp dirs scoped to the parent test.
	type buildResult struct {
		spec       langSpec
		outDir     string
		compareDir string
	}
	results := make([]buildResult, 0, len(specs))

	for _, spec := range specs {
		// Use parent t.TempDir() so dirs survive across subtests.
		outDir := t.TempDir()
		cfg := baseConfig(spec.lang)
		cfg.OutputDir = outDir
		spec.configure(cfg)

		t.Logf("generating %s into %s", spec.name, outDir)
		b := NewBuilder(cfg)
		err := b.BuildLanguage(t.Context())
		b.Close()
		if err != nil {
			// Skip languages whose protoc plugin is not installed rather
			// than failing the entire test.
			if strings.Contains(err.Error(), "not found in PATH") {
				t.Logf("SKIP %s generation (missing tool): %v", spec.name, err)
				continue
			}
			t.Fatalf("BuildLanguage(%s) failed: %v", spec.name, err)
		}

		cmpDir := outDir
		if spec.compareSub != "" {
			cmpDir = filepath.Join(outDir, spec.compareSub)
		}

		results = append(results, buildResult{spec: spec, outDir: outDir, compareDir: cmpDir})
	}

	// Build verification subtests — compile generated code for each language.
	for _, res := range results {
		res := res
		t.Run("build/"+res.spec.name, func(t *testing.T) {
			switch {
			case res.spec.lang == "go":
				buildVerifyGo(t, res.outDir)
			case res.spec.lang == "java":
				buildVerifyJava(t, res.outDir)
			case res.spec.lang == "typescript":
				buildVerifyTypeScript(t, res.outDir)
			case res.spec.lang == "python":
				buildVerifyPython(t, res.outDir)
			case res.spec.lang == "rust":
				buildVerifyRust(t, res.outDir)
			}
		})
	}

	// Find Go result as reference.
	var goCompareDir string
	for _, res := range results {
		if res.spec.name == "go" {
			goCompareDir = res.compareDir
			break
		}
	}
	if goCompareDir == "" {
		t.Fatal("Go build result not found")
	}

	// Compare each non-Go language against Go.
	for _, res := range results {
		if res.spec.name == "go" {
			continue
		}
		res := res
		t.Run("compare/"+res.spec.name, func(t *testing.T) {
			onlyGo, onlyLang, libOnlyGo, libOnlyLang := compareOutputs(t, goCompareDir, res.compareDir)

			if len(onlyGo) > 0 {
				t.Errorf("files only in Go (missing from %s):", res.spec.name)
				for _, f := range onlyGo {
					t.Errorf("  %s", f)
				}
			}
			if len(onlyLang) > 0 {
				t.Errorf("files only in %s (missing from Go):", res.spec.name)
				for _, f := range onlyLang {
					t.Errorf("  %s", f)
				}
			}
			if len(libOnlyGo) > 0 {
				t.Errorf("lib dirs only in Go (missing from %s):", res.spec.name)
				for _, d := range libOnlyGo {
					t.Errorf("  sys/%s/", d)
				}
			}
			if len(libOnlyLang) > 0 {
				t.Errorf("lib dirs only in %s (missing from Go):", res.spec.name)
				for _, d := range libOnlyLang {
					t.Errorf("  sys/%s/", d)
				}
			}
			if len(onlyGo)+len(onlyLang)+len(libOnlyGo)+len(libOnlyLang) == 0 {
				t.Logf("All files match between Go and %s", res.spec.name)
			}
		})
	}
}

// ============================================================================
// Build verification helpers
// ============================================================================

// requireTool skips the subtest if the named executable is not on PATH.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH, skipping build verification", name)
	}
}

// runBuildCmd executes a command in dir and fails the test on non-zero exit.
func runBuildCmd(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	t.Logf("running: %s %s (in %s)", name, strings.Join(args, " "), dir)
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed:\n%s", name, strings.Join(args, " "), out)
	}
}

// buildVerifyGo creates a go.mod and runs go build on the generated output.
func buildVerifyGo(t *testing.T, outDir string) {
	t.Helper()
	requireTool(t, "go")

	// Bootstrap go.mod — the Go generator does not create one.
	goVer := strings.TrimPrefix(runtime.Version(), "go")
	if parts := strings.SplitN(goVer, ".", 3); len(parts) >= 2 {
		goVer = parts[0] + "." + parts[1]
	}
	goMod := fmt.Sprintf("module example.com/myproj/tacplus\n\ngo %s\n", goVer)
	if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	goEnv := []string{
		"GONOSUMDB=gitlab.com/seabeam/*",
		"GOPRIVATE=gitlab.com/seabeam/*",
	}
	runBuildCmd(t, outDir, goEnv, "go", "mod", "tidy")
	runBuildCmd(t, outDir, goEnv, "go", "build", "./...")
}

// buildVerifyJava runs mvn compile on the generated Maven project.
func buildVerifyJava(t *testing.T, outDir string) {
	t.Helper()
	requireTool(t, "mvn")

	javaHome := os.Getenv("JAVA_HOME")
	if javaHome == "" {
		// macOS: discover JAVA_HOME via java_home utility.
		out, err := exec.Command("/usr/libexec/java_home", "-v", "21").Output()
		if err == nil {
			javaHome = strings.TrimSpace(string(out))
		}
	}
	if javaHome == "" {
		t.Skip("JAVA_HOME not set and could not be discovered")
	}

	runBuildCmd(t, outDir, []string{"JAVA_HOME=" + javaHome}, "mvn", "compile", "-q", "-B", "-Dtacplus.skip=true")
}

// buildVerifyTypeScript runs npm install and tsc on each generated module.
func buildVerifyTypeScript(t *testing.T, outDir string) {
	t.Helper()
	requireTool(t, "npm")

	runBuildCmd(t, outDir, nil, "npm", "install")

	// Compile each module that has a tsconfig.json.
	var tscErrors []string
	_ = filepath.Walk(outDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "tsconfig.json" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, _ := filepath.Rel(outDir, dir)
		t.Logf("tsc --noEmit in %s", rel)
		cmd := exec.Command("npx", "tsc", "--noEmit")
		cmd.Dir = dir
		out, cerr := cmd.CombinedOutput()
		if cerr != nil {
			tscErrors = append(tscErrors, fmt.Sprintf("--- %s ---\n%s", rel, strings.TrimSpace(string(out))))
		}
		return nil
	})
	if len(tscErrors) > 0 {
		t.Fatalf("tsc failures:\n%s", strings.Join(tscErrors, "\n"))
	}
}

// buildVerifyPython syntax-checks all generated .py files.
func buildVerifyPython(t *testing.T, outDir string) {
	t.Helper()
	requireTool(t, "python3")

	var failed []string
	_ = filepath.Walk(outDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".py") {
			return nil
		}
		cmd := exec.Command("python3", "-m", "py_compile", path)
		out, cerr := cmd.CombinedOutput()
		if cerr != nil {
			rel, _ := filepath.Rel(outDir, path)
			failed = append(failed, fmt.Sprintf("  %s: %s", rel, strings.TrimSpace(string(out))))
		}
		return nil
	})
	if len(failed) > 0 {
		t.Fatalf("py_compile failures:\n%s", strings.Join(failed, "\n"))
	}
}

// buildVerifyRust runs cargo check on the generated Rust workspace.
func buildVerifyRust(t *testing.T, outDir string) {
	t.Helper()
	requireTool(t, "cargo")

	runBuildCmd(t, outDir, nil, "cargo", "check")
}

// ============================================================================
// Code-compare logic
// ============================================================================

type cmpFileEntry struct {
	rel        string
	normalized string
	inSys     bool
}

func compareOutputs(t *testing.T, dirA, dirB string) (onlyA, onlyB, libOnlyA, libOnlyB []string) {
	t.Helper()

	filesA, err := collectCompareFiles(dirA)
	if err != nil {
		t.Fatalf("collectFiles(%s): %v", dirA, err)
	}
	filesB, err := collectCompareFiles(dirB)
	if err != nil {
		t.Fatalf("collectFiles(%s): %v", dirB, err)
	}

	// Split into sys vs non-sys
	libDirsA := collectCompareLibDirs(filesA, dirA)
	libDirsB := collectCompareLibDirs(filesB, dirB)
	nonLibA := filterCompareNonLib(filesA)
	nonLibB := filterCompareNonLib(filesB)

	// Compare non-lib files
	for _, key := range mergedKeys(nonLibA, nonLibB) {
		entryA, okA := nonLibA[key]
		_, okB := nonLibB[key]
		switch {
		case okA && !okB:
			onlyA = append(onlyA, entryA.rel)
		case !okA && okB:
			onlyB = append(onlyB, nonLibB[key].rel)
		}
	}

	// Compare lib dirs
	for _, key := range mergedStringKeys(libDirsA, libDirsB) {
		_, okA := libDirsA[key]
		_, okB := libDirsB[key]
		switch {
		case okA && !okB:
			libOnlyA = append(libOnlyA, libDirsA[key])
		case !okA && okB:
			libOnlyB = append(libOnlyB, libDirsB[key])
		}
	}

	return
}

func collectCompareFiles(root string) (map[string]cmpFileEntry, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	result := make(map[string]cmpFileEntry)
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == "proto" || name == "examples" || name == "__pycache__" || name == "target" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		skipFiles := map[string]bool{
			"package.json":   true,
			"Cargo.toml":     true,
			"lib.rs":         true,
			"mod.rs":         true,
			"__init__.py":    true,
			"pyproject.toml": true,
			"index.ts":       true,
			"tsconfig.json":  true,
			".npmrc":         true,
			"traits.rs":      true,
			"go.mod":         true,
			"go.sum":         true,
			"pom.xml":        true,
		}
		if strings.HasSuffix(info.Name(), "_test.go") || skipFiles[info.Name()] {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		absPath, _ := filepath.Abs(path)
		inSys := cmpPathContainsSys(absPath, absRoot)
		normalized := cmpNormalizePath(rel)
		result[normalized] = cmpFileEntry{rel: rel, normalized: normalized, inSys: inSys}
		return nil
	})
	return result, err
}

var (
	pbRe  = regexp.MustCompile(`[._]pb\d*\.[a-zA-Z]+$`)
	extRe = regexp.MustCompile(`\.(go|java|ts|tsx|js|jsx|py|rs)$`)
)

func cmpNormalizePath(rel string) string {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)

	// Strip .pb.* or _pb.* before the final extension
	base = pbRe.ReplaceAllStringFunc(base, func(m string) string {
		i := strings.LastIndex(m, ".")
		return m[i:]
	})

	// Replace standard language extensions with .code
	if extRe.MatchString(base) {
		base = extRe.ReplaceAllString(base, ".code")
	}

	// Remove underscores, strip "OuterClass" suffix, and lowercase
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	name = strings.TrimSuffix(name, "OuterClass")
	name = cmpNormalize(name)

	// Strip "grpcclient" suffix
	name = strings.TrimSuffix(name, "grpcclient")

	// For gRPC stub files: normalize to {parent_dir}grpc
	if strings.HasSuffix(name, "grpc") && dir != "." {
		dirParts := strings.Split(filepath.ToSlash(dir), "/")
		parentDirName := cmpNormalize(dirParts[len(dirParts)-1])
		name = parentDirName + "grpc"
	}

	base = name + ext

	// Normalize directory segments
	if dir != "." {
		parts := strings.Split(filepath.ToSlash(dir), "/")
		for i, p := range parts {
			parts[i] = cmpNormalize(p)
		}
		dir = strings.Join(parts, "/")
		return dir + "/" + base
	}
	return base
}

func cmpNormalize(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

func cmpPathContainsSys(absPath, absRoot string) bool {
	parts := strings.Split(filepath.ToSlash(absPath), "/")
	for i, p := range parts {
		if strings.ToLower(p) == "sys" && i+2 < len(parts) {
			return true
		}
	}
	return false
}

func cmpLibChildKey(absPath string) (key string, original string) {
	parts := strings.Split(filepath.ToSlash(absPath), "/")
	for i, p := range parts {
		if strings.ToLower(p) == "sys" && i+2 < len(parts) {
			return strings.ToLower(strings.ReplaceAll(parts[i+1], "_", "")), parts[i+1]
		}
	}
	return "", ""
}

func collectCompareLibDirs(files map[string]cmpFileEntry, root string) map[string]string {
	result := make(map[string]string)
	for _, f := range files {
		if f.inSys {
			absPath, _ := filepath.Abs(filepath.Join(root, f.rel))
			key, orig := cmpLibChildKey(absPath)
			if key != "" {
				result[key] = orig
			}
		}
	}
	return result
}

func filterCompareNonLib(files map[string]cmpFileEntry) map[string]cmpFileEntry {
	result := make(map[string]cmpFileEntry)
	for k, f := range files {
		if !f.inSys {
			result[k] = f
		}
	}
	return result
}

func mergedKeys(a, b map[string]cmpFileEntry) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergedStringKeys(a, b map[string]string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
