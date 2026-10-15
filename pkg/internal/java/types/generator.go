// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/registry"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/restdispatch"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// findGrpcJavaPlugin attempts to locate the protoc-gen-grpc-java plugin
func findGrpcJavaPlugin() string {
	// Check common locations for the gRPC Java plugin
	locations := []string{
		"/usr/local/bin/protoc-gen-grpc-java",
		"/opt/homebrew/bin/protoc-gen-grpc-java",
		"/usr/bin/protoc-gen-grpc-java",
	}

	// Check environment variable first
	if envPath := os.Getenv("PROTOC_GRPC_JAVA"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	// Check common locations
	for _, loc := range locations {
		if _, err := os.Stat(loc); err == nil {
			return loc
		}
	}

	// Try to find in PATH
	if path, err := exec.LookPath("protoc-gen-grpc-java"); err == nil {
		return path
	}

	return ""
}

// copyProtoWithJavaPackage copies a proto file and adds/modifies the java_package option
func copyProtoWithJavaPackage(srcPath, dstPath, relPath string, config *tactype.Config) error {
	// Read source proto file
	content, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read proto file: %w", err)
	}

	// Determine the Java package based on the proto file location
	// relPath is like "sys/cloudvendor.proto" or "services/foobar.proto"
	// Strip tacplus/ prefix — import namespace doesn't affect output structure
	pathParts := strings.Split(relPath, string(filepath.Separator))
	var typeDir string
	if len(pathParts) > 1 {
		typeDir = pathParts[0] // e.g., "sys", "services"
	} else {
		typeDir = ""
	}

	// Derive Java package from the proto package declaration — the filename is irrelevant.
	// The proto `package` declaration (e.g., `package ironmaiden;`) is the sole source of truth.
	source := string(content)
	protoPackage := extractProtoPackage(source)
	declaresServiceState := proto.DeclaresServiceStateSource(source)
	var javaPackage string
	if serviceName, fileName := proto.ServiceProtoParts(relPath); serviceName != "" {
		if fileName == serviceName {
			// Flat layout: {dir}/iron-maiden.proto with `package ironmaiden;` → .{typeDir}.ironmaiden
			javaPackage = fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, protoPackage)
		} else {
			// Directory layout: {dir}/{name}/{file}.proto → .{typeDir}.{protoPackage}.{subPkg}
			var subPkg string
			switch {
			case fileName == "types":
				subPkg = protoPackage
			case proto.IsServicesFileName(fileName):
				subPkg = "services"
			case declaresServiceState:
				subPkg = "services"
			default:
				subPkg = fileName
			}
			javaPackage = fmt.Sprintf("%s.types.%s.%s.%s", config.JavaPackageBase, typeDir, protoPackage, subPkg)
		}
	} else {
		// Standard non-service file — use proto package for Java package
		if typeDir != "" {
			javaPackage = fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, protoPackage)
		} else {
			javaPackage = fmt.Sprintf("%s.types.%s", config.JavaPackageBase, protoPackage)
		}
	}

	// Sanitize hyphens in java_package (Java identifiers cannot contain hyphens)
	javaPackage = strings.ReplaceAll(javaPackage, "-", "_")

	// Modify proto content to add/replace java_package option
	modifiedContent := addOrReplaceJavaPackage(string(content), javaPackage)

	// Add java_outer_classname to prevent protoc outer class name conflicts.
	// For hyphenated filenames: always set explicitly since protoc can't handle hyphens.
	// For non-hyphenated filenames: set only when the derived outer class (CamelCase of stem)
	// would collide with a message, service, or enum name inside the file.
	// e.g. claude-sensor.proto (package telemetry) → "Telemetry"
	// e.g. argus.proto with "service Argus" → "ArgusOuterClass"
	filenameStem := strings.TrimSuffix(filepath.Base(relPath), ".proto")
	if len(protoPackage) > 0 {
		if strings.Contains(filenameStem, "-") {
			outerClassName := strings.ToUpper(protoPackage[:1]) + protoPackage[1:]
			modifiedContent = addOrReplaceOption(modifiedContent, "java_outer_classname", outerClassName)
		} else {
			derived := strings.ToUpper(filenameStem[:1]) + filenameStem[1:]
			if hasOuterClassConflict(source, derived) {
				// Only auto-set if the source proto doesn't already specify java_outer_classname.
				reHasOption := regexp.MustCompile(`(?m)^[^\n/]*?option\s+java_outer_classname\s*=`)
				if !reHasOption.MatchString(source) {
					modifiedContent = addOrReplaceOption(modifiedContent, "java_outer_classname", derived+"OuterClass")
				}
			}
		}
	}

	// No header added here — SplitServiceProtosToDirectories runs after and would
	// reorganize the header into the body. The proto files are intermediate
	// build artifacts consumed by protoc, not final output.

	// Create destination directory
	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write modified proto file
	if err := os.WriteFile(dstPath, []byte(modifiedContent), 0644); err != nil {
		return fmt.Errorf("failed to write proto file: %w", err)
	}

	return nil
}

// addJavaPackageToServiceSubdirs walks service subdirectories in protoDir and adds
// java_package and java_outer_classname options to any proto files that don't already have them.
// Called after SplitServiceProtosToDirectories creates new files.
//
// The java_outer_classname preserves the service name as the outer class
// (e.g., "Baseball" for baseball/types.proto) since protoc would otherwise derive
// "Types" from the filename.
func addJavaPackageToServiceSubdirs(protoDir string, config *tactype.Config) error {
	topEntries, err := os.ReadDir(protoDir)
	if err != nil {
		return nil
	}

	for _, topEntry := range topEntries {
		if !topEntry.IsDir() || topEntry.Name() == "sys" || topEntry.Name() == "google" {
			continue
		}
		topDirName := topEntry.Name()
		topDirPath := filepath.Join(protoDir, topDirName)

		if walkErr := filepath.Walk(topDirPath, func(path string, info os.FileInfo, err error) error {
			return addJavaPackageToSubdirFile(path, info, err, protoDir, topDirName, config)
		}); walkErr != nil {
			return walkErr
		}
	}
	return nil
}

