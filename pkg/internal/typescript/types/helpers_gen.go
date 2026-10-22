// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/templates"
)

// isIntegerType checks if a field type is an integer type
func isIntegerType(typ string) bool {
	return typ == "int32" || typ == "int64" ||
		typ == "uint32" || typ == "uint64" ||
		typ == "sint32" || typ == "sint64" ||
		typ == "fixed32" || typ == "fixed64" ||
		typ == "sfixed32" || typ == "sfixed64"
}

// isFloatType checks if a field type is a floating-point type
func isFloatType(typ string) bool {
	return typ == "float" || typ == "double"
}

// isNumericType checks if a field type is any numeric type
func isNumericType(typ string) bool {
	return isIntegerType(typ) || isFloatType(typ)
}

// processProtoFiles parses proto files and generates TypeScript enum/constant helpers
func processProtoFiles(protocDir, outputDir string) ([]model.ProtoFile, error) {
	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Use descriptor-based parser (robust, uses protoc internally)
	parser, err := parser.NewDescriptorParser(protocDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all proto files
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

	// Generate enum helper files for all proto files
	for _, pf := range protoFiles {
		subdir := filepath.Dir(pf.RelPath)
		if subdir == "." {
			subdir = ""
		}

		if err := generateTypeScriptHelpers(pf, outputDir, subdir, protoFiles); err != nil {
			return nil, fmt.Errorf("failed to generate helpers for %s: %w", pf.RelPath, err)
		}

		// Generate filesystem helpers (directoryFromFileSystem / directoryToFileSystem)
		// next to the generated _pb module (types/sys/filesystem), not the raw proto subdir.
		if proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package) {
			fsSubdir := filepath.Dir(typescriptModuleDirForProtoFile("", pf))
			if err := generateFilesystemHelpers(outputDir, fsSubdir); err != nil {
				return nil, fmt.Errorf("failed to generate filesystem helpers: %w", err)
			}
		}
	}

	return protoFiles, nil
}

// helpersTemplateData holds all pre-computed sections for the helpers.ts template.
type helpersTemplateData struct {
	Header        string
	RelPath       string
	Imports       string
	EnumHelpers   string
	Constants     string
	Serialization string
	FromMap       string
	Defaults      string
	Merge         string
	EnvVar        string
	LlmDescriptor string
}

// generateTypeScriptHelpers generates a single consolidated helpers.ts file
// containing enums, constants, serialization, validation, fromMap, defaults,
// and LLM type descriptors.
func generateTypeScriptHelpers(pf model.ProtoFile, baseDir, subdir string, allFiles []model.ProtoFile) error {
	protoFilename := strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto")
	outputPath := filepath.Join(typescriptModuleDirForProtoFile(filepath.Join(baseDir, "typescript"), pf), "helpers.ts")
	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Pre-compute each section into separate string builders
	var importsB strings.Builder
	serviceStateFiles := typeScriptServiceStateFileMap(allFiles)
	if proto.DeclaresServiceStateModel(pf) {
		serviceStateFiles[filepath.ToSlash(pf.RelPath)] = true
	}
	envVarCtx := buildTsEnvVarContext(pf)
	writeCombinedImports(&importsB, pf, protoFilename, envVarCtx, serviceStateFiles)

	var enumHelpersB strings.Builder
	if len(pf.Enums) > 0 {
		writeEnumHelpers(&enumHelpersB, pf)
	}

	var constantsB strings.Builder
	if len(pf.Constants) > 0 || hasFieldDefaults(pf) {
		writeConstants(&constantsB, pf)
	}

	var serializationB strings.Builder
	if len(pf.Messages) > 0 {
		if err := writeSerializationAndValidation(&serializationB, pf); err != nil {
			return err
		}
	}

	var fromMapB strings.Builder
	if hasParamMapMessages(pf) {
		if err := writeFromMap(&fromMapB, pf); err != nil {
			return err
		}
	}

	var defaultsB strings.Builder
	writeDefaults(&defaultsB, pf)
	writeUndefaults(&defaultsB, pf)

	var mergeB strings.Builder
	writeMerge(&mergeB, pf)

	var envVarB strings.Builder
	if len(pf.Messages) > 0 {
		writeEnvVar(&envVarB, pf, envVarCtx)
	}

	data := helpersTemplateData{
		Header:        header.SimpleHeader(consts.LangTypeScript),
		RelPath:       pf.RelPath,
		Imports:       importsB.String(),
		EnumHelpers:   enumHelpersB.String(),
		Constants:     constantsB.String(),
		Serialization: serializationB.String(),
		FromMap:       fromMapB.String(),
		Defaults:      defaultsB.String(),
		Merge:         mergeB.String(),
		EnvVar:        envVarB.String(),
		LlmDescriptor: "",
	}

	typeFS, err := fs.Sub(templates.FS, "typescript/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "helpers.ts.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute helpers template: %w", err)
	}

	return os.WriteFile(outputPath, result, 0644)
}

// hasParamMapMessages returns true if any message in pf has a ParamMapConfig
func hasParamMapMessages(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			return true
		}
	}
	return false
}

// hasAssertRequiredMessages returns true if any message has an assert_required
// annotation on a message field, which requires the create() function from
// @bufbuild/protobuf to initialize nil fields.
func hasAssertRequiredMessages(pf model.ProtoFile) bool {
	hasDefaults := messagesWithDefaults(pf)
	for _, msg := range pf.Messages {
		if !hasDefaults[msg.Name] {
			continue
		}
		for _, field := range msg.Fields {
			if field.IsMessage && !field.IsRepeated && !field.IsMap && !field.IsOneOf && field.TypePackage == "" {
				if _, ok := field.Annotations["assert_required"]; ok {
					return true
				}
			}
		}
	}
	return false
}

// hasDefaultHostFields returns true if any message has a default_host annotation,
// which requires create() and HostSchema to construct typed Host messages.
func hasDefaultHostFields(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			if _, ok := field.Annotations["default_host"]; ok {
				return true
			}
		}
	}
	return false
}

