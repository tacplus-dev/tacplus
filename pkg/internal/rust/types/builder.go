// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	internalproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// ServiceInfo is a type alias for common service.ServiceInfo.
type ServiceInfo = service.ServiceInfo

// generateAggregationFiles creates group-level and root-level aggregation files
// for gRPC servers, HTTP clients, and state clients to match Go's layout.
func generateAggregationFiles(config *tactype.Config) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal")

	categories := []struct {
		dir  string
		name string
	}{
		{"grpc/servers", "grpc_servers"},
		// state_clients aggregation is handled by generateRustStateClientAggregators
		// in statewrapper_gen.go, which generates proper StateClients structs.
	}

	for _, cat := range categories {
		catDir := filepath.Join(smDir, cat.dir)
		if _, err := os.Stat(catDir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(catDir)
		if err != nil {
			continue
		}

		var groups []string
		for _, e := range entries {
			if e.IsDir() {
				groups = append(groups, e.Name())
			}
		}

		for _, group := range groups {
			groupDir := filepath.Join(catDir, group)
			if err := generateRsGroupAggregator(groupDir, cat.name); err != nil {
				return fmt.Errorf("failed to generate group %s/%s: %w", cat.dir, group, err)
			}
		}

		if err := generateRsRootAggregator(catDir, cat.name, groups); err != nil {
			return fmt.Errorf("failed to generate root %s: %w", cat.dir, err)
		}
	}

	return nil
}

// generateRsGroupAggregator creates a group-level aggregation file (e.g., sm/internal/grpc_clients/services/http_clients.rs).
func generateRsGroupAggregator(groupDir, fileName string) error {
	entries, err := os.ReadDir(groupDir)
	if err != nil {
		return nil
	}

	var mods []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(groupDir, e.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".rs") {
				continue
			}
			if se.Name() == "mod.rs" || se.Name() == fileName+".rs" {
				continue
			}
			baseName := strings.TrimSuffix(se.Name(), ".rs")
			mods = append(mods, fmt.Sprintf("pub use super::%s::%s;", e.Name(), baseName))
		}
	}

	if len(mods) == 0 {
		return nil
	}

	content := "// Auto-generated aggregator\n" + strings.Join(mods, "\n") + "\n"
	outputPath := filepath.Join(groupDir, fileName+".rs")
	return os.WriteFile(outputPath, []byte(content), 0644)
}

// generateRsRootAggregator creates a root-level aggregation file (e.g., sm/internal/grpc_clients/http_clients.rs).
func generateRsRootAggregator(catDir, fileName string, groups []string) error {
	var mods []string
	for _, group := range groups {
		groupAgg := filepath.Join(catDir, group, fileName+".rs")
		if _, err := os.Stat(groupAgg); err == nil {
			mods = append(mods, fmt.Sprintf("pub use super::%s::%s;", group, fileName))
		}
	}

	if len(mods) == 0 {
		return nil
	}

	content := "// Auto-generated root aggregator\n" + strings.Join(mods, "\n") + "\n"
	outputPath := filepath.Join(catDir, fileName+".rs")
	return os.WriteFile(outputPath, []byte(content), 0644)
}

