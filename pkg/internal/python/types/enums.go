// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// pyEnumValueData holds pre-computed data for a single enum value.
type pyEnumValueData struct {
	ProtoName       string // e.g., "LOG_LEVEL_INFO"
	NormalizedValue string // e.g., "info" (for valid_values and to_string)
	NormalizedKey   string // e.g., "info" (for parse mapping key)
}

// pyEnumData holds pre-computed data for a single enum.
type pyEnumData struct {
	EnumName    string
	HelperName  string
	PackageName string
	ValidValues []pyEnumValueData
}

// pyEnumsData holds the template data for all enums.
type pyEnumsData struct {
	PackageName string
	Enums       []pyEnumData
}

// writeEnumHelpers writes enum helper classes to the combined helpers.py builder
func writeEnumHelpers(b *strings.Builder, pf model.ProtoFile, protoFilename string) error {
	if len(pf.Enums) == 0 {
		return nil
	}

	var enumDataList []pyEnumData

	for _, enum := range pf.Enums {
		// Filter out sentinel values (UNDEFINED/NO_ERROR/UNSPECIFIED)
		var validValues []pyEnumValueData
		for _, v := range enum.Values {
			if model.IsEnumSentinel(v.Name) {
				continue
			}

			var friendlyValue string
			if v.JsonValue != "" {
				friendlyValue = v.JsonValue
			} else if enum.Name == "KeyType" {
				friendlyValue = strings.TrimPrefix(v.Name, "KEY_TYPE_")
			} else {
				friendlyValue = strings.ToLower(strings.ReplaceAll(v.Name, "_", "-"))
			}

			normalizedValue := strings.ToLower(strings.ReplaceAll(friendlyValue, "_", "-"))

			validValues = append(validValues, pyEnumValueData{
				ProtoName:       v.Name,
				NormalizedValue: normalizedValue,
				NormalizedKey:   normalizedValue,
			})
		}

		if len(validValues) == 0 {
			continue
		}

		enumDataList = append(enumDataList, pyEnumData{
			EnumName:    enum.Name,
			HelperName:  enum.Name + "Helper",
			PackageName: protoFilename,
			ValidValues: validValues,
		})
	}

	if len(enumDataList) == 0 {
		return nil
	}

	data := pyEnumsData{
		PackageName: protoFilename,
		Enums:       enumDataList,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()

	result, err := tmplutil.ExecuteTemplate(typeFS, "enums.py.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute enums template: %w", err)
	}

	b.Write(result)
	return nil
}