// writeCombinedImports writes a merged set of imports for all sections of helpers.ts
func writeCombinedImports(b *strings.Builder, pf model.ProtoFile, protoFilename string, envVarCtx tsEnvVarContext, serviceStateFiles map[string]bool) {
	// Collect all message names
	messageNames := make([]string, 0, len(pf.Messages))
	for _, msg := range pf.Messages {
		messageNames = append(messageNames, msg.Name)
	}

	// Collect all enum names
	enumNames := getEnumNames(pf.Enums)

	// Collect schema names for messages
	schemaNames := make([]string, 0, len(pf.Messages))
	for _, msg := range pf.Messages {
		schemaNames = append(schemaNames, msg.Name+"Schema")
	}

	// Collect additional enum names needed by fromMap (key enums)
	keyEnumNames := make(map[string]bool)
	fieldEnumNames := make(map[string]bool)
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			keyEnumNames[msg.ParamMapConfig.KeyEnum] = true
			for _, field := range msg.Fields {
				if field.ParamKey != "" && field.IsEnum {
					enumTypeName := field.Type
					// field.Type can be qualified (e.g. "key.KeyType"); strip package prefix
					if strings.Contains(enumTypeName, ".") {
						parts := strings.Split(enumTypeName, ".")
						enumTypeName = parts[len(parts)-1]
					}
					fieldEnumNames[enumTypeName] = true
				}
			}
		}
	}

	// Build the proto _pb.js import: message types + schemas + enum types
	var pbImports []string
	pbImports = append(pbImports, messageNames...)
	pbImports = append(pbImports, schemaNames...)
	pbImports = append(pbImports, enumNames...)

	// Add key enum types from fromMap (they may already be in enumNames)
	existingEnums := make(map[string]bool)
	for _, e := range enumNames {
		existingEnums[e] = true
	}
	// keyEnumNames is a Go map — sort before appending, or the import list
	// reorders across otherwise-identical runs (GEN-DET-1).
	sortedKeyEnums := make([]string, 0, len(keyEnumNames))
	for enumName := range keyEnumNames {
		sortedKeyEnums = append(sortedKeyEnums, enumName)
	}
	sort.Strings(sortedKeyEnums)
	for _, enumName := range sortedKeyEnums {
		if !existingEnums[enumName] {
			pbImports = append(pbImports, enumName)
		}
	}

	if len(pbImports) > 0 {
		fmt.Fprintf(b, "import { %s } from './%s_pb.js';\n",
			strings.Join(pbImports, ", "), protoFilename)
	}

	helpersDir := filepath.ToSlash(typescriptModuleDirForProtoFile("", pf))
	errorsPath := typeScriptRelativeImport(helpersDir, "types/sys/errors/errors.js")
	if len(pf.Enums) > 0 || len(pf.Messages) > 0 {
		fmt.Fprintf(b, "import { newErrMissingInvalidData } from '%s';\n", errorsPath)
	}

	// Import @bufbuild/protobuf helpers (serialization + create for fromMap)
	var protobufImports []string
	if len(pf.Messages) > 0 {
		protobufImports = append(protobufImports, "toBinary", "fromBinary", "toJson", "fromJson")
	}
	needsCreate := hasParamMapMessages(pf) || hasAssertRequiredMessages(pf) || hasDefaultHostFields(pf)
	if needsCreate {
		protobufImports = append(protobufImports, "create")
	}
	if len(protobufImports) > 0 {
		fmt.Fprintf(b, "import { %s } from '@bufbuild/protobuf';\n", strings.Join(protobufImports, ", "))
	}

	// Import HostSchema when default_host annotations are present
	if hasDefaultHostFields(pf) {
		fmt.Fprintf(b, "import { HostSchema } from '%s';\n", typeScriptRelativeImport(helpersDir, "types/sys/host/host_pb.js"))
	}

	// Import cross-package helpers needed by EnvVar functions
	if len(pf.Messages) > 0 {
		writeEnvVarCrossPackageImports(b, helpersDir, envVarCtx, serviceStateFiles)
	}

	// Import serialization format helpers (needed by messages)
	if len(pf.Messages) > 0 {
		b.WriteString("import * as yaml from 'js-yaml';\n")
		b.WriteString("import * as toml from '@iarna/toml';\n")
	}

	b.WriteString("\n")
}

// writeEnvVarCrossPackageImports generates import statements for cross-package
// helper functions referenced by the EnvVar section (ToEnvVar, FromEnvVar,
// ToString, parse functions).
func writeEnvVarCrossPackageImports(b *strings.Builder, helpersDir string, envVarCtx tsEnvVarContext, serviceStateFiles map[string]bool) {
	if len(envVarCtx.imports) == 0 {
		return
	}

	importPkgs := make([]string, 0, len(envVarCtx.imports))
	for pkg := range envVarCtx.imports {
		importPkgs = append(importPkgs, pkg)
	}
	sort.Strings(importPkgs)

	for _, pkg := range importPkgs {
		cp := envVarCtx.imports[pkg]
		if cp.importPath == "" || len(cp.funcAliases) == 0 {
			continue
		}

		targetDir := typescriptModuleDirForProtoFeatures("", cp.importPath, typeScriptServiceStateFile(serviceStateFiles, cp.importPath), false)
		relPath := typeScriptRelativeImport(helpersDir, filepath.ToSlash(filepath.Join(targetDir, "helpers.js")))

		// Collect and sort function names for deterministic output
		var funcNames []string
		for fn := range cp.funcAliases {
			funcNames = append(funcNames, fn)
		}
		// Sort for deterministic output
		sortStrings(funcNames)

		var importNames []string
		for _, fn := range funcNames {
			importNames = append(importNames, fmt.Sprintf("%s as %s", fn, cp.funcAliases[fn]))
		}

		fmt.Fprintf(b, "import { %s } from '%s';\n",
			strings.Join(importNames, ", "), relPath)
	}
}

