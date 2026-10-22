// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// rustKeywords is the set of Rust reserved keywords that must be escaped with r# prefix.
var rustKeywords = map[string]bool{
	"as": true, "break": true, "const": true, "continue": true, "crate": true,
	"else": true, "enum": true, "extern": true, "false": true, "fn": true,
	"for": true, "if": true, "impl": true, "in": true, "let": true,
	"loop": true, "match": true, "mod": true, "move": true, "mut": true,
	"pub": true, "ref": true, "return": true, "self": true, "static": true,
	"struct": true, "super": true, "trait": true, "true": true, "type": true,
	"unsafe": true, "use": true, "where": true, "while": true,
	"async": true, "await": true, "dyn": true,
	"abstract": true, "become": true, "box": true, "do": true, "final": true,
	"macro": true, "override": true, "priv": true, "try": true, "typeof": true,
	"unsized": true, "virtual": true, "yield": true,
}

// escapeRustKeyword prefixes Rust reserved keywords with r# so they can be used as identifiers.
func escapeRustKeyword(name string) string {
	if rustKeywords[name] {
		return "r#" + name
	}
	return name
}

// rustConstantData holds data for a single constant in the template.
type rustConstantData struct {
	Name      string
	RustType  string
	RustValue string
}

// rustConstantsTemplateData holds data for the constants template.
type rustConstantsTemplateData struct {
	FileConstants []rustConstantData
	FieldDefaults []rustConstantData
}

// rustDefaultEntry holds data for a single field default in the defaults template.
type rustDefaultEntry struct {
	FieldName  string
	ConstName  string
	Kind       string // "string", "int", "float", "bool", "host"
	NeedsCast  bool
	CastType   string
	HostHost   string // For "host" kind: the default hostname
	HostPort   int32  // For "host" kind: the default port
	IsOptional bool   // true for proto3 optional fields (prost generates Option<T>)
}

// rustSubMsgCall holds data for a sub-message defaults call in Rust.
type rustSubMsgCall struct {
	FieldName   string // snake_case field name
	FuncName    string // function name prefix (before "_defaults")
	InitIfNil   bool   // true for assert_required fields — initialize with Default if None
	HasDefaults bool   // true if the sub-message has its own defaults function
}

// rustDefaultsTemplateData holds data for the defaults template.
type rustDefaultsTemplateData struct {
	TypeName    string
	FnName      string
	Defaults    []rustDefaultEntry
	SubMsgCalls []rustSubMsgCall
}

// writeConstants writes constant definitions to the shared builder
func writeConstants(b *strings.Builder, pf model.ProtoFile) {
	b.WriteString("// ---- Constants ----\n")

	// Build file-level constants
	var fileConsts []rustConstantData
	for _, c := range pf.Constants {
		rustName := c.Name
		rustType := c.Type
		if rustType == "" {
			rustType = inferRustTypeFromValue(c.Value)
		}
		rustType = mapTypeToRust(rustType)
		rustValue := convertValueToRust(c.Value)

		fileConsts = append(fileConsts, rustConstantData{
			Name:      rustName,
			RustType:  rustType,
			RustValue: rustValue,
		})
	}

	// Build field-level default constants
	var fieldDefaults []rustConstantData
	for _, dc := range collectFieldDefaultConstants(pf) {
		fieldDefaults = append(fieldDefaults, rustConstantData{
			Name:      dc.Name,
			RustType:  dc.RustType,
			RustValue: dc.RustValue,
		})
	}

	data := rustConstantsTemplateData{
		FileConstants: fileConsts,
		FieldDefaults: fieldDefaults,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "constants.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing constants template: %s\n", err)
		return
	}

	b.Write(result)
	b.WriteString("\n")
}

// fieldDefaultConstant holds a derived constant from a field default annotation.
type fieldDefaultConstant struct {
	Name      string
	RustType  string
	RustValue string
}

