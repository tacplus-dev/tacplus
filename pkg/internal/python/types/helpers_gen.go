// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/templates"
)

// pythonKeywords is the set of Python reserved keywords that cannot be used as identifiers.
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true,
	"assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true, "global": true,
	"if": true, "import": true, "in": true, "is": true, "lambda": true,
	"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
	"return": true, "try": true, "while": true, "with": true, "yield": true,
}

// escapePyKeyword appends a trailing underscore to Python reserved keywords.
//
// This is only valid for identifiers the generator invents in Python source it
// owns — module aliases, wrapper attributes, method names — where "def from()"
// would not parse. It must NOT be used for protobuf field access: protobuf-python
// keeps the raw proto field name on the message object (a field named "from" is
// reachable only as getattr(msg, 'from')), so an escaped name there is simply a
// name that does not exist. Use pyFieldGet/pyFieldSet for field access instead.
func escapePyKeyword(name string) string {
	if pythonKeywords[name] {
		return name + "_"
	}
	return name
}

// pyFieldGet renders a Python expression reading proto field name from obj.
//
// Dot access to a keyword-named attribute is a SyntaxError, so keyword fields are
// read with getattr. Single quotes are used for the name because these expressions
// are interpolated into double-quoted f-strings, where nesting the same quote
// character does not parse before Python 3.12.
func pyFieldGet(obj, name string) string {
	if pythonKeywords[name] {
		return fmt.Sprintf("getattr(%s, '%s')", obj, name)
	}
	return obj + "." + name
}

// pyFieldSet renders a Python statement assigning expr to proto field name on obj.
// See pyFieldGet for why keyword-named fields cannot use dot syntax.
func pyFieldSet(obj, name, expr string) string {
	if pythonKeywords[name] {
		return fmt.Sprintf("setattr(%s, '%s', %s)", obj, name, expr)
	}
	return obj + "." + name + " = " + expr
}

// pyFieldFuncMap is the shared template func map plus the proto field accessors.
func pyFieldFuncMap() template.FuncMap {
	fm := tmplutil.BaseFuncMap()
	fm["pyGet"] = pyFieldGet
	fm["pySet"] = pyFieldSet
	return fm
}

// pyImportsData holds the template data for the imports section of helpers.py.
type pyImportsData struct {
	HasMessages         bool
	HasEnums            bool
	NeedsTimedelta      bool
	TypingImports       []string
	NeedsPb2Import      bool
	ProtoFilename       string
	ExceptionImportFrom string
}

// pyDefaultField holds data for a single field with a default annotation.
type pyDefaultField struct {
	PyFieldName  string
	ConstantName string
	AnnKey       string
	// ProtoFieldName is the raw proto field name, for HasField()/ClearField(),
	// which take the proto name rather than a Python attribute.
	ProtoFieldName string
	HostHost       string // For default_host: the default hostname
	HostPort       int32  // For default_host: the default port number
}

// pySubMsgCall holds data for a sub-message defaults call.
type pySubMsgCall struct {
	HasFieldName  string // proto field name for HasField() check
	PyFieldName   string // protobuf-python field accessor
	FuncName      string // function name, e.g. "cluster_config"
	InitIfNil     bool   // true for assert_required fields — initialize if not set
	HasDefaults   bool   // true if the sub-message has its own defaults function
	QualifiedType string // qualified type for initialization, e.g. "myservice_pb2.SomeConfig"
}

// pyDefaultMessage holds data for a single message with default fields.
type pyDefaultMessage struct {
	TypeName      string
	FuncName      string
	QualifiedType string
	Fields        []pyDefaultField
	SubMsgCalls   []pySubMsgCall
}

