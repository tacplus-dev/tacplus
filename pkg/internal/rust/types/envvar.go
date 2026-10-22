// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// buildCrossPackageMapRust maps proto package names to fully-qualified Rust crate module paths.
// e.g., "host" -> "crate::sys::host", "gateway" -> "crate::services::gateway"
func buildCrossPackageMapRust(pf model.ProtoFile) map[string]string {
	result := make(map[string]string)
	for _, imp := range pf.Imports {
		if imp.Package == "" {
			continue
		}
		parentDir := filepath.Dir(imp.Path)
		cratePath := "crate::" + strings.ReplaceAll(parentDir, "/", "::") + "::" + imp.Package
		result[imp.Package] = cratePath
	}
	return result
}

// rustKeywordEscape prefixes Rust reserved keywords with r# for use as identifiers.
func rustKeywordEscape(name string) string {
	switch name {
	case "as", "break", "const", "continue", "crate", "else", "enum", "extern",
		"false", "fn", "for", "if", "impl", "in", "let", "loop", "match", "mod",
		"move", "mut", "pub", "ref", "return", "self", "static", "struct", "super",
		"trait", "true", "type", "unsafe", "use", "where", "while", "async", "await",
		"dyn", "abstract", "become", "box", "do", "final", "macro", "override",
		"priv", "typeof", "unsized", "virtual", "yield", "try":
		return "r#" + name
	}
	return name
}

// writeEnvVar generates to_env_var and from_env_var functions for every message in the proto file.
// These convert between protobuf messages and HashMap<String, String> maps
// using a deterministic key scheme identical to the Go/Java/TypeScript/Python generators.
func writeEnvVar(b *strings.Builder, pf model.ProtoFile) {
	b.WriteString("// ============================================================\n")
	b.WriteString("// Environment Variable Helpers\n")
	b.WriteString("// ============================================================\n")

	crossPkgs := buildCrossPackageMapRust(pf)

	for _, msg := range pf.Messages {
		writeMessageToEnvVarRust(b, msg, pf, crossPkgs)
		writeMessageFromEnvVarRust(b, msg, pf, crossPkgs)
	}
}

// computeEnvVarPrefix computes the env var key prefix for a message.
// Identical to Go/Java/TypeScript/Python: "PROTO_<PATH>_<MESSAGE>"
func computeEnvVarPrefix(relPath string, msgName string) string {
	pathPart := strings.TrimSuffix(relPath, ".proto")
	pathPart = strings.ReplaceAll(pathPart, "/", "_")
	pathPart = strings.ToUpper(pathPart)
	msgPart := strings.ToUpper(strutil.ToSnakeCase(msgName))
	if pathPart == "" {
		return "PROTO_" + msgPart
	}
	return "PROTO_" + pathPart + "_" + msgPart
}

func writeMessageToEnvVarRust(b *strings.Builder, msg model.Message, pf model.ProtoFile, crossPkgs map[string]string) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)
	rustTypeName := toProstTypeName(msg.Name)
	snakeName := strutil.ToSnakeCase(msg.Name)
	fields := rustEnvVarFields(msg)
	msgParam := "msg"
	mutability := "mut "
	if len(fields) == 0 {
		msgParam = "_msg"
		mutability = ""
	}

	fmt.Fprintf(b, "/// Converts %s to environment variable key-value pairs.\n", rustTypeName)
	fmt.Fprintf(b, "pub fn %s_to_env_var(%s: &%s) -> std::collections::HashMap<String, String> {\n", snakeName, msgParam, rustTypeName)
	fmt.Fprintf(b, "let %sresult = std::collections::HashMap::new();\n", mutability)

	for _, field := range fields {
		rustFieldName := rustKeywordEscape(strutil.ToSnakeCase(field.Name))
		envKey := prefix + "_" + strings.ToUpper(field.Name)
		accessor := "msg." + rustFieldName

		switch {
		case field.IsEnum:
			writeEnumFieldToEnvVarRust(b, field, accessor, envKey, crossPkgs)
		case field.IsMessage:
			writeMessageFieldToEnvVarRust(b, field, accessor, crossPkgs)
		default:
			writeScalarFieldToEnvVarRust(b, field, accessor, envKey)
		}
	}

	b.WriteString("result\n}\n")
}