// processProtoFiles walks through all proto files, parses them, and generates helper files
func processProtoFiles(protocDir, outputDir string) ([]model.ProtoFile, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Use descriptor-based parser (robust, uses protoc internally)
	parser, err := parser.NewDescriptorParser(protocDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var protoFiles []model.ProtoFile

	// Walk through all proto files
	err = filepath.Walk(protocDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		// Parse proto file using descriptor parser
		protoFile, err := parser.ConvertToProtoFile(path)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}

		if len(protoFile.Enums) > 0 || len(protoFile.Constants) > 0 || len(protoFile.Messages) > 0 {
			protoFiles = append(protoFiles, protoFile)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// (spec.rpc).requires_device: which messages owe a canonicalization
	// function. Rust has no runtime reflection, so the approval request hash is
	// generated rather than walked — see requires_device_gen.go. The plan is
	// empty, and nothing is emitted, for a proto set declaring no guarded rpc.
	approvalPlan := buildRustApprovalPlan(parser, protoFiles)

	// Track which subdirectories contain helper files
	subdirHelpers := make(map[string][]string)

	hasDefaults := messagesWithDefaults(protoFiles)
	// Generate a single helpers.rs per proto file
	for _, pf := range protoFiles {
		// Skip helper generation only for the spec package — it has its own special-case
		// generation below and conflicts with prost-generated spec.rs (Rust can't have
		// both spec.rs and spec/mod.rs in the same crate). All other sys protos get
		// helpers generated normally (serialization, enums, validation, defaults, merge).
		if pf.Package == "spec" {
			continue
		}

		// Skip google/ well-known types — prost handles them natively
		if strings.HasPrefix(pf.RelPath, "google/") || strings.HasPrefix(pf.RelPath, "google"+string(filepath.Separator)) {
			continue
		}

		parentDir := filepath.Dir(pf.RelPath)
		// All type directories go under types/ wrapper (matching Go layout).
		// Files are placed directly in the module dir (no extra types/ subdir).
		var subdir string
		if strings.HasPrefix(parentDir, "sys") {
			// e.g., sys/alpn.proto -> types/sys/alpn/
			subdir = filepath.Join("types", parentDir, pf.Package)
		} else if filepath.Base(parentDir) == pf.Package {
			// e.g., services/baseball/baseball.proto -> types/services/baseball/baseball/
			subdir = filepath.Join("types", parentDir, pf.Package)
		} else {
			// e.g., common/ballsports.proto -> types/common/ballsports/
			subdir = filepath.Join("types", parentDir, pf.Package)
		}

		var b strings.Builder
		b.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
		fmt.Fprintf(&b, "// Source: %s\n", pf.RelPath)
		b.WriteString(header.Copyright(consts.LangRust))

		// Write combined imports
		writeCombinedImports(&b, pf)

		// Write sections
		if len(pf.Enums) > 0 {
			writeEnumHelpers(&b, pf)
		}
		if len(pf.Constants) > 0 || hasFieldDefaults(pf, hasDefaults) {
			writeConstants(&b, pf)
		}
		if len(pf.Messages) > 0 {
			writeSerialization(&b, pf, protoFiles)
		}
		if len(pf.Messages) > 0 {
			writeValidation(&b, pf)
		}
		if len(pf.Messages) > 0 {
			if err := writeRelease(&b, pf, protoFiles); err != nil {
				return nil, fmt.Errorf("writeRelease %s: %w", pf.RelPath, err)
			}
		}
		writePublicView(&b, pf)
		if len(pf.Messages) > 0 {
			writeProofHarnesses(&b, pf)
		}
		if hasParamMapMessages(pf) {
			if err := writeFromMap(&b, pf); err != nil {
				return nil, fmt.Errorf("writeFromMap %s: %w", pf.RelPath, err)
			}
		}
		writeDefaults(&b, pf, hasDefaults)
		writeUndefaults(&b, pf, hasDefaults)
		writeMerge(&b, pf)
		if len(pf.Messages) > 0 {
			writeEnvVar(&b, pf)
		}
		if len(pf.Messages) > 0 {
			writeApprovalCanonical(&b, pf, protoFiles, approvalPlan)
		}

		// Combine #[cfg(test)] sections into a single test module
		writeTests(&b, pf)

		// Write single helpers.rs
		helperDir := filepath.Join(outputDir, subdir)
		if err := os.MkdirAll(helperDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create helper directory %s: %w", helperDir, err)
		}
		outputPath := filepath.Join(helperDir, "helpers.rs")
		if err := os.WriteFile(outputPath, []byte(b.String()), 0644); err != nil {
			return nil, fmt.Errorf("failed to write helpers.rs for %s: %w", pf.RelPath, err)
		}

		// Track only "helpers" in subdirHelpers
		subdirHelpers[subdir] = []string{"helpers"}
	}

	// Generate mod.rs files for each subdirectory
	for subdir := range subdirHelpers {
		if err := generateModFile(outputDir, subdir); err != nil {
			return nil, fmt.Errorf("failed to generate mod.rs for %s: %w", subdir, err)
		}
	}

	// Generate parent mod.rs files for multi-level directories (e.g., libraries/, common/)
	if err := generateParentModFiles(outputDir, subdirHelpers); err != nil {
		return nil, fmt.Errorf("failed to generate parent mod files: %w", err)
	}

	// Generate additional filesystem-specific helpers (directory_to_filesystem, directory_from_filesystem).
	// These are supplemental to the standard helpers generated in the main loop above.
	for _, pf := range protoFiles {
		if internalproto.EmitsFilesystemHelpers(pf.RelPath, pf.Package) {
			subdir := filepath.Join("types", "sys", "filesystem")
			helperDir := filepath.Join(outputDir, subdir)
			if err := os.MkdirAll(helperDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create filesystem helper dir: %w", err)
			}
			var fsB strings.Builder
			fsB.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
			fmt.Fprintf(&fsB, "// Source: %s\n", pf.RelPath)
			fsB.WriteString(header.Copyright(consts.LangRust) + "\n")
			writeFilesystemHelpers(&fsB)
			outputPath := filepath.Join(helperDir, "filesystem_helpers.rs")
			if err := os.WriteFile(outputPath, []byte(fsB.String()), 0644); err != nil {
				return nil, fmt.Errorf("failed to write filesystem_helpers.rs: %w", err)
			}
			// Write mod.rs that declares the filesystem_helpers module
			modPath := filepath.Join(helperDir, "mod.rs")
			modContent := "// Code generated by tacplus. DO NOT EDIT.\n" + header.Copyright(consts.LangRust) + "pub mod filesystem_helpers;\n#[allow(ambiguous_glob_reexports, unused_imports)]pub use filesystem_helpers::*;\n"
			if err := os.WriteFile(modPath, []byte(modContent), 0644); err != nil {
				return nil, fmt.Errorf("failed to write filesystem mod.rs: %w", err)
			}
			break
		}
	}

	return protoFiles, nil
}

// generateModFile creates a mod.rs file in the subdirectory that declares the helpers module
func generateModFile(baseDir, subdir string) error {
	modPath := filepath.Join(baseDir, subdir, "mod.rs")

	var b strings.Builder
	b.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
	b.WriteString(header.Copyright(consts.LangRust))
	b.WriteString("// Note: Proto module declaration will be added by generateModuleStructure\n")

	// Declare the single helpers module
	b.WriteString("pub mod helpers;\n")

	// Re-export all items from helpers for convenience
	b.WriteString("// Re-export all items for convenience\n")
	b.WriteString("#[allow(ambiguous_glob_reexports, unused_imports)]pub use helpers::*;\n")

	return os.WriteFile(modPath, []byte(b.String()), 0644)
}

// hasParamMapMessages returns true if any message in the proto file has ParamMapConfig
func hasParamMapMessages(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			return true
		}
	}
	return false
}

// helpersHeaderData holds pre-computed data for the helpers header template.
type helpersHeaderData struct {
	RelPath       string
	HasEnums      bool
	HasMessages   bool
	HasParamMaps  bool
	NetImports    string
	NeedsRegex    bool
	NeedsBase64   bool
	NeedsEncType  bool
	NeedsDuration bool
	NeedsHost     bool
}

// testsWrapperData holds data for the test module wrapper template.
type testsWrapperData struct {
	HasEnumTests bool
	TestContent  string
}

// modFileData holds data for the mod.rs template.
type modFileData struct {
	Modules       []string
	SkipReExports bool // if true, only declare modules without `pub use *` re-exports
}

// writeCombinedImports writes all necessary imports for the combined helpers.rs file
func writeCombinedImports(b *strings.Builder, pf model.ProtoFile) {
	// Validation imports - check what validators are needed
	usage := &validatorUsage{}
	for _, msg := range pf.Messages {
		trackValidatorUsage(msg, usage)
	}

	var netImports []string
	if usage.needsIpAddr || usage.needsCidr {
		netImports = append(netImports, "IpAddr")
	}
	if usage.needsIpv4Addr {
		netImports = append(netImports, "Ipv4Addr")
	}
	if usage.needsIpv6Addr {
		netImports = append(netImports, "Ipv6Addr")
	}

	needsDuration := false
	for _, c := range pf.Constants {
		if strings.Contains(c.Type, "Duration") || strings.Contains(c.Value, "Duration") { // Go code representation
			needsDuration = true
			break
		}
	}

	// Also check for default_host annotations (which generate Host { ... } in defaults)
	needsHost := usage.needsHost
	if !needsHost {
		for _, msg := range pf.Messages {
			for _, field := range msg.Fields {
				if _, ok := field.Annotations["default_host"]; ok {
					needsHost = true
					break
				}
			}
			if needsHost {
				break
			}
		}
	}

	data := helpersHeaderData{
		RelPath:       pf.RelPath,
		HasEnums:      len(pf.Enums) > 0,
		HasMessages:   len(pf.Messages) > 0,
		HasParamMaps:  hasParamMapMessages(pf),
		NetImports:    strings.Join(netImports, ", "),
		NeedsRegex:    usage.needsEmail || usage.needsFqdn || usage.needsSemver,
		NeedsBase64:   usage.needsBase64,
		NeedsEncType:  usage.needsEncType,
		NeedsDuration: needsDuration,
		NeedsHost:     needsHost,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	maps.Copy(funcMap, header.Funcs())
	result, err := tmplutil.ExecuteTemplate(typeFS, "helpers_header.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing helpers header template: %s\n", err)
		return
	}

	b.Write(result)
	b.WriteString("\n")
}