// pyDefaultsData holds the template data for the defaults section.
type pyDefaultsData struct {
	Defaults []pyDefaultMessage
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

	// Generate exceptions module once alongside errors_pb2.py.
	errorsSubdir := filepath.Join("types", "sys", "errors")
	errorsDir := filepath.Join(outputDir, errorsSubdir)
	if err := os.MkdirAll(errorsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create errors directory %s: %w", errorsDir, err)
	}
	if err := generateExceptions(outputDir, errorsSubdir); err != nil {
		return nil, fmt.Errorf("failed to generate exceptions: %w", err)
	}

	// Generate consolidated helpers.py for each proto file
	for _, pf := range protoFiles {
		subdir, protoFilename := pythonProtoPackageSubdir(pf.RelPath, pf.Package)

		// Build a single helpers.py from all sections
		var b strings.Builder

		// Write combined header
		b.WriteString(header.SimpleHeader(consts.LangPython))
		fmt.Fprintf(&b, "# Source: %s\n", pf.RelPath)

		// Write combined imports section
		if err := writeCombinedImports(&b, pf, subdir, protoFilename); err != nil {
			return nil, fmt.Errorf("failed to write imports for %s: %w", pf.RelPath, err)
		}

		// Write each section
		if len(pf.Enums) > 0 {
			if err := writeEnumHelpers(&b, pf, protoFilename); err != nil {
				return nil, fmt.Errorf("failed to write enum helpers for %s: %w", pf.RelPath, err)
			}
		}
		if len(pf.Constants) > 0 || hasFieldDefaults(pf) {
			if err := writeConstants(&b, pf); err != nil {
				return nil, fmt.Errorf("failed to write constants for %s: %w", pf.RelPath, err)
			}
		}
		if len(pf.Messages) > 0 {
			if err := writeSerialization(&b, pf, protoFilename); err != nil {
				return nil, fmt.Errorf("failed to write serialization for %s: %w", pf.RelPath, err)
			}
		}
		writePublicView(&b, pf, protoFilename)
		if len(pf.Messages) > 0 {
			if err := writeValidation(&b, pf); err != nil {
				return nil, fmt.Errorf("failed to write validation for %s: %w", pf.RelPath, err)
			}
		}
		if len(pf.Messages) > 0 {
			if err := writeFromMap(&b, pf); err != nil {
				return nil, fmt.Errorf("failed to write from-map for %s: %w", pf.RelPath, err)
			}
		}
		if err := writeDefaults(&b, pf); err != nil {
			return nil, fmt.Errorf("failed to write defaults for %s: %w", pf.RelPath, err)
		}
		if err := writeUndefaults(&b, pf); err != nil {
			return nil, fmt.Errorf("failed to write undefaults for %s: %w", pf.RelPath, err)
		}
		if err := writeMerge(&b, pf); err != nil {
			return nil, fmt.Errorf("failed to write merge for %s: %w", pf.RelPath, err)
		}
		if len(pf.Messages) > 0 {
			writeEnvVar(&b, pf)
		}

		// Write single helpers.py
		outputPath := filepath.Join(outputDir, subdir, "helpers.py")
		helpersDir := filepath.Dir(outputPath)
		if err := os.MkdirAll(helpersDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create output directory %s: %w", helpersDir, err)
		}
		if err := os.WriteFile(outputPath, []byte(b.String()), 0644); err != nil {
			return nil, fmt.Errorf("failed to write helpers.py for %s: %w", pf.RelPath, err)
		}

		// Generate filesystem helpers (directory_from_filesystem / directory_to_filesystem)
		if proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package) {
			if err := generateFilesystemHelpers(outputDir, subdir); err != nil {
				return nil, fmt.Errorf("failed to generate filesystem helpers: %w", err)
			}
		}
	}

	return protoFiles, nil
}

func pythonProtoPackageSubdir(relPath, protoPackage string) (subdir, protoFilename string) {
	protoFilename = pyModuleName(strings.TrimSuffix(filepath.Base(relPath), ".proto"))
	packageName := pythonProtoPackageName(protoPackage, protoFilename)
	safeParentDir := pyPath(filepath.Dir(relPath))
	if safeParentDir == "" {
		return filepath.Join("types", packageName), protoFilename
	}
	return filepath.Join("types", safeParentDir, packageName), protoFilename
}

func pyExceptionImportFrom(subdir string) string {
	subdir = filepath.ToSlash(subdir)
	subdirAfterTypes := strings.TrimPrefix(subdir, "types/")
	switch {
	case subdirAfterTypes == "sys/errors":
		return ".exceptions"
	case strings.HasPrefix(subdirAfterTypes, "sys/"):
		return "..errors.exceptions"
	default:
		depth := len(strings.Split(subdirAfterTypes, "/"))
		return strings.Repeat(".", depth+1) + "sys.errors.exceptions"
	}
}