func writeScalarFieldToEnvVarRust(b *strings.Builder, field model.Field, accessor, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarToEnvVarRust(b, field, accessor, envKey)
		return
	}

	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(ref val) = %s {\n", accessor)
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "if !val.is_empty() { result.insert(%q.to_string(), val.clone()); }\n", envKey)
		case "bool":
			fmt.Fprintf(b, "result.insert(%q.to_string(), val.to_string());\n", envKey)
		case "bytes":
			fmt.Fprintf(b, "result.insert(%q.to_string(), base64::Engine::encode(&base64::engine::general_purpose::STANDARD, val));\n", envKey)
		default:
			fmt.Fprintf(b, "result.insert(%q.to_string(), val.to_string());\n", envKey)
		}
		b.WriteString("}\n")
		return
	}

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.clone());\n", envKey, accessor)
	case "bool":
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.to_string());\n", envKey, accessor)
	case "bytes":
		fmt.Fprintf(b, "result.insert(%q.to_string(), base64::Engine::encode(&base64::engine::general_purpose::STANDARD, &%s));\n", envKey, accessor)
	default:
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.to_string());\n", envKey, accessor)
	}
}

func writeRepeatedScalarToEnvVarRust(b *strings.Builder, field model.Field, accessor, envKey string) {
	fmt.Fprintf(b, "if !%s.is_empty() {\n", accessor)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.join(\",\"));\n", envKey, accessor)
	case "bool":
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.iter().map(|v| v.to_string()).collect::<Vec<_>>().join(\",\"));\n", envKey, accessor)
	case "bytes":
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.iter().map(|v| base64::Engine::encode(&base64::engine::general_purpose::STANDARD, v)).collect::<Vec<_>>().join(\",\"));\n", envKey, accessor)
	default:
		fmt.Fprintf(b, "result.insert(%q.to_string(), %s.iter().map(|v| v.to_string()).collect::<Vec<_>>().join(\",\"));\n", envKey, accessor)
	}

	b.WriteString("}\n")
}

func writeEnumFieldToEnvVarRust(b *strings.Builder, field model.Field, accessor, envKey string, crossPkgs map[string]string) {
	typePath, isCrossPackage := rustEnumPath(field, crossPkgs)

	if field.IsRepeated {
		fmt.Fprintf(b, "if !%s.is_empty() {\n", accessor)
		if isCrossPackage {
			fmt.Fprintf(b, "result.insert(%q.to_string(), %s.iter().map(|v| match %s::try_from(*v) { Ok(e) => e.as_str_name().to_string(), Err(_) => v.to_string() }).collect::<Vec<_>>().join(\",\"));\n",
				envKey, accessor, typePath)
		} else {
			fmt.Fprintf(b, "result.insert(%q.to_string(), %s.iter().map(|v| %s::to_string_value(*v)).collect::<Vec<_>>().join(\",\"));\n",
				envKey, accessor, typePath)
		}
		b.WriteString("}\n")
		return
	}

	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = %s {\n", accessor)
		if isCrossPackage {
			fmt.Fprintf(b, "result.insert(%q.to_string(), match %s::try_from(val) { Ok(e) => e.as_str_name().to_string(), Err(_) => val.to_string() });\n", envKey, typePath)
		} else {
			fmt.Fprintf(b, "result.insert(%q.to_string(), %s::to_string_value(val));\n", envKey, typePath)
		}
		b.WriteString("}\n")
	} else {
		if isCrossPackage {
			fmt.Fprintf(b, "result.insert(%q.to_string(), match %s::try_from(%s) { Ok(e) => e.as_str_name().to_string(), Err(_) => %s.to_string() });\n", envKey, typePath, accessor, accessor)
		} else {
			fmt.Fprintf(b, "result.insert(%q.to_string(), %s::to_string_value(%s));\n", envKey, typePath, accessor)
		}
	}
}

