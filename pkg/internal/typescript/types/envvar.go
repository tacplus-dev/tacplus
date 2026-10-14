// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// writeEnvVar generates toEnvVar and fromEnvVar functions for every message in the proto file.
// These convert between protobuf messages and environment variable Record<string, string> maps
// using a deterministic key scheme identical to the Go/Java generators.
func writeEnvVar(b *strings.Builder, pf model.ProtoFile, ctx tsEnvVarContext) {
	for _, msg := range pf.Messages {
		writeMessageToEnvVar(b, msg, pf, ctx)
		writeMessageFromEnvVar(b, msg, pf, ctx)
	}
}

type tsEnvVarContext struct {
	imports map[string]*tsEnvVarImport
}

type tsEnvVarImport struct {
	importPath  string
	funcAliases map[string]string
}

func buildTsEnvVarContext(pf model.ProtoFile) tsEnvVarContext {
	ctx := tsEnvVarContext{imports: make(map[string]*tsEnvVarImport)}

	pkgToImport := make(map[string]string)
	for _, imp := range pf.Imports {
		pkgToImport[imp.Package] = imp.Path
	}

	ensurePkg := func(pkg string) *tsEnvVarImport {
		if _, ok := ctx.imports[pkg]; !ok {
			ctx.imports[pkg] = &tsEnvVarImport{
				importPath:  pkgToImport[pkg],
				funcAliases: make(map[string]string),
			}
		}
		return ctx.imports[pkg]
	}

	addFunc := func(pkg, fn string) {
		cp := ensurePkg(pkg)
		if _, ok := cp.funcAliases[fn]; !ok {
			cp.funcAliases[fn] = tsEnvVarAlias(pkg, fn)
		}
	}

	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			if field.IsOneOf || field.IsMap {
				continue
			}
			if field.IsMessage && field.IsRepeated {
				continue
			}
			if isWellKnownType(field.TypePackage) {
				continue
			}
			if field.TypePackage == "" || field.TypePackage == pf.Package {
				continue
			}

			if field.IsEnum {
				addFunc(field.TypePackage, strutil.LcFirst(field.Type)+"ToString")
				addFunc(field.TypePackage, "parse"+field.Type)
			} else if field.IsMessage {
				addFunc(field.TypePackage, field.Type+"ToEnvVar")
				addFunc(field.TypePackage, field.Type+"FromEnvVar")
			}
		}
	}

	return ctx
}

func tsEnvVarAlias(pkg, fn string) string {
	safePkg := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, pkg)
	return strutil.ToLowerCamelCase(safePkg) + strutil.ToPascalCase(fn)
}

func tsEnvVarHelperName(ctx tsEnvVarContext, field model.Field, fallback string) string {
	if field.TypePackage == "" {
		return fallback
	}
	cp := ctx.imports[field.TypePackage]
	if cp == nil {
		return fallback
	}
	if alias, ok := cp.funcAliases[fallback]; ok {
		return alias
	}
	return fallback
}

// computeEnvVarPrefix computes the env var key prefix for a message.
// Identical to Go/Java: "PROTO_<PATH>_<MESSAGE>"
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

func writeMessageToEnvVar(b *strings.Builder, msg model.Message, pf model.ProtoFile, ctx tsEnvVarContext) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)

	fmt.Fprintf(b, `/**
 * Converts %s to environment variable key-value pairs.
 * @param m The message to convert
 * @returns Record of env var keys to string values
 */
export function %sToEnvVar(m: %s): Record<string, string> {
const result: Record<string, string> = {};
`, msg.Name, msg.Name, msg.Name)

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownType(field.TypePackage) {
			continue
		}

		tsFieldName := tsProtoFieldName(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)
		accessor := "m." + tsFieldName

		switch {
		case field.IsEnum:
			writeEnumFieldToEnvVar(b, field, accessor, envKey, pf, ctx)
		case field.IsMessage:
			writeMessageFieldToEnvVar(b, field, accessor, tsFieldName, ctx)
		default:
			writeScalarFieldToEnvVar(b, field, accessor, envKey)
		}
	}

	b.WriteString("return result;\n}\n\n")
}