// writeTests writes a combined #[cfg(test)] module with all test sections
func writeTests(b *strings.Builder, pf model.ProtoFile) {
	hasEnumTests := false
	for _, enum := range pf.Enums {
		for _, v := range enum.Values {
			if !model.IsEnumSentinel(v.Name) {
				hasEnumTests = true
				break
			}
		}
		if hasEnumTests {
			break
		}
	}

	hasSerializationTests := len(pf.Messages) > 0
	hasValidationTests := len(pf.Messages) > 0

	if !hasEnumTests && !hasSerializationTests && !hasValidationTests {
		return
	}

	// Build test content into a separate builder
	var testContent strings.Builder

	// Enum tests
	if hasEnumTests {
		testContent.WriteString("// Enum tests\n")
		for _, enum := range pf.Enums {
			writeEnumTestContent(&testContent, enum)
		}
	}

	// Serialization tests
	if hasSerializationTests {
		testContent.WriteString("// Serialization tests\n")
		for _, msg := range pf.Messages {
			writeSerializationTestContent(&testContent, msg)
		}
	}

	// Validation tests
	if hasValidationTests {
		testContent.WriteString("// Validation tests\n")
		for _, msg := range pf.Messages {
			writeValidationTestContent(&testContent, msg)
		}
	}

	data := testsWrapperData{
		HasEnumTests: hasEnumTests,
		TestContent:  testContent.String(),
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	maps.Copy(funcMap, header.Funcs())
	result, err := tmplutil.ExecuteTemplate(typeFS, "tests_wrapper.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing tests wrapper template: %s\n", err)
		return
	}

	b.Write(result)
}

// generateParentModFiles creates mod.rs files for parent directories (e.g., libraries/, common/)
func generateParentModFiles(baseDir string, subdirHelpers map[string][]string) error {
	// Track parent directories and their child modules
	parents := make(map[string]map[string]bool)

	for subdir := range subdirHelpers {
		// Split path to get parent and child
		// e.g., "sys/logger" -> parent: "sys", child: "logger"
		if strings.Contains(subdir, "/") {
			parts := strings.Split(subdir, "/")
			if len(parts) >= 2 {
				parent := parts[0]
				child := parts[1]
				if parents[parent] == nil {
					parents[parent] = make(map[string]bool)
				}
				parents[parent][child] = true
			}
		}
	}

	// Generate mod.rs for each parent directory
	for parent, children := range parents {
		modPath := filepath.Join(baseDir, parent, "mod.rs")

		var b strings.Builder
		b.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
		b.WriteString(header.Copyright(consts.LangRust))

		// Declare all child modules
		var sortedChildren []string
		for child := range children {
			sortedChildren = append(sortedChildren, child)
		}
		sort.Strings(sortedChildren)

		for _, child := range sortedChildren {
			fmt.Fprintf(&b, "pub mod %s;\n", child)
		}

		if err := os.WriteFile(modPath, []byte(b.String()), 0644); err != nil {
			return fmt.Errorf("failed to create %s: %w", modPath, err)
		}
	}

	return nil
}