func writeMessageFieldToEnvVarRust(b *strings.Builder, field model.Field, accessor string, crossPkgs map[string]string) {
	funcName := fmt.Sprintf("%s_to_env_var", strutil.ToSnakeCase(field.Type))
	if cratePath, ok := crossPkgs[field.TypePackage]; ok && field.TypePackage != "" {
		funcName = cratePath + "::" + funcName
	}
	fmt.Fprintf(b, "if let Some(ref sub) = %s {\n", accessor)
	fmt.Fprintf(b, "result.extend(%s(sub));\n", funcName)
	b.WriteString("}\n")
}

func writeMessageFromEnvVarRust(b *strings.Builder, msg model.Message, pf model.ProtoFile, crossPkgs map[string]string) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)
	rustTypeName := toProstTypeName(msg.Name)
	snakeName := strutil.ToSnakeCase(msg.Name)
	fields := rustEnvVarFields(msg)
	envParam := "env"
	mutability := "mut "
	if len(fields) == 0 {
		envParam = "_env"
		mutability = ""
	}

	fmt.Fprintf(b, "/// Creates %s from environment variable key-value pairs.\n", rustTypeName)
	fmt.Fprintf(b, "pub fn %s_from_env_var(%s: &std::collections::HashMap<String, String>) -> %s {\n", snakeName, envParam, rustTypeName)
	fmt.Fprintf(b, "let %sresult = %s::default();\n", mutability, rustTypeName)

	for _, field := range fields {
		rustFieldName := rustKeywordEscape(strutil.ToSnakeCase(field.Name))
		envKey := prefix + "_" + strings.ToUpper(field.Name)

		switch {
		case field.IsEnum:
			writeEnumFieldFromEnvVarRust(b, field, rustFieldName, envKey, crossPkgs)
		case field.IsMessage:
			writeMessageFieldFromEnvVarRust(b, field, rustFieldName, crossPkgs)
		default:
			writeScalarFieldFromEnvVarRust(b, field, rustFieldName, envKey)
		}
	}

	b.WriteString("result\n}\n")
}

func writeScalarFieldFromEnvVarRust(b *strings.Builder, field model.Field, rustFieldName, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarFromEnvVarRust(b, field, rustFieldName, envKey)
		return
	}

	fmt.Fprintf(b, "if let Some(val) = env.get(%q) {\nif !val.is_empty() {\n", envKey)

	if field.IsOptional {
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "result.%s = Some(val.clone());\n", rustFieldName)
		case "int32", "sint32", "sfixed32":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<i32>() { result.%s = Some(v); }\n", rustFieldName)
		case "int64", "sint64", "sfixed64":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<i64>() { result.%s = Some(v); }\n", rustFieldName)
		case "uint32", "fixed32":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<u32>() { result.%s = Some(v); }\n", rustFieldName)
		case "uint64", "fixed64":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<u64>() { result.%s = Some(v); }\n", rustFieldName)
		case "float":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<f32>() { result.%s = Some(v); }\n", rustFieldName)
		case "double":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<f64>() { result.%s = Some(v); }\n", rustFieldName)
		case "bool":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<bool>() { result.%s = Some(v); }\n", rustFieldName)
		case "bytes":
			fmt.Fprintf(b, "if let Ok(v) = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, val) { result.%s = Some(v); }\n", rustFieldName)
		}
	} else {
		switch field.Type {
		case "string":
			fmt.Fprintf(b, "result.%s = val.clone();\n", rustFieldName)
		case "int32", "sint32", "sfixed32":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<i32>() { result.%s = v; }\n", rustFieldName)
		case "int64", "sint64", "sfixed64":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<i64>() { result.%s = v; }\n", rustFieldName)
		case "uint32", "fixed32":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<u32>() { result.%s = v; }\n", rustFieldName)
		case "uint64", "fixed64":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<u64>() { result.%s = v; }\n", rustFieldName)
		case "float":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<f32>() { result.%s = v; }\n", rustFieldName)
		case "double":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<f64>() { result.%s = v; }\n", rustFieldName)
		case "bool":
			fmt.Fprintf(b, "if let Ok(v) = val.parse::<bool>() { result.%s = v; }\n", rustFieldName)
		case "bytes":
			fmt.Fprintf(b, "if let Ok(v) = base64::Engine::decode(&base64::engine::general_purpose::STANDARD, val) { result.%s = v; }\n", rustFieldName)
		}
	}

	b.WriteString("}\n}\n")
}

