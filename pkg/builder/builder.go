// Copyright (c) 2026 Second Star Cavalier LLC

package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/formatter"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/metadata"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/packages"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	csharptypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/csharp/types"
	gotypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/go/types"
	javatypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/java/types"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	pythontypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/python/types"
	rusttypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/rust/types"
	swifttypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/swift/types"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	typescripttypes "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/typescript/types"
)

// Builder orchestrates the entire build process for protobuf code generation
type Builder struct {
	config      *tactype.Config
	flatTempDir string // temp dir for FlatProtoDir (cleaned up on Close)
}

// NewBuilder creates a new Builder with the given configuration
func NewBuilder(cfg *tactype.Config) *Builder {
	return &Builder{config: cfg}
}

// Close cleans up temporary resources created by the builder.
func (b *Builder) Close() {
	if b.flatTempDir != "" {
		os.RemoveAll(b.flatTempDir)
		b.flatTempDir = ""
	}
}

// EnsureFlatProtoDir creates the de-circularized flat proto dir if not already set.
// This is used by both BuildLanguage and example generation.
func (b *Builder) EnsureFlatProtoDir() error {
	if b.config.FlatProtoDir != "" {
		return nil
	}
	tmpDir, err := os.MkdirTemp("", "tacplus-flat-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for flat protos: %w", err)
	}
	flatDir, err := proto.CopyAndPrepareProtoDir(b.config.InputDir, tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to prepare protos: %w", err)
	}
	b.config.FlatProtoDir = flatDir
	b.flatTempDir = tmpDir
	return nil
}