// collectFieldDefaultConstants iterates over all message fields and extracts
// default_string, default_int, default_float, and default_bool annotations,
// returning them as Rust constants in SCREAMING_SNAKE_CASE.
func collectFieldDefaultConstants(pf model.ProtoFile) []fieldDefaultConstant {
	dupFields := findDuplicateDefaultFieldsRust(pf.Messages)
	var consts []fieldDefaultConstant
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				ann, ok := field.Annotations[key]
				if !ok {
					continue
				}
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				var rustType, rustValue string
				switch ann.Type {
				case model.AnnotationTypeString:
					rustType = "&str"
					rustValue = rustQuote(ann.StringValue)
				case model.AnnotationTypeInt:
					rustType = "i64"
					rustValue = fmt.Sprintf("%d", ann.IntValue)
				case model.AnnotationTypeFloat:
					rustType = "f64"
					rustValue = formatRustFloat(ann.FloatValue)
				case model.AnnotationTypeBool:
					rustType = "bool"
					rustValue = fmt.Sprintf("%t", ann.BoolValue)
				default:
					continue
				}
				consts = append(consts, fieldDefaultConstant{
					Name:      constName,
					RustType:  rustType,
					RustValue: rustValue,
				})
			}
			// default_host: generate a DEFAULT_{FIELD}_PORT constant
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields) + "_PORT"
				consts = append(consts, fieldDefaultConstant{
					Name:      constName,
					RustType:  "i32",
					RustValue: fmt.Sprintf("%d", av.HostValue.Port),
				})
			}
		}
	}
	return consts
}

// findDuplicateDefaultFieldsRust wraps strutil.FindDuplicateDefaultFields for Rust.
func findDuplicateDefaultFieldsRust(messages []model.Message) map[string]bool {
	defaultKeys := []string{"default_string", "default_int", "default_float", "default_bool", "default_host"}
	var entries []struct {
		MsgName    string
		FieldNames []string
	}
	for _, msg := range messages {
		var names []string
		for _, field := range msg.Fields {
			for _, key := range defaultKeys {
				if _, ok := field.Annotations[key]; ok {
					names = append(names, field.Name)
					break
				}
			}
		}
		if len(names) > 0 {
			entries = append(entries, struct {
				MsgName    string
				FieldNames []string
			}{msg.Name, names})
		}
	}
	return strutil.FindDuplicateDefaultFields(entries)
}

// hasFieldDefaults returns true if any message field in pf carries a
// default_string, default_int, default_float, or default_bool annotation.
func hasFieldDefaults(pf model.ProtoFile) bool {
	return len(messagesWithDefaults(pf)) > 0
}

// formatRustFloat formats a float64 value for Rust, ensuring it always has a
// decimal point (e.g., "3.0" instead of "3" for whole numbers).
// Contains checks on the formatted string itself (not proto data).
func formatRustFloat(f float64) string {
	s := fmt.Sprintf("%g", f)
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}

// inferRustTypeFromValue infers a Rust-compatible type name from a constant's value.
func inferRustTypeFromValue(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return "string"
	}
	if value == "true" || value == "false" {
		return "bool"
	}
	if strings.Contains(value, ".") && !strings.Contains(value, "time.") {
		return "float64"
	}
	return "int64"
}

// mapTypeToRust converts Go types to Rust types.
// Uses Contains on Go type strings (e.g. "time.Duration") because these are
// Go code representations from the constant generator, not proto descriptors.
func mapTypeToRust(goType string) string {
	switch {
	case goType == "string":
		return "&str"
	case goType == "bool":
		return "bool"
	case goType == "float64":
		return "f64"
	case goType == "float32":
		return "f32"
	case goType == "int", goType == "int64":
		return "i64"
	case goType == "int32":
		return "i32"
	case goType == "uint64":
		return "u64"
	case goType == "uint32":
		return "u32"
	case strings.Contains(goType, "Duration"):
		return "Duration"
	default:
		return "i64"
	}
}

