// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"fmt"
	"path/filepath"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// writeEnvVar generates configToEnvVar and configFromEnvVar static methods for every
// message in the proto file. These methods convert between protobuf messages and
// environment variable maps using a deterministic key scheme identical to the Go generator.
func writeEnvVar(b *strings.Builder, pf model.ProtoFile, outerClassName string, config *tactype.Config) {
	// Build cross-package Helpers FQ map: TypePackage -> "com.xxx.types.sys.host.Helpers"
	crossHelpers := buildCrossPackageHelpersMap(pf, config)

	for _, msg := range pf.Messages {
		oc := msgOuterClass(msg, outerClassName)
		writeMessageToEnvVar(b, msg, pf.RelPath, oc, crossHelpers)
		writeMessageFromEnvVar(b, msg, pf.RelPath, oc, crossHelpers)
	}
}

// buildCrossPackageHelpersMap maps proto package names to fully-qualified Java Helpers class paths.
// Uses the proto package declaration (imp.Package) as the source of truth — filenames are irrelevant.
func buildCrossPackageHelpersMap(pf model.ProtoFile, config *tactype.Config) map[string]string {
	typesBase := config.JavaPackageBase + ".types"
	result := make(map[string]string)
	for _, imp := range pf.Imports {
		if imp.Package == "" {
			continue
		}
		// imp.Path is like "sys/host.proto" or "services/myservice/types.proto"
		pathWithoutExt := strings.TrimSuffix(imp.Path, ".proto")
		parts := strings.Split(pathWithoutExt, "/")
		if len(parts) < 2 {
			continue
		}
		dirPart := parts[0] // e.g., "sys", "services", "common"
		// Use the proto package name (source of truth), not the filename
		pkgName := imp.Package
		if len(parts) >= 3 {
			// Directory layout: {dir}/baseball/types.proto → {dir}.{pkg}.{pkg}.Helpers
			result[imp.Package] = typesBase + "." + dirPart + "." + pkgName + "." + pkgName + ".Helpers"
		} else {
			// Flat layout: {dir}/iron-maiden.proto → {dir}.{pkg}.Helpers
			result[imp.Package] = typesBase + "." + dirPart + "." + pkgName + ".Helpers"
		}
	}
	return result
}

// computeEnvVarPrefix computes the env var key prefix for a message.
// e.g., relPath="services/myservice/types.proto", msgName="Config" -> "PROTO_SERVICES_MYSERVICE_TYPES_CONFIG"
func computeEnvVarPrefix(relPath string, msgName string) string {
	// Strip .proto extension
	pathPart := strings.TrimSuffix(relPath, ".proto")
	// Replace path separators with underscores
	pathPart = strings.ReplaceAll(pathPart, string(filepath.Separator), "_")
	pathPart = strings.ReplaceAll(pathPart, "/", "_")
	// Uppercase
	pathPart = strings.ToUpper(pathPart)

	// Convert PascalCase message name to UPPER_SNAKE_CASE
	msgPart := strings.ToUpper(strutil.ToSnakeCase(msgName))

	if pathPart == "" {
		return "PROTO_" + msgPart
	}
	return "PROTO_" + pathPart + "_" + msgPart
}

// writeMessageToEnvVar generates a single toEnvVar method for a message.
func writeMessageToEnvVar(b *strings.Builder, msg model.Message, relPath, outerClassName string, crossHelpers map[string]string) {
	prefix := computeEnvVarPrefix(relPath, msg.Name)
	fullTypeName := outerClassName + "." + msg.Name
	funcName := strutil.ToLowerFirst(msg.Name) + "ToEnvVar"

	fmt.Fprintf(b, "public static Map<String, String> %s(%s msg) {\n", funcName, fullTypeName)
	b.WriteString("Map<String, String> result = new HashMap<>();\n")

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownTypePackage(field.TypePackage) {
			continue
		}

		javaFieldName := javaFieldNameUpper(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)
		getterName := "get" + javaFieldName

		switch {
		case field.IsEnum:
			writeEnumToEnvVar(b, field, javaFieldName, envKey, getterName, crossHelpers)
		case field.IsMessage:
			writeMessageFieldToEnvVar(b, field, javaFieldName, getterName, crossHelpers)
		default:
			writeScalarToEnvVar(b, field, javaFieldName, envKey, getterName)
		}
	}

	b.WriteString("return result;\n")
	b.WriteString("}\n")
}

