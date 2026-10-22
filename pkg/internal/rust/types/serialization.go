// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// rustEnumFieldInfo describes an enum field for serialization normalization.
type rustEnumFieldInfo struct {
	FieldName    string // proto/json field name (e.g., "token_purpose")
	CamelName    string // the key pbjson writes: lowerCamelCase (e.g., "tokenPurpose"), or the (spec.json_key)
	EnumTypeName string // Rust enum type, possibly with crate path (e.g., "Level" or "crate::sys::logger::logger::Level")
	IsRepeated   bool
}

// rustMessageFieldInfo describes a sub-message field that needs recursive enum normalization.
type rustMessageFieldInfo struct {
	FieldName   string // proto/json field name (e.g., "logger")
	CamelName   string // the key pbjson writes: lowerCamelCase (e.g., "logger"), or the (spec.json_key)
	NormalizeFn string // e.g., "normalize_logger_config_enums"
	StringifyFn string // e.g., "stringify_logger_config_enums"
	IsRepeated  bool
	CratePath   string // fully qualified crate path for cross-package (e.g., "crate::sys::logger::logger"), empty for same-package
}

// rustJsonAlias is one (spec.json_aliases) key, read as the field's own key.
type rustJsonAlias struct {
	Alias     string
	Canonical string // the field's json_key, or its proto name (pbjson reads both)
}

// rustSerializationData holds pre-computed data for the serialization template.
type rustSerializationData struct {
	TypeName           string
	SnakeName          string
	NeedsNormalization bool // true if there are enum, alias or message fields needing normalization
	EnumFields         []rustEnumFieldInfo
	MessageFields      []rustMessageFieldInfo
	// Aliases are renamed to their field's key before deserialization, as Go's
	// FromJSON does (an alias never overrides the canonical key).
	Aliases []rustJsonAlias
}

// rustJsonKey is the key pbjson writes a field under: its (spec.json_key),
// which injectJsonNameForJsonKey made its json_name, or lowerCamelCase.
func rustJsonKey(field model.Field) string {
	if field.JsonKey != "" {
		return field.JsonKey
	}
	return strutil.ToLowerCamelCase(field.Name)
}

// rustJsonAliases lists msg's (spec.json_aliases) keys with the key each is
// read as.
func rustJsonAliases(msg model.Message) []rustJsonAlias {
	var aliases []rustJsonAlias
	for _, field := range msg.Fields {
		canonical := field.Name
		if field.JsonKey != "" {
			canonical = field.JsonKey
		}
		for _, alias := range field.JsonAliases {
			aliases = append(aliases, rustJsonAlias{Alias: alias, Canonical: canonical})
		}
	}
	return aliases
}

// writeSerialization writes serialization functions to the shared builder.
// allProtos is the full set of parsed proto files, needed to resolve cross-package
// message fields when determining if they contain enum fields.
func writeSerialization(b *strings.Builder, pf model.ProtoFile, allProtos []model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	// Build a lookup of package -> proto file for cross-package resolution
	protoByPkg := make(map[string]model.ProtoFile)
	for _, p := range allProtos {
		protoByPkg[p.Package] = p
	}

	b.WriteString("// ---- Serialization ----\n\n")

	// Shared utility: normalize YAML floats that represent whole numbers to integers.
	// Go's yaml.v3 serializes int64 values through map[string]any as float64,
	// producing scientific notation (e.g., 1.773884836e+09). Rust's serde cannot
	// deserialize these floats into i64 fields. This function recursively walks
	// the JSON Value tree and converts whole-number floats to i64.
	b.WriteString(`/// Recursively converts JSON float values that represent whole numbers to integers.
/// This handles Go's yaml.v3 serializing int64 as float64 scientific notation.
fn normalize_yaml_numbers(v: &mut serde_json::Value) {
    match v {
        serde_json::Value::Number(n) => {
            if let Some(f) = n.as_f64() {
                if f.fract() == 0.0 && f >= (i64::MIN as f64) && f <= (i64::MAX as f64) {
                    *v = serde_json::Value::Number(serde_json::Number::from(f as i64));
                }
            }
        }
        serde_json::Value::Object(map) => {
            for val in map.values_mut() {
                normalize_yaml_numbers(val);
            }
        }
        serde_json::Value::Array(arr) => {
            for item in arr.iter_mut() {
                normalize_yaml_numbers(item);
            }
        }
        _ => {}
    }
}
`)

	// Generate serialization functions for each message
	for _, msg := range pf.Messages {
		if err := generateMessageSerialization(b, msg, pf, protoByPkg); err != nil {
			fmt.Fprintf(b, "// ERROR generating serialization for %s: %s\n", msg.Name, err)
		}
	}
}