// convertValueToRust converts Go value syntax to Rust syntax.
// Uses Contains to detect Go duration literals for cross-language conversion.
func convertValueToRust(value string) string {
	// Handle Duration
	if strings.Contains(value, "time.Second") ||
		strings.Contains(value, "time.Minute") ||
		strings.Contains(value, "time.Hour") {
		// Convert Go duration to Rust Duration
		value = strings.ReplaceAll(value, "time.", "")
		value = strings.ReplaceAll(value, "Second", "_SECOND")
		value = strings.ReplaceAll(value, "Minute", "_MINUTE")
		value = strings.ReplaceAll(value, "Hour", "_HOUR")
		value = strings.ReplaceAll(value, "*", "* ")

		// Parse the numeric part and unit
		if strings.Contains(value, "_SECOND") {
			numPart := strings.TrimSpace(strings.Split(value, "_SECOND")[0])
			numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
			return fmt.Sprintf("Duration::from_secs(%s)", numPart)
		} else if strings.Contains(value, "_MINUTE") {
			numPart := strings.TrimSpace(strings.Split(value, "_MINUTE")[0])
			numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
			return fmt.Sprintf("Duration::from_secs(%s * 60)", numPart)
		} else if strings.Contains(value, "_HOUR") {
			numPart := strings.TrimSpace(strings.Split(value, "_HOUR")[0])
			numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
			return fmt.Sprintf("Duration::from_secs(%s * 3600)", numPart)
		}
	}

	return value
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
// Messages without defaults get an empty function so callers can unconditionally call them.
func writeDefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsRust(pf.Messages)

	b.WriteString("// ---- Type Defaults ----\n")

	for _, msg := range pf.Messages {
		// Direct field defaults
		var defaults []rustDefaultEntry
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				ann, ok := field.Annotations[key]
				if !ok {
					continue
				}
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

				entry := rustDefaultEntry{
					FieldName:  rustFieldName,
					ConstName:  constName,
					IsOptional: field.IsOptional,
				}

				switch ann.Type {
				case model.AnnotationTypeString:
					entry.Kind = "string"
				case model.AnnotationTypeInt:
					entry.Kind = "int"
					rustFieldType := protoTypeToRust(field.Type)
					if rustFieldType != "i64" {
						entry.NeedsCast = true
						entry.CastType = rustFieldType
					}
				case model.AnnotationTypeFloat:
					entry.Kind = "float"
					rustFieldType := protoTypeToRust(field.Type)
					if rustFieldType != "f64" {
						entry.NeedsCast = true
						entry.CastType = rustFieldType
					}
				case model.AnnotationTypeBool:
					entry.Kind = "bool"
				default:
					continue
				}

				defaults = append(defaults, entry)
			}
			// default_host: initialize host.Host fields with default values
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
				defaults = append(defaults, rustDefaultEntry{
					FieldName:  rustFieldName,
					Kind:       "host",
					IsOptional: field.IsOptional,
					HostHost:   av.HostValue.Host,
					HostPort:   av.HostValue.Port,
				})
			}
		}

		// Sub-message defaults (including assert_required initialization)
		var subMsgCalls []rustSubMsgCall
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}

			_, isRequired := field.Annotations["assert_required"]
			transitive := hasDefaults[field.Type]

			if !isRequired && !transitive {
				continue
			}

			typeName := toProstTypeName(field.Type)
			subMsgCalls = append(subMsgCalls, rustSubMsgCall{
				FieldName:   escapeRustKeyword(strutil.ToSnakeCase(field.Name)),
				FuncName:    strutil.ToSnakeCase(typeName),
				InitIfNil:   isRequired,
				HasDefaults: transitive,
			})
		}

		typeName := toProstTypeName(msg.Name)
		data := rustDefaultsTemplateData{
			TypeName:    typeName,
			FnName:      strutil.ToSnakeCase(typeName),
			Defaults:    defaults,
			SubMsgCalls: subMsgCalls,
		}

		typeFS, err := fs.Sub(templates.FS, "rust/types")
		if err != nil {
			fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
			continue
		}

		funcMap := tmplutil.BaseFuncMap()
		result, err := tmplutil.ExecuteTemplate(typeFS, "defaults.rs.tmpl", funcMap, data)
		if err != nil {
			fmt.Fprintf(b, "// ERROR executing defaults template: %s\n", err)
			continue
		}

		b.Write(result)
	}
}

