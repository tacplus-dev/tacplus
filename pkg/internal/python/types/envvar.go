// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// writeEnvVar generates to_env_var and from_env_var functions for every message in the proto file.
// These convert between protobuf messages and environment variable dict[str, str] maps
// using a deterministic key scheme identical to the Go/Java/TypeScript generators.
func writeEnvVar(b *strings.Builder, pf model.ProtoFile) {
	b.WriteString("\n# ============================================================\n")
	b.WriteString("# Environment Variable Helpers\n")
	b.WriteString("# ============================================================\n\n")

	for _, msg := range pf.Messages {
		writeMessageToEnvVarPy(b, msg, pf)
		writeMessageFromEnvVarPy(b, msg, pf)
	}
}

// computeEnvVarPrefix computes the env var key prefix for a message.
// Identical to Go/Java/TypeScript: "PROTO_<PATH>_<MESSAGE>"
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

func writeMessageToEnvVarPy(b *strings.Builder, msg model.Message, pf model.ProtoFile) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)
	pyMsgClass := msg.Name

	fmt.Fprintf(b, "def %s_to_env_var(msg: '%s') -> dict[str, str]:\n", strutil.ToSnakeCase(msg.Name), pyMsgClass)
	fmt.Fprintf(b, "    \"\"\"Converts %s to environment variable key-value pairs.\"\"\"\n", msg.Name)
	b.WriteString("    result: dict[str, str] = {}\n")

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownTypePy(field.TypePackage) {
			continue
		}

		envKey := prefix + "_" + strings.ToUpper(field.Name)
		accessor := pyFieldGet("msg", strutil.ToSnakeCase(field.Name))

		switch {
		case field.IsEnum:
			writeEnumFieldToEnvVarPy(b, field, accessor, envKey)
		case field.IsMessage:
			writeMessageFieldToEnvVarPy(b, field, accessor)
		default:
			writeScalarFieldToEnvVarPy(b, field, accessor, envKey)
		}
	}

	b.WriteString("    return result\n\n")
}

func writeScalarFieldToEnvVarPy(b *strings.Builder, field model.Field, accessor, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarToEnvVarPy(b, field, accessor, envKey)
		return
	}

	switch field.Type {
	case "string":
		if field.IsOptional {
			fmt.Fprintf(b, "    if %s is not None and %s != '':\n        result['%s'] = %s\n", accessor, accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "    result['%s'] = %s\n", envKey, accessor)
		}
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64":
		if field.IsOptional {
			fmt.Fprintf(b, "    if %s is not None:\n        result['%s'] = str(%s)\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "    result['%s'] = str(%s)\n", envKey, accessor)
		}
	case "float", "double":
		if field.IsOptional {
			fmt.Fprintf(b, "    if %s is not None:\n        result['%s'] = str(%s)\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "    result['%s'] = str(%s)\n", envKey, accessor)
		}
	case "bool":
		if field.IsOptional {
			fmt.Fprintf(b, "    if %s is not None:\n        result['%s'] = str(%s).lower()\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "    result['%s'] = str(%s).lower()\n", envKey, accessor)
		}
	case "bytes":
		if field.IsOptional {
			fmt.Fprintf(b, "    if %s is not None:\n        import base64\n        result['%s'] = base64.b64encode(%s).decode('utf-8')\n", accessor, envKey, accessor)
		} else {
			fmt.Fprintf(b, "    import base64\n    result['%s'] = base64.b64encode(%s).decode('utf-8')\n", envKey, accessor)
		}
	}
}

func writeRepeatedScalarToEnvVarPy(b *strings.Builder, field model.Field, accessor, envKey string) {
	switch field.Type {
	case "string":
		fmt.Fprintf(b, "    if len(%s) > 0:\n        result['%s'] = ','.join(%s)\n", accessor, envKey, accessor)
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64",
		"float", "double":
		fmt.Fprintf(b, "    if len(%s) > 0:\n        result['%s'] = ','.join(str(v) for v in %s)\n", accessor, envKey, accessor)
	case "bool":
		fmt.Fprintf(b, "    if len(%s) > 0:\n        result['%s'] = ','.join(str(v).lower() for v in %s)\n", accessor, envKey, accessor)
	}
}

func writeEnumFieldToEnvVarPy(b *strings.Builder, field model.Field, accessor, envKey string) {
	toStringFunc := strutil.ToSnakeCase(field.Type) + "_to_string"

	if field.IsRepeated {
		fmt.Fprintf(b, "    if len(%s) > 0:\n        result['%s'] = ','.join(%s(v) for v in %s)\n",
			accessor, envKey, toStringFunc, accessor)
		return
	}

	if field.IsOptional {
		fmt.Fprintf(b, "    if %s is not None:\n        result['%s'] = %s(%s)\n", accessor, envKey, toStringFunc, accessor)
	} else {
		fmt.Fprintf(b, "    result['%s'] = %s(%s)\n", envKey, toStringFunc, accessor)
	}
}

func writeMessageFieldToEnvVarPy(b *strings.Builder, field model.Field, accessor string) {
	toEnvVarFunc := strutil.ToSnakeCase(field.Type) + "_to_env_var"
	fmt.Fprintf(b, "    if %s is not None:\n        result.update(%s(%s))\n", accessor, toEnvVarFunc, accessor)
}

func writeMessageFromEnvVarPy(b *strings.Builder, msg model.Message, pf model.ProtoFile) {
	prefix := computeEnvVarPrefix(pf.RelPath, msg.Name)
	pyMsgClass := msg.Name

	fmt.Fprintf(b, "def %s_from_env_var(env: dict[str, str]) -> '%s':\n", strutil.ToSnakeCase(msg.Name), pyMsgClass)
	fmt.Fprintf(b, "    \"\"\"Creates %s from environment variable key-value pairs.\"\"\"\n", msg.Name)
	fmt.Fprintf(b, "    result = %s()\n", pyMsgClass)

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownTypePy(field.TypePackage) {
			continue
		}

		rawFieldName := strutil.ToSnakeCase(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)

		switch {
		case field.IsEnum:
			writeEnumFieldFromEnvVarPy(b, field, rawFieldName, envKey)
		case field.IsMessage:
			writeMessageFieldFromEnvVarPy(b, field, rawFieldName)
		default:
			writeScalarFieldFromEnvVarPy(b, field, rawFieldName, envKey)
		}
	}

	b.WriteString("    return result\n\n")
}

func writeScalarFieldFromEnvVarPy(b *strings.Builder, field model.Field, rawFieldName, envKey string) {
	if field.IsRepeated {
		writeRepeatedScalarFromEnvVarPy(b, field, rawFieldName, envKey)
		return
	}

	valGet := fmt.Sprintf("env.get('%s', '')", envKey)
	set := func(expr string) string { return pyFieldSet("result", rawFieldName, expr) }

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, set("_val"))
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, set("int(_val)"))
	case "uint32", "fixed32", "uint64", "fixed64":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, set("int(_val)"))
	case "float", "double":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, set("float(_val)"))
	case "bool":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, set("_val.lower() == 'true'"))
	case "bytes":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        import base64\n        %s\n", valGet, set("base64.b64decode(_val)"))
	}
}