func typeScriptRelativeImport(fromDir, targetFile string) string {
	rel, err := filepath.Rel(filepath.FromSlash(fromDir), filepath.FromSlash(targetFile))
	if err != nil {
		return filepath.ToSlash(targetFile)
	}
	relPath := filepath.ToSlash(rel)
	if !strings.HasPrefix(relPath, ".") {
		relPath = "./" + relPath
	}
	return relPath
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// writeEnumHelpers writes enum helper functions directly to the buffer
func writeEnumHelpers(b *strings.Builder, pf model.ProtoFile) {
	b.WriteString("// ============================================================\n")
	b.WriteString("// Enum Helpers\n")
	b.WriteString("// ============================================================\n")

	for _, enum := range pf.Enums {
		generateTypeScriptEnumHelpers(b, enum)
	}
}

// tsConstantEntry holds a single constant for template rendering.
type tsConstantEntry struct {
	Name    string
	TsType  string
	TsValue string
}

// tsDefaultsFunc holds data for a TypeDefaults function.
type tsDefaultsFunc struct {
	MsgName     string
	FuncName    string
	Assignments []string // pre-computed assignment statements
}

// tsConstantsData holds pre-computed data for the constants template.
type tsConstantsData struct {
	Constants     []tsConstantEntry
	FieldDefaults []tsDefaultConstant
	Defaults      []tsDefaultsFunc
}

// writeConstants writes constant declarations to the buffer using a template.
func writeConstants(b *strings.Builder, pf model.ProtoFile) {
	fieldDefaults := collectFieldDefaults(pf)

	// Build constant entries with pre-computed TS types and values
	var constants []tsConstantEntry
	for _, c := range pf.Constants {
		constants = append(constants, tsConstantEntry{
			Name:    c.Name,
			TsType:  mapConstTypeToTypeScript(c.Type, c.Value),
			TsValue: convertConstValueToTypeScript(c.Value, c.Type),
		})
	}

	data := tsConstantsData{
		Constants:     constants,
		FieldDefaults: fieldDefaults,
	}

	typeFS, err := fs.Sub(templates.FS, "typescript/types")
	if err != nil {
		// Fallback to inline generation
		writeConstantsInline(b, pf, fieldDefaults)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "constants.ts.tmpl", funcMap, data)
	if err != nil {
		// Fallback to inline generation
		writeConstantsInline(b, pf, fieldDefaults)
		return
	}

	b.Write(result)
}

// writeConstantsInline is the fallback inline generation for constants.
func writeConstantsInline(b *strings.Builder, pf model.ProtoFile, fieldDefaults []tsDefaultConstant) {
	b.WriteString("// ============================================================\n")
	b.WriteString("// Constants\n")
	b.WriteString("// ============================================================\n")

	for _, c := range pf.Constants {
		tsType := mapConstTypeToTypeScript(c.Type, c.Value)
		tsValue := convertConstValueToTypeScript(c.Value, c.Type)
		constName := c.Name

		if tsType != "" {
			fmt.Fprintf(b, "export const %s: %s = %s;\n", constName, tsType, tsValue)
		} else {
			fmt.Fprintf(b, "export const %s = %s;\n", constName, tsValue)
		}
	}

	if len(pf.Constants) > 0 && len(fieldDefaults) > 0 {
		b.WriteString("\n")
	}
	for _, dc := range fieldDefaults {
		fmt.Fprintf(b, "export const %s: %s = %s;\n", dc.Name, dc.TsType, dc.TsValue)
	}

	b.WriteString("\n")
}

// writeSerializationAndValidation writes serialization and validation functions to the buffer
func writeSerializationAndValidation(b *strings.Builder, pf model.ProtoFile) error {
	// Generate serialization helpers
	b.WriteString("// ============================================================\n")
	b.WriteString("// JSON Serialization Helpers\n")
	b.WriteString("// ============================================================\n")

	for _, msg := range pf.Messages {
		if err := generateTypeScriptSerializationFunctions(b, msg); err != nil {
			return err
		}
	}
	writePublicView(b, pf)

	// Generate validation functions
	b.WriteString("// ============================================================\n")
	b.WriteString("// Validation Helpers\n")
	b.WriteString("// ============================================================\n")

	for _, msg := range pf.Messages {
		if err := generateTypeScriptValidationFunction(b, msg); err != nil {
			return err
		}
	}

	return nil
}

// writeFromMap writes fromMap/toMap functions to the buffer
func writeFromMap(b *strings.Builder, pf model.ProtoFile) error {
	b.WriteString("// ============================================================\n")
	b.WriteString("// FromMap / ToMap Helpers\n")
	b.WriteString("// ============================================================\n")

	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			if err := generateTypeScriptFromMapMethod(b, msg, pf); err != nil {
				return err
			}
		}
	}

	return nil
}

// messagesWithDefaults computes the transitive set of message names that have defaults.
// A message has defaults if it has direct default annotations on its fields, if it
// has assert_required message fields (which need nil-initialization), or if it
// has a non-repeated, non-map, same-package message field whose type has defaults.
func messagesWithDefaults(pf model.ProtoFile) map[string]bool {
	direct := make(map[string]bool)
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool", "default_host"} {
				if _, ok := field.Annotations[key]; ok {
					direct[msg.Name] = true
					break
				}
			}
			if direct[msg.Name] {
				break
			}
			// assert_required on message fields — Defaults must initialize nil fields
			if field.IsMessage && !field.IsRepeated && !field.IsMap && !field.IsOneOf && field.TypePackage == "" {
				if _, ok := field.Annotations["assert_required"]; ok {
					direct[msg.Name] = true
					break
				}
			}
		}
	}

	changed := true
	for changed {
		changed = false
		for _, msg := range pf.Messages {
			if direct[msg.Name] {
				continue
			}
			for _, field := range msg.Fields {
				if field.IsMessage && !field.IsRepeated && !field.IsMap && field.TypePackage == "" && direct[field.Type] {
					direct[msg.Name] = true
					changed = true
					break
				}
			}
		}
	}

	return direct
}