// reorganizeProstOutput moves prost-generated flat .rs files into nested directories
// matching their proto file directory structure.
// When multiple proto files share the same package name (e.g., services/baseball/services.proto
// and services/football/services.proto both have "package services"), prost merges them into a
// single .rs file. This function splits the merged file so each directory only gets its own types.
// Returns a map of package name -> proto directory for fixing imports (uses first directory found).
func reorganizeProstOutput(inputDir, outputDir string) (map[string]string, error) {
	// Build a mapping of package name -> list of (protoDir, protoFilePath) pairs
	type protoDirInfo struct {
		dir      string
		filePath string
	}
	packageToDirsMap := make(map[string][]protoDirInfo)

	err := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		packageName := ""
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "package ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					packageName = strings.TrimSuffix(parts[1], ";")
					break
				}
			}
		}

		if packageName == "" {
			return nil
		}

		// Skip google well-known types — prost handles them natively
		if strings.HasPrefix(packageName, "google.") {
			return nil
		}

		relPath, err := filepath.Rel(inputDir, path)
		if err != nil {
			return err
		}
		protoDir := filepath.Dir(relPath)

		// Check if this directory is already tracked
		found := false
		for _, d := range packageToDirsMap[packageName] {
			if d.dir == protoDir {
				found = true
				break
			}
		}
		if !found {
			packageToDirsMap[packageName] = append(packageToDirsMap[packageName], protoDirInfo{
				dir:      protoDir,
				filePath: path,
			})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to scan proto files: %w", err)
	}

	// Build the single-directory map using the first occurrence for each package.
	packageToDirMap := make(map[string]string)
	for pkg, dirs := range packageToDirsMap {
		packageToDirMap[pkg] = dirs[0].dir
	}

	// Find all .rs files at the root of outputDir (prost-generated)
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read output directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rs") {
			continue
		}

		if entry.Name() == "lib.rs" {
			continue
		}

		// Skip .serde.rs and .tonic.rs files — they are moved alongside their main .rs file.
		// protoc-gen-prost generates include!("pkg.serde.rs") at the bottom of pkg.rs,
		// so these must remain siblings of the main .rs file.
		if strings.Contains(entry.Name(), ".serde.") || strings.Contains(entry.Name(), ".tonic.") {
			continue
		}

		moduleName := strings.TrimSuffix(entry.Name(), ".rs")

		// Skip google.* prost files — prost handles well-known types natively
		// and dotted module names are invalid in Rust.
		if strings.HasPrefix(moduleName, "google.") {
			// Remove main file and any serde/tonic siblings
			for _, suffix := range []string{".rs", ".serde.rs", ".tonic.rs"} {
				f := filepath.Join(outputDir, moduleName+suffix)
				if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
					zap.L().Debug("Failed to remove google proto file", zap.String("file", moduleName+suffix), zap.Error(err))
				}
			}
			continue
		}

		protoDirs, found := packageToDirsMap[moduleName]
		if !found {
			continue
		}

		srcPath := filepath.Join(outputDir, entry.Name())
		srcContent, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", srcPath, err)
		}

		if len(protoDirs) == 1 {
			// Single directory — just move the file
			// All types go under types/ wrapper (including sys)
			protoDir := protoDirs[0].dir
			var targetDir string
			if strings.HasPrefix(protoDir, "sys") {
				// e.g., sys/alpn.proto -> types/sys/alpn/
				targetDir = filepath.Join(outputDir, "types", protoDir, moduleName)
			} else if filepath.Base(protoDir) == moduleName {
				// e.g., services/baseball/baseball.proto -> types/services/baseball/baseball/
				targetDir = filepath.Join(outputDir, "types", protoDir, moduleName)
			} else if protoDir == "." {
				if err := os.Remove(srcPath); err != nil {
					return nil, fmt.Errorf("failed to remove %s: %w", srcPath, err)
				}
				continue
			} else {
				// e.g., common/ballsports.proto -> types/common/ballsports/
				targetDir = filepath.Join(outputDir, "types", protoDir, moduleName)
			}

			if err := os.MkdirAll(targetDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory %s: %w", targetDir, err)
			}

			dstPath := filepath.Join(targetDir, entry.Name())
			if err := os.Rename(srcPath, dstPath); err != nil {
				return nil, fmt.Errorf("failed to move %s to %s: %w", srcPath, dstPath, err)
			}

			// Also move .serde.rs and .tonic.rs siblings (protoc-gen-prost include!() needs them)
			for _, suffix := range []string{".serde.rs", ".tonic.rs"} {
				siblingName := moduleName + suffix
				sibSrc := filepath.Join(outputDir, siblingName)
				if _, err := os.Stat(sibSrc); err == nil {
					sibDst := filepath.Join(targetDir, siblingName)
					if err := os.Rename(sibSrc, sibDst); err != nil {
						return nil, fmt.Errorf("failed to move %s to %s: %w", sibSrc, sibDst, err)
					}
				}
			}

			zap.L().Debug("Moved file", zap.String("from", entry.Name()), zap.String("to", strings.TrimPrefix(dstPath, outputDir+string(filepath.Separator))))
		} else {
			// Multiple directories share this package — split the prost output.
			// Extract type/service/enum names from each proto file to determine ownership.
			dirTypeNames := make(map[string]map[string]bool) // protoDir -> set of snake_case names
			for _, pdi := range protoDirs {
				content, err := os.ReadFile(pdi.filePath)
				if err != nil {
					continue
				}
				names := make(map[string]bool)
				for _, line := range strings.Split(string(content), "\n") {
					trimmed := strings.TrimSpace(line)
					// Match "message FooBar {" or "service FooBar {" or "enum FooBar {"
					for _, prefix := range []string{"message ", "service ", "enum "} {
						if strings.HasPrefix(trimmed, prefix) {
							rest := strings.TrimPrefix(trimmed, prefix)
							typeName := strings.Fields(rest)[0]
							typeName = strings.TrimSuffix(typeName, "{")
							typeName = strings.TrimSpace(typeName)
							// Store both PascalCase (for struct/enum matching) and snake_case (for mod matching)
							names[typeName] = true
							// prost may transform acronyms (e.g. MTLSConfig → MtlsConfig); add both
							names[toProstTypeName(typeName)] = true
							snakeName := toSnakeCaseSimple(typeName)
							names[snakeName] = true
							// For services, prost generates {tonic_module}_client and
							// {tonic_module}_server modules (ContractS2S → contract_s2s).
							serviceModuleName := tonicServiceModuleName(typeName)
							names[serviceModuleName+"_client"] = true
							names[serviceModuleName+"_server"] = true
						}
					}
				}
				dirTypeNames[pdi.dir] = names
			}

			// Read serde/tonic sibling content before we remove them, so we can distribute
			// the relevant impls to each split directory.
			siblingContent := make(map[string]string) // suffix -> content
			for _, suffix := range []string{".serde.rs", ".tonic.rs"} {
				sibPath := filepath.Join(outputDir, moduleName+suffix)
				if data, err := os.ReadFile(sibPath); err == nil {
					siblingContent[suffix] = string(data)
				}
			}

			// Split the prost output into segments: each top-level pub item is a segment
			segments := splitProstOutput(string(srcContent))

			// For each directory, collect matching segments and write the filtered file
			for _, pdi := range protoDirs {
				names := dirTypeNames[pdi.dir]
				var filteredSegments []string
				for _, seg := range segments {
					segName := extractTopLevelName(seg)
					if segName == "" || names[segName] {
						// Include header comments and matched segments
						filteredSegments = append(filteredSegments, seg)
					}
				}

				var targetDir string
				if strings.HasPrefix(pdi.dir, "sys") {
					// sys/ goes under types/ wrapper (same as other types)
					targetDir = filepath.Join(outputDir, "types", pdi.dir, moduleName)
				} else {
					targetDir = filepath.Join(outputDir, "types", pdi.dir, moduleName)
				}

				if err := os.MkdirAll(targetDir, 0755); err != nil {
					return nil, fmt.Errorf("failed to create directory %s: %w", targetDir, err)
				}

				dstPath := filepath.Join(targetDir, entry.Name())
				filteredContent := strings.Join(filteredSegments, "\n")

				// Strip any include!() directives for sibling files from filteredContent.
				// We re-add them below only when there is actual sibling content for this directory.
				// This prevents stale includes from leaking into directories that have no sibling file.
				for _, suffix := range []string{".serde.rs", ".tonic.rs"} {
					includeDirective := `include!("` + moduleName + suffix + `");`
					filteredContent = strings.ReplaceAll(filteredContent, includeDirective, "")
				}

				// Distribute serde/tonic sibling files: extract only the impl/mod blocks
				// for types owned by this directory and write per-directory sibling files.
				// Then add the include!() directive to this split's main .rs.
				for _, suffix := range []string{".serde.rs", ".tonic.rs"} {
					content, hasSibling := siblingContent[suffix]
					if !hasSibling {
						continue
					}
					var dirContent string
					if suffix == ".tonic.rs" {
						// tonic.rs uses pub mod blocks, not impl blocks.
						dirContent = extractModsForTypes(content, names)
					} else {
						dirContent = extractImplsForTypes(content, names)
					}
					if dirContent == "" {
						continue
					}
					sibDst := filepath.Join(targetDir, moduleName+suffix)
					if err := os.WriteFile(sibDst, []byte(dirContent), 0644); err != nil {
						return nil, fmt.Errorf("failed to write %s: %w", sibDst, err)
					}
					// Add the include!() directive for this split's .rs file.
					includeDirective := `include!("` + moduleName + suffix + `");`
					if !strings.Contains(filteredContent, includeDirective) {
						filteredContent += "\n" + includeDirective + "\n"
					}
				}

				if err := os.WriteFile(dstPath, []byte(filteredContent), 0644); err != nil {
					return nil, fmt.Errorf("failed to write %s: %w", dstPath, err)
				}

				zap.L().Debug("Split and placed file", zap.String("from", entry.Name()),
					zap.String("to", strings.TrimPrefix(dstPath, outputDir+string(filepath.Separator))),
					zap.Int("segments", len(filteredSegments)))
			}

			// Remove original main .rs and any serde/tonic siblings
			if err := os.Remove(srcPath); err != nil {
				return nil, fmt.Errorf("failed to remove original %s: %w", srcPath, err)
			}
			for _, suffix := range []string{".serde.rs", ".tonic.rs"} {
				sibPath := filepath.Join(outputDir, moduleName+suffix)
				if _, err := os.Stat(sibPath); err == nil {
					if err := os.Remove(sibPath); err != nil {
						return nil, fmt.Errorf("failed to remove %s: %w", sibPath, err)
					}
				}
			}
		}
	}

	return packageToDirMap, nil
}