func writeRepeatedScalarFromEnvVarPy(b *strings.Builder, field model.Field, rawFieldName, envKey string) {
	valGet := fmt.Sprintf("env.get('%s', '')", envKey)
	target := pyFieldGet("result", rawFieldName)

	switch field.Type {
	case "string":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s.extend(_val.split(','))\n", valGet, target)
	case "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64",
		"uint32", "fixed32", "uint64", "fixed64":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s.extend(int(s.strip()) for s in _val.split(','))\n", valGet, target)
	case "float", "double":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s.extend(float(s.strip()) for s in _val.split(','))\n", valGet, target)
	case "bool":
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s.extend(s.strip().lower() == 'true' for s in _val.split(','))\n", valGet, target)
	}
}

func writeEnumFieldFromEnvVarPy(b *strings.Builder, field model.Field, rawFieldName, envKey string) {
	parseFunc := "parse_" + strutil.ToSnakeCase(field.Type)
	valGet := fmt.Sprintf("env.get('%s', '')", envKey)

	if field.IsRepeated {
		fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s.extend(%s(s.strip()) for s in _val.split(','))\n",
			valGet, pyFieldGet("result", rawFieldName), parseFunc)
		return
	}

	fmt.Fprintf(b, "    _val = %s\n    if _val:\n        %s\n", valGet, pyFieldSet("result", rawFieldName, parseFunc+"(_val)"))
}

func writeMessageFieldFromEnvVarPy(b *strings.Builder, field model.Field, rawFieldName string) {
	fromEnvVarFunc := strutil.ToSnakeCase(field.Type) + "_from_env_var"
	fmt.Fprintf(b, "    _sub = %s(env)\n    if _sub.ByteSize() > 0:\n        %s.CopyFrom(_sub)\n",
		fromEnvVarFunc, pyFieldGet("result", rawFieldName))
}

// isWellKnownTypePy returns true if the type package is a protobuf well-known type.
func isWellKnownTypePy(typePackage string) bool {
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb",
		"google.protobuf":
		return true
	}
	return false
}
