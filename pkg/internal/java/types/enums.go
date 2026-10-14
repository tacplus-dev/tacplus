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

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// enumParseCase holds pre-computed data for a parse switch case.
type enumParseCase struct {
	NormalizedAliases []string
	ValueName         string
}

// enumToStringCase holds pre-computed data for a toString switch case.
type enumToStringCase struct {
	ValueName   string
	ReturnValue string
}

// enumShortNameEntry holds a quoted short name with last-item flag.
type enumShortNameEntry struct {
	Quoted string
	IsLast bool
}

// enumHelperData holds all pre-computed data for the enum helper template.
type enumHelperData struct {
	Header          string
	PackagePath     string
	HelperName      string
	FullEnumName    string
	ParseCases      []enumParseCase
	ToStringCases   []enumToStringCase
	ValidShortNames []enumShortNameEntry
}

// generateEnumHelpers generates Java helper classes for enums
func generateEnumHelpers(pf model.ProtoFile, baseDir, subdir string) error {
	if len(pf.Enums) == 0 {
		return nil
	}

	for _, enum := range pf.Enums {
		if err := generateEnumHelper(pf, enum, baseDir, subdir); err != nil {
			return err
		}
	}

	return nil
}

// generateEnumHelper generates a helper class for a single enum
func generateEnumHelper(pf model.ProtoFile, enum model.Enum, baseDir, subdir string) error {
	// Filter out sentinel values (UNDEFINED/NO_ERROR/UNSPECIFIED)
	var validValues []model.EnumValue
	for _, v := range enum.Values {
		if !model.IsEnumSentinel(v.Name) {
			validValues = append(validValues, v)
		}
	}

	if len(validValues) == 0 {
		return nil
	}

	// Detect actual outer class name (may be *OuterClass for naming conflicts)
	outerClassName := detectOuterClassName(baseDir, subdir, pf.RelPath)

	// Fully qualified enum name
	fullEnumName := outerClassName + "." + enum.Name
	helperName := enum.Name + "Helper"
	packagePath := strings.ReplaceAll(subdir, "/", ".")

	// Pre-compute parse cases
	var parseCases []enumParseCase
	for _, v := range validValues {
		pc := enumParseCase{
			NormalizedAliases: quoteEnumAliases(javaEnumParseAliases(enum, v)),
			ValueName:         v.Name,
		}

		parseCases = append(parseCases, pc)
	}

	// Pre-compute toString cases
	var toStringCases []enumToStringCase
	for _, v := range validValues {
		shortName := extractEnumShortName(enum.Name, v.Name)
		tc := enumToStringCase{
			ValueName: v.Name,
		}
		if v.JsonValue != "" {
			tc.ReturnValue = fmt.Sprintf("%q", v.JsonValue)
		} else {
			tc.ReturnValue = fmt.Sprintf("%q", shortName)
		}
		toStringCases = append(toStringCases, tc)
	}

	// Pre-compute valid short names
	var validShortNames []enumShortNameEntry
	for i, v := range validValues {
		shortName := extractEnumShortName(enum.Name, v.Name)
		validShortNames = append(validShortNames, enumShortNameEntry{
			Quoted: fmt.Sprintf("%q", shortName),
			IsLast: i == len(validValues)-1,
		})
	}

	data := enumHelperData{
		Header:          header.GeneratedHeader(consts.LangJava, pf.RelPath),
		PackagePath:     packagePath,
		HelperName:      helperName,
		FullEnumName:    fullEnumName,
		ParseCases:      parseCases,
		ToStringCases:   toStringCases,
		ValidShortNames: validShortNames,
	}

	// Load and execute template
	typeFS, err := fs.Sub(templates.FS, "java/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, "enums.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to read enums template: %w", err)
	}
	tmpl, err := template.New("enums").Parse(string(content))
	if err != nil {
		return fmt.Errorf("failed to parse enums template: %w", err)
	}

	var result strings.Builder
	if err := tmpl.Execute(&result, data); err != nil {
		return fmt.Errorf("failed to execute enums template: %w", err)
	}

	// Write to file
	outputDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	filename := helperName + ".java"
	outputPath := filepath.Join(outputDir, filename)
	return os.WriteFile(outputPath, []byte(result.String()), 0644)
}

// extractEnumShortName extracts the short name from an enum value
// For CLOUD_VENDOR_AWS -> "aws"
// For LOG_LEVEL_INFO -> "info"
// For UNDEFINED -> "undefined"
func extractEnumShortName(enumName, valueName string) string {
	// Convert enum name to snake_case uppercase
	// CloudVendor -> CLOUD_VENDOR
	var enumPrefix string
	for i, r := range enumName {
		if i > 0 && r >= 'A' && r <= 'Z' {
			enumPrefix += "_"
		}
		enumPrefix += string(r)
	}
	enumPrefix = strings.ToUpper(enumPrefix) + "_"

	if strings.HasPrefix(valueName, enumPrefix) {
		// Extract suffix after the prefix
		suffix := strings.TrimPrefix(valueName, enumPrefix)
		return strings.ToLower(suffix)
	}

	// If no prefix match, just lowercase the whole thing
	return strings.ToLower(valueName)
}

func javaEnumParseAliases(enum model.Enum, value model.EnumValue) []string {
	shortName := normalizeJavaEnumToken(extractEnumShortName(enum.Name, value.Name))
	aliases := []string{shortName}
	aliases = appendUniqueString(aliases, normalizeJavaEnumToken(value.Name))
	if value.JsonValue != "" {
		aliases = appendUniqueString(aliases, normalizeJavaEnumToken(value.JsonValue))
	}
	return aliases
}

func normalizeJavaEnumToken(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}

func appendUniqueString(values []string, candidate string) []string {
	if candidate == "" {
		return values
	}
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func quoteEnumAliases(aliases []string) []string {
	quoted := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		quoted = append(quoted, fmt.Sprintf("%q", alias))
	}
	return quoted
}