// writeUndefaults generates the inverse of writeDefaults for every message: resets fields that
// match their default values back to zero so only user overrides are persisted.
// Messages without defaults get an empty function.
func writeUndefaults(b *strings.Builder, pf model.ProtoFile) {
	hasDefaults := messagesWithDefaults(pf)
	dupFields := findDuplicateDefaultFieldsRust(pf.Messages)

	b.WriteString("// ---- Type Undefaults ----\n")

	for _, msg := range pf.Messages {
		var defaults []rustDefaultEntry
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				ann, ok := field.Annotations[key]
				if !ok {
					continue
				}
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

				entry := rustDefaultEntry{
					FieldName:  rustFieldName,
					ConstName:  constName,
					IsOptional: field.IsOptional,
				}

				switch ann.Type {
				case model.AnnotationTypeString:
					entry.Kind = "string"
				case model.AnnotationTypeInt:
					entry.Kind = "int"
					rustFieldType := protoTypeToRust(field.Type)
					if rustFieldType != "i64" {
						entry.NeedsCast = true
						entry.CastType = rustFieldType
					}
				case model.AnnotationTypeFloat:
					entry.Kind = "float"
					rustFieldType := protoTypeToRust(field.Type)
					if rustFieldType != "f64" {
						entry.NeedsCast = true
						entry.CastType = rustFieldType
					}
				case model.AnnotationTypeBool:
					entry.Kind = "bool"
				default:
					continue
				}

				defaults = append(defaults, entry)
			}
			// default_host: reset host.Host fields that match defaults
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
				defaults = append(defaults, rustDefaultEntry{
					FieldName:  rustFieldName,
					Kind:       "host",
					IsOptional: field.IsOptional,
					HostHost:   av.HostValue.Host,
					HostPort:   av.HostValue.Port,
				})
			}
		}

		var subMsgCalls []rustSubMsgCall
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsRepeated || field.IsMap || !field.IsMessage || field.TypePackage != "" {
				continue
			}
			if !hasDefaults[field.Type] {
				continue
			}
			typeName := toProstTypeName(field.Type)
			subMsgCalls = append(subMsgCalls, rustSubMsgCall{
				FieldName:   escapeRustKeyword(strutil.ToSnakeCase(field.Name)),
				FuncName:    strutil.ToSnakeCase(typeName),
				HasDefaults: true,
			})
		}

		typeName := toProstTypeName(msg.Name)
		data := rustDefaultsTemplateData{
			TypeName:    typeName,
			FnName:      strutil.ToSnakeCase(typeName),
			Defaults:    defaults,
			SubMsgCalls: subMsgCalls,
		}

		typeFS, err := fs.Sub(templates.FS, "rust/types")
		if err != nil {
			fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
			continue
		}

		funcMap := tmplutil.BaseFuncMap()
		result, err := tmplutil.ExecuteTemplate(typeFS, "undefaults.rs.tmpl", funcMap, data)
		if err != nil {
			fmt.Fprintf(b, "// ERROR executing undefaults template: %s\n", err)
			continue
		}

		b.Write(result)
	}
}

// fieldDefaultInfo holds information about a field default for writeDefaults
type fieldDefaultInfo struct {
	FieldName string
	ConstName string
	AnnType   model.AnnotationType
	FieldType string // proto field type (e.g., "int32", "int64", "string")
}

