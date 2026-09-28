// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// convertParamKeyToRustVariant converts a SCREAMING_SNAKE_CASE param_key to PascalCase Rust enum variant
// Example: "TOKEN_PARAM_KEY_CLIENT_ID" -> "ClientId"
// Example: "TOKEN_PARAM_KEY_STATE" -> "State"
func convertParamKeyToRustVariant(paramKey, packageName string) string {
	// Remove package prefix (e.g., "TOKEN_")
	packagePrefix := strings.ToUpper(packageName) + "_"
	paramKey = strings.TrimPrefix(paramKey, packagePrefix)

	// Remove common prefixes like "PARAM_KEY_"
	paramKey = strings.TrimPrefix(paramKey, "PARAM_KEY_")

	// Convert SCREAMING_SNAKE_CASE to PascalCase
	words := strings.Split(paramKey, "_")
	var result strings.Builder
	for _, word := range words {
		if word == "" {
			continue
		}
		// Capitalize first letter, lowercase the rest
		result.WriteString(strings.ToUpper(word[:1]))
		if len(word) > 1 {
			result.WriteString(strings.ToLower(word[1:]))
		}
	}
	return result.String()
}

func normalizeRustParamAlias(value string) string {
	return strings.ToUpper(strings.ReplaceAll(value, "-", "_"))
}

func rustParamKeyAliases(enumVal model.EnumValue, packageName string) []string {
	seen := make(map[string]bool)
	var aliases []string
	add := func(value string) {
		if value == "" {
			return
		}
		normalized := normalizeRustParamAlias(value)
		if !seen[normalized] {
			seen[normalized] = true
			aliases = append(aliases, normalized)
		}
	}

	add(enumVal.Name)
	short := strings.TrimPrefix(enumVal.Name, strings.ToUpper(packageName)+"_")
	short = strings.TrimPrefix(short, "PARAM_KEY_")
	add(short)
	add(enumVal.JsonValue)
	return aliases
}

func rustParseType(field model.Field) string {
	if field.IsEnum {
		return "i32"
	}
	switch field.Type {
	case "int32", "sint32", "sfixed32":
		return "i32"
	case "uint32", "fixed32":
		return "u32"
	case "int64", "sint64", "sfixed64":
		return "i64"
	case "uint64", "fixed64":
		return "u64"
	case "float":
		return "f32"
	case "double":
		return "f64"
	case "bool":
		return "bool"
	default:
		return ""
	}
}

func writeRustFieldAssignment(b *strings.Builder, field model.Field, valueExpr string) {
	fieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
	if field.IsOptional {
		fmt.Fprintf(b, "result.%s = Some(%s);\n", fieldName, valueExpr)
	} else {
		fmt.Fprintf(b, "result.%s = %s;\n", fieldName, valueExpr)
	}
}

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

// writeFromMap writes from_map methods to the shared builder for messages with ParamMapConfig
func writeFromMap(b *strings.Builder, pf model.ProtoFile) error {
	b.WriteString("// ---- FromMap / ToMap ----\n")

	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			if err := generateFromMapMethod(b, msg, pf); err != nil {
				return err
			}
		}
	}
	return nil
}

func generateFromMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
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

	// Generate function documentation
	fmt.Fprintf(b, "/// Creates a %s from a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(b, "/// The parameter keys are validated against %s enum values.\n", keyEnumType)
	if config.Strict {
		b.WriteString("/// Unknown parameter keys will return an error (strict mode).\n")
	} else {
		b.WriteString("/// Unknown parameter keys are silently ignored (non-strict mode).\n")
	}
	fmt.Fprintf(b, "pub fn %s_from_map(params: &HashMap<String, String>) -> Result<%s, String> {\n", strutil.ToSnakeCase(msg.Name), msg.Name)
	fmt.Fprintf(b, "let mut result = %s::default();\n", msg.Name)

	b.WriteString("for (key, value) in params {\n")
	b.WriteString("let normalized_key = key.to_uppercase().replace('-', \"_\");\n")
	fmt.Fprintf(b, "let param_key = match normalized_key.as_str() {\n")
	for _, enumVal := range keyEnum.Values {
		variant := convertParamKeyToRustVariant(enumVal.Name, pf.Package)
		aliases := rustParamKeyAliases(enumVal, pf.Package)
		if len(aliases) == 0 {
			continue
		}
		var quoted []string
		for _, alias := range aliases {
			quoted = append(quoted, rustQuote(alias))
		}
		fmt.Fprintf(b, "%s => Some(%s::%s),\n", strings.Join(quoted, " | "), keyEnumType, variant)
	}
	b.WriteString("_ => None,\n")
	b.WriteString("};\n")
	b.WriteString("let param_key = match param_key {\n")
	b.WriteString("Some(k) => k,\n")
	b.WriteString("None => {\n")
	if config.Strict {
		b.WriteString("return Err(format!(\"Invalid parameter key: {}\", key));\n")
	} else {
		b.WriteString("continue; // Ignore unknown keys in non-strict mode\n")
	}
	b.WriteString("}\n")
	b.WriteString("};\n")

	b.WriteString("match param_key {\n")

	// Generate cases for each field with param_key annotation
	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue // Skip fields without param_key annotation
		}

		// Convert SCREAMING_SNAKE_CASE param_key to PascalCase enum variant
		// Example: "TOKEN_PARAM_KEY_CLIENT_ID" -> "ClientId"
		rustEnumValue := convertParamKeyToRustVariant(field.ParamKey, pf.Package)
		fmt.Fprintf(b, "%s::%s => {\n", keyEnumType, rustEnumValue)

		if field.IsRepeated {
			fieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
			if parseType := rustParseType(field); parseType != "" {
				fmt.Fprintf(b, "result.%s = value.split(',').filter_map(|s| s.parse::<%s>().ok()).collect();\n", fieldName, parseType)
			} else {
				fmt.Fprintf(b, "result.%s = value.split(',').map(|s| s.to_string()).collect();\n", fieldName)
			}
		} else if parseType := rustParseType(field); parseType != "" {
			fmt.Fprintf(b, "if let Ok(val) = value.parse::<%s>() {\n", parseType)
			writeRustFieldAssignment(b, field, "val")
			b.WriteString("}\n")
		} else if field.Type == "bytes" {
			writeRustFieldAssignment(b, field, "value.as_bytes().to_vec()")
		} else {
			writeRustFieldAssignment(b, field, "value.clone()")
		}
		b.WriteString("}\n")
	}

	b.WriteString("_ => {} // Unknown enum value\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("Ok(result)\n")
	b.WriteString("}\n")

	// Generate ToMap method
	if err := generateToMapMethod(b, msg, pf); err != nil {
		return err
	}

	return nil
}

func generateToMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
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
	fmt.Fprintf(b, "/// Converts a %s to a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(b, "/// The map keys are the json_return_value of %s enum values.\n", keyEnumType)
	b.WriteString("/// Only non-empty fields are included in the map.\n")
	fmt.Fprintf(b, "pub fn %s_to_map(m: &%s) -> HashMap<String, String> {\n", strutil.ToSnakeCase(msg.Name), msg.Name)
	b.WriteString("let mut result = HashMap::new();\n")

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
		fieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
		if field.IsRepeated {
			// For repeated fields, join with comma
			fmt.Fprintf(b, "if !m.%s.is_empty() {\n", fieldName)
			if rustParseType(field) != "" {
				fmt.Fprintf(b, "result.insert(%s.to_string(), m.%s.iter().map(|v| v.to_string()).collect::<Vec<_>>().join(\",\"));\n", rustQuote(jsonKey), fieldName)
			} else {
				fmt.Fprintf(b, "result.insert(%s.to_string(), m.%s.join(\",\"));\n", rustQuote(jsonKey), fieldName)
			}
			b.WriteString("}\n")
		} else if field.IsEnum || isNumericType(field.Type) {
			// For enum and numeric fields, convert to string
			if field.IsOptional {
				fmt.Fprintf(b, "if let Some(ref val) = m.%s {\n", fieldName)
				fmt.Fprintf(b, "result.insert(%s.to_string(), val.to_string());\n", rustQuote(jsonKey))
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if m.%s != Default::default() {\n", fieldName)
				fmt.Fprintf(b, "result.insert(%s.to_string(), m.%s.to_string());\n", rustQuote(jsonKey), fieldName)
				b.WriteString("}\n")
			}
		} else if field.Type == "bytes" {
			if field.IsOptional {
				fmt.Fprintf(b, "if let Some(ref val) = m.%s {\n", fieldName)
				fmt.Fprintf(b, "if !val.is_empty() {\n")
				fmt.Fprintf(b, "result.insert(%s.to_string(), String::from_utf8_lossy(val).to_string());\n", rustQuote(jsonKey))
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !m.%s.is_empty() {\n", fieldName)
				fmt.Fprintf(b, "result.insert(%s.to_string(), String::from_utf8_lossy(&m.%s).to_string());\n", rustQuote(jsonKey), fieldName)
				b.WriteString("}\n")
			}
		} else {
			// For string fields
			if field.IsOptional {
				fmt.Fprintf(b, "if let Some(ref val) = m.%s {\n", fieldName)
				fmt.Fprintf(b, "if !val.is_empty() {\n")
				fmt.Fprintf(b, "result.insert(%s.to_string(), val.clone());\n", rustQuote(jsonKey))
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !m.%s.is_empty() {\n", fieldName)
				fmt.Fprintf(b, "result.insert(%s.to_string(), m.%s.clone());\n", rustQuote(jsonKey), fieldName)
				b.WriteString("}\n")
			}
		}
	}

	b.WriteString("result\n")
	b.WriteString("}\n")

	return nil
}