// writeDefaults writes TypeDefaults functions for every message in the proto file.
// Messages without defaults get an empty function (null guard + return) so callers
// can unconditionally call defaults without checking whether the type has annotations.
func writeDefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsTS(pf.Messages)

	var defaults []tsDefaultsFunc
	for _, msg := range pf.Messages {
		funcName := strutil.LcFirst(msg.Name) + "Defaults"
		var assignments []string

		// Direct field defaults
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool", "default_host"} {
				if _, ok := field.Annotations[key]; ok {
					tsFieldName := snakeToCamelCase(field.Name)
					var zeroCheck string
					switch key {
					case "default_string":
						zeroCheck = fmt.Sprintf(`m.%s === ""`, tsFieldName)
					case "default_int":
						if isBigIntProtoType(field.Type) {
							zeroCheck = fmt.Sprintf(`m.%s === 0n`, tsFieldName)
						} else {
							zeroCheck = fmt.Sprintf(`m.%s === 0`, tsFieldName)
						}
					case "default_float":
						zeroCheck = fmt.Sprintf(`m.%s === 0`, tsFieldName)
					case "default_bool":
						zeroCheck = fmt.Sprintf(`!m.%s`, tsFieldName)
					case "default_host":
						// host.Host fields are messages — use create(HostSchema, {...})
						av := field.Annotations["default_host"]
						if av.HostValue != nil {
							assignments = append(assignments, fmt.Sprintf(
								"if (!m.%s) { m.%s = create(HostSchema, { host: %q, port: %d }); }",
								tsFieldName, tsFieldName, av.HostValue.Host, av.HostValue.Port))
						}
						continue
					}
					if zeroCheck == "" {
						continue
					}
					upperConstName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
					assignments = append(assignments, fmt.Sprintf("if (%s) { m.%s = %s; }", zeroCheck, tsFieldName, upperConstName))
				}
			}
		}

		// Sub-message defaults (including assert_required initialization)
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}

			_, isRequired := field.Annotations["assert_required"]
			transitive := hasDefaults[field.Type]

			if !isRequired && !transitive {
				continue
			}

			tsFieldName := snakeToCamelCase(field.Name)
			subFuncName := strutil.LcFirst(field.Type) + "Defaults"

			if isRequired {
				// assert_required: initialize if falsy, then apply defaults
				assignments = append(assignments, fmt.Sprintf("if (!m.%s) { m.%s = create(%sSchema); }", tsFieldName, tsFieldName, field.Type))
				if transitive {
					assignments = append(assignments, fmt.Sprintf("%s(m.%s);", subFuncName, tsFieldName))
				}
			} else {
				// Optional: only apply defaults if already set
				assignments = append(assignments, fmt.Sprintf("if (m.%s) { %s(m.%s); }", tsFieldName, subFuncName, tsFieldName))
			}
		}

		defaults = append(defaults, tsDefaultsFunc{
			MsgName:     msg.Name,
			FuncName:    funcName,
			Assignments: assignments,
		})
	}

	if len(defaults) == 0 {
		return
	}

	data := tsConstantsData{
		Defaults: defaults,
	}

	typeFS, err := fs.Sub(templates.FS, "typescript/types")
	if err != nil {
		writeDefaultsInline(b, defaults)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "constants.ts.tmpl", funcMap, data)
	if err != nil {
		writeDefaultsInline(b, defaults)
		return
	}

	b.Write(result)
}

// writeDefaultsInline is the fallback inline generation for defaults.
func writeDefaultsInline(b *strings.Builder, defaults []tsDefaultsFunc) {
	for _, d := range defaults {
		b.WriteString("// ============================================================\n")
		b.WriteString("// Type Defaults\n")
		b.WriteString("// ============================================================\n")
		fmt.Fprintf(b, "/** * Applies default values to %s fields that are at their zero value. */\n", d.MsgName)
		fmt.Fprintf(b, "export function %s(m: %s): void {\n", d.FuncName, d.MsgName)
		fmt.Fprintf(b, "if (!m) { return; }\n")
		for _, a := range d.Assignments {
			fmt.Fprintf(b, "%s\n", a)
		}
		b.WriteString("}\n")
	}
}

// writeUndefaults generates the inverse of writeDefaults for every message: resets fields that
// match their default values back to zero so only user overrides are persisted.
// Messages without defaults get an empty function (null guard + return).
func writeUndefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsTS(pf.Messages)

	for _, msg := range pf.Messages {
		funcName := strutil.LcFirst(msg.Name) + "Undefaults"
		var assignments []string

		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool", "default_host"} {
				if _, ok := field.Annotations[key]; ok {
					tsFieldName := snakeToCamelCase(field.Name)
					upperConstName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
					var cmp string
					switch key {
					case "default_string":
						cmp = fmt.Sprintf("m.%s === %s", tsFieldName, upperConstName)
					case "default_int":
						if isBigIntProtoType(field.Type) {
							cmp = fmt.Sprintf("m.%s === BigInt(%s)", tsFieldName, upperConstName)
						} else {
							cmp = fmt.Sprintf("m.%s === %s", tsFieldName, upperConstName)
						}
					case "default_float":
						cmp = fmt.Sprintf("m.%s === %s", tsFieldName, upperConstName)
					case "default_bool":
						cmp = fmt.Sprintf("m.%s === %s", tsFieldName, upperConstName)
					case "default_host":
						// host.Host fields — compare host+port, reset to undefined
						av := field.Annotations["default_host"]
						if av.HostValue != nil {
							assignments = append(assignments, fmt.Sprintf(
								"if (m.%s && m.%s.host === %q && m.%s.port === %d) { m.%s = undefined; }",
								tsFieldName, tsFieldName, av.HostValue.Host, tsFieldName, av.HostValue.Port, tsFieldName))
						}
						continue
					}
					var zero string
					switch key {
					case "default_string":
						zero = `""`
					case "default_int":
						if isBigIntProtoType(field.Type) {
							zero = "0n"
						} else {
							zero = "0"
						}
					case "default_float":
						zero = "0"
					case "default_bool":
						zero = "false"
					}
					assignments = append(assignments, fmt.Sprintf("if (%s) { m.%s = %s; }", cmp, tsFieldName, zero))
				}
			}
		}

		// Sub-message undefaults
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}
			if hasDefaults[field.Type] {
				tsFieldName := snakeToCamelCase(field.Name)
				subFuncName := strutil.LcFirst(field.Type) + "Undefaults"
				assignments = append(assignments, fmt.Sprintf("if (m.%s) { %s(m.%s); }", tsFieldName, subFuncName, tsFieldName))
			}
		}

		b.WriteString("// ============================================================\n")
		b.WriteString("// Type Undefaults\n")
		b.WriteString("// ============================================================\n")
		fmt.Fprintf(b, "/** Resets %s fields that match defaults back to zero for minimal persistence. */\n", msg.Name)
		fmt.Fprintf(b, "export function %s(m: %s): void {\n", funcName, msg.Name)
		fmt.Fprintf(b, "if (!m) { return; }\n")
		for _, a := range assignments {
			fmt.Fprintf(b, "%s\n", a)
		}
		b.WriteString("}\n")
	}
}