// collectEnumInfo walks a message's fields to find enum and message fields
// that need normalization. For message fields, it recursively checks if the
// sub-message has any enum or (spec.json_aliases) field (at any depth).
func collectEnumInfo(msg model.Message, currentPkg string, protoByPkg map[string]model.ProtoFile, visited map[string]bool) ([]rustEnumFieldInfo, []rustMessageFieldInfo) {
	var enumFields []rustEnumFieldInfo
	var messageFields []rustMessageFieldInfo

	for _, field := range msg.Fields {
		if field.IsEnum {
			// Skip enums from the spec package — spec helpers are not generated
			// (spec has a module conflict) and spec enums don't appear in config files
			enumPkg := field.TypePackage
			if enumPkg == "" {
				enumPkg = currentPkg
			}
			if enumPkg == "spec" {
				continue
			}

			enumTypeName := toProstEnumName(field.Type)
			// Qualify cross-package enum types with crate path
			if field.TypePackage != "" {
				pf, ok := protoByPkg[field.TypePackage]
				if ok {
					parentDir := filepath.Dir(pf.RelPath)
					enumTypeName = fmt.Sprintf("%s::%s::%s",
						"crate", strings.ReplaceAll(parentDir, "/", "::"),
						field.TypePackage+"::"+enumTypeName)
				}
			}
			camelName := rustJsonKey(field)
			enumFields = append(enumFields, rustEnumFieldInfo{
				FieldName:    field.Name,
				CamelName:    camelName,
				EnumTypeName: enumTypeName,
				IsRepeated:   field.IsRepeated,
			})
		} else if field.IsMessage && !field.IsMap {
			// Check if the sub-message has enum fields (needs normalization)
			pkg := field.TypePackage
			if pkg == "" {
				pkg = currentPkg
			}

			// Avoid infinite recursion on self-referencing messages
			key := pkg + "." + field.Type
			if visited[key] {
				continue
			}
			visited[key] = true

			subMsg := findMessage(pkg, field.Type, protoByPkg)
			if subMsg == nil {
				continue
			}

			subEnums, subMsgs := collectEnumInfo(*subMsg, pkg, protoByPkg, visited)
			if len(subEnums) > 0 || len(subMsgs) > 0 || len(rustJsonAliases(*subMsg)) > 0 {
				subTypeName := toProstTypeName(subMsg.Name)
				subSnake := strutil.ToSnakeCase(subTypeName)

				// Determine crate path for cross-package sub-messages
				cratePath := ""
				if field.TypePackage != "" {
					pf, ok := protoByPkg[field.TypePackage]
					if ok {
						parentDir := filepath.Dir(pf.RelPath)
						cratePath = fmt.Sprintf("crate::%s::%s", strings.ReplaceAll(parentDir, "/", "::"), field.TypePackage)
					}
				}

				camelName := rustJsonKey(field)
				messageFields = append(messageFields, rustMessageFieldInfo{
					FieldName:   field.Name,
					CamelName:   camelName,
					NormalizeFn: "normalize_" + subSnake + "_enums",
					StringifyFn: "stringify_" + subSnake + "_enums",
					IsRepeated:  field.IsRepeated,
					CratePath:   cratePath,
				})
			}
		}
	}

	return enumFields, messageFields
}

// findMessage looks up a message by package name and type name across all parsed protos.
func findMessage(pkg, typeName string, protoByPkg map[string]model.ProtoFile) *model.Message {
	pf, ok := protoByPkg[pkg]
	if !ok {
		return nil
	}
	for i := range pf.Messages {
		if pf.Messages[i].Name == typeName {
			return &pf.Messages[i]
		}
	}
	return nil
}