// splitProstOutput splits a prost-generated .rs file into segments at top-level pub boundaries.
// Each segment is a complete top-level item (pub struct, pub mod, pub enum, etc.) including
// all nested content. The first segment may be a header comment.
func splitProstOutput(content string) []string {
	lines := strings.Split(content, "\n")
	var segments []string
	var current []string

	for _, line := range lines {
		// Detect start of new top-level item
		trimmed := strings.TrimSpace(line)
		isTopLevel := (strings.HasPrefix(trimmed, "pub struct ") ||
			strings.HasPrefix(trimmed, "pub enum ") ||
			strings.HasPrefix(trimmed, "pub mod ") ||
			strings.HasPrefix(trimmed, "pub fn ")) &&
			!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")

		// Also detect doc comments (///) and attributes (#[...]) preceding a top-level item
		isDocOrAttr := strings.HasPrefix(trimmed, "///") || strings.HasPrefix(trimmed, "#[")

		if isTopLevel && len(current) > 0 {
			// Check if current ends with doc comments / attributes that belong to this new item
			splitIdx := len(current)
			for i := len(current) - 1; i >= 0; i-- {
				t := strings.TrimSpace(current[i])
				if strings.HasPrefix(t, "///") || strings.HasPrefix(t, "#[") || t == "" {
					splitIdx = i
				} else {
					break
				}
			}

			if splitIdx < len(current) {
				// Split: first part goes to previous segment, doc/attrs go to new segment
				segments = append(segments, strings.Join(current[:splitIdx], "\n"))
				current = current[splitIdx:]
			} else {
				segments = append(segments, strings.Join(current, "\n"))
				current = nil
			}
		}

		_ = isDocOrAttr // used in logic above
		current = append(current, line)
	}

	if len(current) > 0 {
		segments = append(segments, strings.Join(current, "\n"))
	}

	return segments
}

// extractTopLevelName extracts the name from a top-level pub item in a prost segment.
// Returns the snake_case name (e.g., "baseball_state" for "pub struct BaseballState")
// or "" if no name could be extracted.
func extractTopLevelName(segment string) string {
	for _, line := range strings.Split(segment, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"pub struct ", "pub enum ", "pub mod ", "pub fn "} {
			if strings.HasPrefix(trimmed, prefix) {
				rest := strings.TrimPrefix(trimmed, prefix)
				// Get the name (before { or < or space)
				name := rest
				for _, ch := range []string{" ", "{", "<", "("} {
					if idx := strings.Index(name, ch); idx >= 0 {
						name = name[:idx]
					}
				}
				return name
			}
		}
	}
	return ""
}

// toSnakeCaseSimple converts PascalCase to snake_case for matching proto names to Rust names.
// Delegates to strutil.ToSnakeCase for consistent acronym handling (e.g. "OpenAIService" → "open_ai_service").
func toSnakeCaseSimple(s string) string {
	return strutil.ToSnakeCase(s)
}