// writeMerge generates merge functions for every message in the proto file.
// Each function fills zero-valued fields in dst from src. dst takes precedence.
func writeMerge(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	// Build set of messages that have fields (and thus get merge functions).
	msgHasFields := make(map[string]bool)
	for _, msg := range pf.Messages {
		if len(msg.Fields) > 0 {
			msgHasFields[msg.Name] = true
		}
	}

	for _, msg := range pf.Messages {
		if len(msg.Fields) == 0 {
			continue
		}

		funcName := "merge" + msg.Name

		b.WriteString("// ============================================================\n")
		fmt.Fprintf(b, "// Merge: %s\n", msg.Name)
		b.WriteString("// ============================================================\n")
		fmt.Fprintf(b, "/** Fills zero-valued fields in dst from src. dst takes precedence. */\n")
		fmt.Fprintf(b, "export function %s(dst: %s, src: %s): void {\n", funcName, msg.Name, msg.Name)

		// Collect oneof groups
		oneofGroups := map[string][]model.Field{}
		var oneofOrder []string

		for _, field := range msg.Fields {
			// Skip oneof fields — handled below as groups
			if field.IsOneOf {
				if _, seen := oneofGroups[field.OneOfName]; !seen {
					oneofOrder = append(oneofOrder, field.OneOfName)
				}
				oneofGroups[field.OneOfName] = append(oneofGroups[field.OneOfName], field)
				continue
			}

			tsFieldName := snakeToCamelCase(field.Name)

			switch {
			case field.IsMap:
				if field.MapValueIsMessage && field.MapValueTypePackage == "" && msgHasFields[field.MapValueType] {
					// Map with same-package message value: merge matching entries
					subFuncName := "merge" + field.MapValueType
					fmt.Fprintf(b, "if (Object.keys(dst.%s).length === 0) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName)
					fmt.Fprintf(b, "else { for (const k of Object.keys(src.%s)) {\n", tsFieldName)
					fmt.Fprintf(b, "if (k in dst.%s) { %s(dst.%s[k], src.%s[k]); }\n",
						tsFieldName, subFuncName, tsFieldName, tsFieldName)
					fmt.Fprintf(b, "else { dst.%s[k] = src.%s[k]; }\n",
						tsFieldName, tsFieldName)
					b.WriteString("} }\n")
				} else {
					fmt.Fprintf(b, "if (Object.keys(dst.%s).length === 0) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName)
				}
			case field.IsRepeated:
				fmt.Fprintf(b, "if (dst.%s.length === 0) { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			case field.IsMessage:
				if field.TypePackage == "" && msgHasFields[field.Type] {
					// Same-package message with fields: recurse if both non-nil
					subFuncName := "merge" + field.Type
					fmt.Fprintf(b, "if (!dst.%s && src.%s) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName, tsFieldName)
					fmt.Fprintf(b, "else if (dst.%s && src.%s) { %s(dst.%s, src.%s); }\n",
						tsFieldName, tsFieldName, subFuncName, tsFieldName, tsFieldName)
				} else {
					// Cross-package or empty message: take src if dst is falsy
					fmt.Fprintf(b, "if (!dst.%s && src.%s) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName, tsFieldName)
				}
			case field.IsEnum:
				fmt.Fprintf(b, "if (dst.%s === 0) { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			case field.Type == "string":
				fmt.Fprintf(b, "if (dst.%s === \"\") { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			case field.Type == "bool":
				fmt.Fprintf(b, "if (dst.%s === false) { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			case field.Type == "bytes":
				fmt.Fprintf(b, "if (dst.%s.length === 0) { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			case isIntegerType(field.Type):
				if isBigIntProtoType(field.Type) {
					fmt.Fprintf(b, "if (dst.%s === 0n) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName)
				} else {
					fmt.Fprintf(b, "if (dst.%s === 0) { dst.%s = src.%s; }\n",
						tsFieldName, tsFieldName, tsFieldName)
				}
			case isFloatType(field.Type):
				fmt.Fprintf(b, "if (dst.%s === 0) { dst.%s = src.%s; }\n",
					tsFieldName, tsFieldName, tsFieldName)
			}
		}

		// Handle oneof groups: protobuf-es represents oneof as a discriminated
		// union on a single property named after the group (e.g. `value.case`).
		// If dst has no variant set, copy the entire union from src.
		for _, oneofName := range oneofOrder {
			tsPropName := snakeToCamelCase(oneofName)
			fmt.Fprintf(b, "  if (dst.%s.case === undefined && src.%s.case !== undefined) {\n",
				tsPropName, tsPropName)
			fmt.Fprintf(b, "    dst.%s = src.%s;\n", tsPropName, tsPropName)
			b.WriteString("  }\n")
		}

		b.WriteString("}\n")
	}
}

// NOTE: generateTypeScriptEnums has been consolidated into writeEnumHelpers()
// which writes directly to the shared helpers.ts buffer.

func generateTypeScriptEnumHelpers(b *strings.Builder, enum model.Enum) {
	toTsEnumValue := func(valueName string) string {
		return typescriptEnumValueName(enum, valueName)
	}

	toFriendlyString := func(v model.EnumValue) string {
		// If json_return_value annotation is set, use it
		if v.JsonValue != "" {
			return v.JsonValue
		}
		// Otherwise, convert from UPPER_SNAKE_CASE to kebab-case
		prefix := strutil.ToSnakeCase(enum.Name) + "_"
		name := strings.TrimPrefix(v.Name, strings.ToUpper(prefix))
		name = strings.ToLower(name)
		name = strings.ReplaceAll(name, "_", "-")
		return name
	}

	// For case-insensitive matching in switch statements
	toFriendlyStringLower := func(v model.EnumValue) string {
		return strings.ToLower(toFriendlyString(v))
	}

	var nonUndefinedValues []model.EnumValue
	for _, v := range enum.Values {
		if !model.IsEnumSentinel(v.Name) && v.Number != 0 {
			nonUndefinedValues = append(nonUndefinedValues, v)
		}
	}

	// values() function
	fmt.Fprintf(b, "export function %sValues(): %s[] {\n", strutil.LcFirst(enum.Name), enum.Name)
	b.WriteString("return [\n")
	for _, v := range nonUndefinedValues {
		fmt.Fprintf(b, "%s.%s,\n", enum.Name, toTsEnumValue(v.Name))
	}
	b.WriteString("];}\n")

	// stringValues() function
	fmt.Fprintf(b, "export function %sStringValues(): string[] {\n", strutil.LcFirst(enum.Name))
	b.WriteString("return [")
	for i, v := range nonUndefinedValues {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "'%s'", toFriendlyString(v))
	}
	b.WriteString("];}\n")

	// parse() function
	fmt.Fprintf(b, "export function parse%s(s: string): %s {\n", enum.Name, enum.Name)
	b.WriteString("if (!s) {\n")
	if len(enum.Values) > 0 && enum.Values[0].Number == 0 {
		fmt.Fprintf(b, "return %s.%s;\n", enum.Name, toTsEnumValue(enum.Values[0].Name))
	} else {
		fmt.Fprintf(b, "return %s.UNRECOGNIZED;\n", enum.Name)
	}
	b.WriteString("}\n")
	b.WriteString("const normalized = s.toLowerCase().replace(/_/g, '-');\n")
	b.WriteString("switch (normalized) {\n")
	for _, v := range nonUndefinedValues {
		friendlyName := toFriendlyStringLower(v)
		fmt.Fprintf(b, "case '%s':\n", friendlyName)
		fmt.Fprintf(b, "return %s.%s;\n", enum.Name, toTsEnumValue(v.Name))
	}
	b.WriteString("default:\n")
	fmt.Fprintf(b, "throw newErrMissingInvalidData(undefined, `Invalid %s: ${s}`);\n", strings.ToLower(enum.Name))
	b.WriteString("}}\n")

	// parse plural function
	fmt.Fprintf(b, "export function parse%ss(s: string): %s[] {\n", enum.Name, enum.Name)
	b.WriteString("if (!s) {\n")
	b.WriteString("return [];\n")
	b.WriteString("}\n")
	b.WriteString("return s.split(',').map(part => part.trim()).filter(part => part !== '').map(part => parse" + enum.Name + "(part));\n")
	b.WriteString("}\n")

	// toString() function
	fmt.Fprintf(b, "export function %sToString(v: %s): string {\n", strutil.LcFirst(enum.Name), enum.Name)
	b.WriteString("switch (v) {\n")
	for _, v := range nonUndefinedValues {
		friendlyName := toFriendlyString(v)
		fmt.Fprintf(b, "case %s.%s:\n", enum.Name, toTsEnumValue(v.Name))
		fmt.Fprintf(b, "return '%s';\n", friendlyName)
	}
	b.WriteString("default:\n")
	b.WriteString("return '';\n")
	b.WriteString("}}\n")
}

// Helper functions

func getEnumNames(enums []model.Enum) []string {
	names := make([]string, len(enums))
	for i, e := range enums {
		names[i] = e.Name
	}
	return names
}

// NOTE: generateTypeScriptValidation has been consolidated into writeSerializationAndValidation()
// which writes directly to the shared helpers.ts buffer.

// generateTypeScriptSerializationFunctions generates serialization helper functions for a message
func generateTypeScriptSerializationFunctions(b *strings.Builder, msg model.Message) error {
	// Note: imports are added at the file level, not per-function

	// toJSON function
	fmt.Fprintf(b, `/**
 * Converts %s to JSON string.
 * @param m The message to serialize
 * @param pretty If true, formats the JSON with indentation
 * @returns JSON string representation
 */
export function %sToJSON(m: %s, pretty: boolean = false): string {
	const json = toJson(%sSchema, m);
	return pretty ? JSON.stringify(json, null, 2) : JSON.stringify(json);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// fromJSON function
	fmt.Fprintf(b, `/**
 * Creates %s from JSON string or object.
 * @param data JSON string or object
 * @returns Parsed message instance
 */
export function %sFromJSON(data: string | object): %s {
	const jsonObj = typeof data === 'string' ? JSON.parse(data) : data;
	return fromJson(%sSchema, jsonObj);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// toBinary function
	fmt.Fprintf(b, `/**
 * Converts %s to binary protobuf format.
 * @param m The message to serialize
 * @returns Uint8Array containing the binary data
 */
export function %sToBinary(m: %s): Uint8Array {
	return toBinary(%sSchema, m);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// fromBinary function
	fmt.Fprintf(b, `/**
 * Creates %s from binary protobuf data.
 * @param data Binary protobuf data
 * @returns Parsed message instance
 */
export function %sFromBinary(data: Uint8Array): %s {
	return fromBinary(%sSchema, data);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// toYAML function
	fmt.Fprintf(b, `/**
 * Converts %s to YAML string.
 * @param m The message to serialize
 * @returns YAML string representation
 */
export function %sToYAML(m: %s): string {
	const json = toJson(%sSchema, m);
	return yaml.dump(json);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// toTOML function
	fmt.Fprintf(b, `/**
 * Converts %s to TOML string.
 * @param m The message to serialize
 * @returns TOML string representation
 */
export function %sToTOML(m: %s): string {
	const json = toJson(%sSchema, m);
	return toml.stringify(json as toml.JsonMap);
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, msg.Name)

	// fromBytes with auto-detection (updated to support YAML/TOML)
	fmt.Fprintf(b, `/**
 * Creates %s from bytes, auto-detecting the format (JSON, YAML, TOML, or binary protobuf).
 * @param data Bytes to parse
 * @returns Parsed message instance
 * @throws Error if the data cannot be parsed
 */
export function %sFromBytes(data: Uint8Array | string): %s {
	// If it's a string, try to detect format
	if (typeof data === 'string') {
		// Try JSON first (most common)
		if (data.trimStart().startsWith('{') || data.trimStart().startsWith('[')) {
			try {
				return %sFromJSON(data);
			} catch (e) {
				// Fall through to try other formats
			}
		}

		// Try YAML
		try {
			const yamlObj = yaml.load(data) as string | object;
			return %sFromJSON(yamlObj);
		} catch (e) {
			// Fall through to try TOML
		}

		// Try TOML
		try {
			const tomlObj = toml.parse(data) as string | object;
			return %sFromJSON(tomlObj);
		} catch (e) {
			throw newErrMissingInvalidData(undefined, 'Failed to parse %s from string: not valid JSON, YAML, or TOML');
		}
	}

	// Try to detect format from the first byte
	// JSON starts with '{' (0x7B) or '[' (0x5B) or whitespace
	if (data.length > 0) {
		const firstByte = data[0];
		// Check for JSON markers (after trimming whitespace)
		let i = 0;
		while (i < data.length && (data[i] === 0x20 || data[i] === 0x09 || data[i] === 0x0A || data[i] === 0x0D)) {
			i++;
		}
		if (i < data.length && (data[i] === 0x7B || data[i] === 0x5B)) {
			// Looks like JSON
			try {
				const text = new TextDecoder().decode(data);
				return %sFromJSON(text);
			} catch (e) {
				// Fall through to try other formats
			}
		}

		// Try to decode as text and parse as YAML/TOML
		try {
			const text = new TextDecoder().decode(data);

			// Try YAML
			try {
				const yamlObj = yaml.load(text) as string | object;
				return %sFromJSON(yamlObj);
			} catch (e) {
				// Fall through
			}

			// Try TOML
			try {
				const tomlObj = toml.parse(text) as string | object;
				return %sFromJSON(tomlObj);
			} catch (e) {
				// Fall through
			}
		} catch (e) {
			// Fall through to try binary
		}
	}

	// Try binary protobuf
	try {
		return %sFromBinary(data);
	} catch (e) {
		throw newErrMissingInvalidData(undefined, `+"`Failed to parse %s: invalid format`"+`);
	}
}

`, msg.Name, strutil.LcFirst(msg.Name), msg.Name, strutil.LcFirst(msg.Name), strutil.LcFirst(msg.Name), strutil.LcFirst(msg.Name), msg.Name, strutil.LcFirst(msg.Name), strutil.LcFirst(msg.Name), strutil.LcFirst(msg.Name), strutil.LcFirst(msg.Name), msg.Name)

	return nil
}

// NOTE: generateTypeScriptFromMap has been consolidated into writeFromMap()
// which writes directly to the shared helpers.ts buffer.

func generateTypeScriptFromMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
	config := msg.ParamMapConfig
	keyEnumType := config.KeyEnum

	// Find the key enum
	var keyEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == keyEnumType {
			keyEnum = &pf.Enums[i]
			break
		}
	}

	if keyEnum == nil {
		return fmt.Errorf("key enum %s not found for message %s", keyEnumType, msg.Name)
	}

	// Note: We rely on fields having explicit param_key annotations that map to enum values

	// Generate function
	fmt.Fprintf(b, "/** * Creates a %s from a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(b, "* The parameter keys are validated against %s enum values.\n", keyEnumType)
	if config.Strict {
		b.WriteString("* Unknown parameter keys will throw an error (strict mode).\n")
	} else {
		b.WriteString("* Unknown parameter keys are silently ignored (non-strict mode).\n")
	}
	b.WriteString("*/\n")
	fmt.Fprintf(b, "export function %sFromMap(params: Record<string, string>): %s {\n",
		strutil.ToLowerFirst(msg.Name), msg.Name)
	fmt.Fprintf(b, "const result = create(%sSchema, {});\n", msg.Name)

	b.WriteString("for (const [key, value] of Object.entries(params)) {\n")
	fmt.Fprintf(b, "const paramKey = parse%s(key);\n", keyEnumType)
	b.WriteString("if (paramKey === undefined) {\n")
	if config.Strict {
		b.WriteString("throw newErrMissingInvalidData(undefined, `Invalid parameter key: ${key}`);\n")
	} else {
		b.WriteString("continue; // Ignore unknown keys in non-strict mode\n")
	}
	b.WriteString("}\n")

	b.WriteString("switch (paramKey) {\n")

	// Generate cases for each field with param_key annotation
	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue // Skip fields without param_key annotation
		}

		// Get TypeScript enum value.
		tsEnumValue := getTsEnumValue(field.ParamKey, keyEnumType)
		tsFieldName := snakeToCamelCase(field.Name)
		fmt.Fprintf(b, "case %s.%s:\n", keyEnumType, tsEnumValue)

		if field.IsRepeated {
			b.WriteString("if (value) {\n")
			fmt.Fprintf(b, "result.%s = value.split(',');\n", tsFieldName)
			b.WriteString("}\n")
		} else if isNumericType(field.Type) {
			if isFloatType(field.Type) {
				b.WriteString("const floatVal = parseFloat(value);\n")
				b.WriteString("if (!isNaN(floatVal)) {\n")
				fmt.Fprintf(b, "result.%s = floatVal;\n", tsFieldName)
				b.WriteString("}\n")
			} else {
				b.WriteString("const intVal = parseInt(value, 10);\n")
				b.WriteString("if (!isNaN(intVal)) {\n")
				fmt.Fprintf(b, "result.%s = intVal;\n", tsFieldName)
				b.WriteString("}\n")
			}
		} else if field.IsEnum {
			// For enum fields, parse from string using the enum's parse function
			enumTypeName := field.Type
			// Strip package prefix if present (field.Type can be qualified e.g. "key.KeyType")
			if strings.Contains(enumTypeName, ".") {
				parts := strings.Split(enumTypeName, ".")
				enumTypeName = parts[len(parts)-1]
			}
			enumParseFunc := "parse" + enumTypeName
			fmt.Fprintf(b, "result.%s = %s(value);\n", tsFieldName, enumParseFunc)
		} else {
			fmt.Fprintf(b, "result.%s = value;\n", tsFieldName)
		}
		b.WriteString("break;\n")
	}

	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("return result;\n")
	b.WriteString("}\n")

	// Generate ToMap method
	if err := generateTypeScriptToMapMethod(b, msg, pf); err != nil {
		return err
	}

	return nil
}

func generateTypeScriptToMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
	config := msg.ParamMapConfig
	keyEnumType := config.KeyEnum

	// Find the key enum
	var keyEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == keyEnumType {
			keyEnum = &pf.Enums[i]
			break
		}
	}

	if keyEnum == nil {
		return fmt.Errorf("key enum %s not found for message %s", keyEnumType, msg.Name)
	}

	// Build a map of enum value name -> json_return_value
	enumValueToJsonValue := make(map[string]string)
	for _, enumVal := range keyEnum.Values {
		if enumVal.JsonValue != "" {
			enumValueToJsonValue[enumVal.Name] = enumVal.JsonValue
		}
	}

	// Generate function documentation
	fmt.Fprintf(b, "/** * Converts a %s to a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(b, "* The map keys are the json_return_value of %s enum values.\n", keyEnumType)
	b.WriteString("* Only non-empty fields are included in the map.\n")
	b.WriteString("*/\n")
	fmt.Fprintf(b, "export function %sToMap(m: %s): Record<string, string> {\n",
		strutil.ToLowerFirst(msg.Name), msg.Name)
	b.WriteString("const result: Record<string, string> = {};\n")

	// Generate conversion logic for each field with param_key annotation
	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue // Skip fields without param_key annotation
		}

		// Get the json_return_value for this enum value
		jsonKey, hasJsonKey := enumValueToJsonValue[field.ParamKey]
		if !hasJsonKey {
			continue // Skip if enum value doesn't have json_return_value
		}

		// Generate field conversion based on type
		tsFieldName := snakeToCamelCase(field.Name)
		if field.IsRepeated {
			// For repeated fields, join with comma
			fmt.Fprintf(b, "if (m.%s && m.%s.length > 0) {\n", tsFieldName, tsFieldName)
			fmt.Fprintf(b, "result['%s'] = m.%s.join(',');\n", jsonKey, tsFieldName)
			b.WriteString("}\n")
		} else if isNumericType(field.Type) {
			// For numeric fields, convert to string
			fmt.Fprintf(b, "if (m.%s !== undefined && m.%s !== null) {\n", tsFieldName, tsFieldName)
			fmt.Fprintf(b, "result['%s'] = m.%s.toString();\n", jsonKey, tsFieldName)
			b.WriteString("}\n")
		} else if field.IsEnum {
			// For enum fields, convert to string using the enum's toString function
			enumTypeName := field.Type
			// Strip package prefix if present (field.Type can be qualified e.g. "key.KeyType")
			if strings.Contains(enumTypeName, ".") {
				parts := strings.Split(enumTypeName, ".")
				enumTypeName = parts[len(parts)-1]
			}
			enumToStringFunc := strutil.ToLowerFirst(enumTypeName) + "ToString"
			fmt.Fprintf(b, "if (m.%s) {\n", tsFieldName)
			fmt.Fprintf(b, "result['%s'] = %s(m.%s);\n", jsonKey, enumToStringFunc, tsFieldName)
			b.WriteString("}\n")
		} else {
			// For string fields
			fmt.Fprintf(b, "if (m.%s) {\n", tsFieldName)
			fmt.Fprintf(b, "result['%s'] = m.%s;\n", jsonKey, tsFieldName)
			b.WriteString("}\n")
		}
	}

	b.WriteString("return result;\n")
	b.WriteString("}\n")

	return nil
}

func getTsEnumValue(enumValueName, enumTypeName string) string {
	return typescriptEnumValueNameByType(enumTypeName, enumValueName)
}

func typescriptEnumValueName(enum model.Enum, valueName string) string {
	return typescriptEnumValueNameByTypeAndValues(enum.Name, enum.Values, valueName)
}

func typescriptEnumValueNameByType(enumTypeName, valueName string) string {
	return typescriptEnumValueNameByTypeAndValues(enumTypeName, nil, valueName)
}

func typescriptEnumValueNameByTypeAndValues(enumTypeName string, values []model.EnumValue, valueName string) string {
	prefix := typescriptEnumSharedPrefix(enumTypeName, values)
	if prefix != "" {
		valueName = valueName[len(prefix):]
	}
	return safeTypescriptObjectProperty(valueName)
}

func typescriptEnumSharedPrefix(enumTypeName string, values []model.EnumValue) string {
	prefix := typescriptCamelToSnakeCase(enumTypeName) + "_"
	if len(values) == 0 {
		return prefix
	}
	for _, value := range values {
		if !strings.HasPrefix(strings.ToLower(value.Name), prefix) {
			return ""
		}
		shortName := value.Name[len(prefix):]
		if shortName == "" || (shortName[0] >= '0' && shortName[0] <= '9') {
			return ""
		}
	}
	return prefix
}

func typescriptCamelToSnakeCase(camel string) string {
	if camel == "" {
		return ""
	}
	var b strings.Builder
	b.WriteByte(camel[0])
	for i := 1; i < len(camel); i++ {
		if camel[i] >= 'A' && camel[i] <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteByte(camel[i])
	}
	return strings.ToLower(b.String())
}

func safeTypescriptObjectProperty(name string) string {
	switch name {
	case "constructor", "toString", "toJSON", "valueOf":
		return name + "$"
	default:
		return name
	}
}

// snakeToCamelCase converts snake_case to camelCase (used for protobuf v2 field names)
func snakeToCamelCase(s string) string {
	return tsProtoFieldName(s)
}

func tsProtoFieldName(s string) string {
	if s != "" && !strings.Contains(s, "_") && s != strings.ToLower(s) {
		return s
	}
	return strutil.SnakeToCamelCase(s)
}
