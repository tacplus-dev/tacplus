// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
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

// isEnumType checks if a field type is an enum type
func isEnumType(typ string, enums []model.Enum) bool {
	for _, enum := range enums {
		if enum.Name == typ {
			return true
		}
	}
	return false
}

// generateFromMap generates fromMap methods for messages with ParamMapConfig
func generateFromMap(pf model.ProtoFile, baseDir, subdir string) error {
	hasParamMapMessages := false
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			hasParamMapMessages = true
			break
		}
	}

	if !hasParamMapMessages {
		return nil
	}

	// For Java, we generate a separate helper class for each message
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			if err := generateFromMapClass(msg, pf, baseDir, subdir); err != nil {
				return err
			}
		}
	}

	return nil
}

func generateFromMapClass(msg model.Message, pf model.ProtoFile, baseDir, subdir string) error {
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

	// Build a map of enum value name -> json_return_value (needed for toMap)
	enumValueToJsonValue := make(map[string]string)
	for _, enumVal := range keyEnum.Values {
		if enumVal.JsonValue != "" {
			enumValueToJsonValue[enumVal.Name] = enumVal.JsonValue
		}
	}

	// Note: We rely on fields having explicit param_key annotations that map to enum values

	// Determine package name from subdir
	packageName := strings.ReplaceAll(subdir, string(filepath.Separator), ".")

	// Detect actual outer class name (may be *OuterClass for naming conflicts)
	outerClassName := detectOuterClassName(baseDir, subdir, pf.RelPath)

	var b strings.Builder

	// File header
	header := header.GeneratedHeader(consts.LangJava, pf.RelPath)
	b.WriteString(header)
	b.WriteString("\n")
	fmt.Fprintf(&b, "package %s;\n", packageName)

	// Imports
	b.WriteString("import java.util.Map;\n")
	b.WriteString("import java.util.HashMap;\n")
	b.WriteString("import java.util.Arrays;\n")
	b.WriteString("import java.util.stream.Collectors;\n")

	// Generate helper class
	className := msg.Name + "MapHelper"
	fmt.Fprintf(&b, "public class %s {\n", className)

	// Generate fromMap method
	b.WriteString("/**\n")
	fmt.Fprintf(&b, "* Creates a %s from a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(&b, "* The parameter keys are validated against %s enum values.\n", keyEnumType)
	if config.Strict {
		b.WriteString("* Unknown parameter keys will throw an exception (strict mode).\n")
	} else {
		b.WriteString("* Unknown parameter keys are silently ignored (non-strict mode).\n")
	}
	b.WriteString("*/\n")
	fmt.Fprintf(&b, "public static %s.%s.Builder fromMap(Map<String, String> params) throws IllegalArgumentException {\n", outerClassName, msg.Name)
	fmt.Fprintf(&b, "%s.%s.Builder builder = %s.%s.newBuilder();\n", outerClassName, msg.Name, outerClassName, msg.Name)

	b.WriteString("for (Map.Entry<String, String> entry : params.entrySet()) {\n")
	b.WriteString("String key = entry.getKey();\n")
	b.WriteString("String value = entry.getValue();\n")

	fmt.Fprintf(&b, "%s.%s paramKey;\n", outerClassName, keyEnumType)
	b.WriteString("try {\n")
	b.WriteString("// Try to parse the enum from string\n")
	fmt.Fprintf(&b, "paramKey = %s.%s.valueOf(key.toUpperCase().replace('-', '_'));\n", outerClassName, keyEnumType)
	b.WriteString("} catch (IllegalArgumentException e) {\n")
	if config.Strict {
		b.WriteString("throw new IllegalArgumentException(\"Invalid parameter key: \" + key);\n")
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

		fmt.Fprintf(&b, "case %s:\n", field.ParamKey)

		setterName := "set" + javaFieldNameUpper(field.Name)

		// Check if field is an enum type
		isEnum := field.IsEnum || isEnumType(field.Type, pf.Enums)

		if field.IsRepeated {
			b.WriteString("if (value != null && !value.isEmpty()) {\n")
			if isEnum {
				// For repeated enum fields, parse each value
				b.WriteString("String[] parts = value.split(\",\");\n")
				b.WriteString("for (String part : parts) {\n")
				b.WriteString("try {\n")
				fmt.Fprintf(&b, "builder.add%s(%s.%s.valueOf(part.toUpperCase().replace('-', '_')));\n",
					javaFieldNameUpper(field.Name), outerClassName, field.Type)
				b.WriteString("} catch (IllegalArgumentException e) {\n")
				b.WriteString("// Ignore invalid enum values\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				// For repeated string fields
				fmt.Fprintf(&b, "builder.addAll%s(Arrays.asList(value.split(\",\")));\n",
					javaFieldNameUpper(field.Name))
			}
			b.WriteString("}\n")
		} else if isEnum {
			// For enum fields, parse the string value to enum
			b.WriteString("try {\n")
			fmt.Fprintf(&b, "builder.%s(%s.%s.valueOf(value.toUpperCase().replace('-', '_')));\n",
				setterName, outerClassName, field.Type)
			b.WriteString("} catch (IllegalArgumentException e) {\n")
			b.WriteString("// Ignore invalid enum values\n")
			b.WriteString("}\n")
		} else if isIntegerType(field.Type) {
			b.WriteString("try {\n")
			if field.Type == "int64" {
				fmt.Fprintf(&b, "builder.%s(Long.parseLong(value));\n", setterName)
			} else {
				fmt.Fprintf(&b, "builder.%s(Integer.parseInt(value));\n", setterName)
			}
			b.WriteString("} catch (NumberFormatException e) {\n")
			b.WriteString("// Ignore invalid numbers\n")
			b.WriteString("}\n")
		} else if isFloatType(field.Type) {
			b.WriteString("try {\n")
			fmt.Fprintf(&b, "builder.%s(Double.parseDouble(value));\n", setterName)
			b.WriteString("} catch (NumberFormatException e) {\n")
			b.WriteString("// Ignore invalid numbers\n")
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(&b, "builder.%s(value);\n", setterName)
		}
		b.WriteString("break;\n")
	}

	b.WriteString("default:\n")
	b.WriteString("// Unknown enum value\n")
	b.WriteString("break;\n")
	b.WriteString("}\n")
	b.WriteString("}\n")
	b.WriteString("return builder;\n")
	b.WriteString("}\n")

	// Generate toMap method
	b.WriteString("/**\n")
	fmt.Fprintf(&b, "* Converts a %s to a map of URL parameters.\n", msg.Name)
	fmt.Fprintf(&b, "* The map keys are the json_return_value of %s enum values.\n", keyEnumType)
	b.WriteString("* Only non-null/non-empty fields are included in the map.\n")
	b.WriteString("*/\n")
	fmt.Fprintf(&b, "public static Map<String, String> toMap(%s.%s m) {\n", outerClassName, msg.Name)
	b.WriteString("Map<String, String> result = new HashMap<>();\n")
	b.WriteString("if (m == null) {\n")
	b.WriteString("return result;\n")
	b.WriteString("}\n")

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

		getterName := "get" + javaFieldNameUpper(field.Name)

		// Check if field is an enum type
		isEnum := field.IsEnum || isEnumType(field.Type, pf.Enums)

		// Generate field conversion based on type
		if field.IsRepeated {
			// For repeated fields in protobuf, use getXxxList() method
			getterListName := getterName + "List"
			fmt.Fprintf(&b, "if (!m.%s().isEmpty()) {\n", getterListName)
			if isEnum {
				// For repeated enum fields, convert each to lowercase with hyphens
				fmt.Fprintf(&b, "result.put(\"%s\", m.%s().stream()\n", jsonKey, getterListName)
				b.WriteString(".map(e -> e.name().toLowerCase().replace('_', '-'))\n")
				b.WriteString(".collect(Collectors.joining(\",\")));\n")
			} else {
				// For repeated string fields
				fmt.Fprintf(&b, "result.put(\"%s\", String.join(\",\", m.%s()));\n", jsonKey, getterListName)
			}
			b.WriteString("}\n")
		} else if isEnum {
			// For enum fields, convert to lowercase with hyphens
			// Use UNRECOGNIZED as the sentinel value for protobuf enums
			fmt.Fprintf(&b, "if (m.has%s() && m.%s() != %s.%s.UNRECOGNIZED) {\n",
				javaFieldNameUpper(field.Name), getterName,
				outerClassName, field.Type)
			fmt.Fprintf(&b, "result.put(\"%s\", m.%s().name().toLowerCase().replace('_', '-'));\n", jsonKey, getterName)
			b.WriteString("}\n")
		} else if strings.Contains(field.Type, "int") || strings.Contains(field.Type, "double") || strings.Contains(field.Type, "float") { // proto scalar type names (int32, int64, sint32, double, float)
			// For numeric fields, convert to string
			fmt.Fprintf(&b, "if (m.has%s()) {\n", javaFieldNameUpper(field.Name))
			fmt.Fprintf(&b, "result.put(\"%s\", String.valueOf(m.%s()));\n", jsonKey, getterName)
			b.WriteString("}\n")
		} else {
			// For string fields
			fmt.Fprintf(&b, "if (m.has%s() && !m.%s().isEmpty()) {\n",
				javaFieldNameUpper(field.Name), getterName)
			fmt.Fprintf(&b, "result.put(\"%s\", m.%s());\n", jsonKey, getterName)
			b.WriteString("}\n")
		}
	}

	b.WriteString("return result;\n")
	b.WriteString("}\n")
	b.WriteString("}\n")

	// Write to file
	outputDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	outputPath := filepath.Join(outputDir, className+".java")
	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}