// BuildLanguage executes the build process for a single language.
// The config must have Language, OutputDir, InputDir, and DepsYAMLPath set.
func (b *Builder) BuildLanguage(ctx context.Context) error {
	lang := b.config.Language
	if lang == "" {
		return fmt.Errorf("no language specified in config")
	}

	// Set proto dir for package registry so it finds dependencies.yaml
	if b.config.ProtoDir != "" {
		packages.SetProtoDir(b.config.ProtoDir)
	}

	// Determine version mode
	localMode := b.config.LocalMode
	versionStr := b.config.VersionTag
	if localMode {
		if versionStr == "" {
			versionStr = "0.0.0-SNAPSHOT"
		}
		zap.L().Info("build mode", zap.String("mode", "LOCAL"), zap.String("version", versionStr))
	} else {
		zap.L().Info("build mode", zap.String("mode", "VERSIONED"), zap.String("tag", b.config.VersionTag))
	}

	// Verify required directories
	if err := b.validatePaths(); err != nil {
		return err
	}

	// Create a de-circularized flat copy of the protos for protoc-based parsing.
	// Raw source protos may have circular cross-service imports that protoc cannot handle.
	// This copy removes cross-service imports and replaces state_import field types with
	// bytes, while keeping the flat file structure that generators expect.
	if err := b.EnsureFlatProtoDir(); err != nil {
		return err
	}

	// Reject reserved (removed) spec option fields before anything else: protoc
	// silently DISCARDS reserved-name fields in option literals, so without this
	// the declared intent (e.g. a cardinality hint) vanishes without a trace.
	if err := proto.ValidateReservedSpecOptionFields(b.config.FlatProtoDir); err != nil {
		return err
	}
	// Validate state consistency annotations before generation
	if err := proto.ValidateStateConsistency(b.config.FlatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateConsistencyImports(b.config.FlatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateC2STopicActorIdentity(b.config.FlatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateMaxSize(b.config.FlatProtoDir); err != nil {
		return err
	}
	if err := b.validateSemanticAnnotations(); err != nil {
		return err
	}

	langDir := b.config.LangDir()
	zap.L().Info("generating output", zap.String("lang", lang), zap.String("dir", langDir))

	// Generate types for the language
	if err := b.generateLanguage(lang, versionStr, localMode); err != nil {
		return fmt.Errorf("failed to generate %s: %w", lang, err)
	}

	// Format generated code
	if err := formatter.Format(lang, langDir); err != nil {
		return fmt.Errorf("failed to format %s output: %w", lang, err)
	}

	zap.L().Info("build complete", zap.String("output", langDir), zap.String("mode", versionStr))

	return nil
}

func (b *Builder) validateSemanticAnnotations() error {
	dp, err := parser.NewDescriptorParser(b.config.FlatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to parse descriptors for semantic validation: %w", err)
	}
	allParsedFiles, err := parsedProtoFilesForValidation(dp, b.config.FlatProtoDir)
	if err != nil {
		return err
	}
	if err := parser.ValidateTimeseriesAnnotations(allParsedFiles); err != nil {
		return err
	}
	if err := parser.ValidateRedactAnnotations(allParsedFiles); err != nil {
		return err
	}
	if err := dp.ValidateNoNestedRedact(); err != nil {
		return err
	}
	if err := parser.ValidateReservedFieldNumbers(allParsedFiles); err != nil {
		return err
	}
	if err := parser.ValidateRedactLanguageSupport(allParsedFiles, b.config.Language); err != nil {
		return err
	}
	if err := parser.ValidateProjectionLanguageSupport(allParsedFiles, b.config.SelfPackage, b.config.Language); err != nil {
		return err
	}
	if err := parser.ValidateServiceStores(allParsedFiles); err != nil {
		return err
	}
	if err := parser.ValidateRoutingLabels(dp); err != nil {
		return err
	}
	if err := parser.ValidateC2SHostLabels(dp); err != nil {
		return err
	}
	if err := parser.ValidateMethodCollectionsForPackages(dp, collection.BuildMessageIndex(allParsedFiles), b.config.SelfPackage); err != nil {
		return err
	}
	if err := parser.ValidateServiceSurfaces(dp); err != nil {
		return err
	}
	if err := parser.ValidateAuthAnnotations(dp); err != nil {
		return err
	}
	if err := parser.ValidateSpiffeIdentityAnnotations(dp); err != nil {
		return err
	}
	if err := parser.ValidatePrincipalAnnotations(dp); err != nil {
		return err
	}
	if err := parser.ValidateOwnerDefaultAnnotations(dp); err != nil {
		return err
	}
	if err := parser.ValidateMinGroupSizeAnnotations(dp); err != nil {
		return err
	}
	if err := parser.ValidateSmbAuthority(dp); err != nil {
		return err
	}
	if err := parser.ValidateRestPathCollisions(dp); err != nil {
		return err
	}
	return nil
}

func parsedProtoFilesForValidation(dp *parser.DescriptorParser, protoDir string) ([]model.ProtoFile, error) {
	var result []model.ProtoFile
	err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".proto") {
			return nil
		}
		pf, err := dp.ConvertToProtoFile(path)
		if err != nil {
			return fmt.Errorf("failed to parse %s for semantic validation: %w", path, err)
		}
		if len(pf.Enums) > 0 || len(pf.Constants) > 0 || len(pf.Messages) > 0 {
			result = append(result, pf)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// clientLiteUnsupportedServerSurfaces returns a loud, named-reason error string
// when a C2D-only lite target (swift/csharp) is asked to emit server-side
// generation it cannot host — an S2S client, a gRPC server, or
// ServiceManager/state. Returns "" for non-lite languages or when only client
// surfaces are requested. Mirrors validatePythonClientOnlyConfig so the same
// misconfiguration fails the same way across every client target instead of
// being silently stripped (PAR-5). C2S/W2S are rejected separately, before this.
func clientLiteUnsupportedServerSurfaces(config *tactype.Config, lang string) string {
	if lang != "swift" && lang != "csharp" {
		return ""
	}
	var reasons []string
	if config.GrpcS2SClientAll || len(config.GrpcS2SClients) > 0 {
		reasons = append(reasons, "S2S client generation")
	}
	if config.GrpcServerAll || len(config.GrpcServers) > 0 {
		reasons = append(reasons, "server generation")
	}
	if config.AllStates || len(config.States) > 0 {
		reasons = append(reasons, "ServiceManager/state generation")
	}
	if len(reasons) == 0 {
		return ""
	}
	return fmt.Sprintf("%s is a client-lite target; only C2D/C2S/W2S client generation is supported. Remove %s or use a full-service target (go, rust, java)", lang, strings.Join(reasons, "; "))
}

// validatePaths ensures required directories and files exist
func (b *Builder) validatePaths() error {
	// Check proto directory
	if _, err := os.Stat(b.config.InputDir); os.IsNotExist(err) {
		return fmt.Errorf("proto directory not found: %s", b.config.InputDir)
	}

	// Reject user protos placed under the reserved tacplus namespaces.
	if err := validateReservedNamespaces(b.config.InputDir); err != nil {
		return err
	}

	// Check dependencies.yaml
	if _, err := os.Stat(b.config.DepsYAMLPath); os.IsNotExist(err) {
		return fmt.Errorf("dependencies.yaml not found: %s", b.config.DepsYAMLPath)
	}

	return nil
}

// validateReservedNamespaces rejects user protos placed under the tacplus-reserved
// `sys/` (declarative embedded primitives) or `chassis/` (runtime machinery)
// namespaces. Tacplus owns and injects those namespaces; a user proto authored
// under either would collide with generated output. Users import `sys/*.proto`
// (e.g. `import "sys/spec.proto"`) but must never author files there.
func validateReservedNamespaces(inputDir string) error {
	var offenders []string
	walkErr := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		rel, relErr := filepath.Rel(inputDir, path)
		if relErr != nil {
			return relErr
		}
		if seg := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]; seg == "sys" || seg == "chassis" {
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("scanning proto dir for reserved namespaces: %w", walkErr)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		return fmt.Errorf("proto files under the reserved tacplus namespaces sys/ and chassis/ are not allowed (tacplus owns and injects these — import sys/*.proto but do not author it): %s", strings.Join(offenders, ", "))
	}
	return nil
}

// generateLanguage generates code for a specific language
func (b *Builder) generateLanguage(lang, versionStr string, localMode bool) error {
	zap.L().Info("generating language", zap.String("language", strings.ToUpper(lang)))

	// Output directory: config.OutputDir is already set by the caller
	langOutputDir := b.config.OutputDir

	// Clean generated directories before generation
	if err := b.cleanGeneratedDirectories(langOutputDir); err != nil {
		return fmt.Errorf("failed to clean generated directories: %w", err)
	}

	// Get package name for this language
	langPackage, err := b.getLanguagePackage(lang)
	if err != nil {
		return err
	}

	zap.L().Info("language generation config",
		zap.String("output", langOutputDir),
		zap.String("package", langPackage),
		zap.String("version", versionStr),
	)

	// Create language-specific config
	config := &tactype.Config{
		Language:           lang,
		InputDir:           b.config.InputDir,
		OutputDir:          langOutputDir,
		ProjectDir:         b.config.ProjectDir,
		DepsYAMLPath:       b.config.DepsYAMLPath,
		VersionTag:         b.config.VersionTag,
		LocalMode:          localMode,
		Lang:               b.config.Lang,
		Registry:           b.config.Registry,
		ProtocGenSwift:     b.config.ProtocGenSwift,
		ProtocGenGrpcSwift: b.config.ProtocGenGrpcSwift,
		FlatProtoDir:       b.config.FlatProtoDir,
		GrpcServers:        b.config.GrpcServers,
		GrpcS2SClients:     b.config.GrpcS2SClients,
		GrpcC2SClients:     b.config.GrpcC2SClients,
		GrpcW2SClients:     b.config.GrpcW2SClients,
		GrpcC2DClients:     b.config.GrpcC2DClients,
		RestClients:        b.config.RestClients,
		GrpcServerAll:      b.config.GrpcServerAll,
		GrpcS2SClientAll:   b.config.GrpcS2SClientAll,
		RestClientAll:      b.config.RestClientAll,
		States:             b.config.States,
		AllStates:          b.config.AllStates,
		SelfPackage:        b.config.SelfPackage,
		SysOnly:            b.config.SysOnly,
	}

	// Call the appropriate language generator
	// Target matrix: python/typescript/csharp/swift all support C2S+W2S+C2D.
	// Swift and csharp both generate C2S (native gRPC) and W2S (gRPC-over-REST)
	// client surfaces alongside C2D, driven by the proto grpc_c2s_client /
	// grpc_w2s_client declarations.
	// PAR-5: swift/csharp are C2D-only lite targets. Any server-side generation
	// the proto asks for — an S2S client, a gRPC server, ServiceManager/state —
	// must be rejected LOUDLY with named reasons, exactly as the python/typescript
	// full-client targets reject server-side generation, instead of being silently
	// stripped. Mirrors validatePythonClientOnlyConfig.
	if lang == "swift" || lang == "csharp" {
		if reason := clientLiteUnsupportedServerSurfaces(config, lang); reason != "" {
			return fmt.Errorf("%s", reason)
		}
	}

	switch lang {
	case "go":
		config.ModuleBasePath = langPackage
		err = gotypes.Generate(config)
	case "typescript":
		config.TsPackageBase = langPackage
		err = typescripttypes.Generate(config)
	case "python":
		config.PythonPackageBase = langPackage
		err = pythontypes.Generate(config)
	case "rust":
		config.CrateBasePath = langPackage
		err = rusttypes.Generate(config)
	case "java":
		config.JavaPackageBase = langPackage
		err = javatypes.Generate(config)
	case "swift":
		config.SwiftPackageBase = langPackage
		err = swifttypes.Generate(config)
	case "csharp":
		config.CSharpPackageBase = langPackage
		err = csharptypes.Generate(config)
	default:
		return fmt.Errorf("unsupported language: %s", lang)
	}

	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	// Generate project metadata file (PROJECT_NAME constant from SelfPackage).
	if config.SelfPackage != "" {
		metaDir := config.OutputDir
		javaPackagePath := ""
		if lang == "java" {
			// Java needs the file inside the package directory tree.
			metaDir = filepath.Join(config.OutputDir, "src", "main", "java")
			javaPackagePath = strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
		} else if lang == "swift" {
			// Swift metadata lives in the package target, alongside the generated facade.
			metaDir = filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase)
		}
		if err := metadata.GenerateMetaData(lang, config.SelfPackage, metaDir, javaPackagePath); err != nil {
			return fmt.Errorf("failed to generate metadata: %w", err)
		}

		// For Rust, append `pub mod meta_data;` to lib.rs so the metadata module is accessible.
		// meta_data.rs is generated after the Rust generator writes lib.rs, so it must be appended here.
		if lang == "rust" {
			libPath := filepath.Join(config.OutputDir, "lib.rs")
			f, err := os.OpenFile(libPath, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				return fmt.Errorf("failed to open lib.rs to add meta_data module: %w", err)
			}
			if _, err := f.WriteString("pub mod meta_data;\n"); err != nil {
				f.Close()
				return fmt.Errorf("failed to write meta_data module to lib.rs: %w", err)
			}
			f.Close()
		}
	}

	zap.L().Info("generation complete", zap.String("language", strings.ToUpper(lang)))
	return nil
}

// getLanguagePackage returns the package name for a given language from config.
// The package base is already set on Config by the build command (from --package flag or default).
func (b *Builder) getLanguagePackage(lang string) (string, error) {
	switch lang {
	case "go":
		return b.config.ModuleBasePath, nil
	case "java":
		return b.config.JavaPackageBase, nil
	case "typescript":
		return b.config.TsPackageBase, nil
	case "python":
		return b.config.PythonPackageBase, nil
	case "rust":
		return b.config.CrateBasePath, nil
	case "swift":
		return b.config.SwiftPackageBase, nil
	case "csharp":
		return b.config.CSharpPackageBase, nil
	}
	return "", fmt.Errorf("unknown language: %s", lang)
}

// cleanGeneratedDirectories selectively removes known generated subdirectories
// and build files from the output directory while preserving manually
// maintained content.
func (b *Builder) cleanGeneratedDirectories(outputDir string) error {
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		return nil // Nothing to clean
	}

	if b.config.Verbose {
		zap.L().Debug("cleaning generated directories", zap.String("dir", outputDir))
	}

	// Known tacplus-generated infrastructure dirs + fully-regenerated dirs
	generatedDirs := []string{"examples", "sys", "sm", "spec", "proto", "interfaces", "types", "utils"}
	for _, dir := range generatedDirs {
		p := filepath.Join(outputDir, dir)
		if _, err := os.Stat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("failed to remove generated directory %s: %w", dir, err)
			}
		}
	}

	// Also remove any top-level directory containing protoc-generated files.
	// This catches directories with arbitrary names (e.g., "common2", "services2")
	// that would otherwise be missed by the hardcoded list above.
	if err := b.cleanProtocOutputDirs(outputDir); err != nil {
		return err
	}

	// Remove generated build files so they get regenerated.
	// pom.xml is excluded — it's hand-maintained for Java projects.
	for _, f := range []string{"package.json", "go.mod", "go.sum", "Package.swift"} {
		p := filepath.Join(outputDir, f)
		if _, err := os.Stat(p); err == nil {
			os.Remove(p)
		}
	}

	return nil
}

// cleanProtocOutputDirs scans outputDir for top-level directories containing
// protoc-generated files (.pb.go, _pb2.py, _pb.ts, _pb.rs, .pb.java, .pb.swift, .grpc.swift) and
// removes them. This avoids hardcoding directory names like "common2" or
// "services2" that vary across projects.
func (b *Builder) cleanProtocOutputDirs(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil // nothing to scan
	}

	// Directories to preserve regardless of content
	preserve := map[string]bool{"sm": true, "sys": true}

	for _, entry := range entries {
		if !entry.IsDir() || preserve[entry.Name()] {
			continue
		}
		dirPath := filepath.Join(outputDir, entry.Name())
		if containsProtocOutput(dirPath) {
			if err := os.RemoveAll(dirPath); err != nil {
				return fmt.Errorf("failed to remove protoc output directory %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

// containsProtocOutput checks whether a directory tree contains any
// protoc-generated files (by extension pattern).
func containsProtocOutput(dir string) bool {
	protocSuffixes := []string{".pb.go", "_pb2.py", "_pb.ts", "_pb.rs", ".pb.java", ".pb.swift", ".grpc.swift"}
	found := false
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		for _, suffix := range protocSuffixes {
			if strings.HasSuffix(name, suffix) {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}