// extractImplsForTypes parses a .serde.rs file and returns only the top-level impl blocks
// (Serialize / Deserialize) whose type name appears in typeNames. The @generated header
// comment is preserved. Returns "" when no matching impl blocks are found.
func extractImplsForTypes(content string, typeNames map[string]bool) string {
	lines := strings.Split(content, "\n")
	var header []string
	var blocks []string
	var current []string
	depth := 0
	inImpl := false
	includeThis := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inImpl {
			// Accumulate header comments before any impl block
			if len(blocks) == 0 && (strings.HasPrefix(trimmed, "//") || trimmed == "") {
				header = append(header, line)
				continue
			}
			// Start of an impl block at top level
			if strings.HasPrefix(trimmed, "impl") {
				typeName := extractImplTypeName(trimmed)
				includeThis = typeNames[typeName]
				inImpl = true
				current = []string{line}
				depth = strings.Count(line, "{") - strings.Count(line, "}")
				if depth <= 0 {
					// Single-line impl (unusual but handle gracefully)
					inImpl = false
					if includeThis {
						blocks = append(blocks, strings.Join(current, "\n"))
					}
					current = nil
					depth = 0
				}
				continue
			}
			// Skip other top-level content (unexpected in serde files)
			continue
		}

		// Inside an impl block: accumulate lines and track brace depth
		current = append(current, line)
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			inImpl = false
			depth = 0
			if includeThis {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = nil
			includeThis = false
		}
	}

	if len(blocks) == 0 {
		return ""
	}

	var sb strings.Builder
	// Write non-empty header lines
	for _, h := range header {
		if strings.TrimSpace(h) != "" {
			sb.WriteString(h)
			sb.WriteByte('\n')
		}
	}
	// Write impl blocks separated by a blank line
	for i, block := range blocks {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(block)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// extractModsForTypes parses a .tonic.rs file and returns only the top-level
// pub mod blocks whose module name appears in typeNames. Used during the split
// of a tonic.rs file shared across multiple proto directories.
// The @generated header comment is preserved. Returns "" when no matching blocks.
func extractModsForTypes(content string, typeNames map[string]bool) string {
	lines := strings.Split(content, "\n")
	var header []string
	var blocks []string
	var current []string
	depth := 0
	inMod := false
	includeThis := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inMod {
			// Accumulate header comments before any mod block
			if len(blocks) == 0 && (strings.HasPrefix(trimmed, "//") || trimmed == "") {
				header = append(header, line)
				continue
			}
			// Start of a top-level pub mod block
			if strings.HasPrefix(trimmed, "pub mod ") {
				rest := strings.TrimPrefix(trimmed, "pub mod ")
				modName := strings.Fields(rest)[0]
				modName = strings.TrimSuffix(modName, "{")
				modName = strings.TrimSpace(modName)
				includeThis = typeNames[modName]
				inMod = true
				current = []string{line}
				depth = strings.Count(line, "{") - strings.Count(line, "}")
				if depth <= 0 {
					inMod = false
					if includeThis {
						blocks = append(blocks, strings.Join(current, "\n"))
					}
					current = nil
					depth = 0
				}
				continue
			}
			// Skip other top-level content (attributes, blank lines between mods)
			continue
		}

		// Inside a mod block: accumulate lines and track brace depth
		current = append(current, line)
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			inMod = false
			depth = 0
			if includeThis {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = nil
			includeThis = false
		}
	}

	if len(blocks) == 0 {
		return ""
	}

	var sb strings.Builder
	// Write non-empty header lines
	for _, h := range header {
		if strings.TrimSpace(h) != "" {
			sb.WriteString(h)
			sb.WriteByte('\n')
		}
	}
	// Write mod blocks separated by a blank line
	for i, block := range blocks {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(block)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// extractImplTypeName parses the Rust type name from an impl declaration line.
// Handles patterns such as:
//
//	impl serde::Serialize for TypeName {
//	impl<'de> serde::Deserialize<'de> for TypeName {
func extractImplTypeName(line string) string {
	forIdx := strings.Index(line, " for ")
	if forIdx < 0 {
		return ""
	}
	rest := line[forIdx+5:] // skip " for "
	name := rest
	for _, ch := range []string{" ", "{", "<"} {
		if idx := strings.Index(name, ch); idx >= 0 {
			name = name[:idx]
		}
	}
	return strings.TrimSpace(name)
}

// generateModuleStructure scans the output directory for .rs files generated by prost
// and creates mod.rs files to properly organize them into a module hierarchy
func generateModuleStructure(outputDir string, crateBasePath string) error {
	// Map to track directories and their .rs files
	dirModules := make(map[string][]string)

	// Walk through output directory to find all .rs files
	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip target directory entirely
		if info.IsDir() && info.Name() == "target" {
			return filepath.SkipDir
		}

		// Skip directories that are separate crates (have Cargo.toml).
		// These are workspace members (e.g., sys/servicemanager/) and should
		// not be declared as modules of the main crate.
		// Exception: the "types" directory IS a separate crate but its module
		// structure is managed by generateModuleStructure — the proto .rs files
		// and helper mod.rs files live inside it and need to be discovered.
		if info.IsDir() && info.Name() != "types" {
			cargoToml := filepath.Join(path, "Cargo.toml")
			if _, err := os.Stat(cargoToml); err == nil {
				return filepath.SkipDir
			}
		}

		// Skip directories and non-Rust files
		if info.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}

		// Get relative path from output dir
		relPath, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}

		// Skip lib.rs, mod.rs, and protoc-gen-prost sibling files (.serde.rs, .tonic.rs).
		// Sibling files are include!()'d by their main .rs and should not be declared as modules.
		base := filepath.Base(relPath)
		if base == "lib.rs" || base == "mod.rs" {
			return nil
		}
		if strings.Contains(base, ".serde.") || strings.Contains(base, ".tonic.") {
			return nil
		}

		// Get directory and module name
		dir := filepath.Dir(relPath)
		moduleName := strings.TrimSuffix(base, ".rs")

		// Add to directory's module list
		if dir == "." {
			dir = ""
		}
		dirModules[dir] = append(dirModules[dir], moduleName)

		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to scan output directory: %w", err)
	}

	// Find all directories that need mod.rs files (including intermediate dirs with subdirs)
	allDirs := make(map[string]bool)
	for dir := range dirModules {
		if dir == "" {
			continue
		}
		// Add this directory
		allDirs[dir] = true

		// Add all parent directories
		parts := strings.Split(dir, string(filepath.Separator))
		for i := 1; i < len(parts); i++ {
			parentDir := filepath.Join(parts[:i]...)
			allDirs[parentDir] = true
		}
	}

	// Generate mod.rs files for each directory (except root)
	for dir, modules := range dirModules {
		if dir == "" {
			// Root level - these will be added to lib.rs
			continue
		}

		// Check if mod.rs already exists (don't overwrite helper mod.rs)
		modPath := filepath.Join(outputDir, dir, "mod.rs")
		skip := dir == "s2sm" || dir == "c2sm" || dir == "c2dm" || dir == "w2sm" ||
			strings.HasPrefix(dir, "s2sm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "c2sm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "c2dm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "w2sm"+string(filepath.Separator)) ||
			dir == filepath.Join("types", "services") ||
			dir == filepath.Join("types", "sys")
		if _, err := os.Stat(modPath); err == nil {
			// mod.rs exists, append prost-generated modules to it
			if err := appendToModFile(modPath, modules, skip); err != nil {
				return err
			}
		} else {
			// Create new mod.rs
			if err := createProtoModFileWithReExports(modPath, modules, skip); err != nil {
				return err
			}
		}
	}

	// Ensure mod.rs files also declare subdirectory modules.
	// Directories may have both .rs files (handled above) AND subdirectories
	// that need `pub mod` declarations.
	for dir := range allDirs {
		if dir == "" {
			continue
		}

		modPath := filepath.Join(outputDir, dir, "mod.rs")

		// Find subdirectories that need module declarations
		dirPath := filepath.Join(outputDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue // Skip if can't read directory
		}

		var subdirs []string
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != "target" {
				// Check if this subdirectory has a mod.rs file or will get one
				subdir := filepath.Join(dir, entry.Name())
				subdirPath := filepath.Join(outputDir, subdir)
				subdirModPath := filepath.Join(subdirPath, "mod.rs")

				// Include if mod.rs exists or will be created
				if _, err := os.Stat(subdirModPath); err == nil || allDirs[subdir] {
					subdirs = append(subdirs, entry.Name())
				}
			}
		}

		if len(subdirs) == 0 {
			continue
		}

		// Suppress `pub use *` re-exports for directories that aggregate
		// multiple packages with potentially overlapping type names.
		// - s2sm/c2sm/c2dm/w2sm, s2sm/internal: grpc_clients/state_clients define GrpcClients/StateClients
		// - types/services: different services may define types with the same name (e.g. MapEmpty, Config)
		// - s2sm/internal/*/services: individual service packages re-exported would conflict
		skipReExports := dir == "s2sm" || dir == "c2sm" || dir == "c2dm" || dir == "w2sm" ||
			strings.HasPrefix(dir, "s2sm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "c2sm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "c2dm"+string(filepath.Separator)) ||
			strings.HasPrefix(dir, "w2sm"+string(filepath.Separator)) ||
			dir == filepath.Join("types", "services") ||
			dir == filepath.Join("types", "sys")

		if _, err := os.Stat(modPath); err == nil {
			// mod.rs already exists — append subdirectory modules
			if err := appendToModFile(modPath, subdirs, skipReExports); err != nil {
				return err
			}
		} else {
			// Create new mod.rs with subdirectory modules
			if err := createProtoModFileWithReExports(modPath, subdirs, skipReExports); err != nil {
				return err
			}
		}
	}

	// Update lib.rs with top-level modules
	// Always create lib.rs, even if there are no root-level .rs files
	rootModules := dirModules[""]
	if err := updateLibFile(outputDir, rootModules, dirModules, crateBasePath); err != nil {
		return err
	}

	return nil
}

