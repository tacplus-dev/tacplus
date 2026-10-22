// Copyright (c) 2026 Seabeam LLC
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
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/templates"
)

// hasFieldDefaults returns true if any message in the proto file has defaults
// (see messagesWithDefaults).
func hasFieldDefaults(pf model.ProtoFile, hasDefaults map[string]bool) bool {
	for _, msg := range pf.Messages {
		if hasDefaults[model.DefaultsKey(pf.Package, msg.Name)] {
			return true
		}
	}
	return false
}

// constantsTemplateData holds all pre-computed data for the constants template.
type constantsTemplateData struct {
	Header        string
	PackagePath   string
	NeedsDuration bool
	ConstantsBody string
}

// generateConstants generates Constants.java file with constant definitions
func generateConstants(pf model.ProtoFile, baseDir, subdir string) error {
	packagePath := strings.ReplaceAll(subdir, "/", ".")
	outputDir := filepath.Join(baseDir, subdir)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	// Check if we need Duration import
	needsDuration := false
	for _, c := range pf.Constants {
		if strings.Contains(c.Type, "Duration") || strings.Contains(c.Value, "Duration") { // Go code representation
			needsDuration = true
			break
		}
	}

	// Pre-render the constants body
	var body strings.Builder

	// Generate constants
	for _, c := range pf.Constants {
		javaName := c.Name
		javaType := mapTypeToJava(c.Type)

		if c.Type == "" || javaType == "long" {
			javaType = inferJavaTypeFromValue(c.Value)
		}

		javaValue := convertValueToJava(c.Value, javaType)
		fmt.Fprintf(&body, "public static final %s %s = %s;\n", javaType, javaName, javaValue)
	}

	// Generate constants from field-level default annotations.
	// Build duplicate field name map to avoid collisions when multiple messages
	// have fields with the same name (e.g., ClickHouseConfig.retention_days and
	// DgraphConfig.retention_days both producing DEFAULT_RETENTION_DAYS).
	dupFields := findDuplicateDefaultFields(pf.Messages)
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				annVal, ok := field.Annotations[annKey]
				if !ok {
					continue
				}
				javaName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				if annKey == "default_host" {
					javaName += "_PORT"
				}
				javaType, javaValue := defaultAnnotationToJava(annKey, annVal)
				fmt.Fprintf(&body, "public static final %s %s = %s;\n", javaType, javaName, javaValue)
			}
		}
	}

	data := constantsTemplateData{
		Header:        header.GeneratedHeader(consts.LangJava, pf.RelPath),
		PackagePath:   packagePath,
		NeedsDuration: needsDuration,
		ConstantsBody: body.String(),
	}

	// Load and execute template
	typeFS, err := fs.Sub(templates.FS, "java/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, "constants.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to read constants template: %w", err)
	}
	tmpl, err := template.New("constants").Parse(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse constants template: %w", err)
	}

	var result strings.Builder
	if err := tmpl.Execute(&result, data); err != nil {
		return fmt.Errorf("failed to execute constants template: %w", err)
	}

	outputPath := filepath.Join(outputDir, "Constants.java")
	return os.WriteFile(outputPath, []byte(result.String()), 0644)
}