func writeScalarFieldToEnvVar(b *strings.Builder, field model.Field, accessor, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarToEnvVar(b, field, accessor, envKey)
		return
	}

	switch field.Type {
	case "string":
		if field.IsOptional {
			fmt.Fprintf(b, "if (%s !== undefined && %s !== '') { result['%s'] = %s; }\n", accessor, accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "result['%s'] = %s;\n", envKey, accessor)
		}
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64":
		if field.IsOptional {
			fmt.Fprintf(b, "if (%s !== undefined) { result['%s'] = String(%s); }\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "result['%s'] = String(%s);\n", envKey, accessor)
		}
	case "float", "double":
		if field.IsOptional {
			fmt.Fprintf(b, "if (%s !== undefined) { result['%s'] = String(%s); }\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "result['%s'] = String(%s);\n", envKey, accessor)
		}
	case "bool":
		if field.IsOptional {
			fmt.Fprintf(b, "if (%s !== undefined) { result['%s'] = String(%s); }\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "result['%s'] = String(%s);\n", envKey, accessor)
		}
	case "bytes":
		// bytes in TS protobuf are Uint8Array; base64 encode
		if field.IsOptional {
			fmt.Fprintf(b, "if (%s !== undefined) { result['%s'] = Buffer.from(%s).toString('base64'); }\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "result['%s'] = Buffer.from(%s).toString('base64');\n", envKey, accessor)
		}
	}
}

func writeRepeatedScalarToEnvVar(b *strings.Builder, field model.Field, accessor, envKey string) {
	switch field.Type {
	case "string":
		fmt.Fprintf(b, "if (%s.length > 0) { result['%s'] = %s.join(','); }\n", accessor, envKey, accessor)
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64",
		"float", "double", "bool":
		fmt.Fprintf(b, "if (%s.length > 0) { result['%s'] = %s.map(String).join(','); }\n", accessor, envKey, accessor)
	}
}

func writeEnumFieldToEnvVar(b *strings.Builder, field model.Field, accessor, envKey string, pf model.ProtoFile, ctx tsEnvVarContext) {
	toStringFunc := tsEnvVarHelperName(ctx, field, strutil.LcFirst(field.Type)+"ToString")

	if field.IsRepeated {
		fmt.Fprintf(b, "if (%s.length > 0) { result['%s'] = %s.map(v => %s(v)).join(','); }\n",
			accessor, envKey, accessor, toStringFunc)
		return
	}

	if field.IsOptional {
		fmt.Fprintf(b, "if (%s !== undefined) { result['%s'] = %s(%s); }\n", accessor, envKey, toStringFunc, accessor)
	} else {
		fmt.Fprintf(b, "result['%s'] = %s(%s);\n", envKey, toStringFunc, accessor)
	}
}

func writeMessageFieldToEnvVar(b *strings.Builder, field model.Field, accessor, _tsFieldName string, ctx tsEnvVarContext) {
	toEnvVarFunc := tsEnvVarHelperName(ctx, field, field.Type+"ToEnvVar")
	fmt.Fprintf(b, "if (%s) { Object.assign(result, %s(%s)); }\n", accessor, toEnvVarFunc, accessor)
}

func writeMessageFromEnvVar(b *strings.Builder, msg model.Message, pf model.ProtoFile, ctx tsEnvVarContext) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)

	fmt.Fprintf(b, `/**
 * Creates %s from environment variable key-value pairs.
 * @param env Record of env var keys to string values
 * @returns Partial %s populated from env vars
 */
export function %sFromEnvVar(env: Record<string, string>): Partial<%s> {
const result: Record<string, unknown> = {};
`, msg.Name, msg.Name, msg.Name, msg.Name)

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownType(field.TypePackage) {
			continue
		}

		tsFieldName := tsProtoFieldName(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)

		switch {
		case field.IsEnum:
			writeEnumFieldFromEnvVar(b, field, tsFieldName, envKey, pf, ctx)
		case field.IsMessage:
			writeMessageFieldFromEnvVar(b, field, tsFieldName, ctx)
		default:
			writeScalarFieldFromEnvVar(b, field, tsFieldName, envKey)
		}
	}

	fmt.Fprintf(b, "return result as Partial<%s>;\n}\n\n", msg.Name)
}