func addJavaPackageToSubdirFile(path string, info os.FileInfo, err error, protoDir, topDirName string, config *tactype.Config) error {
	if err != nil {
		return err
	}
	if info.IsDir() || !strings.HasSuffix(path, ".proto") {
		return nil
	}

	relPath, _ := filepath.Rel(protoDir, path)
	serviceName, fileName := proto.ServiceProtoParts(relPath)
	if serviceName == "" {
		return nil // Not a subdirectory file
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Use proto package declaration as the source of truth — filename is irrelevant.
	source := string(content)
	protoPackage := extractProtoPackage(source)
	if protoPackage == "" {
		protoPackage = strings.ReplaceAll(serviceName, "-", "_") // fallback
	}

	declaresServiceState := proto.DeclaresServiceStateSource(source)
	isTypesFile := fileName == "types" || fileName == serviceName
	var javaPackage string
	if fileName == serviceName {
		// Flat layout: {dir}/iron-maiden.proto → .{dir}.ironmaiden
		javaPackage = fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, topDirName, protoPackage)
	} else {
		// Directory layout: {dir}/{name}/{file}.proto → .{dir}.{protoPackage}.{subPkg}
		var subPkg string
		switch {
		case isTypesFile:
			subPkg = protoPackage
		case proto.IsServicesFileName(fileName):
			subPkg = "services"
		case declaresServiceState:
			subPkg = "services"
		default:
			subPkg = strings.ReplaceAll(fileName, "-", "_")
		}
		javaPackage = fmt.Sprintf("%s.types.%s.%s.%s", config.JavaPackageBase, topDirName, protoPackage, subPkg)
	}

	// Add java_package option
	modified := addOrReplaceJavaPackage(string(content), javaPackage)

	// Add java_outer_classname to prevent protoc from using the filename
	// as the outer class name (which would give "Types", "Services", "State" for all services).
	servicePascal := strutil.ToCamelCase(protoPackage)
	var outerClassName string
	switch {
	case isTypesFile:
		outerClassName = servicePascal
	case proto.IsServicesFileName(fileName):
		outerClassName = servicePascal + "ServiceProto"
	case declaresServiceState:
		outerClassName = servicePascal + "State"
	default:
		outerClassName = servicePascal
	}
	// If the source proto already specifies java_outer_classname, honour it instead of
	// auto-deriving one. This allows proto authors to resolve naming conflicts explicitly.
	reHasOuterClass := regexp.MustCompile(`(?m)^[^\n/]*?option\s+java_outer_classname\s*=`)
	if !reHasOuterClass.MatchString(source) {
		// If the outer class name collides with a message, service, or enum name, append OuterClass.
		// protoc rejects files where the outer class name matches any top-level type name.
		if hasOuterClassConflict(source, outerClassName) {
			outerClassName += "OuterClass"
		}
		modified = addOrReplaceOption(modified, "java_outer_classname", outerClassName)
	}

	return os.WriteFile(path, []byte(modified), 0644)
}

// addJavaPackageToGeneratedTenantConfig adds java_package and java_outer_classname options to
// generated/tenant_config.proto. This file is produced by GenerateTenantConfig after
// ServiceProtoParts runs (which explicitly skips the "generated" directory), so it is not
// handled by addJavaPackageToServiceSubdirs.
func addJavaPackageToGeneratedTenantConfig(protoDir string, config *tactype.Config) error {
	path := filepath.Join(protoDir, "generated", "tenant_config.proto")
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // GenerateTenantConfig wrote nothing (no slices found); nothing to do
	}
	if err != nil {
		return err
	}

	javaPackage := fmt.Sprintf("%s.types.generated.tenant_config", config.JavaPackageBase)
	modified := addOrReplaceJavaPackage(string(content), javaPackage)

	outerClassName := "TenantConfig"
	if hasOuterClassConflict(string(content), outerClassName) {
		outerClassName += "OuterClass"
	}
	modified = addOrReplaceOption(modified, "java_outer_classname", outerClassName)

	return os.WriteFile(path, []byte(modified), 0644)
}

// addJavaPackageToGeneratedProjectConfig adds java_package and java_outer_classname options to
// generated/project_config.proto. This file is produced by GenerateProjectConfig after
// ServiceProtoParts runs (which explicitly skips the "generated" directory), so it is not
// handled by addJavaPackageToServiceSubdirs.
func addJavaPackageToGeneratedProjectConfig(protoDir string, config *tactype.Config) error {
	path := filepath.Join(protoDir, "generated", "project_config.proto")
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // GenerateProjectConfig wrote nothing (no slices found); nothing to do
	}
	if err != nil {
		return err
	}

	javaPackage := fmt.Sprintf("%s.types.generated.project_config", config.JavaPackageBase)
	modified := addOrReplaceJavaPackage(string(content), javaPackage)

	outerClassName := "ProjectConfig"
	if hasOuterClassConflict(string(content), outerClassName) {
		outerClassName += "OuterClass"
	}
	modified = addOrReplaceOption(modified, "java_outer_classname", outerClassName)

	return os.WriteFile(path, []byte(modified), 0644)
}

// hasOuterClassConflict returns true if the given name matches any top-level
// message, service, or enum name in the proto content. protoc rejects files
// where the outer class name matches a type declared inside the file.
func hasOuterClassConflict(content, name string) bool {
	for _, keyword := range []string{"message", "service", "enum"} {
		if strings.Contains(content, keyword+" "+name+" ") ||
			strings.Contains(content, keyword+" "+name+"\n") ||
			strings.Contains(content, keyword+" "+name+"{") {
			return true
		}
	}
	return false
}

// addOrReplaceOption adds or replaces a proto option like java_outer_classname.
func addOrReplaceOption(content, optionName, value string) string {
	optionLine := fmt.Sprintf("option %s = \"%s\";", optionName, value)
	optionRegex := regexp.MustCompile(`(?m)^option\s+` + regexp.QuoteMeta(optionName) + `\s*=\s*"[^"]*"\s*;`)
	if optionRegex.MatchString(content) {
		return optionRegex.ReplaceAllString(content, optionLine)
	}

	// Insert after java_package option if present, otherwise after package declaration
	javaPackageRegex := regexp.MustCompile(`(?m)^option\s+java_package\s*=\s*"[^"]*"\s*;`)
	if loc := javaPackageRegex.FindStringIndex(content); loc != nil {
		return content[:loc[1]] + "\n" + optionLine + content[loc[1]:]
	}

	packageRegex := regexp.MustCompile(`(?m)^package\s+\w+\s*;`)
	if loc := packageRegex.FindStringIndex(content); loc != nil {
		return content[:loc[1]] + "\n" + optionLine + content[loc[1]:]
	}

	return content + "\n" + optionLine + "\n"
}

// extractProtoPackage extracts the package name from proto file content
func extractProtoPackage(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			// Extract package name from: package foo;
			line = strings.TrimPrefix(line, "package ")
			line = strings.TrimSuffix(line, ";")
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// addOrReplaceJavaPackage adds or replaces the java_package option in proto content.
// Uses Contains on raw proto text for syntax detection (comments, options).
func addOrReplaceJavaPackage(content, javaPackage string) string {
	lines := strings.Split(content, "\n")
	var result []string
	javaPackageAdded := false
	inMultiLineComment := false

	// Pre-scan: check if there's already a java_package option in the file.
	// If so, we'll replace it in-place rather than adding after the package declaration.
	hasExistingJavaPackage := strings.Contains(content, "option java_package")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip single-line comments BEFORE checking for /* to avoid false
		// positives from /* appearing inside // comments (e.g. "// path /realms/*").
		if strings.HasPrefix(trimmed, "//") {
			result = append(result, line)
			continue
		}

		// Track multi-line comments
		if strings.Contains(trimmed, "/*") {
			inMultiLineComment = true
		}
		if strings.Contains(trimmed, "*/") {
			inMultiLineComment = false
			result = append(result, line)
			continue
		}
		if inMultiLineComment {
			result = append(result, line)
			continue
		}

		// Replace existing java_package option
		if strings.Contains(trimmed, "option java_package") {
			result = append(result, fmt.Sprintf("option java_package = \"%s\";", javaPackage))
			javaPackageAdded = true
			continue
		}

		// Add java_package after package declaration (only if no existing option to replace)
		if strings.HasPrefix(trimmed, "package ") && !javaPackageAdded && !hasExistingJavaPackage {
			result = append(result, line)
			// Add java_package on the next line
			result = append(result, fmt.Sprintf("option java_package = \"%s\";", javaPackage))
			javaPackageAdded = true
			continue
		}

		// Add java_package before first message/enum/service if not yet added
		if !javaPackageAdded && (strings.HasPrefix(trimmed, "message ") ||
			strings.HasPrefix(trimmed, "enum ") ||
			strings.HasPrefix(trimmed, "service ")) {
			// Insert java_package before this line
			result = append(result, fmt.Sprintf("option java_package = \"%s\";", javaPackage))
			result = append(result, "")
			result = append(result, line)
			javaPackageAdded = true
			continue
		}

		result = append(result, line)

		// If we're at the end and haven't added it yet, add it after package
		if i == len(lines)-1 && !javaPackageAdded {
			result = append(result, fmt.Sprintf("option java_package = \"%s\";", javaPackage))
		}
	}

	return strings.Join(result, "\n")
}

