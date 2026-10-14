// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// pyConstantData holds data for a single constant in the template.
type pyConstantData struct {
	Name  string
	Type  string // empty if no type annotation
	Value string
}

// pyConstantsData holds the template data for the constants section.
type pyConstantsData struct {
	Constants     []pyConstantData
	FieldDefaults []pyConstantData
}

// writeConstants writes the constants section to the combined helpers.py builder
func writeConstants(b *strings.Builder, pf model.ProtoFile) error {
	var constants []pyConstantData
	for _, c := range pf.Constants {
		pythonName := c.Name
		pythonValue := convertValueToPython(c.Value)
		pythonType := ""
		if c.Type != "" {
			pythonType = mapTypeToPython(c.Type)
		}
		constants = append(constants, pyConstantData{
			Name:  pythonName,
			Type:  pythonType,
			Value: pythonValue,
		})
	}

	dupFields := findDuplicateDefaultFieldsPy(pf.Messages)
	var fieldDefaults []pyConstantData
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, annKey := range defaultAnnotationKeys {
				annVal, ok := field.Annotations[annKey]
				if !ok {
					continue
				}
				if annKey == "default_host" {
					if annVal.HostValue != nil {
						constName := strutil.DefaultConstName(msg.Name, field.Name, dupFields) + "_PORT"
						fieldDefaults = append(fieldDefaults, pyConstantData{
							Name:  constName,
							Type:  "int",
							Value: fmt.Sprintf("%d", annVal.HostValue.Port),
						})
					}
					continue
				}
				pythonName := strutil.DefaultConstName(msg.Name, field.Name, dupFields)
				pythonType, pythonValue := defaultAnnotationToPython(annKey, annVal)
				fieldDefaults = append(fieldDefaults, pyConstantData{
					Name:  pythonName,
					Type:  pythonType,
					Value: pythonValue,
				})
			}
		}
	}

	data := pyConstantsData{
		Constants:     constants,
		FieldDefaults: fieldDefaults,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "constants.py.tmpl", tmplutil.BaseFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute constants template: %w", err)
	}

	b.Write(result)
	return nil
}

// mapTypeToPython converts Go types to Python type hints.
// Uses Contains on Go type strings (e.g. "time.Duration") because these are
// Go code representations from the constant generator, not proto descriptors.
func mapTypeToPython(goType string) string {
	switch {
	case goType == "string":
		return "str"
	case goType == "bool":
		return "bool"
	case goType == "int", goType == "int64", goType == "int32":
		return "int"
	case strings.Contains(goType, "Duration"):
		return "timedelta"
	default:
		return "int"
	}
}

// convertValueToPython converts Go value syntax to Python syntax.
// Uses Contains to detect Go duration literals for cross-language conversion.
func convertValueToPython(value string) string {
	// Handle Duration/timedelta
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
					return fmt.Sprintf("timedelta(seconds=%s)", numPart)
				}
			}
		} else if strings.Contains(value, "Minute") {
			parts := strings.Split(value, "Minute")
			if len(parts) > 0 {
				numPart := strings.TrimSpace(parts[0])
				numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
				if numPart != "" {
					return fmt.Sprintf("timedelta(minutes=%s)", numPart)
				}
			}
		} else if strings.Contains(value, "Hour") {
			parts := strings.Split(value, "Hour")
			if len(parts) > 0 {
				numPart := strings.TrimSpace(parts[0])
				numPart = strings.TrimSpace(strings.ReplaceAll(numPart, "*", ""))
				if numPart != "" {
					return fmt.Sprintf("timedelta(hours=%s)", numPart)
				}
			}
		}
	}

	// Convert Go-style booleans to Python
	if value == "true" {
		return "True"
	}
	if value == "false" {
		return "False"
	}

	return value
}

// hasFieldDefaults returns true if any message field in pf carries a
// default_string, default_int, default_float, or default_bool annotation.
func hasFieldDefaults(pf model.ProtoFile) bool {
	return len(messagesWithDefaults(pf)) > 0
}

// findDuplicateDefaultFieldsPy wraps strutil.FindDuplicateDefaultFields for Python.
func findDuplicateDefaultFieldsPy(messages []model.Message) map[string]bool {
	var entries []struct {
		MsgName    string
		FieldNames []string
	}
	for _, msg := range messages {
		var names []string
		for _, field := range msg.Fields {
			for _, key := range defaultAnnotationKeys {
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

// defaultAnnotationKeys is the ordered set of field-level default annotations
// that should be emitted as Python constants.
var defaultAnnotationKeys = []string{
	"default_string",
	"default_int",
	"default_float",
	"default_bool",
	"default_host",
}

// defaultAnnotationToPython converts a field-level default annotation into the
// corresponding Python type hint and formatted literal value.
func defaultAnnotationToPython(annKey string, annVal model.AnnotationValue) (pythonType, pythonValue string) {
	switch annKey {
	case "default_string":
		return "str", fmt.Sprintf("%q", annVal.StringValue)
	case "default_int":
		return "int", fmt.Sprintf("%d", annVal.IntValue)
	case "default_float":
		return "float", pyFloatLiteral(annVal.FloatValue)
	case "default_bool":
		if annVal.BoolValue {
			return "bool", "True"
		}
		return "bool", "False"
	default:
		return "str", fmt.Sprintf("%q", annVal.StringValue)
	}
}