func writeScalarFieldFromEnvVar(b *strings.Builder, field model.Field, tsFieldName, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarFromEnvVar(b, field, tsFieldName, envKey)
		return
	}

	valCheck := fmt.Sprintf("if (env['%s'] !== undefined && env['%s'] !== '') {\n", envKey, envKey)
	val := fmt.Sprintf("env['%s']", envKey)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "%sresult['%s'] = %s;\n}\n", valCheck, tsFieldName, val)
	case "int32", "sint32", "sfixed32":
		fmt.Fprintf(b, "%sresult['%s'] = parseInt(%s, 10);\n}\n", valCheck, tsFieldName, val)
	case "int64", "sint64", "sfixed64":
		fmt.Fprintf(b, "%sresult['%s'] = BigInt(%s);\n}\n", valCheck, tsFieldName, val)
	case "uint32", "fixed32":
		fmt.Fprintf(b, "%sresult['%s'] = parseInt(%s, 10);\n}\n", valCheck, tsFieldName, val)
	case "uint64", "fixed64":
		fmt.Fprintf(b, "%sresult['%s'] = BigInt(%s);\n}\n", valCheck, tsFieldName, val)
	case "float", "double":
		fmt.Fprintf(b, "%sresult['%s'] = parseFloat(%s);\n}\n", valCheck, tsFieldName, val)
	case "bool":
		fmt.Fprintf(b, "%sresult['%s'] = %s === 'true';\n}\n", valCheck, tsFieldName, val)
	case "bytes":
		fmt.Fprintf(b, "%sresult['%s'] = Buffer.from(%s, 'base64');\n}\n", valCheck, tsFieldName, val)
	}
}

func writeRepeatedScalarFromEnvVar(b *strings.Builder, field model.Field, tsFieldName, envKey string) {
	valCheck := fmt.Sprintf("if (env['%s'] !== undefined && env['%s'] !== '') {\n", envKey, envKey)
	val := fmt.Sprintf("env['%s']", envKey)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',');\n}\n", valCheck, tsFieldName, val)
	case "int32", "sint32", "sfixed32", "uint32", "fixed32":
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',').map(s => parseInt(s.trim(), 10));\n}\n", valCheck, tsFieldName, val)
	case "int64", "sint64", "sfixed64", "uint64", "fixed64":
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',').map(s => BigInt(s.trim()));\n}\n", valCheck, tsFieldName, val)
	case "float", "double":
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',').map(s => parseFloat(s.trim()));\n}\n", valCheck, tsFieldName, val)
	case "bool":
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',').map(s => s.trim() === 'true');\n}\n", valCheck, tsFieldName, val)
	}
}

func writeEnumFieldFromEnvVar(b *strings.Builder, field model.Field, tsFieldName, envKey string, pf model.ProtoFile, ctx tsEnvVarContext) {
	parseFunc := tsEnvVarHelperName(ctx, field, "parse"+field.Type)

	valCheck := fmt.Sprintf("if (env['%s'] !== undefined && env['%s'] !== '') {\n", envKey, envKey)
	val := fmt.Sprintf("env['%s']", envKey)

	if field.IsRepeated {
		fmt.Fprintf(b, "%sresult['%s'] = %s.split(',').map(s => %s(s.trim()));\n}\n",
			valCheck, tsFieldName, val, parseFunc)
		return
	}

	fmt.Fprintf(b, "%sresult['%s'] = %s(%s);\n}\n", valCheck, tsFieldName, parseFunc, val)
}

func writeMessageFieldFromEnvVar(b *strings.Builder, field model.Field, tsFieldName string, ctx tsEnvVarContext) {
	fromEnvVarFunc := tsEnvVarHelperName(ctx, field, field.Type+"FromEnvVar")
	fmt.Fprintf(b, "{ const sub = %s(env); if (Object.keys(sub).length > 0) { result['%s'] = sub; } }\n",
		fromEnvVarFunc, tsFieldName)
}

// isWellKnownType returns true if the type package is a protobuf well-known type.
func isWellKnownType(typePackage string) bool {
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb",
		"google.protobuf":
		return true
	}
	return false
}
