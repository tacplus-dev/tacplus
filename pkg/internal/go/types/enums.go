// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// enumTemplateData holds pre-computed data for the enums template.
type enumTemplateData struct {
	EnumName          string
	LowerEnumName     string
	CaseDesc          string
	ZeroValue         string
	NonSentinelValues []enumValueData
	AllValues         []enumValueData
}

// enumValueData holds pre-computed data for a single enum value.
type enumValueData struct {
	Name                 string
	FriendlyName         string
	CaseStatement        string // e.g. `case "token":` or `case "my-value", "my_value":`
	LiberalCaseStatement string // e.g. `case "token", "path-type-token":`
}

func generateGoEnumHelpers(b *strings.Builder, enum model.Enum) error {
	// Compute the zero value
	zeroValue := enum.Name + "(0)"
	if len(enum.Values) > 0 && enum.Values[0].Number == 0 {
		zeroValue = enum.Name + "_" + enum.Values[0].Name
	}

	// Case description for ToString
	caseDesc := "lowercase"
	if enum.Name == "KeyType" {
		caseDesc = "RFC 7517 compliant uppercase"
	}

	// Build non-sentinel values with pre-computed data
	var nonSentinel []enumValueData
	for _, v := range enum.Values {
		if model.IsEnumSentinel(v.Name) || v.Number == 0 {
			continue
		}

		friendlyName := enumFriendlyString(enum, v)
		lowerName := strings.ToLower(friendlyName)
		hyphenName := strings.ReplaceAll(lowerName, "_", "-")

		// Full proto enum name (e.g. "token_source_key" from "TOKEN_SOURCE_KEY")
		fullName := strings.ToLower(v.Name)
		fullNameHyphen := strings.ReplaceAll(fullName, "_", "-")

		// Parse case statement — includes short name AND full proto name
		// so that both "key" and "token-source-key" / "TOKEN_SOURCE_KEY" work
		var caseFormats []string
		caseFormats = append(caseFormats, hyphenName)
		if strings.Contains(lowerName, "_") && lowerName != hyphenName {
			caseFormats = append(caseFormats, lowerName)
		}
		if fullNameHyphen != hyphenName && fullNameHyphen != lowerName {
			caseFormats = append(caseFormats, fullNameHyphen)
		}
		caseStmt := "case \"" + strings.Join(caseFormats, "\", \"") + "\":"

		// Liberal parse case statement
		var formats []string
		formats = append(formats, friendlyName)
		if fullNameHyphen != friendlyName {
			formats = append(formats, fullNameHyphen)
		}
		liberalCase := "case \"" + strings.Join(formats, "\", \"") + "\":"

		nonSentinel = append(nonSentinel, enumValueData{
			Name:                 v.Name,
			FriendlyName:         friendlyName,
			CaseStatement:        caseStmt,
			LiberalCaseStatement: liberalCase,
		})
	}

	// Build all values
	var allValues []enumValueData
	for _, v := range enum.Values {
		allValues = append(allValues, enumValueData{
			Name: v.Name,
		})
	}

	data := enumTemplateData{
		EnumName:          enum.Name,
		LowerEnumName:     strings.ToLower(enum.Name),
		CaseDesc:          caseDesc,
		ZeroValue:         zeroValue,
		NonSentinelValues: nonSentinel,
		AllValues:         allValues,
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "enums.go.tmpl", funcMap, data)
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

// enumFriendlyString converts an enum value to its friendly string representation.
func enumFriendlyString(enum model.Enum, value model.EnumValue) string {
	// Use JsonValue from annotation if present
	if value.JsonValue != "" {
		return value.JsonValue
	}

	// Otherwise, generate friendly name from enum value name
	prefix := strutil.ToSnakeCase(enum.Name) + "_"
	name := strings.TrimPrefix(value.Name, strings.ToUpper(prefix))

	// Special case for KeyType: RFC 7517 requires uppercase "RSA", "EC", "OKP"
	if enum.Name == "KeyType" {
		name = strings.ToUpper(name)
	} else {
		name = strings.ToLower(name)
	}

	name = strings.ReplaceAll(name, "_", "-")
	return name
}