// writeCombinedImports writes all imports needed by all helper sections at the top of helpers.py
func writeCombinedImports(b *strings.Builder, pf model.ProtoFile, subdir, protoFilename string) error {
	hasMessages := len(pf.Messages) > 0
	hasEnums := len(pf.Enums) > 0
	hasConstants := len(pf.Constants) > 0 || hasFieldDefaults(pf)

	// Check if we need Dict import (from map functions)
	needsDict := false
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			needsDict = true
			break
		}
	}

	// Check if we need timedelta import (from constants)
	needsTimedelta := false
	for _, c := range pf.Constants {
		if strings.Contains(c.Type, "Duration") || strings.Contains(c.Value, "time.") { // Go code representation, not proto descriptor
			needsTimedelta = true
			break
		}
	}

	// typing imports
	var typingImports []string
	if hasEnums {
		typingImports = append(typingImports, "List", "Optional")
	}
	if hasMessages {
		typingImports = append(typingImports, "Union")
	}
	if needsDict {
		typingImports = append(typingImports, "Dict")
	}
	// Deduplicate
	if len(typingImports) > 0 {
		seen := make(map[string]bool)
		var unique []string
		for _, t := range typingImports {
			if !seen[t] {
				seen[t] = true
				unique = append(unique, t)
			}
		}
		typingImports = unique
	}

	data := pyImportsData{
		HasMessages:         hasMessages,
		HasEnums:            hasEnums,
		NeedsTimedelta:      needsTimedelta,
		TypingImports:       typingImports,
		NeedsPb2Import:      hasEnums || hasMessages || hasConstants,
		ProtoFilename:       protoFilename,
		ExceptionImportFrom: pyExceptionImportFrom(subdir),
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "helpers.py.tmpl", tmplutil.BaseFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute helpers imports template: %w", err)
	}

	b.Write(result)
	b.WriteString("\n")
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
			for _, key := range defaultAnnotationKeys {
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

// writeDefaults generates {type}_defaults() functions for every message in the proto file.
// Messages without defaults get an empty function (None check + pass) so callers can
// unconditionally call defaults without checking whether the type has default annotations.
func writeDefaults(b *strings.Builder, pf model.ProtoFile) error {
	hasDefaults := messagesWithDefaults(pf)

	protoFilename := pyModuleName(strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto"))

	var defaults []pyDefaultMessage

	dupFields := findDuplicateDefaultFieldsPy(pf.Messages)

	for _, msg := range pf.Messages {
		var fields []pyDefaultField
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				annVal, ok := field.Annotations[annKey]
				if !ok {
					continue
				}
				pyFieldName := strutil.ToSnakeCase(field.Name)
				constantName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				df := pyDefaultField{
					PyFieldName:    pyFieldName,
					ConstantName:   constantName,
					AnnKey:         annKey,
					ProtoFieldName: field.Name,
				}
				if annKey == "default_host" && annVal.HostValue != nil {
					df.HostHost = annVal.HostValue.Host
					df.HostPort = annVal.HostValue.Port
				}
				fields = append(fields, df)
			}
		}

		var subMsgCalls []pySubMsgCall
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}

			_, isRequired := field.Annotations["assert_required"]
			transitive := hasDefaults[field.Type]

			if !isRequired && !transitive {
				continue
			}

			subMsgCalls = append(subMsgCalls, pySubMsgCall{
				HasFieldName:  field.Name,
				PyFieldName:   strutil.ToSnakeCase(field.Name),
				FuncName:      strutil.ToSnakeCase(field.Type),
				InitIfNil:     isRequired,
				HasDefaults:   transitive,
				QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, field.Type),
			})
		}

		defaults = append(defaults, pyDefaultMessage{
			TypeName:      msg.Name,
			FuncName:      strutil.ToSnakeCase(msg.Name),
			QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, msg.Name),
			Fields:        fields,
			SubMsgCalls:   subMsgCalls,
		})
	}

	if len(defaults) == 0 {
		return nil
	}

	data := pyDefaultsData{
		Defaults: defaults,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "defaults.py.tmpl", pyFieldFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute defaults template: %w", err)
	}

	b.Write(result)
	return nil
}

// writeUndefaults generates {type}_undefaults() functions for every message — the inverse of defaults.
// Resets fields matching their default values back to zero for minimal persistence.
// Messages without defaults get an empty function (None check + pass).
func writeUndefaults(b *strings.Builder, pf model.ProtoFile) error {
	hasDefaults := messagesWithDefaults(pf)

	protoFilename := pyModuleName(strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto"))

	var defaults []pyDefaultMessage

	dupFields := findDuplicateDefaultFieldsPy(pf.Messages)

	for _, msg := range pf.Messages {
		var fields []pyDefaultField
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				annVal, ok := field.Annotations[annKey]
				if !ok {
					continue
				}
				pyFieldName := strutil.ToSnakeCase(field.Name)
				constantName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				df := pyDefaultField{
					PyFieldName:    pyFieldName,
					ConstantName:   constantName,
					AnnKey:         annKey,
					ProtoFieldName: field.Name,
				}
				if annKey == "default_host" && annVal.HostValue != nil {
					df.HostHost = annVal.HostValue.Host
					df.HostPort = annVal.HostValue.Port
				}
				fields = append(fields, df)
			}
		}

		var subMsgCalls []pySubMsgCall
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}
			if !hasDefaults[field.Type] {
				continue
			}
			subMsgCalls = append(subMsgCalls, pySubMsgCall{
				HasFieldName:  field.Name,
				PyFieldName:   strutil.ToSnakeCase(field.Name),
				FuncName:      strutil.ToSnakeCase(field.Type),
				HasDefaults:   true,
				QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, field.Type),
			})
		}

		defaults = append(defaults, pyDefaultMessage{
			TypeName:      msg.Name,
			FuncName:      strutil.ToSnakeCase(msg.Name),
			QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, msg.Name),
			Fields:        fields,
			SubMsgCalls:   subMsgCalls,
		})
	}

	if len(defaults) == 0 {
		return nil
	}

	data := pyDefaultsData{
		Defaults: defaults,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "undefaults.py.tmpl", pyFieldFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute undefaults template: %w", err)
	}

	b.Write(result)
	return nil
}