// ModuleInfo represents a Maven module to be generated
type ModuleInfo struct {
	Name         string   // e.g., "types-sys-cloudvendor"
	ProtoFile    string   // e.g., "sys/cloudvendor.proto"
	TypeDir      string   // e.g., "sys"
	ProtoPackage string   // e.g., "cloudvendor"
	FilenameStem string   // e.g., "cloudvendor" (proto filename without .proto extension)
	JavaPackage  string   // e.g., "com.gitlab...sys.cloudvendor"
	ProtoFiles   []string // All proto files for this module
}

// generateModule generates Java code for a single proto package into the shared source tree
func generateModule(module *ModuleInfo, protoDir string, config *tactype.Config, parentDir string, _allFiles []model.ProtoFile, svcRPCs []proto.ServiceRPCEntry, vsEntries []proto.VServiceEntry) error {
	// All generated code goes into a shared source tree: parentDir/src/main/java/
	// The java_package option in each proto file ensures files land in the correct subdirectories.
	srcDir := filepath.Join(parentDir, "src", "main", "java")

	zap.L().Info("Generating module", zap.String("module", module.Name))

	// Create source directory structure
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return fmt.Errorf("failed to create source directory: %w", err)
	}

	// Build package path and create package directory.
	// java_package already routes to the correct leaf package for all modules.
	packagePath := strings.ReplaceAll(module.JavaPackage, ".", string(filepath.Separator))
	packageDir := filepath.Join(srcDir, packagePath)
	if err := os.MkdirAll(packageDir, 0755); err != nil {
		return fmt.Errorf("failed to create package directory: %w", err)
	}

	// Check if any proto files contain gRPC service definitions.
	// Use a regex anchored to line start to avoid false positives from
	// comments, field names, or message names containing "service".
	hasServices := false
	serviceRe := regexp.MustCompile(`(?m)^service\s+\w+`)
	for _, protoFile := range module.ProtoFiles {
		protoPath := filepath.Join(protoDir, protoFile)
		content, err := os.ReadFile(protoPath)
		if err == nil && serviceRe.Match(content) {
			hasServices = true
			break
		}
	}

	// Run protoc for this module's proto files, output to types/ subdirectory
	args := []string{
		"--java_out=" + srcDir,
		"--proto_path=" + protoDir,
	}

	// Add gRPC Java generation if services are present
	if hasServices {
		// Try to find grpc-java plugin
		grpcJavaPlugin := findGrpcJavaPlugin()
		if grpcJavaPlugin != "" {
			args = append(args, "--plugin=protoc-gen-grpc-java="+grpcJavaPlugin)
			args = append(args, "--grpc-java_out="+srcDir)
			zap.L().Info("Generating gRPC stubs", zap.String("plugin", grpcJavaPlugin))
		} else {
			return fmt.Errorf("gRPC services found but protoc-gen-grpc-java plugin not found; install protoc-gen-grpc-java or set PROTOC_GRPC_JAVA")
		}
	}

	args = append(args, module.ProtoFiles...)

	cmd := exec.Command(config.ProtocPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("protoc failed for %s: %w\n%s", module.Name, err, string(output))
	}

	// Replace protoc-generated headers with our consistent tacplus header
	if err := header.ReplaceProtocHeaders(srcDir, consts.LangJava, ".java", "package"); err != nil {
		return fmt.Errorf("failed to replace protoc headers: %w", err)
	}

	// Use descriptor-based parser (robust, uses protoc internally)
	parser, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Generate helper files for all protos (types, services, state).
	// Merge all parsed proto files into one combined ProtoFile so that
	// multiple proto files in the same package produce a single Helpers.java.
	var mergedProto model.ProtoFile
	mergedProto.Package = module.ProtoPackage
	defaultOuterClass := ""

	for _, protoFile := range module.ProtoFiles {

		protoPath := filepath.Join(protoDir, protoFile)
		parsedProto, err := parser.ConvertToProtoFile(protoPath)
		if err != nil {
			return fmt.Errorf("failed to parse proto file %s: %w", protoFile, err)
		}

		helperBaseDir := srcDir
		helperSubdir := packagePath
		outerClassName := detectOuterClassName(helperBaseDir, helperSubdir, filepath.Join(protoDir, protoFile))

		// Tag each enum and message with its outer class name for correct
		// Java type qualification when multiple proto files share a package.
		for i := range parsedProto.Enums {
			parsedProto.Enums[i].OuterClass = outerClassName
		}
		for i := range parsedProto.Messages {
			parsedProto.Messages[i].OuterClass = outerClassName
		}

		mergedProto.Enums = append(mergedProto.Enums, parsedProto.Enums...)
		mergedProto.Constants = append(mergedProto.Constants, parsedProto.Constants...)
		mergedProto.Messages = append(mergedProto.Messages, parsedProto.Messages...)
		mergedProto.Imports = append(mergedProto.Imports, parsedProto.Imports...)
		if defaultOuterClass == "" {
			defaultOuterClass = outerClassName
		}

		// Generate ErrorUtils with gRPC error helper functions (sys/errors.proto only)
		if len(parsedProto.Messages) > 0 {
			if err := generateErrorUtils(parsedProto, helperBaseDir, helperSubdir); err != nil {
				return fmt.Errorf("failed to generate ErrorUtils: %w", err)
			}
		}
	}

	// Generate consolidated Helpers.java with all static methods from all proto files.
	helperBaseDir := srcDir
	helperSubdir := packagePath
	if len(mergedProto.Enums) > 0 || len(mergedProto.Constants) > 0 || len(mergedProto.Messages) > 0 || hasFieldDefaults(mergedProto) {
		if err := generateHelpers(mergedProto, helperBaseDir, helperSubdir, defaultOuterClass, config, svcRPCs, vsEntries); err != nil {
			return fmt.Errorf("failed to generate Helpers: %w", err)
		}
	}

	// Generate JBMC proof harnesses in a separate proof/ directory (not in src/main/java)
	// so that mvn compile doesn't try to compile them — CProver is not a Maven dependency.
	proofBaseDir := filepath.Join(parentDir, "proof", "java")
	if err := writeJbmcProofHarnesses(mergedProto, proofBaseDir, helperSubdir, defaultOuterClass); err != nil {
		return fmt.Errorf("failed to generate JBMC proof harnesses: %w", err)
	}

	return nil
}