// createProtoModFileWithReExports creates a mod.rs file, optionally suppressing `pub use *` re-exports.
func createProtoModFileWithReExports(modPath string, modules []string, skipReExports bool) error {
	data := modFileData{
		Modules:       modules,
		SkipReExports: skipReExports,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return fmt.Errorf("failed to get template FS: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	maps.Copy(funcMap, header.Funcs())
	result, err := tmplutil.ExecuteTemplate(typeFS, "mod.rs.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute mod.rs template: %w", err)
	}

	return os.WriteFile(modPath, result, 0644)
}

// appendToModFile appends prost module declarations to existing mod.rs (for helper files)
func appendToModFile(modPath string, modules []string, skipReExports bool) error {
	// Read existing content
	content, err := os.ReadFile(modPath)
	if err != nil {
		return err
	}

	// Parse existing modules to avoid duplicates
	existingModules := make(map[string]bool)
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pub mod ") && strings.HasSuffix(line, ";") {
			modName := strings.TrimSuffix(strings.TrimPrefix(line, "pub mod "), ";")
			existingModules[strings.TrimSpace(modName)] = true
		}
	}

	// Only append new modules
	var newModules []string
	for _, module := range modules {
		if !existingModules[module] {
			newModules = append(newModules, module)
		}
	}

	if len(newModules) == 0 {
		return nil // Nothing to add
	}

	// Prepend new modules at the beginning (after headers, before helper modules)
	// This ensures proto types are available when helper modules are processed
	var b strings.Builder

	// Write header lines first
	headerWritten := false
	for _, origLine := range lines {
		trimmedLine := strings.TrimSpace(origLine)
		// Inner attributes (#![...]) belong to the file head too: an item emitted
		// before one would make it a compile error.
		if strings.HasPrefix(trimmedLine, "//") || strings.HasPrefix(trimmedLine, "#![") || trimmedLine == "" {
			b.WriteString(origLine)
			b.WriteString("\n")
		} else {
			// Found first non-comment line, insert proto modules here
			if !headerWritten {
				b.WriteString("// Proto modules\n")
				for _, module := range newModules {
					fmt.Fprintf(&b, "pub mod %s;\n", module)
				}
				// Add re-exports for proto modules (unless suppressed)
				if !skipReExports {
					for _, module := range newModules {
						fmt.Fprintf(&b, "#[allow(ambiguous_glob_reexports, unused_imports)]\npub use %s::*;\n", module)
					}
				}
				b.WriteString("\n")
				headerWritten = true
			}
			b.WriteString(origLine)
			b.WriteString("\n")
		}
	}

	// If we never found a non-comment line, append at the end
	if !headerWritten {
		b.WriteString("// Proto modules\n")
		for _, module := range newModules {
			fmt.Fprintf(&b, "pub mod %s;\n", module)
		}
		if !skipReExports {
			for _, module := range newModules {
				fmt.Fprintf(&b, "#[allow(ambiguous_glob_reexports, unused_imports)]\npub use %s::*;\n", module)
			}
		}
	}

	return os.WriteFile(modPath, []byte(b.String()), 0644)
}

// updateLibFile updates lib.rs with all top-level module declarations.
// crateBasePath is the crate name (e.g., "gateway2-tacplus") used to re-export the types crate.
func updateLibFile(outputDir string, rootModules []string, allDirs map[string][]string, crateBasePath string) error {
	libPath := filepath.Join(outputDir, "lib.rs")

	var b strings.Builder
	b.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
	b.WriteString(header.Copyright(consts.LangRust))

	// Re-export the types crate as the `types` module.
	// The types crate is a separate workspace member to avoid cyclic dependencies
	// (lib crates depend on types, main crate depends on lib crates).
	crateUnderscore := strings.ReplaceAll(crateBasePath, "-", "_")
	fmt.Fprintf(&b, "pub use %s_types as types;\n", crateUnderscore)

	// Find all top-level directories (these become top-level modules)
	topLevelDirs := make(map[string]bool)
	for dir := range allDirs {
		if dir == "" {
			continue
		}
		// Get the first path component
		parts := strings.Split(dir, string(filepath.Separator))
		topLevelDirs[parts[0]] = true
	}

	// Also scan for directories with mod.rs files (our helper modules)
	entries, err := os.ReadDir(outputDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != "target" {
				modFile := filepath.Join(outputDir, entry.Name(), "mod.rs")
				if _, err := os.Stat(modFile); err == nil {
					topLevelDirs[entry.Name()] = true
				}
			}
		}
	}

	// Declare top-level directory modules (skip "types" — it's a separate crate, re-exported above)
	var sortedDirs []string
	for dir := range topLevelDirs {
		if dir == "types" {
			continue
		}
		sortedDirs = append(sortedDirs, dir)
	}
	sort.Strings(sortedDirs)
	for _, dir := range sortedDirs {
		fmt.Fprintf(&b, "pub mod %s;\n", dir)
	}

	// Also declare any root-level .rs files
	for _, module := range rootModules {
		fmt.Fprintf(&b, "pub mod %s;\n", module)
	}

	return os.WriteFile(libPath, []byte(b.String()), 0644)
}

// updateLibFileWithHelpers updates lib.rs to include parent directories for helper modules
func updateLibFileWithHelpers(outputDir string) error {
	libPath := filepath.Join(outputDir, "lib.rs")

	// Read existing lib.rs
	content, err := os.ReadFile(libPath)
	if err != nil {
		return fmt.Errorf("failed to read lib.rs: %w", err)
	}

	// Scan for top-level directories with mod.rs files
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("failed to read output directory: %w", err)
	}

	// Parse existing module declarations to avoid duplicates
	existingModules := make(map[string]bool)
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pub mod ") && strings.HasSuffix(line, ";") {
			modName := strings.TrimSuffix(strings.TrimPrefix(line, "pub mod "), ";")
			existingModules[strings.TrimSpace(modName)] = true
		}
	}

	// Find directories with mod.rs that aren't already declared
	var newModules []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "target" {
			// Skip "types" — it's a separate crate, re-exported via `pub use ... as types;`
			if entry.Name() == "types" {
				continue
			}
			modFile := filepath.Join(outputDir, entry.Name(), "mod.rs")
			if _, err := os.Stat(modFile); err == nil {
				if !existingModules[entry.Name()] {
					newModules = append(newModules, entry.Name())
				}
			}
		}
	}

	// If there are new modules to add, append them
	if len(newModules) > 0 {
		var b strings.Builder
		b.Write(content)
		if !strings.HasSuffix(string(content), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("// Helper modules\n")
		for _, module := range newModules {
			fmt.Fprintf(&b, "pub mod %s;\n", module)
		}

		if err := os.WriteFile(libPath, []byte(b.String()), 0644); err != nil {
			return fmt.Errorf("failed to write lib.rs: %w", err)
		}
	}

	return nil
}

