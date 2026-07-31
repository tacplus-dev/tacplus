// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// isIntegerType checks if a field type is an integer type
func isIntegerType(typ string) bool {
	return typ == "int32" || typ == "int64" ||
		typ == "uint32" || typ == "uint64" ||
		typ == "sint32" || typ == "sint64" ||
		typ == "fixed32" || typ == "fixed64" ||
		typ == "sfixed32" || typ == "sfixed64"
}

// isFloatType checks if a field type is a floating-point type
func isFloatType(typ string) bool {
	return typ == "float" || typ == "double"
}

// isNumericType checks if a field type is any numeric type
func isNumericType(typ string) bool {
	return isIntegerType(typ) || isFloatType(typ)
}

// pyMapFieldData holds pre-computed data for a from_map field.
type pyMapFieldData struct {
	FieldName       string
	PythonEnumValue string
	IsRepeated      bool
	IsInteger       bool
	IsFloat         bool
}

// pyToMapFieldData holds pre-computed data for a to_map field.
type pyToMapFieldData struct {
	FieldName      string
	ProtoFieldName string
	JsonKey        string
	IsRepeated     bool
	IsNumeric      bool
}

// pyMapMessageData holds pre-computed data for a single message's map functions.
type pyMapMessageData struct {
	TypeName    string
	FuncName    string // snake_case
	KeyEnumType string
	Strict      bool
	Fields      []pyMapFieldData
	HasToMap    bool
	ToMapFields []pyToMapFieldData
}

// pyMapData holds the template data for all map functions.
type pyMapData struct {
	MapMessages []pyMapMessageData
}

// writeFromMap writes from_map/to_map functions to the combined helpers.py builder
func writeFromMap(b *strings.Builder, pf model.ProtoFile) error {
	hasParamMapMessages := false
	for _, msg := range pf.Messages {
		if msg.ParamMapConfig != nil {
			hasParamMapMessages = true
			break
		}
	}

	if !hasParamMapMessages {
		return nil
	}

	var mapMessages []pyMapMessageData

	for _, msg := range pf.Messages {
		if msg.ParamMapConfig == nil {
			continue
		}

		config := msg.ParamMapConfig
		keyEnumType := config.KeyEnum

		// Find the key enum
		var keyEnum *model.Enum
		for i := range pf.Enums {
			if pf.Enums[i].Name == keyEnumType {
				keyEnum = &pf.Enums[i]
				break
			}
		}

		if keyEnum == nil {
			continue
		}

		// Build from_map fields
		var fields []pyMapFieldData
		for _, field := range msg.Fields {
			if field.ParamKey == "" {
				continue
			}

			pythonEnumValue := keyEnumType + "." + field.ParamKey

			fields = append(fields, pyMapFieldData{
				FieldName:       escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				PythonEnumValue: pythonEnumValue,
				IsRepeated:      field.IsRepeated,
				IsInteger:       isIntegerType(field.Type),
				IsFloat:         strings.Contains(field.Type, "double") || strings.Contains(field.Type, "float"), // proto scalar type names
			})
		}

		// Build to_map fields
		enumValueToJsonValue := make(map[string]string)
		for _, enumVal := range keyEnum.Values {
			if enumVal.JsonValue != "" {
				enumValueToJsonValue[enumVal.Name] = enumVal.JsonValue
			}
		}

		var toMapFields []pyToMapFieldData
		for _, field := range msg.Fields {
			if field.ParamKey == "" {
				continue
			}
			jsonKey, hasJsonKey := enumValueToJsonValue[field.ParamKey]
			if !hasJsonKey {
				continue
			}

			toMapFields = append(toMapFields, pyToMapFieldData{
				FieldName:      escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				ProtoFieldName: field.Name,
				JsonKey:        jsonKey,
				IsRepeated:     field.IsRepeated,
				IsNumeric:      isNumericType(field.Type),
			})
		}

		mapMessages = append(mapMessages, pyMapMessageData{
			TypeName:    msg.Name,
			FuncName:    strutil.ToSnakeCase(msg.Name),
			KeyEnumType: keyEnumType,
			Strict:      config.Strict,
			Fields:      fields,
			HasToMap:    len(toMapFields) > 0,
			ToMapFields: toMapFields,
		})
	}

	if len(mapMessages) == 0 {
		return nil
	}

	data := pyMapData{
		MapMessages: mapMessages,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "map.py.tmpl", tmplutil.BaseFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute map template: %w", err)
	}

	b.Write(result)
	return nil
}