// Generate generates Java code from proto files
func Generate(config *tactype.Config) error {
	// Resolve protoc paths
	if err := config.ResolveProtocPaths(); err != nil {
		return err
	}

	// Clean generated subdirectories while preserving hand-maintained files
	// (pom.xml, etc.) that are tracked in git but not regenerated.
	for _, sub := range []string{"src", "proto", "proto-annotated", "registry.json", "registry.schema.json", "AGENT.md"} {
		p := filepath.Join(config.OutputDir, sub)
		if _, err := os.Stat(p); err == nil {
			if err := os.RemoveAll(p); err != nil {
				return fmt.Errorf("failed to remove %s: %w", sub, err)
			}
		}
	}

	// Create proto directory for proto files with java_package options
	protoDir := filepath.Join(config.LangDir(), "proto")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return fmt.Errorf("failed to create proto directory: %w", err)
	}

	// Collect all proto files and determine modules
	modules := make(map[string]*ModuleInfo)

	err := filepath.Walk(config.InputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the generated/ directory — its protos are registered separately below
		// (after ScanAndGenerateConfigHolder creates/updates them) to avoid double-registration.
		if info.IsDir() && filepath.Base(path) == "generated" {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		relPath, err := filepath.Rel(config.InputDir, path)
		if err != nil {
			return err
		}

		// Read proto file to get package name
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(content)
		protoPackage := extractProtoPackage(source)
		if protoPackage == "" {
			return fmt.Errorf("no package declaration in %s", relPath)
		}
		declaresServiceState := proto.DeclaresServiceStateSource(source)

		// Determine type directory and module keying.
		pathParts := strings.Split(relPath, string(filepath.Separator))
		typeDir := ""
		if len(pathParts) > 1 {
			typeDir = pathParts[0]
		}

		// Handle service subdirectory structure: services/{name}/{file}.proto
		// Each file in the subdirectory becomes its own module.
		// Use proto package name (not filename) for Java packages — the filename is irrelevant.
		var moduleKey, moduleName, filenameStem string
		var javaPackage string
		if serviceName, fileName := proto.ServiceProtoParts(relPath); serviceName != "" {
			filenameStem = protoPackage + "_" + fileName
			moduleKey = typeDir + "." + protoPackage + "." + fileName
			moduleName = fmt.Sprintf("types-%s-%s-%s", typeDir, protoPackage, fileName)
			if fileName == serviceName {
				// Flat layout: {dir}/baseball.proto → .{typeDir}.{protoPackage}
				javaPackage = fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, protoPackage)
			} else {
				var subPkg string
				switch {
				case fileName == "types":
					subPkg = protoPackage
				case proto.IsServicesFileName(fileName):
					subPkg = "services"
				case declaresServiceState:
					subPkg = "services"
				default:
					subPkg = fileName
				}
				javaPackage = fmt.Sprintf("%s.types.%s.%s.%s", config.JavaPackageBase, typeDir, protoPackage, subPkg)
			}
		} else {
			// Standard flat file — use proto package name
			filenameStem = protoPackage
			moduleKey = typeDir + "." + protoPackage
			moduleName = fmt.Sprintf("types-%s-%s", typeDir, protoPackage)
			javaPackage = fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, protoPackage)
		}

		if _, exists := modules[moduleKey]; !exists {
			modules[moduleKey] = &ModuleInfo{
				Name:         moduleName,
				TypeDir:      typeDir,
				ProtoPackage: protoPackage,
				FilenameStem: filenameStem,
				JavaPackage:  javaPackage,
				ProtoFiles:   []string{},
			}
		}
		modules[moduleKey].ProtoFiles = append(modules[moduleKey].ProtoFiles, relPath)

		// Copy proto file with java_package option added
		if err := copyProtoWithJavaPackage(path, filepath.Join(protoDir, relPath), relPath, config); err != nil {
			return fmt.Errorf("failed to copy proto file %s: %w", relPath, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to collect proto files: %w", err)
	}

	// Overlay embedded sys protos into protoDir so protoc can resolve imports.
	// Sys are embedded in tacplus and may not exist in the user's source proto directory.
	if err := proto.CopySysProtos(protoDir); err != nil {
		return fmt.Errorf("failed to copy sys protos to proto dir: %w", err)
	}

	// Generate generated/*.proto from grpc_config annotations.
	// Must happen before any descriptor parser runs (service protos may import it).
	_, svcRPCs, vsEntries, err := proto.ScanAndGenerateConfigHolder(config.InputDir, protoDir)
	if err != nil {
		return fmt.Errorf("failed to generate generated/*.proto: %w", err)
	}

	// Inject the MCP session carrier into every service that declares an
	// RPC_PRINCIPAL_MCP tool. It has to be a real rpc in the tree protoc
	// compiles, and this is the first point at which every import a service
	// proto can name (generated/, sys/) resolves, so the descriptor parse it
	// needs succeeds. A tree with no MCP rpc is untouched.
	mcpCarriers, err := proto.AppendMcpCarrierRpcsToProtos(protoDir, config.InputDir)
	if err != nil {
		return fmt.Errorf("failed to inject MCP session carrier rpcs: %w", err)
	}
	if len(mcpCarriers) > 0 {
		// The scan above ran over the un-injected tree, so the carrier is missing
		// from its service's <Svc>RPCs enum and TokenEntitlements mapping. A c2s
		// carrier needs an enum value exactly like the rpcs declared beside it.
		_, svcRPCs, vsEntries, err = proto.ScanAndGenerateConfigHolder(config.InputDir, protoDir)
		if err != nil {
			return fmt.Errorf("failed to regenerate generated/*.proto after MCP carrier injection: %w", err)
		}
	}
	if err := proto.AppendEventBusWrappersToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append eventbus topic wrappers to protos: %w", err)
	}
	if err := proto.AppendRoutingEnvelopesToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append routing envelopes to protos: %w", err)
	}
	if _, err := proto.PrepareEventBusSupportProto(protoDir, proto.EventBusSupportProtoOptions{}, config.FlatProtoDir); err != nil {
		return err
	}

	// Generate virtual service protos into generated/.
	// Must happen before the generated/ discovery below so they're registered as modules.
	if _, err := proto.GenerateOpaEvalProto(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to generate OpaEval virtual service proto: %w", err)
	}

	if err := proto.ValidateVirtualImports(config.FlatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateRecordLinkage(config.FlatProtoDir); err != nil {
		return err
	}

	// Auto-derive virtual service clients from proto annotations.
	virtualClients, err := proto.DeriveVirtualServiceClients(config.FlatProtoDir, config.SelfPackage)
	if err != nil {
		return fmt.Errorf("failed to derive virtual service clients: %w", err)
	}

	// Infer gRPC client dependencies from grpc_s2s_client/c2s/w2s proto annotations.
	if err := service.InferGrpcClientsFromAnnotations(config, protoDir); err != nil {
		return fmt.Errorf("failed to infer gRPC clients from proto annotations: %w", err)
	}
	// Derived clients are unioned in AFTER inference, not appended before it:
	// inference skips its explicit-declaration block when GrpcS2SClients is
	// already non-empty, so a service declaring both (spec.record_read) and an
	// explicit (spec.grpc_s2s_client) used to lose the explicit one silently.
	config.GrpcS2SClients = service.MergeGrpcEntries(config.GrpcS2SClients, virtualClients)

	// (spec.rpc).requires_device auto-injects the client to the approval broker on
	// the authoritative token minter, unioned in after inference for exactly the
	// same reason as the derived virtual clients above: the developer writes only
	// the boolean, and a package that also declares an explicit
	// (spec.grpc_s2s_client) must not lose it.
	approvalBrokerClients, err := proto.DeriveApprovalBrokerClient(config.FlatProtoDir, config.SelfPackage)
	if err != nil {
		return fmt.Errorf("failed to derive approval-broker client: %w", err)
	}
	config.GrpcS2SClients = service.MergeGrpcEntries(config.GrpcS2SClients, approvalBrokerClients)

	// Register generated/ protos as per-package Java modules so protoc compiles them.
	// Each proto file has its own package (e.g., services, entitlements, accounting_method)
	// and gets its own module entry keyed by "generated.{package}".
	generatedDir := filepath.Join(protoDir, "generated")
	if genEntries, readErr := os.ReadDir(generatedDir); readErr == nil {
		for _, entry := range genEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
				continue
			}
			relPath := filepath.Join("generated", entry.Name())
			srcPath := filepath.Join(generatedDir, entry.Name())

			content, contentErr := os.ReadFile(srcPath)
			if contentErr != nil {
				return fmt.Errorf("failed to read generated proto %s: %w", relPath, contentErr)
			}
			protoPackage := extractProtoPackage(string(content))
			if protoPackage == "" {
				protoPackage = strings.TrimSuffix(entry.Name(), ".proto")
			}

			if err := copyProtoWithJavaPackage(srcPath, srcPath, relPath, config); err != nil {
				return fmt.Errorf("failed to add java_package to generated proto %s: %w", relPath, err)
			}

			moduleKey := "generated." + protoPackage
			javaPackage := fmt.Sprintf("%s.types.generated.%s", config.JavaPackageBase, protoPackage)

			if existing, exists := modules[moduleKey]; exists {
				existing.ProtoFiles = append(existing.ProtoFiles, relPath)
				continue
			}

			modules[moduleKey] = &ModuleInfo{
				Name:         fmt.Sprintf("types-generated-%s", protoPackage),
				TypeDir:      filepath.Join("generated", protoPackage),
				ProtoPackage: protoPackage,
				FilenameStem: protoPackage,
				JavaPackage:  javaPackage,
				ProtoFiles:   []string{relPath},
			}
		}
	}

	// Register sys protos as modules so they get compiled by protoc.
	// CopySysProtos just overlaid the embedded lib protos into protoDir for import resolution,
	// but they also need to be compiled to produce Java classes used by wrappers.
	sysDir := filepath.Join(protoDir, "sys")
	if entries, err := os.ReadDir(sysDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".proto")

			relPath := filepath.Join("sys", entry.Name())
			content, err := os.ReadFile(filepath.Join(sysDir, entry.Name()))
			if err != nil {
				continue
			}
			protoPackage := extractProtoPackage(string(content))
			if protoPackage == "" {
				protoPackage = name
			}

			// Use proto package for module key and java package (source of truth)
			moduleKey := "sys." + protoPackage

			// Always add java_package option to the proto file for correct protoc routing
			if err := copyProtoWithJavaPackage(
				filepath.Join(sysDir, entry.Name()),
				filepath.Join(protoDir, relPath),
				relPath, config); err != nil {
				zap.L().Warn("Failed to add java_package to lib proto",
					zap.String("file", entry.Name()), zap.Error(err))
			}

			if _, exists := modules[moduleKey]; exists {
				// Module already exists — add this file to it (same proto package, different filename)
				modules[moduleKey].ProtoFiles = append(modules[moduleKey].ProtoFiles, relPath)
				continue
			}

			javaPackage := fmt.Sprintf("%s.types.sys.%s", config.JavaPackageBase, protoPackage)

			modules[moduleKey] = &ModuleInfo{
				Name:         fmt.Sprintf("types-sys-%s", protoPackage),
				TypeDir:      "sys",
				ProtoPackage: protoPackage,
				FilenameStem: protoPackage,
				JavaPackage:  javaPackage,
				ProtoFiles:   []string{relPath},
			}
		}
	}

	// Register embedded common protos as modules so shared runtime request
	// types (for example sys/tokenrequest.proto) get compiled.
	commonDir := filepath.Join(protoDir, "common")
	if entries, err := os.ReadDir(commonDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".proto")
			relPath := filepath.Join("common", entry.Name())
			srcPath := filepath.Join(commonDir, entry.Name())
			content, err := os.ReadFile(srcPath)
			if err != nil {
				continue
			}
			protoPackage := extractProtoPackage(string(content))
			if protoPackage == "" {
				protoPackage = name
			}

			if err := copyProtoWithJavaPackage(srcPath, filepath.Join(protoDir, relPath), relPath, config); err != nil {
				zap.L().Warn("Failed to add java_package to common proto",
					zap.String("file", entry.Name()), zap.Error(err))
			}

			moduleKey := "common." + protoPackage
			if existing, exists := modules[moduleKey]; exists {
				existing.ProtoFiles = append(existing.ProtoFiles, relPath)
				continue
			}

			modules[moduleKey] = &ModuleInfo{
				Name:         fmt.Sprintf("types-common-%s", protoPackage),
				TypeDir:      "common",
				ProtoPackage: protoPackage,
				FilenameStem: protoPackage,
				JavaPackage:  fmt.Sprintf("%s.types.common.%s", config.JavaPackageBase, protoPackage),
				ProtoFiles:   []string{relPath},
			}
		}
	}

	// Inject per-service ConfigState messages into each service's own proto file.
	zap.L().Info("Injecting per-service ConfigState messages")
	feConfigs, err := proto.InjectGrpcConfigState(config.InputDir, protoDir, config.FlatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to inject gRPC config state: %w", err)
	}
	if err := proto.ResolveServiceStates(config, feConfigs); err != nil {
		return err
	}

	// Generate TenantConfig proto from (spec.config_slice) tenant-scope annotations.
	if err := proto.GenerateTenantConfig(config.InputDir, config.FlatProtoDir, protoDir); err != nil {
		return fmt.Errorf("failed to generate tenant_config.proto: %w", err)
	}
	// Generate ProjectConfig proto from (spec.config_slice) project-scope annotations.
	if err := proto.GenerateProjectConfig(config.InputDir, config.FlatProtoDir, protoDir); err != nil {
		return fmt.Errorf("failed to generate project_config.proto: %w", err)
	}

	// Add java_package to generated/tenant_config.proto (ServiceProtoParts skips "generated").
	if err := addJavaPackageToGeneratedTenantConfig(protoDir, config); err != nil {
		return fmt.Errorf("failed to add java_package to generated/tenant_config.proto: %w", err)
	}
	// Add java_package to generated/project_config.proto.
	if err := addJavaPackageToGeneratedProjectConfig(protoDir, config); err != nil {
		return fmt.Errorf("failed to add java_package to generated/project_config.proto: %w", err)
	}

	// Add java_package options to service subdirectory proto files.
	if err := addJavaPackageToServiceSubdirs(protoDir, config); err != nil {
		return fmt.Errorf("failed to add java_package to service subdirs: %w", err)
	}

	// Append state/store service definitions BEFORE trace injection so that
	// auto-appended message types also receive trace fields.
	// Inject per-field "<field>_redacted" indicator bools before native compilation
	// so they appear in every language's generated type.
	if err := proto.InjectRedactIndicatorFields(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	// <T>Public for every (spec.public_view) message: its SHOW fields only.
	if err := proto.AppendPublicViewMessagesToProtos(protoDir); err != nil {
		return fmt.Errorf("failed to append public view messages: %w", err)
	}
	if err := proto.AppendEntitlementMessagesToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}
	if err := proto.AppendStateServiceToProtos(protoDir, protoDir); err != nil {
		return fmt.Errorf("failed to append state service to protos: %w", err)
	}
	if err := proto.AppendStoreServiceToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append store service to protos: %w", err)
	}
	if err := proto.GenerateTransformationMessages(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to generate projection messages: %w", err)
	}
	if err := proto.AppendRecordMapServiceToProtos(protoDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append record map service to protos: %w", err)
	}
	// The CLIENT-FACING half, after the record plane above because it folds the
	// same criteria and must see the same records. Emits nothing unless a
	// service declares (spec.record_read).c2s.
	if err := proto.AppendRecordC2SServiceToProtos(protoDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append client-facing record read plane to protos: %w", err)
	}
	// TimeseriesGroupCounts / TimeseriesQuery injection removed 2026-08-25 with
	// the timedb boneyard (BIG GOAL 3, first move #1) — no timeseries_store
	// exists in any proto set, so both could only no-op, and neither ever had a
	// consumer.
	// Auto-register virtual service gRPC servers based on proto annotations.
	// OpaEval for services importing sys/opa.proto.
	if isOpaServer, err := proto.IsOpaServer(config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to check OPA server status: %w", err)
	} else if isOpaServer {
		opaProto, opaPkg := proto.FindAnnotatedSourceFileFromDir(config.FlatProtoDir, "opa_eval")
		config.GrpcServers = append(config.GrpcServers, tactype.GrpcEntry{
			ProtoFile:    opaProto,
			ServiceName:  "OpaEval",
			ProtoPackage: opaPkg,
		})
	}
	// Inject trace round fields into all message types.
	// Parse from protoDir so appended types are visible.
	skippedTraceTypes, err := proto.InjectTraceFields(protoDir, protoDir)
	if err != nil {
		return fmt.Errorf("failed to inject trace fields: %w", err)
	}
	if err := proto.SwapEmptyInputs(protoDir); err != nil {
		return fmt.Errorf("failed to swap empty inputs to TraceRequest: %w", err)
	}
	if err := proto.InjectRegistryEnums(protoDir); err != nil {
		return fmt.Errorf("failed to inject registry enums into protos: %w", err)
	}

	// Discover newly created subdirectory files (e.g., services.proto or state sources)
	// that were created after the initial walk (e.g., by proto splitting).
	// Walk all top-level directories except sys and google.
	if topEntries, topErr := os.ReadDir(protoDir); topErr == nil {
		for _, topEntry := range topEntries {
			if !topEntry.IsDir() || topEntry.Name() == "sys" || topEntry.Name() == "google" {
				continue
			}
			topDirName := topEntry.Name()
			topDirPath := filepath.Join(protoDir, topDirName)
			serviceDirs, err := os.ReadDir(topDirPath)
			if err != nil {
				continue
			}
			for _, dirEntry := range serviceDirs {
				if !dirEntry.IsDir() {
					continue
				}
				serviceName := dirEntry.Name()
				subDir := filepath.Join(topDirPath, serviceName)
				subEntries, err := os.ReadDir(subDir)
				if err != nil {
					continue
				}
				for _, subEntry := range subEntries {
					if subEntry.IsDir() || !strings.HasSuffix(subEntry.Name(), ".proto") {
						continue
					}
					relPath := filepath.Join(topDirName, serviceName, subEntry.Name())
					fileName := strings.TrimSuffix(subEntry.Name(), ".proto")

					// Read proto content to get the package declaration (source of truth)
					content, err := os.ReadFile(filepath.Join(subDir, subEntry.Name()))
					if err != nil {
						continue
					}
					source := string(content)
					protoPackage := extractProtoPackage(source)
					if protoPackage == "" {
						protoPackage = strings.ReplaceAll(serviceName, "-", "_") // fallback
					}
					declaresServiceState := proto.DeclaresServiceStateSource(source)

					moduleKey := topDirName + "." + protoPackage + "." + fileName
					if _, exists := modules[moduleKey]; exists {
						continue
					}

					var subPkg string
					switch {
					case fileName == "types" || fileName == serviceName:
						subPkg = protoPackage
					case proto.IsServicesFileName(fileName):
						subPkg = "services"
					case declaresServiceState:
						subPkg = "services"
					default:
						subPkg = fileName
					}
					javaPackage := fmt.Sprintf("%s.types.%s.%s.%s", config.JavaPackageBase, topDirName, protoPackage, subPkg)
					moduleName := fmt.Sprintf("types-%s-%s-%s", topDirName, protoPackage, fileName)

					modules[moduleKey] = &ModuleInfo{
						Name:         moduleName,
						TypeDir:      topDirName,
						ProtoPackage: protoPackage,
						FilenameStem: protoPackage + "_" + fileName,
						JavaPackage:  javaPackage,
						ProtoFiles:   []string{relPath},
					}
				}
			}
		}
	}

	zap.L().Info("Generating Java modules", zap.Int("count", len(modules)))

	// Parent directory for generated code
	parentDir := config.OutputDir

	// Pre-parse all proto files for cross-package resolution (LLM type descriptors).
	allProtoParser, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for allFiles: %w", err)
	}
	// Sort module keys for deterministic iteration order. Go map iteration
	// is randomized; without sorting, the order in which modules are generated
	// leaks into the output (e.g. ReplaceProtocHeaders from a later module
	// post-processes Helpers.java from an earlier one, changing blank lines).
	moduleKeys := make([]string, 0, len(modules))
	for k := range modules {
		moduleKeys = append(moduleKeys, k)
	}
	sort.Strings(moduleKeys)

	var allParsedFiles []model.ProtoFile
	for _, key := range moduleKeys {
		module := modules[key]
		for _, pf := range module.ProtoFiles {
			parsed, parseErr := allProtoParser.ConvertToProtoFile(filepath.Join(protoDir, pf))
			if parseErr != nil {
				continue
			}
			allParsedFiles = append(allParsedFiles, parsed)
		}
	}

	// Generate each module
	for _, key := range moduleKeys {
		module := modules[key]
		zap.L().Info("Module proto files", zap.String("module", module.Name), zap.Strings("files", module.ProtoFiles))
		if err := generateModule(module, protoDir, config, parentDir, allParsedFiles, svcRPCs, vsEntries); err != nil {
			return fmt.Errorf("failed to generate module %s: %w", module.Name, err)
		}
	}

	// Generate utils module with ProtoException for validation
	zap.L().Info("Generating utils module")
	if err := generateUtilsModule(parentDir, config); err != nil {
		return fmt.Errorf("failed to generate utils module: %w", err)
	}

	// Generate shared Validators class in sys/utils/
	zap.L().Info("Generating Validators class")
	if err := generateValidators(parentDir, config); err != nil {
		return fmt.Errorf("failed to generate Validators: %w", err)
	}

	// Generate Utils class with DecodeBase64 and GrpcStatusToHTTP
	zap.L().Info("Generating Utils class")
	if err := generateUtilsClass(parentDir, config); err != nil {
		return fmt.Errorf("failed to generate Utils: %w", err)
	}

	// Build grpcName → AccountingInfo mapping for accounting method generation.
	accountingMap := make(map[string]service.AccountingInfo, len(svcRPCs))
	for _, svc := range svcRPCs {
		rpcs := make(map[string]bool, len(svc.RPCs))
		for _, rpc := range svc.RPCs {
			rpcs[rpc] = true
		}
		accountingMap[svc.GrpcName] = service.AccountingInfo{EnumName: svc.EnumName, RPCs: rpcs}
	}

	// Generate service wrappers for services with generate_user_api.
	// Use protoDir (split protos) instead of config.InputDir to avoid circular import errors.
	servicesInfo, err := detectServicesForWrapper(config, protoDir)
	if err != nil {
		return fmt.Errorf("failed to detect services for wrapper: %w", err)
	}
	// Generate service wrappers — only for gRPC server services
	serverServices := resolveServerServices(config, servicesInfo)
	// Additively include own-package c2d daemon services: the ServiceManager
	// hosts them on a local unix-socket c2d server (HasC2DGrpc), never on the
	// TLS gRPC listener. resolveServerServices filters c2d out, so add them back
	// here so the daemon server surface (wrapper + Handler) is generated.
	serverServices = append(serverServices, resolveC2DServerServices(config, servicesInfo)...)
	// hasAccountingByService (ServiceName -> bool) records, per wrapper, whether
	// it ended up with accounting wired in (post Q2-exclusion) — i.e. whether it
	// defines setEmitter. ServiceManager's wiring loop must only call setEmitter
	// on wrappers that have it.
	var hasAccountingByService map[string]bool
	if len(serverServices) > 0 {
		zap.L().Info("Generating Java service wrappers", zap.Int("count", len(serverServices)))
		hasAccountingByService, err = generateServiceWrappers(config, serverServices, protoDir, accountingMap, skippedTraceTypes)
		if err != nil {
			return fmt.Errorf("failed to generate service wrappers: %w", err)
		}
	}

	// Generate gRPC client wrappers — typed client stubs for gRPC client services
	clientServices := resolveClientServices(config, servicesInfo)
	if len(clientServices) > 0 {
		zap.L().Info("Generating Java gRPC client wrappers", zap.Int("count", len(clientServices)))
		if err := generateGrpcClients(config, clientServices, protoDir); err != nil {
			return fmt.Errorf("failed to generate gRPC clients: %w", err)
		}
	}
	if err := parser.ValidateTokenMinters(protoDir, s2sGenerationEnabled(config, serverServices)); err != nil {
		return err
	}

	// hasClientManagers tracks whether this build emitted any client-facing
	// service manager (c2sm/c2dm/w2sm). When true the build authenticates as a
	// client (CLI/daemon/webapp) and gets the client identity manager
	// (client_im), which mints via the client-reachable C2S exchanger.
	hasClientManagers := false

	// Generate c2sm (client-to-service) gRPC client wrappers
	c2sServices := resolveC2SClientServices(config, servicesInfo)
	if len(c2sServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "c2sm"
		zap.L().Info("Generating Java c2s gRPC client wrappers", zap.Int("count", len(c2sServices)))
		if err := generateGrpcClients(config, c2sServices, protoDir); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2s gRPC clients: %w", err)
		}

		// Generate c2sm entry point (lightweight ServiceManager with channel + grpc accessor)
		zap.L().Info("Generating Java c2sm ServiceManager")
		if err := generateC2SServiceManager(config, c2sServices); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2sm ServiceManager: %w", err)
		}
		hasClientManagers = true
		config.SmPrefix = origPrefix
	}

	// Generate c2dm (client-to-daemon) gRPC client wrappers when the proto graph
	// declares in-scope service_c2d services (or an explicit GrpcC2DClients allowlist).
	if c2dServices := resolveC2DClientServices(config, servicesInfo); len(c2dServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "c2dm"
		zap.L().Info("Generating Java c2d gRPC client wrappers", zap.Int("count", len(c2dServices)))
		if err := generateGrpcClients(config, c2dServices, protoDir); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d gRPC clients: %w", err)
		}
		zap.L().Info("Generating Java c2dm ServiceManager")
		if err := generateC2DServiceManager(config, c2dServices); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2dm ServiceManager: %w", err)
		}
		hasClientManagers = true
		config.SmPrefix = origPrefix
	}

	// Generate w2sm (web-to-service) HTTP/SSE/WebSocket client wrappers
	w2sServices := resolveW2SClientServices(config, servicesInfo)
	if len(w2sServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "w2sm"
		zap.L().Info("Generating Java w2s client wrappers", zap.Int("count", len(w2sServices)))
		if err := generateW2SClients(config, w2sServices, protoDir); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate w2s clients: %w", err)
		}
		zap.L().Info("Generating Java w2sm ServiceManager")
		if err := generateW2SServiceManager(config); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate w2sm ServiceManager: %w", err)
		}
		hasClientManagers = true
		config.SmPrefix = origPrefix
	}

	// Generate REST client wrappers — typed REST stubs for gRPC client services
	restClientServices := resolveRestClientServices(config, servicesInfo)
	if len(restClientServices) > 0 {
		zap.L().Info("Generating Java REST client wrappers", zap.Int("count", len(restClientServices)))
		if err := generateRestClients(config, restClientServices, protoDir); err != nil {
			return fmt.Errorf("failed to generate REST clients: %w", err)
		}
	}

	// Generate the REST -> gRPC dispatch plane for a (spec.service).rest_dispatch
	// build. The gateway and rest_dispatch options sit on the self package's
	// proto, not in tacplus.yaml.
	if config.SelfPackage != "" && len(servicesInfo) > 0 {
		dp, err := parser.NewDescriptorParser(protoDir)
		if err != nil {
			return fmt.Errorf("failed to create descriptor parser for REST dispatch: %w", err)
		}
		config.IsGateway, config.HasRestDispatch = restdispatch.Detect(dp, config.SelfPackage)
		if config.HasRestDispatch {
			zap.L().Info("Generating Java REST dispatch", zap.Bool("isGateway", config.IsGateway))
			if err := generateJavaRestDispatch(config, servicesInfo, protoDir); err != nil {
				return fmt.Errorf("failed to generate REST dispatch: %w", err)
			}
		}
	}

	// Generate per-type REST CRUD clients
	restCrudPackages := resolveRestCrudPackagesJava(config, allParsedFiles)
	if len(restCrudPackages) > 0 {
		zap.L().Info("Generating per-type REST CRUD clients (Java)", zap.Int("packages", len(restCrudPackages)))
		if err := generateJavaRestCrudClients(config, restCrudPackages); err != nil {
			return fmt.Errorf("failed to generate REST CRUD clients: %w", err)
		}
	}

	// Generate typed disk-persistence helpers for service_store messages (no-op
	// when there are none). Runs after modules + Helpers are generated and while
	// the split protos still carry spec annotations (stripped at the end).
	{
		diskSrcDir := filepath.Join(parentDir, "src", "main", "java")
		if err := generateJavaDiskStore(config, protoDir, diskSrcDir); err != nil {
			return fmt.Errorf("failed to generate diskstore helpers: %w", err)
		}
	}

	// Generate store-neutral timeseries projection helpers for
	// (spec.timeseries_type) messages (no-op when there are none). Same placement
	// as diskstore: after modules + Helpers, while the split protos still carry
	// spec annotations.
	{
		tsSrcDir := filepath.Join(parentDir, "src", "main", "java")
		if err := generateJavaTimeseriesHelpers(config, protoDir, tsSrcDir); err != nil {
			return fmt.Errorf("failed to generate timeseries helpers: %w", err)
		}
		// Extract<Projection>From<Source> / <source>ToProjections for messages
		// with a (spec.timeseries_type).source, beside the timeseries helpers.
		if err := generateJavaExtractionHelpers(config, allParsedFiles, protoDir, tsSrcDir); err != nil {
			return fmt.Errorf("failed to generate extraction helpers: %w", err)
		}
	}

	// Collect event-bus watches from legacy subscribe, explicit event_watch,
	// and validated store-derived watches.
	config.Subscriptions, err = collectJavaSubscriptions(config, protoDir)
	if err != nil {
		return fmt.Errorf("failed to collect event-bus subscriptions: %w", err)
	}
	if len(config.Subscriptions) > 0 {
		zap.L().Info("Found event-bus subscriptions", zap.Int("count", len(config.Subscriptions)))
	}
	config.AnnotatedProtoDir = protoDir

	// Generate handler interface and service_manager.
	if len(serverServices) > 0 || len(config.States) > 0 {
		var grpcSvcs []ServiceInfo
		if len(serverServices) > 0 {
			explicit := filterExplicitGrpcServicesJava(config, serverServices)
			if len(explicit) > 0 {
				grpcSvcs = explicit
				// c2d services are never listed in GrpcServers; they are hosted
				// on the c2d socket server whatever the explicit TLS set is.
				for _, svc := range serverServices {
					if svc.IsC2D {
						grpcSvcs = append(grpcSvcs, svc)
					}
				}
			} else {
				for _, svc := range serverServices {
					// An MCP-only service (IsGrpc false) still carries a
					// synthesized session carrier RPC to mount.
					if svc.IsGrpc || svc.HasMcp {
						grpcSvcs = append(grpcSvcs, svc)
					}
				}
			}
		}
		zap.L().Info("Generating Java handler interface and service_manager")
		if err := generateJavaHandlerInterface(config, grpcSvcs, clientServices, protoDir, hasAccountingByService); err != nil {
			return fmt.Errorf("failed to generate handler interface: %w", err)
		}
		if err := generateJavaServiceManager(config, grpcSvcs, clientServices, protoDir, hasAccountingByService); err != nil {
			return fmt.Errorf("failed to generate service_manager: %w", err)
		}
	}

	// Generate event-bus subscriber classes.
	if len(config.Subscriptions) > 0 {
		zap.L().Info("Generating Java event-bus subscriber classes", zap.Int("count", len(config.Subscriptions)))
		if err := generateJavaSubscribers(config, protoDir); err != nil {
			return fmt.Errorf("failed to generate event-bus subscribers: %w", err)
		}
	}

	// Generate state client wrappers only when explicitly requested by config.
	if len(config.States) > 0 || config.AllStates {
		if err := generateJavaStateClientWrappers(config, servicesInfo, protoDir); err != nil {
			return fmt.Errorf("failed to generate Java state client wrappers: %w", err)
		}
	}

	// Generate registry.json, registry.schema.json, AGENT.md (shared across all languages).
	if err := registry.Generate(registry.Config{
		OutputDir:   config.OutputDir,
		VersionTag:  config.VersionTag,
		InputDir:    protoDir,
		Language:    "java",
		PackageBase: config.JavaPackageBase,
	}, allParsedFiles, allProtoParser); err != nil {
		return fmt.Errorf("failed to generate registry.json: %w", err)
	}

	// Copy registry.json to classpath resources so it can be loaded at runtime.
	{
		src := filepath.Join(config.OutputDir, "registry.json")
		dstDir := filepath.Join(config.OutputDir, "src", "main", "resources")
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			return fmt.Errorf("failed to create resources dir: %w", err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("failed to read registry.json: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, "registry.json"), data, 0644); err != nil {
			return fmt.Errorf("failed to copy registry.json to resources: %w", err)
		}
	}

	// Write library templates
	zap.L().Info("Writing Java library templates")
	declaredAuds, err := proto.ScanAcceptedAudiences(config.InputDir, config.ServiceLabel())
	if err != nil {
		return fmt.Errorf("failed to scan (spec.service).accepted_audiences: %w", err)
	}
	if err := writeLibTemplates(config, proto.AcceptedAudiencesForHost(config.ServiceLabel(), vsEntries, declaredAuds), svcRPCs); err != nil {
		return fmt.Errorf("failed to write library templates: %w", err)
	}

	// Emit the client identity manager (client_im) for builds that authenticate
	// as a client (CLI/daemon/webapp): any c2sm/c2dm/w2sm surface. client_im
	// mints via the client-reachable C2S exchanger, not the authoritative S2S
	// mint (service_im). Discovery runs before StripSpecOptionsFromDir removes
	// the (spec.token_minter) annotations it keys on (config.AnnotatedProtoDir).
	if hasClientManagers {
		if err := writeClientIdentityManager(config); err != nil {
			return fmt.Errorf("failed to write client identity manager: %w", err)
		}
	}

	// Write annotated protos (with spec.* annotations) to proto-annotated/ directory.
	// The codegen-plugin Maven plugin reads these to discover state boundaries.
	zap.L().Info("Writing annotated protos for codegen-plugin")
	if err := proto.WriteAnnotatedProtos(protoDir, parentDir); err != nil {
		return fmt.Errorf("failed to write annotated protos: %w", err)
	}

	// Write codegen-config.json alongside annotated protos so the codegen-plugin
	// can read package configuration instead of requiring hardcoded Maven parameters.
	// Security policies are computed from proto annotations (disable_auth_message,
	// boundary_callers, instance_keyed) instead of a YAML file.
	securityPolicies, err := proto.ComputeSecurityPolicies(config.FlatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to compute security policies: %w", err)
	}
	// Post-condition: the read policy published here must match the caller list
	// gating the generated state services, or the two access planes have drifted.
	if err := proto.ValidateStatePolicyAuthParity(config.FlatProtoDir, securityPolicies); err != nil {
		return err
	}
	acceptedAudiences := proto.AcceptedAudiencesForHost(config.ServiceLabel(), vsEntries, declaredAuds)
	if err := proto.WriteCodegenConfig(parentDir, config.JavaPackageBase, config.Lang.Java.JavaImportBase()+".graphs", acceptedAudiences, securityPolicies, svcRPCs); err != nil {
		return fmt.Errorf("failed to write codegen config: %w", err)
	}

	// Strip spec.* annotations from output proto files and remove spec.proto.
	// These annotations have already been consumed by the compiler during code generation.
	zap.L().Info("Stripping spec annotations from output protos")
	if err := proto.StripSpecOptionsFromDir(protoDir); err != nil {
		return fmt.Errorf("failed to strip spec options: %w", err)
	}

	// Clean up empty directories left over from proto splitting/protoc processing
	srcDir := filepath.Join(parentDir, "src", "main", "java")
	if err := removeEmptyDirs(srcDir); err != nil {
		zap.L().Warn("failed to clean up empty java directories", zap.String("dir", srcDir), zap.Error(err))
	}

	zap.L().Info("Java generation complete")
	zap.L().Info("To build", zap.String("command", "cd "+parentDir+"&& mvn clean install"))
	return nil
}