// pyMergeField holds data for a single field in a merge function.
type pyMergeField struct {
	Kind            string // "string", "int", "float", "bool", "enum", "bytes", "message", "repeated", "map"
	FieldName       string // snake_case proto field name
	ProtoFieldName  string // original proto field name for HasField()
	SamePkg         bool   // for message fields: can we recurse?
	MergeFn         string // for same-pkg messages: function name
	MapValueSamePkg bool   // for map fields with message values: is value type same-package?
	MapValueMergeFn string // for map fields with same-pkg message values: merge function name
}

// pyMergeOneofGroup holds data for a oneof group in a merge function.
type pyMergeOneofGroup struct {
	OneofName string // proto oneof name (snake_case), used with WhichOneof()
}

// pyMergeMessage holds data for a single message's merge function.
type pyMergeMessage struct {
	TypeName      string
	FuncName      string
	QualifiedType string
	Fields        []pyMergeField
	OneofGroups   []pyMergeOneofGroup
}

// pyMergeData holds the template data for the merge section.
type pyMergeData struct {
	Messages []pyMergeMessage
}

// writeMerge generates merge functions for every message in the proto file.
func writeMerge(b *strings.Builder, pf model.ProtoFile) error {
	if len(pf.Messages) == 0 {
		return nil
	}

	protoFilename := pyModuleName(strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto"))

	// Build set of messages with fields — only those get merge functions.
	hasFields := make(map[string]bool)
	for _, m := range pf.Messages {
		if len(m.Fields) > 0 {
			hasFields[m.Name] = true
		}
	}

	var messages []pyMergeMessage

	for _, msg := range pf.Messages {
		if len(msg.Fields) == 0 {
			continue
		}

		var fields []pyMergeField
		var oneofGroups []pyMergeOneofGroup
		seenOneofs := map[string]bool{}
		for _, field := range msg.Fields {
			// Collect oneof groups — handled separately in template
			if field.IsOneOf {
				if !seenOneofs[field.OneOfName] {
					seenOneofs[field.OneOfName] = true
					oneofGroups = append(oneofGroups, pyMergeOneofGroup{
						OneofName: field.OneOfName,
					})
				}
				continue
			}

			pyFieldName := strutil.ToSnakeCase(field.Name)
			mf := pyMergeField{
				FieldName:      pyFieldName,
				ProtoFieldName: field.Name,
			}

			switch {
			case field.IsMap:
				mf.Kind = "map"
				if field.MapValueIsMessage && field.MapValueTypePackage == "" && hasFields[field.MapValueType] {
					mf.MapValueSamePkg = true
					mf.MapValueMergeFn = "merge_" + strutil.ToSnakeCase(field.MapValueType)
				}
			case field.IsRepeated:
				mf.Kind = "repeated"
			case field.IsMessage:
				mf.Kind = "message"
				if field.TypePackage == "" {
					mf.SamePkg = true
					mf.MergeFn = "merge_" + strutil.ToSnakeCase(field.Type)
				}
			case field.IsEnum:
				mf.Kind = "enum"
			case field.Type == "string":
				mf.Kind = "string"
			case field.Type == "bool":
				mf.Kind = "bool"
			case field.Type == "bytes":
				mf.Kind = "bytes"
			case isIntegerType(field.Type):
				mf.Kind = "int"
			case isFloatType(field.Type):
				mf.Kind = "float"
			default:
				continue
			}

			fields = append(fields, mf)
		}

		if len(fields) == 0 {
			continue
		}

		messages = append(messages, pyMergeMessage{
			TypeName:      msg.Name,
			FuncName:      "merge_" + strutil.ToSnakeCase(msg.Name),
			QualifiedType: fmt.Sprintf("%s_pb2.%s", protoFilename, msg.Name),
			Fields:        fields,
			OneofGroups:   oneofGroups,
		})
	}

	if len(messages) == 0 {
		return nil
	}

	data := pyMergeData{
		Messages: messages,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "merge.py.tmpl", pyFieldFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute merge template: %w", err)
	}

	b.Write(result)
	return nil
}