// writeMessageFromEnvVar generates a single fromEnvVar method for a message.
func writeMessageFromEnvVar(b *strings.Builder, msg model.Message, relPath, outerClassName string, crossHelpers map[string]string) {
	prefix := computeEnvVarPrefix(relPath, msg.Name)
	fullTypeName := outerClassName + "." + msg.Name
	funcName := strutil.ToLowerFirst(msg.Name) + "FromEnvVar"

	fmt.Fprintf(b, "public static %s.Builder %s(Map<String, String> env) {\n", fullTypeName, funcName)
	fmt.Fprintf(b, "%s.Builder builder = %s.newBuilder();\n", fullTypeName, fullTypeName)

	// Only declare "val" if there are scalar or enum fields that use it.
	needsVal := false
	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap || (field.IsMessage && field.IsRepeated) {
			continue
		}
		if isWellKnownTypePackage(field.TypePackage) {
			continue
		}
		if !field.IsMessage {
			needsVal = true
			break
		}
	}
	if needsVal {
		b.WriteString("String val;\n")
	}

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownTypePackage(field.TypePackage) {
			continue
		}

		javaFieldName := javaFieldNameUpper(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)
		setterName := "set" + javaFieldName

		switch {
		case field.IsEnum:
			writeEnumFromEnvVar(b, field, javaFieldName, envKey, setterName, crossHelpers)
		case field.IsMessage:
			writeMessageFieldFromEnvVar(b, field, javaFieldName, setterName, crossHelpers)
		default:
			writeScalarFromEnvVar(b, field, javaFieldName, envKey, setterName)
		}
	}

	b.WriteString("return builder;\n")
	b.WriteString("}\n")
}

// writeScalarToEnvVar generates toEnvVar code for a scalar field.
func writeScalarToEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, getterName string) {
	accessor := fmt.Sprintf("msg.%s()", getterName)

	if field.IsRepeated {
		writeRepeatedScalarToEnvVar(b, field, javaFieldName, envKey, getterName)
		return
	}

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "result.put(%q, %s);\n", envKey, accessor)
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64":
		fmt.Fprintf(b, "result.put(%q, String.valueOf(%s));\n", envKey, accessor)
	case "float", "double":
		fmt.Fprintf(b, "result.put(%q, String.valueOf(%s));\n", envKey, accessor)
	case "bool":
		fmt.Fprintf(b, "result.put(%q, String.valueOf(%s));\n", envKey, accessor)
	case "bytes":
		fmt.Fprintf(b, "result.put(%q, Base64.getEncoder().encodeToString(%s.toByteArray()));\n", envKey, accessor)
	}
}

// writeRepeatedScalarToEnvVar generates toEnvVar code for a repeated scalar field.
func writeRepeatedScalarToEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, _getterName string) {
	listGetter := fmt.Sprintf("msg.get%sList()", javaFieldName)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "if (!%s.isEmpty()) {\n", listGetter)
		fmt.Fprintf(b, "result.put(%q, String.join(\",\", %s));\n", envKey, listGetter)
		b.WriteString("}\n")
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64",
		"float", "double", "bool":
		fmt.Fprintf(b, "if (!%s.isEmpty()) {\n", listGetter)
		fmt.Fprintf(b, "result.put(%q, %s.stream().map(String::valueOf).collect(Collectors.joining(\",\")));\n", envKey, listGetter)
		b.WriteString("}\n")
	}
}

// writeEnumToEnvVar generates toEnvVar code for an enum field.
func writeEnumToEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, getterName string, crossHelpers map[string]string) {
	toStringFunc := strutil.ToLowerCamelCase(field.Type) + "ToString"
	if fqHelpers, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		toStringFunc = fqHelpers + "." + toStringFunc
	}

	accessor := fmt.Sprintf("msg.%s()", getterName)

	if field.IsRepeated {
		listGetter := fmt.Sprintf("msg.get%sList()", javaFieldName)
		fmt.Fprintf(b, "if (!%s.isEmpty()) {\n", listGetter)
		fmt.Fprintf(b, "result.put(%q, %s.stream().map(v -> %s(v)).collect(Collectors.joining(\",\")));\n",
			envKey, listGetter, toStringFunc)
		b.WriteString("}\n")
		return
	}

	fmt.Fprintf(b, "result.put(%q, %s(%s));\n", envKey, toStringFunc, accessor)
}

// writeMessageFieldToEnvVar generates toEnvVar code for a sub-message field.
func writeMessageFieldToEnvVar(b *strings.Builder, field model.Field, javaFieldName, getterName string, crossHelpers map[string]string) {
	toEnvVarFunc := strutil.ToLowerFirst(field.Type) + "ToEnvVar"
	if fqHelpers, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		toEnvVarFunc = fqHelpers + "." + toEnvVarFunc
	}

	hasName := "has" + javaFieldName
	accessor := fmt.Sprintf("msg.%s()", getterName)

	fmt.Fprintf(b, "if (msg.%s()) { result.putAll(%s(%s)); }\n", hasName, toEnvVarFunc, accessor)
}