func writeRepeatedScalarFromEnvVarRust(b *strings.Builder, field model.Field, rustFieldName, envKey string) {
	fmt.Fprintf(b, "if let Some(val) = env.get(%q) {\nif !val.is_empty() {\n", envKey)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "result.%s = val.split(',').map(|s| s.to_string()).collect();\n", rustFieldName)
	case "int32", "sint32", "sfixed32":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<i32>().ok()).collect();\n", rustFieldName)
	case "int64", "sint64", "sfixed64":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<i64>().ok()).collect();\n", rustFieldName)
	case "uint32", "fixed32":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<u32>().ok()).collect();\n", rustFieldName)
	case "uint64", "fixed64":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<u64>().ok()).collect();\n", rustFieldName)
	case "float":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<f32>().ok()).collect();\n", rustFieldName)
	case "double":
		fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| s.trim().parse::<f64>().ok()).collect();\n", rustFieldName)
	case "bool":
		fmt.Fprintf(b, "result.%s = val.split(',').map(|s| s.trim() == \"true\").collect();\n", rustFieldName)
	}

	b.WriteString("}\n}\n")
}

func writeEnumFieldFromEnvVarRust(b *strings.Builder, field model.Field, rustFieldName, envKey string, crossPkgs map[string]string) {
	typePath, isCrossPackage := rustEnumPath(field, crossPkgs)

	fmt.Fprintf(b, "if let Some(val) = env.get(%q) {\nif !val.is_empty() {\n", envKey)

	if field.IsRepeated {
		if isCrossPackage {
			fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| { let s = s.trim(); if let Ok(v) = s.parse::<i32>() { Some(v) } else { %s::from_str_name(s).map(|v| v as i32) } }).collect();\n",
				rustFieldName, typePath)
		} else {
			fmt.Fprintf(b, "result.%s = val.split(',').filter_map(|s| %s::parse_i32(s.trim()).ok()).collect();\n",
				rustFieldName, typePath)
		}
	} else if field.IsOptional {
		if isCrossPackage {
			fmt.Fprintf(b, "if let Some(v) = if let Ok(v) = val.parse::<i32>() { Some(v) } else { %s::from_str_name(val).map(|v| v as i32) } { result.%s = Some(v); }\n", typePath, rustFieldName)
		} else {
			fmt.Fprintf(b, "if let Ok(v) = %s::parse_i32(val) { result.%s = Some(v); }\n", typePath, rustFieldName)
		}
	} else {
		if isCrossPackage {
			fmt.Fprintf(b, "if let Some(v) = if let Ok(v) = val.parse::<i32>() { Some(v) } else { %s::from_str_name(val).map(|v| v as i32) } { result.%s = v; }\n", typePath, rustFieldName)
		} else {
			fmt.Fprintf(b, "if let Ok(v) = %s::parse_i32(val) { result.%s = v; }\n", typePath, rustFieldName)
		}
	}

	b.WriteString("}\n}\n")
}

func rustEnumPath(field model.Field, crossPkgs map[string]string) (string, bool) {
	rustEnumName := toProstEnumName(field.Type)
	if cratePath, ok := crossPkgs[field.TypePackage]; ok && field.TypePackage != "" {
		return cratePath + "::" + rustEnumName, true
	}
	return rustEnumName, false
}

func writeMessageFieldFromEnvVarRust(b *strings.Builder, field model.Field, rustFieldName string, crossPkgs map[string]string) {
	funcName := fmt.Sprintf("%s_from_env_var", strutil.ToSnakeCase(field.Type))
	if cratePath, ok := crossPkgs[field.TypePackage]; ok && field.TypePackage != "" {
		funcName = cratePath + "::" + funcName
	}
	fmt.Fprintf(b, "{ let sub = %s(env);\n", funcName)
	fmt.Fprintf(b, "result.%s = Some(sub);\n", rustFieldName)
	b.WriteString("}\n")
}

// isWellKnownTypeRust returns true if the type package is a protobuf well-known type.
func isWellKnownTypeRust(typePackage string) bool {
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb",
		"google.protobuf":
		return true
	}
	return false
}

func rustEnvVarFields(msg model.Message) []model.Field {
	var fields []model.Field
	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownTypeRust(field.TypePackage) {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}
