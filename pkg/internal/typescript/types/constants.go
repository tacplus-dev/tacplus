// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// NOTE: generateConstants has been consolidated into writeConstants() in parser.go
// which writes directly to the shared helpers.ts buffer.
// The helper functions below are still used by writeConstants().

// isBigIntProtoType returns true if the proto type maps to bigint in TypeScript.
// In @bufbuild/protobuf, 64-bit integer types use bigint instead of number.
func isBigIntProtoType(protoType string) bool {
	switch protoType {
	case "int64", "uint64", "sint64", "fixed64", "sfixed64":
		return true
	default:
		return false
	}
}

// tsDefaultConstant holds a derived constant from a field-level default annotation.
type tsDefaultConstant struct {
	Name    string // UPPER_SNAKE_CASE name, e.g. DEFAULT_PARTITION_COUNT
	TsType  string // TypeScript type: string, number, bigint, boolean
	TsValue string // TypeScript literal, e.g. "hello", 271, 5n, 0.95, true
}

// collectFieldDefaults iterates over all message fields looking for
// default_string, default_int, default_float, default_bool, and default_host annotations
// and returns the corresponding TypeScript constants.
func collectFieldDefaults(pf model.ProtoFile) []tsDefaultConstant {
	dupFields := findDuplicateDefaultFieldsTS(pf.Messages)
	var results []tsDefaultConstant
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool"} {
				av, ok := field.Annotations[key]
				if !ok {
					continue
				}
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)

				var tsType, tsValue string
				switch key {
				case "default_string":
					tsType = "string"
					tsValue = fmt.Sprintf("%q", av.StringValue)
				case "default_int":
					// 64-bit integer proto types map to bigint in TypeScript (@bufbuild/protobuf)
					if isBigIntProtoType(field.Type) {
						tsType = "bigint"
						tsValue = fmt.Sprintf("%dn", av.IntValue)
					} else {
						tsType = "number"
						tsValue = fmt.Sprintf("%d", av.IntValue)
					}
				case "default_float":
					tsType = "number"
					tsValue = formatTsFloat(av.FloatValue)
				case "default_bool":
					tsType = "boolean"
					tsValue = fmt.Sprintf("%t", av.BoolValue)
				}

				results = append(results, tsDefaultConstant{
					Name:    constName,
					TsType:  tsType,
					TsValue: tsValue,
				})
			}
			// default_host: generate a DEFAULT_{FIELD}_PORT constant
			if av, ok := field.Annotations["default_host"]; ok && av.HostValue != nil {
				constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields) + "_PORT"
				results = append(results, tsDefaultConstant{
					Name:    constName,
					TsType:  "number",
					TsValue: fmt.Sprintf("%d", av.HostValue.Port),
				})
			}
		}
	}
	return results
}

// findDuplicateDefaultFieldsTS wraps strutil.FindDuplicateDefaultFields for TypeScript.
func findDuplicateDefaultFieldsTS(messages []model.Message) map[string]bool {
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
// default_string, default_int, default_float, default_bool, or default_host annotation.
func hasFieldDefaults(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, key := range []string{"default_string", "default_int", "default_float", "default_bool", "default_host"} {
				if _, ok := field.Annotations[key]; ok {
					return true
				}
			}
		}
	}
	return false
}

// mapConstTypeToTypeScript converts a Go-style type string (from the proto
// constant definition) into its TypeScript equivalent. If the type cannot be
// determined, an empty string is returned so the caller can omit the annotation.
func mapConstTypeToTypeScript(goType, value string) string {
	switch {
	case goType == "string":
		return "string"
	case goType == "bool":
		return "boolean"
	case goType == "int", goType == "int32",
		goType == "uint32",
		goType == "float32", goType == "float64":
		return "number"
	case isBigIntProtoType(goType):
		return "bigint"
	case strings.Contains(goType, "Duration"):
		return "number" // durations are expressed as milliseconds in TS
	case goType == "":
		// Infer from value
		return inferTsTypeFromValue(value)
	default:
		return "number"
	}
}

// inferTsTypeFromValue guesses the TypeScript type from a constant's literal value.
func inferTsTypeFromValue(value string) string {
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return "string"
	}
	if value == "true" || value == "false" {
		return "boolean"
	}
	return "number"
}

// convertConstValueToTypeScript converts a Go-style constant value into valid
// TypeScript syntax. Duration values are converted to milliseconds.
// Uses Contains on Go type/value strings (e.g. "Duration", "time.Second") because
// these are Go code representations from the constant generator, not proto descriptors.
func convertConstValueToTypeScript(value, goType string) string {
	// Handle string literals -- already valid TS
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value
	}

	// Handle time.Duration -- convert to milliseconds
	if strings.Contains(goType, "Duration") ||
		strings.Contains(value, "time.Second") ||
		strings.Contains(value, "time.Minute") ||
		strings.Contains(value, "time.Hour") {
		return convertDurationToMs(value)
	}

	// Handle bigint types -- append 'n' suffix
	if isBigIntProtoType(goType) {
		return value + "n"
	}

	// Plain number or boolean -- return as-is
	return value
}

// convertDurationToMs converts a Go duration expression like "30 * time.Second"
// to a TypeScript millisecond literal like "30 * 1000".
func convertDurationToMs(value string) string {
	if strings.Contains(value, "*") {
		parts := strings.Split(value, "*")
		if len(parts) == 2 {
			num := strings.TrimSpace(parts[0])
			unit := strings.TrimSpace(parts[1])
			switch unit {
			case "time.Second":
				return num + "* 1000"
			case "time.Minute":
				return num + "* 60000"
			case "time.Hour":
				return num + "* 3600000"
			}
		}
	}
	// Fallback for bare units
	if strings.TrimSpace(value) == "time.Second" {
		return "1000"
	}
	if strings.TrimSpace(value) == "time.Minute" {
		return "60000"
	}
	if strings.TrimSpace(value) == "time.Hour" {
		return "3600000"
	}
	return "0"
}

// formatTsFloat formats a float64 value as a TypeScript number literal,
// ensuring it always contains a decimal point (e.g. "1.0" not "1").
// Contains checks on the formatted string itself (not proto data).
func formatTsFloat(f float64) string {
	s := fmt.Sprintf("%g", f)
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}