// writeScalarFromEnvVar generates fromEnvVar code for a scalar field.
func writeScalarFromEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, setterName string) {
	if field.IsRepeated {
		writeRepeatedScalarFromEnvVar(b, field, javaFieldName, envKey, setterName)
		return
	}

	valCheck := fmt.Sprintf("val = env.get(%q);\n", envKey)
	b.WriteString(valCheck)
	b.WriteString("if (val != null && !val.isEmpty()) {\n")

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "builder.%s(val);\n", setterName)
	case "int32", "sint32", "sfixed32":
		fmt.Fprintf(b, "builder.%s(Integer.parseInt(val));\n", setterName)
	case "int64", "sint64", "sfixed64":
		fmt.Fprintf(b, "builder.%s(Long.parseLong(val));\n", setterName)
	case "uint32", "fixed32":
		fmt.Fprintf(b, "builder.%s(Integer.parseUnsignedInt(val));\n", setterName)
	case "uint64", "fixed64":
		fmt.Fprintf(b, "builder.%s(Long.parseUnsignedLong(val));\n", setterName)
	case "float":
		fmt.Fprintf(b, "builder.%s(Float.parseFloat(val));\n", setterName)
	case "double":
		fmt.Fprintf(b, "builder.%s(Double.parseDouble(val));\n", setterName)
	case "bool":
		fmt.Fprintf(b, "builder.%s(Boolean.parseBoolean(val));\n", setterName)
	case "bytes":
		fmt.Fprintf(b, "builder.%s(ByteString.copyFrom(Base64.getDecoder().decode(val)));\n", setterName)
	default:
		// Unknown type, skip
		b.WriteString("// unsupported type: " + field.Type + "\n")
	}

	b.WriteString("}\n")
}

// writeRepeatedScalarFromEnvVar generates fromEnvVar code for a repeated scalar field.
func writeRepeatedScalarFromEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, _setterName string) {
	addAllName := "addAll" + javaFieldName

	valCheck := fmt.Sprintf("val = env.get(%q);\n", envKey)
	b.WriteString(valCheck)
	b.WriteString("if (val != null && !val.isEmpty()) {\n")

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "builder.%s(Arrays.asList(val.split(\",\")));\n", addAllName)
	case "int32", "sint32", "sfixed32":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Integer::parseInt).collect(Collectors.toList()));\n", addAllName)
	case "int64", "sint64", "sfixed64":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Long::parseLong).collect(Collectors.toList()));\n", addAllName)
	case "uint32", "fixed32":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Integer::parseUnsignedInt).collect(Collectors.toList()));\n", addAllName)
	case "uint64", "fixed64":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Long::parseUnsignedLong).collect(Collectors.toList()));\n", addAllName)
	case "float":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Float::parseFloat).collect(Collectors.toList()));\n", addAllName)
	case "double":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Double::parseDouble).collect(Collectors.toList()));\n", addAllName)
	case "bool":
		fmt.Fprintf(b, "builder.%s(Arrays.stream(val.split(\",\")).map(String::trim).map(Boolean::parseBoolean).collect(Collectors.toList()));\n", addAllName)
	default:
		b.WriteString("// unsupported repeated type: " + field.Type + "\n")
	}

	b.WriteString("}\n")
}

// writeEnumFromEnvVar generates fromEnvVar code for an enum field.
func writeEnumFromEnvVar(b *strings.Builder, field model.Field, javaFieldName, envKey, setterName string, crossHelpers map[string]string) {
	parseFunc := "parse" + field.Type
	if fqHelpers, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		parseFunc = fqHelpers + "." + parseFunc
	}

	if field.IsRepeated {
		addAllName := "addAll" + javaFieldName
		valCheck := fmt.Sprintf("val = env.get(%q);\n", envKey)
		b.WriteString(valCheck)
		b.WriteString("if (val != null && !val.isEmpty()) {\n")
		fmt.Fprintf(b, "for (String part : val.split(\",\")) {\n")
		fmt.Fprintf(b, "builder.%s(java.util.Collections.singletonList(%s(part.trim())));\n", addAllName, parseFunc)
		b.WriteString("}\n")
		b.WriteString("}\n")
		return
	}

	valCheck := fmt.Sprintf("val = env.get(%q);\n", envKey)
	b.WriteString(valCheck)
	b.WriteString("if (val != null && !val.isEmpty()) {\n")
	fmt.Fprintf(b, "builder.%s(%s(val));\n", setterName, parseFunc)
	b.WriteString("}\n")
}

// writeMessageFieldFromEnvVar generates fromEnvVar code for a sub-message field.
// Only sets the field on the builder if the sub-message has any fields populated
// from env vars; otherwise the sub-message would be set to its default value,
// which makes hasAnyField checks unreliable (e.g., merge is triggered even
// when no env vars are present).
func writeMessageFieldFromEnvVar(b *strings.Builder, field model.Field, _javaFieldName, setterName string, crossHelpers map[string]string) {
	fromEnvVarFunc := strutil.ToLowerFirst(field.Type) + "FromEnvVar"
	if fqHelpers, ok := crossHelpers[field.TypePackage]; ok && field.TypePackage != "" {
		fromEnvVarFunc = fqHelpers + "." + fromEnvVarFunc
	}

	fmt.Fprintf(b, "{ var sub = %s(env).build();\n", fromEnvVarFunc)
	fmt.Fprintf(b, "if (!sub.equals(sub.getDefaultInstanceForType())) { builder.%s(sub); }\n", setterName)
	b.WriteString("}\n")
}

// isWellKnownTypePackage returns true if the type package is a protobuf well-known type.
func isWellKnownTypePackage(typePackage string) bool {
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb",
		"google.protobuf":
		return true
	}
	return false
}