// s2sGenerationEnabled reports whether this build emits a service-to-service
// surface, and therefore whether the generated ServiceManager will need an S2S
// identity (which is what ValidateTokenMinters polices).
//
// Synthesized STORE services do not count. spec.service_store synthesizes one
// <Msg>StoreService per record and emits each as S2S, so a c2d-only daemon that
// declares records — and uses only the s2sm/diskstore helpers, never the store
// services — was reported as having an S2S surface and hard-failed generation
// with "no authoritative (spec.token_minter) service is declared". sideband2 is
// the concrete case.
//
// Synthesized STATE services DO count, which is why this tests the kind rather
// than Synthesized: a service with a state root must reach its state host, so it
// genuinely needs an S2S identity, and dropping every synthesized service from
// this gate would have silently stopped requiring a minter for that whole class.
func s2sGenerationEnabled(config *tactype.Config, serverServices []ServiceInfo) bool {
	if config.GrpcS2SClientAll || len(config.GrpcS2SClients) > 0 {
		return true
	}
	for _, svc := range serverServices {
		if svc.IsS2S && svc.SynthesizedKind != parser.SynthesizedKindStore {
			return true
		}
	}
	return false
}

// removeEmptyDirs recursively removes empty directories under root.
func removeEmptyDirs(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() || path == root {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		if len(entries) == 0 {
			os.Remove(path)
		}
		return nil
	})
}