// rustMergeField holds data for a single field in a merge function.
type rustMergeField struct {
	Kind            string // "string", "int", "float", "bool", "enum", "bytes", "message", "repeated", "map"
	FieldName       string // snake_case field name
	SamePkg         bool   // for message fields: can we recurse?
	MergeFn         string // for same-pkg messages: function name prefix (before "_merge")
	IsOptional      bool   // true for proto3 optional fields (prost generates Option<T>)
	MapValueSamePkg bool   // for map fields with message values: is value type same-package?
	MapValueMergeFn string // for map fields with same-pkg message values: merge function name prefix
}

// rustMergeOneofGroup holds data for a oneof group in a merge function.
type rustMergeOneofGroup struct {
	FieldName string // snake_case oneof field name (same as the proto oneof name)
}

// rustMergeMessage holds data for a single message's merge function.
type rustMergeMessage struct {
	TypeName    string
	FnName      string
	Fields      []rustMergeField
	OneofGroups []rustMergeOneofGroup
}

// writeMerge generates merge functions for every message in the proto file.
func writeMerge(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	b.WriteString("// ---- Merge ----\n")

	for _, msg := range pf.Messages {
		// Generate merge function even for messages with no fields,
		// because other messages may reference them via same-package merge calls.
		var fields []rustMergeField
		var oneofGroups []rustMergeOneofGroup
		seenOneofs := map[string]bool{}
		for _, field := range msg.Fields {
			// Collect oneof groups — handled separately in template
			if field.IsOneOf {
				if !seenOneofs[field.OneOfName] {
					seenOneofs[field.OneOfName] = true
					oneofGroups = append(oneofGroups, rustMergeOneofGroup{
						FieldName: escapeRustKeyword(strutil.ToSnakeCase(field.OneOfName)),
					})
				}
				continue
			}

			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
			mf := rustMergeField{
				FieldName:  rustFieldName,
				IsOptional: field.IsOptional,
			}

			switch {
			case field.IsMap:
				mf.Kind = "map"
				if field.MapValueIsMessage && field.MapValueTypePackage == "" {
					mf.MapValueSamePkg = true
					valTypeName := toProstTypeName(field.MapValueType)
					mf.MapValueMergeFn = strutil.ToSnakeCase(valTypeName)
				}
			case field.IsRepeated:
				mf.Kind = "repeated"
			case field.IsMessage:
				mf.Kind = "message"
				if field.TypePackage == "" {
					mf.SamePkg = true
					typeName := toProstTypeName(field.Type)
					mf.MergeFn = strutil.ToSnakeCase(typeName)
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

		// Generate merge function even for empty messages (no fields/oneofs)
		// because other messages may reference them via same-package merge calls.
		typeName := toProstTypeName(msg.Name)
		data := rustMergeMessage{
			TypeName:    typeName,
			FnName:      strutil.ToSnakeCase(typeName),
			Fields:      fields,
			OneofGroups: oneofGroups,
		}

		typeFS, err := fs.Sub(templates.FS, "rust/types")
		if err != nil {
			fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
			continue
		}

		funcMap := tmplutil.BaseFuncMap()
		result, err := tmplutil.ExecuteTemplate(typeFS, "merge.rs.tmpl", funcMap, data)
		if err != nil {
			fmt.Fprintf(b, "// ERROR executing merge template: %s\n", err)
			continue
		}

		b.Write(result)
	}
}

// protoTypeToRust converts a proto field type to the corresponding Rust type
func protoTypeToRust(protoType string) string {
	switch protoType {
	case "string":
		return "String"
	case "bool":
		return "bool"
	case "int32", "sint32", "sfixed32":
		return "i32"
	case "int64", "sint64", "sfixed64":
		return "i64"
	case "uint32", "fixed32":
		return "u32"
	case "uint64", "fixed64":
		return "u64"
	case "float":
		return "f32"
	case "double":
		return "f64"
	case "bytes":
		return "Vec<u8>"
	default:
		return "i64"
	}
}