// fixCrateImports updates crate:: imports in proto .rs files to use correct nested paths.
// Uses Contains to find/replace Rust import patterns in prost-generated code text.
// No structured AST is available for generated Rust — text replacement is standard practice.
func fixCrateImports(outputDir string, packageToDirMap map[string]string) error {
	// Find all proto .rs files
	var protoFiles []string
	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == "target" {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}
		base := filepath.Base(path)
		if base == "mod.rs" || base == "lib.rs" {
			return nil
		}
		// Only process prost-generated files
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(content), "@generated") || strings.Contains(string(content), "Code generated by prost-build") {
			protoFiles = append(protoFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Fix imports in each proto file
	for _, protoFile := range protoFiles {
		content, err := os.ReadFile(protoFile)
		if err != nil {
			continue
		}

		contentStr := string(content)
		modified := false

		// For each known package, update imports from crate::package and super::package
		for packageName, protoDir := range packageToDirMap {
			// Fix crate:: imports
			oldCrateImport := fmt.Sprintf("use crate::%s::", packageName)
			oldSuperRef := fmt.Sprintf("super::%s::", packageName)

			// Determine the correct new import path.
			// All types are now under types/ wrapper: crate::types::{dir}::{package}::
			// No extra ::types:: inside each module.
			var newCratePath, newSuperPath string
			if filepath.Base(protoDir) == packageName {
				// e.g., services/baseball -> crate::types::services::baseball::{package}::
				dirPath := strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::")
				newCratePath = fmt.Sprintf("crate::%s::%s::", dirPath, packageName)
				newSuperPath = fmt.Sprintf("crate::%s::%s::", dirPath, packageName)
			} else if protoDir == "." {
				// Root level, no change needed
				continue
			} else if strings.HasPrefix(protoDir, "sys") {
				// e.g., sys/alpn.proto -> crate::types::sys::alpn::{package}::
				newCratePath = fmt.Sprintf("crate::%s::%s::%s::", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), packageName, packageName)
				newSuperPath = fmt.Sprintf("crate::%s::%s::%s::", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), packageName, packageName)
			} else {
				// e.g., common/ballsports.proto -> crate::types::common::ballsports::{package}::
				newCratePath = fmt.Sprintf("crate::%s::%s::%s::", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), packageName, packageName)
				newSuperPath = fmt.Sprintf("crate::%s::%s::%s::", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), packageName, packageName)
			}

			// Fix use crate::package:: statements
			newCrateImport := fmt.Sprintf("use %s", newCratePath)
			if strings.Contains(contentStr, oldCrateImport) {
				contentStr = strings.ReplaceAll(contentStr, oldCrateImport, newCrateImport)
				modified = true
			}

			// Fix super::package:: references in type annotations and other contexts
			if strings.Contains(contentStr, oldSuperRef) {
				contentStr = strings.ReplaceAll(contentStr, oldSuperRef, newSuperPath)
				modified = true
			}
		}

		// Fix super::crate:: paths created by the above replacements.
		// Tonic generates super::super::package:: inside nested service modules (going up
		// two levels). The super::package:: replacement above converts the inner part to
		// crate::..., leaving super::crate:: which is invalid. Since crate:: is absolute,
		// just remove the super:: prefix.
		if strings.Contains(contentStr, "super::crate::") {
			contentStr = strings.ReplaceAll(contentStr, "super::crate::", "crate::")
			modified = true
		}

		if modified {
			if err := os.WriteFile(protoFile, []byte(contentStr), 0644); err != nil {
				return fmt.Errorf("failed to update %s: %w", protoFile, err)
			}
		}
	}

	return nil
}

// fixCrossModuleImports scans all prost-generated .rs files and adds re-exports
// to mod.rs files for modules referenced via super::.
// Uses Contains to detect patterns in generated Rust code text (no Rust AST available).
func fixCrossModuleImports(outputDir string, packageToDirMap map[string]string) error {
	// Find all prost-generated .rs files (not mod.rs)
	var protoFiles []string
	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}
		base := filepath.Base(path)
		if base == "mod.rs" || base == "lib.rs" {
			return nil
		}
		// Only process prost-generated files (they have @generated marker)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}
		if strings.Contains(string(content), "@generated") {
			protoFiles = append(protoFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Track which modules need which re-exports
	// Map: directory -> set of modules to re-export
	reexports := make(map[string]map[string]bool)

	// Scan each proto file for super:: references
	for _, protoFile := range protoFiles {
		content, err := os.ReadFile(protoFile)
		if err != nil {
			continue
		}

		// Get the directory of this file
		dir := filepath.Dir(protoFile)

		// Find all super::module_name references
		contentStr := string(content)
		for _, module := range []string{"key", "x509", "token", "jwk", "http", "error", "consts",
			"grpcserver", "httpserver", "httpclient", "logger", "tokencache",
			"cloudvendor", "outputformat", "hermes", "foobar"} {
			pattern := fmt.Sprintf("super::%s::", module)
			if strings.Contains(contentStr, pattern) {
				if reexports[dir] == nil {
					reexports[dir] = make(map[string]bool)
				}
				reexports[dir][module] = true
			}
		}
	}

	// Update each mod.rs file with necessary re-exports
	for dir, modules := range reexports {
		modPath := filepath.Join(dir, "mod.rs")

		// Read existing mod.rs
		content, err := os.ReadFile(modPath)
		if err != nil {
			continue
		}

		// Parse existing content to avoid duplicates
		existingLines := strings.Split(string(content), "\n")
		hasReexports := false
		for _, line := range existingLines {
			if strings.Contains(line, "pub use crate::") {
				hasReexports = true
				break
			}
		}

		// Add re-exports if not already present
		if !hasReexports && len(modules) > 0 {
			var b strings.Builder
			b.Write(content)
			if !strings.HasSuffix(string(content), "\n") {
				b.WriteString("\n")
			}
			b.WriteString("// Re-export referenced modules for super:: imports\n")

			// Sort modules for consistent output — `modules` is a Go map, so
			// ranging it directly emits the `pub use` lines in random order and
			// byte-diffs mod.rs across otherwise-identical runs (GEN-DET-1).
			var sortedModules []string
			for module := range modules {
				sortedModules = append(sortedModules, module)
			}
			sort.Strings(sortedModules)

			for _, module := range sortedModules {
				// Re-export with correct nested path under types/ wrapper
				protoDir, found := packageToDirMap[module]
				var reexportPath string
				if found {
					if filepath.Base(protoDir) == module {
						// e.g., services/baseball -> crate::types::services::baseball::{module}
						dirPath := strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::")
						reexportPath = fmt.Sprintf("crate::%s::%s", dirPath, module)
					} else if protoDir == "." {
						// Root level
						reexportPath = fmt.Sprintf("crate::%s", module)
					} else if strings.HasPrefix(protoDir, "sys") {
						// e.g., sys/alpn -> crate::types::sys::alpn::{module}
						reexportPath = fmt.Sprintf("crate::%s::%s::%s", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), module, module)
					} else {
						// e.g., common/ballsports -> crate::types::common::ballsports::{module}
						reexportPath = fmt.Sprintf("crate::%s::%s::%s", strings.ReplaceAll(strings.ReplaceAll(protoDir, "-", "_"), string(filepath.Separator), "::"), module, module)
					}
				} else {
					reexportPath = fmt.Sprintf("crate::%s", module)
				}
				fmt.Fprintf(&b, "pub use %s;\n", reexportPath)
			}

			if err := os.WriteFile(modPath, []byte(b.String()), 0644); err != nil {
				return fmt.Errorf("failed to update %s: %w", modPath, err)
			}
		}
	}

	return nil
}