func generateMessageSerialization(b *strings.Builder, msg model.Message, currentPf model.ProtoFile, protoByPkg map[string]model.ProtoFile) error {
	typeName := toProstTypeName(msg.Name)

	// Collect enum field info for normalization
	visited := map[string]bool{currentPf.Package + "." + msg.Name: true}
	enumFields, messageFields := collectEnumInfo(msg, currentPf.Package, protoByPkg, visited)
	aliases := rustJsonAliases(msg)

	data := rustSerializationData{
		TypeName:           typeName,
		SnakeName:          strutil.ToSnakeCase(typeName),
		NeedsNormalization: len(enumFields) > 0 || len(messageFields) > 0 || len(aliases) > 0,
		EnumFields:         enumFields,
		MessageFields:      messageFields,
		Aliases:            aliases,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "serialization.rs.tmpl", funcMap, data)
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

// Note: strutil.ToSnakeCase is defined in stateclient_gen.go

// toProstTypeName converts proto message names to Rust/prost naming conventions.
// Prost uses heck 0.5's to_upper_camel_case. The algorithm tracks a word "mode"
// (Boundary/Lowercase/Uppercase) where digits are transparent (they preserve the
// previous mode). Word boundaries fire on:
//
//  1. mode==Lowercase && next is uppercase  (e.g. "eS" in "ServiceS2S")
//  2. mode==Uppercase && c is uppercase && next is lowercase  (e.g. "An" in "SAnnotation")
//
// Each word is then title-cased (first char upper, rest lower; digits unchanged).
//
// Examples: MTLSConfig → MtlsConfig, HTTPSConfig → HttpsConfig,
//
//	ServiceS2SAnnotation → ServiceS2sAnnotation, X509Cert → X509Cert
func toProstTypeName(protoName string) string {
	runes := []rune(protoName)
	n := len(runes)
	if n == 0 {
		return ""
	}

	const (
		modeBoundary  = 0
		modeLowercase = 1
		modeUppercase = 2
	)

	// Collect word boundaries, then capitalize each word.
	var words []string
	mode := modeBoundary
	init := 0

	for i := 0; i < n; i++ {
		c := runes[i]

		if i+1 < n {
			next := runes[i+1]

			// Compute what mode becomes after this character.
			// Digits are transparent — they keep the previous mode.
			nextMode := mode
			if c >= 'a' && c <= 'z' {
				nextMode = modeLowercase
			} else if c >= 'A' && c <= 'Z' {
				nextMode = modeUppercase
			}

			// Rule 1: mode is lowercase (possibly through digits) and next is uppercase
			if nextMode == modeLowercase && next >= 'A' && next <= 'Z' {
				words = append(words, string(runes[init:i+1]))
				init = i + 1
				mode = modeBoundary
				continue
			}

			// Rule 2: mode is uppercase, current char IS uppercase, next is lowercase
			if mode == modeUppercase && c >= 'A' && c <= 'Z' && next >= 'a' && next <= 'z' {
				if init < i {
					words = append(words, string(runes[init:i]))
				}
				init = i
				mode = modeBoundary
				continue
			}

			mode = nextMode
		}
	}

	// Trailing word
	if init < n {
		words = append(words, string(runes[init:]))
	}

	// Title-case each word: first letter uppercase, rest lowercase.
	// Digits pass through unchanged.
	var result strings.Builder
	for _, word := range words {
		first := true
		for _, r := range word {
			if first && r >= 'a' && r <= 'z' {
				result.WriteRune(r - 'a' + 'A')
				first = false
			} else if !first && r >= 'A' && r <= 'Z' {
				result.WriteRune(r - 'A' + 'a')
			} else {
				if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
					first = false
				}
				result.WriteRune(r)
			}
		}
	}

	return result.String()
}

// prostSnakeName returns the snake_case identifier that prost/heck derives for a
// proto type or method name. It first normalizes through toProstTypeName (the
// heck UpperCamelCase replica that produces the actual prost struct names), then
// snakes the result. This matches exactly how serialization helpers are defined
// (ToSnakeCase(toProstTypeName(...))), so references built with it always line up
// with prost-generated structs/fns and tonic-generated client methods. For names
// that are already prost-stable it is identical to ToSnakeCase(name); it only
// diverges for digit-acronym sequences like "P2S" (→ "p2s", not "p2_s").
func prostSnakeName(name string) string {
	return strutil.ToSnakeCase(toProstTypeName(name))
}

// writeSerializationTestContent writes serialization test functions into the combined test module (no #[cfg(test)] wrapper)
func writeSerializationTestContent(b *strings.Builder, msg model.Message) {
	generateSerializationTestsForMessage(b, msg)
}

// generateSerializationTestsForMessage generates serialization tests for a single message
func generateSerializationTestsForMessage(b *strings.Builder, msg model.Message) {
	typeName := toProstTypeName(msg.Name)

	data := rustSerializationData{
		TypeName:  typeName,
		SnakeName: strutil.ToSnakeCase(typeName),
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "serialization_test.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing serialization test template: %s\n", err)
		return
	}

	b.Write(result)
}