// isNumeric checks if a string represents an integer (no decimal point)
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '-' && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isFloat checks if a string represents a floating-point number (contains a decimal point)
func isFloat(s string) bool {
	if s == "" {
		return false
	}
	hasDot := false
	for i, r := range s {
		if r == '-' && i == 0 {
			continue
		}
		if r == '.' {
			if hasDot {
				return false // multiple dots
			}
			hasDot = true
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return hasDot
}

// inferJavaTypeFromValue determines the Java type from the constant's value string.
// This is used when the proto constant has no explicit type annotation.
func inferJavaTypeFromValue(value string) string {
	// Strip surrounding quotes if present
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		// Quoted string - but check inner content for typed literals
		inner := trimmed[1 : len(trimmed)-1]
		// If the inner value is a boolean
		if inner == "true" || inner == "false" {
			return "boolean"
		}
		// If the inner value is a float
		if isFloat(inner) {
			return "double"
		}
		// If the inner value is an integer
		if isNumeric(inner) {
			return "long"
		}
		return "String"
	}

	// Unquoted value
	if trimmed == "true" || trimmed == "false" {
		return "boolean"
	}
	if isFloat(trimmed) {
		return "double"
	}
	if isNumeric(trimmed) {
		return "long"
	}
	return "String"
}

// mapTypeToJava converts Go types to Java types.
// Uses Contains on Go type strings because these are Go code representations
// from the constant generator (e.g. "time.Duration"), not proto descriptors.
func mapTypeToJava(goType string) string {
	switch {
	case goType == "string":
		return "String"
	case goType == "int", goType == "int64":
		return "long"
	case goType == "int32":
		return "int"
	case goType == "float32", goType == "float64":
		return "double"
	case goType == "bool":
		return "boolean"
	case strings.Contains(goType, "Duration"):
		return "Duration"
	case goType == "":
		// No explicit type - caller should use inferJavaTypeFromValue
		return "long"
	default:
		return "long"
	}
}

// convertValueToJava converts Go value syntax to Java syntax.
// Uses Contains to detect Go duration literals (e.g. "30 * time.Second") for
// cross-language conversion; these are Go code strings, not proto descriptors.
func convertValueToJava(value, javaType string) string {
	// Handle string constants
	if javaType == "String" && !strings.HasPrefix(value, "\"") {
		return fmt.Sprintf("\"%s\"", value)
	}

	// Handle Duration
	if strings.Contains(value, "time.Second") ||
		strings.Contains(value, "time.Minute") ||
		strings.Contains(value, "time.Hour") {
		value = strings.ReplaceAll(value, "time.", "")
		value = strings.ReplaceAll(value, "*", "* ")

		if strings.Contains(value, "Second") {
			parts := strings.Split(value, "Second")
			if len(parts) > 0 {
				numPart := strings.TrimSpace(parts[0])
				numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
				if numPart != "" {
					return fmt.Sprintf("Duration.ofSeconds(%s)", numPart)
				}
			}
		} else if strings.Contains(value, "Minute") {
			parts := strings.Split(value, "Minute")
			if len(parts) > 0 {
				numPart := strings.TrimSpace(parts[0])
				numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
				if numPart != "" {
					return fmt.Sprintf("Duration.ofMinutes(%s)", numPart)
				}
			}
		} else if strings.Contains(value, "Hour") {
			parts := strings.Split(value, "Hour")
			if len(parts) > 0 {
				numPart := strings.TrimSpace(parts[0])
				numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
				if numPart != "" {
					return fmt.Sprintf("Duration.ofHours(%s)", numPart)
				}
			}
		}
	}

	// Handle boolean constants
	if javaType == "boolean" {
		v := strings.Trim(value, "\"")
		if v == "true" || v == "false" {
			return v
		}
		return value
	}

	// Handle double constants
	if javaType == "double" {
		v := strings.Trim(value, "\"")
		// Ensure it has a decimal point for clarity
		if !strings.Contains(v, ".") {
			v = v + ".0"
		}
		return v
	}

	// Add L suffix for long literals
	if javaType == "long" {
		v := strings.Trim(value, "\"")
		if !strings.HasSuffix(v, "L") {
			return v + "L"
		}
		return v
	}

	return value
}

// findDuplicateDefaultFields returns a set of field names that appear with default
// annotations in more than one message within the same proto file.
func findDuplicateDefaultFields(messages []model.Message) map[string]bool {
	var entries []struct {
		MsgName    string
		FieldNames []string
	}
	for _, msg := range messages {
		var names []string
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				if _, ok := field.Annotations[annKey]; ok {
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

// defaultAnnotationKeys is the ordered set of field-level default annotations
// that should be emitted as Java constants.
var defaultAnnotationKeys = []string{
	"default_string",
	"default_int",
	"default_float",
	"default_bool",
	"default_host",
}

// defaultAnnotationToJava converts a field-level default annotation into the
// corresponding Java type and formatted literal value.
func defaultAnnotationToJava(annKey string, annVal model.AnnotationValue) (javaType, javaValue string) {
	switch annKey {
	case "default_string":
		return "String", fmt.Sprintf("%q", annVal.StringValue)
	case "default_int":
		return "long", fmt.Sprintf("%dL", annVal.IntValue)
	case "default_float":
		s := fmt.Sprintf("%g", annVal.FloatValue)
		// Ensure the literal contains a decimal point for clarity
		if !strings.Contains(s, ".") {
			s = s + ".0"
		}
		return "double", s
	case "default_bool":
		return "boolean", fmt.Sprintf("%t", annVal.BoolValue)
	case "default_host":
		if annVal.HostValue != nil {
			return "int", fmt.Sprintf("%d", annVal.HostValue.Port)
		}
		return "int", "0"
	default:
		return "String", fmt.Sprintf("%q", annVal.StringValue)
	}
}
