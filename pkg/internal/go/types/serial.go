// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// serialTemplateData holds pre-computed data for the serialization template.
type serialTemplateData struct {
	Messages []serialMessageData
}

// serialMessageData holds data for a single message's serialization.
type serialMessageData struct {
	TypeName       string
	LowerTypeName  string
	HelperName     string // lowercase type name for JSON helper struct
	Fields         []serialFieldData
	ToJSONFields   []serialAssignment
	FromJSONFields []serialAssignment
	JSONAliases    []serialAliasEntry // alias→canonical key mappings for FromJSON
}

// serialAliasEntry maps an alias key to the canonical JSON key.
type serialAliasEntry struct {
	Alias     string
	Canonical string
}

// serialFieldData holds a field's type info for the JSON helper struct.
type serialFieldData struct {
	GoFieldName string
	JSONType    string
	JSONTag     string
}

// serialAssignment holds a pre-computed assignment string.
type serialAssignment struct {
	Assignment string
}

// goImportAlias returns the Go import alias for a cross-package type reference.
func goImportAlias(typePackage string) string {
	return typePackage
}

func generateMessageSerial(b *strings.Builder, msg model.Message) error {
	data := buildSerialMessageData(msg)

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "serial.go.tmpl", funcMap, serialTemplateData{
		Messages: []serialMessageData{data},
	})
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

func buildSerialMessageData(msg model.Message) serialMessageData {
	typeName := msg.Name
	helperName := strings.ToLower(typeName)

	data := serialMessageData{
		TypeName:      typeName,
		LowerTypeName: strings.ToLower(typeName),
		HelperName:    helperName,
	}

	// Build JSON helper struct fields, ToJSON assignments, and FromJSON assignments
	for _, field := range msg.Fields {
		goFieldName := toGoFieldName(field.Name)
		jsonTag := field.Name
		if field.JsonKey != "" {
			jsonTag = field.JsonKey
		}

		// JSON helper struct field type
		jsonType := computeJSONFieldType(field)
		data.Fields = append(data.Fields, serialFieldData{
			GoFieldName: goFieldName,
			JSONType:    jsonType,
			JSONTag:     jsonTag,
		})

		// ToJSON assignment
		toJSON := computeToJSONAssignment(typeName, field, goFieldName)
		data.ToJSONFields = append(data.ToJSONFields, serialAssignment{Assignment: toJSON})

		// FromJSON assignment
		fromJSON := computeFromJSONAssignment(typeName, field, goFieldName)
		data.FromJSONFields = append(data.FromJSONFields, serialAssignment{Assignment: fromJSON})

		// Collect alias→canonical mappings for FromJSON key normalization
		for _, alias := range field.JsonAliases {
			data.JSONAliases = append(data.JSONAliases, serialAliasEntry{
				Alias:     alias,
				Canonical: jsonTag,
			})
		}
	}

	return data
}

// computeJSONFieldType computes the Go type for a JSON helper struct field.
func computeJSONFieldType(field model.Field) string {
	if field.IsMap {
		if field.MapValueIsMessage {
			// Use json.RawMessage values so each entry is serialized via its ToJSON helper.
			// JSON object keys are always strings, so the JSON helper always uses string keys.
			return "map[string]json.RawMessage"
		}
		if field.MapValueIsEnum {
			// Enum map values are serialized as strings in JSON.
			// JSON object keys are always strings, matching the message map pattern.
			return "map[string]string"
		}
		keyType := protoTypeToGoType(field.MapKeyType)
		valueType := protoTypeToGoType(field.MapValueType)
		return fmt.Sprintf("map[%s]%s", keyType, valueType)
	}

	if field.IsOneOf {
		// Each oneof case gets its own field in the JSON struct.
		if field.IsMessage {
			return "json.RawMessage"
		}
		if field.IsEnum {
			return "string"
		}
		// Scalar oneof: use a pointer so the field is omitted when not set.
		goType := protoTypeToGoType(field.Type)
		return "*" + goType
	}

	if field.IsEnum {
		if field.IsRepeated {
			return "[]string"
		}
		return "string"
	}

	if field.IsMessage {
		if field.IsRepeated {
			return "[]json.RawMessage"
		}
		if field.IsOptional {
			return "*json.RawMessage"
		}
		return "json.RawMessage"
	}

	goType := protoTypeToGoType(field.Type)
	if field.TypePackage != "" {
		goType = goImportAlias(field.TypePackage) + "." + goType
	}
	if field.IsRepeated {
		return "[]" + goType
	}
	if field.IsOptional && isScalarType(goType) {
		return "*" + goType
	}
	return goType
}

// computeToJSONAssignment computes the Go code for a ToJSON field assignment.
func computeToJSONAssignment(_msgName string, field model.Field, goFieldName string) string {
	if field.IsMap {
		if field.MapValueIsMessage {
			return computeMapMessageToJSONAssignment(field, goFieldName)
		}
		if field.MapValueIsEnum {
			return computeMapEnumToJSONAssignment(field, goFieldName)
		}
		// Scalar map: direct copy.
		return fmt.Sprintf("%s: v.%s,", goFieldName, goFieldName)
	}
	if field.IsOneOf {
		return computeOneofToJSONAssignment(field, goFieldName)
	}
	if field.IsEnum {
		return computeEnumToJSONAssignment(field, goFieldName)
	}
	if field.IsMessage {
		return computeMessageToJSONAssignment(field, goFieldName)
	}
	// Scalar fields - direct copy
	return fmt.Sprintf("%s: v.%s,", goFieldName, goFieldName)
}

func computeMapMessageToJSONAssignment(field model.Field, goFieldName string) string {
	marshalFunc := fmt.Sprintf("%sToJSON", field.MapValueType)
	if field.MapValueTypePackage != "" {
		marshalFunc = fmt.Sprintf("%s.%sToJSON", goImportAlias(field.MapValueTypePackage), field.MapValueType)
	}
	// JSON object keys are always strings. Convert non-string proto map keys.
	keyExpr := mapKeyToStringExpr(field.MapKeyType, "k")
	return fmt.Sprintf(`%s: func() map[string]json.RawMessage {
if len(v.%s) == 0 { return nil }
result := make(map[string]json.RawMessage, len(v.%s))
for k, item := range v.%s {
if item != nil {
if data, err := %s(item); err == nil {
result[%s] = data
}
}
}
return result
}(),`, goFieldName, goFieldName, goFieldName, goFieldName, marshalFunc, keyExpr)
}

func computeMapEnumToJSONAssignment(field model.Field, goFieldName string) string {
	converterFunc := fmt.Sprintf("%sToString", field.MapValueType)
	if field.MapValueTypePackage != "" {
		converterFunc = fmt.Sprintf("%s.%sToString", goImportAlias(field.MapValueTypePackage), field.MapValueType)
	}
	// JSON object keys are always strings. Convert non-string proto map keys.
	keyExpr := mapKeyToStringExpr(field.MapKeyType, "k")
	return fmt.Sprintf(`%s: func() map[string]string {
if len(v.%s) == 0 { return nil }
result := make(map[string]string, len(v.%s))
for k, val := range v.%s {
result[%s] = %s(val)
}
return result
}(),`, goFieldName, goFieldName, goFieldName, goFieldName, keyExpr, converterFunc)
}

func computeMapEnumFromJSONAssignment(field model.Field, goFieldName string) string {
	parserFunc := fmt.Sprintf("Parse%s", field.MapValueType)
	if field.MapValueTypePackage != "" {
		parserFunc = fmt.Sprintf("%s.Parse%s", goImportAlias(field.MapValueTypePackage), field.MapValueType)
	}
	fullValueType := field.MapValueType
	if field.MapValueTypePackage != "" {
		fullValueType = goImportAlias(field.MapValueTypePackage) + "." + field.MapValueType
	}
	goKeyType := protoTypeToGoType(field.MapKeyType)
	// JSON object keys are always strings. Parse back to the proto map key type.
	keyConv := mapKeyFromStringBlock(field.MapKeyType)
	return fmt.Sprintf(`// Parse enum map field: %s
if len(raw.%s) > 0 {
result.%s = make(map[%s]%s, len(raw.%s))
for k, v := range raw.%s {
if v != "" {
%sparsed, err := %s(v)
if err == nil {
result.%s[mapKey] = parsed
}
}
}
}`, goFieldName, goFieldName, goFieldName, goKeyType, fullValueType, goFieldName, goFieldName, keyConv, parserFunc, goFieldName)
}

func computeOneofToJSONAssignment(field model.Field, goFieldName string) string {
	if field.IsMessage {
		isWellKnownType := field.TypePackage == "anypb" || field.TypePackage == "structpb" ||
			field.TypePackage == "timestamppb" || field.TypePackage == "durationpb" ||
			field.TypePackage == "emptypb" || field.TypePackage == "fieldmaskpb" ||
			field.TypePackage == "wrapperspb"

		var marshalFunc string
		if isWellKnownType {
			marshalFunc = "protojson.Marshal"
		} else {
			marshalFunc = fmt.Sprintf("%sToJSON", field.Type)
			if field.TypePackage != "" {
				marshalFunc = fmt.Sprintf("%s.%sToJSON", goImportAlias(field.TypePackage), field.Type)
			}
		}
		return fmt.Sprintf(`%s: func() json.RawMessage {
if v.Get%s() == nil { return nil }
data, _ := %s(v.Get%s())
return data
}(),`, goFieldName, goFieldName, marshalFunc, goFieldName)
	}
	if field.IsEnum {
		converterFunc := fmt.Sprintf("%sToString", field.Type)
		if field.TypePackage != "" {
			converterFunc = fmt.Sprintf("%s.%sToString", goImportAlias(field.TypePackage), field.Type)
		}
		return fmt.Sprintf(`%s: func() string {
if v.Get%s() == 0 { return "" }
return %s(v.Get%s())
}(),`, goFieldName, goFieldName, converterFunc, goFieldName)
	}
	// Scalar oneof
	goType := protoTypeToGoType(field.Type)
	return fmt.Sprintf(`%s: func() *%s {
val := v.Get%s()
return &val
}(),`, goFieldName, goType, goFieldName)
}

func computeEnumToJSONAssignment(field model.Field, goFieldName string) string {
	converterFunc := fmt.Sprintf("%sToString", field.Type)
	if field.TypePackage != "" {
		converterFunc = fmt.Sprintf("%s.%sToString", goImportAlias(field.TypePackage), field.Type)
	}

	if field.IsRepeated {
		return fmt.Sprintf(`%s: func() []string {
if len(v.%s) == 0 { return nil }
result := make([]string, len(v.%s))
for i, val := range v.%s {
result[i] = %s(val)
}
return result
}(),`, goFieldName, goFieldName, goFieldName, goFieldName, converterFunc)
	}
	if field.IsOptional {
		return fmt.Sprintf(`%s: func() string {
if v.%s == nil { return "" }
return %s(*v.%s)
}(),`, goFieldName, goFieldName, converterFunc, goFieldName)
	}
	return fmt.Sprintf("%s: %s(v.%s),", goFieldName, converterFunc, goFieldName)
}

func computeMessageToJSONAssignment(field model.Field, goFieldName string) string {
	isWellKnownType := field.TypePackage == "anypb" || field.TypePackage == "structpb" ||
		field.TypePackage == "timestamppb" || field.TypePackage == "durationpb" ||
		field.TypePackage == "emptypb" || field.TypePackage == "fieldmaskpb" ||
		field.TypePackage == "wrapperspb"

	var marshalFunc string
	if isWellKnownType {
		marshalFunc = "protojson.Marshal"
	} else {
		marshalFunc = fmt.Sprintf("%sToJSON", field.Type)
		if field.TypePackage != "" {
			marshalFunc = fmt.Sprintf("%s.%sToJSON", goImportAlias(field.TypePackage), field.Type)
		}
	}

	if field.IsRepeated {
		return fmt.Sprintf(`%s: func() []json.RawMessage {
if len(v.%s) == 0 { return nil }
result := make([]json.RawMessage, 0, len(v.%s))
for _, item := range v.%s {
if item != nil {
if data, err := %s(item); err == nil {
result = append(result, data)
}
}
}
return result
}(),`, goFieldName, goFieldName, goFieldName, goFieldName, marshalFunc)
	}
	if field.IsOptional {
		return fmt.Sprintf(`%s: func() *json.RawMessage {
if v.%s == nil { return nil }
data, err := %s(v.%s)
if err != nil { return nil }
return &data
}(),`, goFieldName, goFieldName, marshalFunc, goFieldName)
	}
	return fmt.Sprintf(`%s: func() json.RawMessage {
if v.%s == nil { return nil }
data, _ := %s(v.%s)
return data
}(),`, goFieldName, goFieldName, marshalFunc, goFieldName)
}

// computeFromJSONAssignment computes the Go code for a FromJSON field assignment.
func computeFromJSONAssignment(msgName string, field model.Field, goFieldName string) string {
	if field.IsMap {
		if field.MapValueIsMessage {
			return computeMapMessageFromJSONAssignment(field, goFieldName)
		}
		if field.MapValueIsEnum {
			return computeMapEnumFromJSONAssignment(field, goFieldName)
		}
		// Scalar map: direct assignment.
		return fmt.Sprintf("result.%s = raw.%s", goFieldName, goFieldName)
	}
	if field.IsOneOf {
		return computeOneofFromJSONAssignment(msgName, field, goFieldName)
	}
	if field.IsEnum {
		return computeEnumFromJSONAssignment(field, goFieldName)
	}
	if field.IsMessage {
		return computeMessageFromJSONAssignment(field, goFieldName)
	}
	// Non-message, non-enum field - direct assignment
	return fmt.Sprintf("result.%s = raw.%s", goFieldName, goFieldName)
}

func computeMapMessageFromJSONAssignment(field model.Field, goFieldName string) string {
	unmarshalFunc := fmt.Sprintf("%sFromJSON", field.MapValueType)
	if field.MapValueTypePackage != "" {
		unmarshalFunc = fmt.Sprintf("%s.%sFromJSON", goImportAlias(field.MapValueTypePackage), field.MapValueType)
	}
	fullType := field.MapValueType
	if field.MapValueTypePackage != "" {
		fullType = goImportAlias(field.MapValueTypePackage) + "." + field.MapValueType
	}
	goKeyType := protoTypeToGoType(field.MapKeyType)
	// JSON object keys are always strings. Convert back to the proto map key type.
	keyConv := mapKeyFromStringBlock(field.MapKeyType)
	return fmt.Sprintf(`// Parse map field: %s
if len(raw.%s) > 0 {
result.%s = make(map[%s]*%s, len(raw.%s))
for k, v := range raw.%s {
if len(v) > 0 {
%sparsed, err := %s(v)
if err == nil {
result.%s[mapKey] = parsed
}
}
}
}`, goFieldName, goFieldName, goFieldName, goKeyType, fullType, goFieldName, goFieldName, keyConv, unmarshalFunc, goFieldName)
}

func computeOneofFromJSONAssignment(msgName string, field model.Field, goFieldName string) string {
	oneofGoFieldName := toGoFieldName(field.OneOfName)
	wrapperType := msgName + "_" + goFieldName
	if field.IsMessage {
		isWellKnownType := field.TypePackage == "structpb" || field.TypePackage == "timestamppb" ||
			field.TypePackage == "durationpb" || field.TypePackage == "wrapperspb" ||
			field.TypePackage == "anypb" || field.TypePackage == "emptypb"

		if isWellKnownType {
			fullType := field.Type
			if field.TypePackage != "" {
				fullType = goImportAlias(field.TypePackage) + "." + field.Type
			}
			return fmt.Sprintf(`// Parse oneof well-known type: %s
if len(raw.%s) > 0 {
parsed := &%s{}
if err := protojson.Unmarshal(raw.%s, parsed); err == nil {
result.%s = &%s{%s: parsed}
}
}`, goFieldName, goFieldName, fullType, goFieldName, oneofGoFieldName, wrapperType, goFieldName)
		}

		unmarshalFunc := fmt.Sprintf("%sFromJSON", field.Type)
		if field.TypePackage != "" {
			unmarshalFunc = fmt.Sprintf("%s.%sFromJSON", goImportAlias(field.TypePackage), field.Type)
		}
		return fmt.Sprintf(`// Parse oneof field: %s
if len(raw.%s) > 0 {
parsed, err := %s(raw.%s)
if err == nil {
result.%s = &%s{%s: parsed}
}
}`, goFieldName, goFieldName, unmarshalFunc, goFieldName, oneofGoFieldName, wrapperType, goFieldName)
	}
	if field.IsEnum {
		parserFunc := fmt.Sprintf("Parse%s", field.Type)
		if field.TypePackage != "" {
			parserFunc = fmt.Sprintf("%s.Parse%s", goImportAlias(field.TypePackage), field.Type)
		}
		return fmt.Sprintf(`// Parse oneof enum field: %s
if raw.%s != "" {
if parsed, err := %s(raw.%s); err == nil {
result.%s = &%s{%s: parsed}
}
}`, goFieldName, goFieldName, parserFunc, goFieldName, oneofGoFieldName, wrapperType, goFieldName)
	}
	// Scalar oneof
	return fmt.Sprintf(`// Parse oneof scalar field: %s
if raw.%s != nil {
result.%s = &%s{%s: *raw.%s}
}`, goFieldName, goFieldName, oneofGoFieldName, wrapperType, goFieldName, goFieldName)
}

func computeEnumFromJSONAssignment(field model.Field, goFieldName string) string {
	parserFunc := fmt.Sprintf("Parse%s", field.Type)
	if field.TypePackage != "" {
		parserFunc = fmt.Sprintf("%s.Parse%s", goImportAlias(field.TypePackage), field.Type)
	}

	fullType := field.Type
	if field.TypePackage != "" {
		fullType = goImportAlias(field.TypePackage) + "." + field.Type
	}

	if field.IsRepeated {
		return fmt.Sprintf(`// Parse repeated enum field: %s
if len(raw.%s) > 0 {
result.%s = make([]%s, 0, len(raw.%s))
for _, val := range raw.%s {
if val != "" {
parsed, err := %s(val)
if err != nil {
// Skip invalid enum values
continue
}
result.%s = append(result.%s, parsed)
}
}
}`, goFieldName, goFieldName, goFieldName, fullType, goFieldName, goFieldName, parserFunc, goFieldName, goFieldName)
	}

	assignOp := fmt.Sprintf("result.%s = parsed", goFieldName)
	if field.IsOptional {
		assignOp = fmt.Sprintf("result.%s = &parsed", goFieldName)
	}

	return fmt.Sprintf(`// Parse enum field: %s
if raw.%s != "" {
if parsed, err := %s(raw.%s); err == nil {
%s
}
}`, goFieldName, goFieldName, parserFunc, goFieldName, assignOp)
}

func computeMessageFromJSONAssignment(field model.Field, goFieldName string) string {
	isWellKnownType := field.TypePackage == "structpb" || field.TypePackage == "timestamppb" ||
		field.TypePackage == "durationpb" || field.TypePackage == "wrapperspb" ||
		field.TypePackage == "anypb" || field.TypePackage == "emptypb"

	if isWellKnownType {
		fullType := field.Type
		if field.TypePackage != "" {
			fullType = goImportAlias(field.TypePackage) + "." + field.Type
		}
		if field.IsOptional {
			return fmt.Sprintf(`// Parse optional well-known type: %s
if raw.%s != nil && len(*raw.%s) > 0 {
result.%s = &%s{}
if err := protojson.Unmarshal(*raw.%s, result.%s); err != nil {
result.%s = nil
}
}`, goFieldName, goFieldName, goFieldName, goFieldName, fullType, goFieldName, goFieldName, goFieldName)
		}
		return fmt.Sprintf(`// Parse well-known type: %s
if len(raw.%s) > 0 {
result.%s = &%s{}
if err := protojson.Unmarshal(raw.%s, result.%s); err != nil {
result.%s = nil
}
}`, goFieldName, goFieldName, goFieldName, fullType, goFieldName, goFieldName, goFieldName)
	}

	fromJSONFunc := fmt.Sprintf("%sFromJSON", field.Type)
	if field.TypePackage != "" {
		fromJSONFunc = fmt.Sprintf("%s.%sFromJSON", goImportAlias(field.TypePackage), field.Type)
	}

	if field.IsRepeated {
		fullType := field.Type
		if field.TypePackage != "" {
			fullType = goImportAlias(field.TypePackage) + "." + field.Type
		}
		return fmt.Sprintf(`// Parse repeated message field: %s
if len(raw.%s) > 0 {
result.%s = make([]*%s, 0, len(raw.%s))
for _, itemJSON := range raw.%s {
if len(itemJSON) > 0 {
parsed, err := %s(itemJSON)
if err == nil {
result.%s = append(result.%s, parsed)
}
}
}
}`, goFieldName, goFieldName, goFieldName, fullType, goFieldName, goFieldName, fromJSONFunc, goFieldName, goFieldName)
	}

	if field.IsOptional {
		return fmt.Sprintf(`// Parse optional message field: %s
if raw.%s != nil && len(*raw.%s) > 0 {
parsed, err := %s(*raw.%s)
if err == nil {
result.%s = parsed
}
}`, goFieldName, goFieldName, goFieldName, fromJSONFunc, goFieldName, goFieldName)
	}
	return fmt.Sprintf(`// Parse message field: %s
if len(raw.%s) > 0 {
parsed, err := %s(raw.%s)
if err == nil {
result.%s = parsed
}
}`, goFieldName, goFieldName, fromJSONFunc, goFieldName, goFieldName)
}

// protoTypeToGoType converts proto types to Go types
func protoTypeToGoType(protoType string) string {
	switch protoType {
	case "string":
		return "string"
	case "int32", "sint32", "sfixed32":
		return "int32"
	case "int64", "sint64", "sfixed64":
		return "int64"
	case "uint32", "fixed32":
		return "uint32"
	case "uint64", "fixed64":
		return "uint64"
	case "float":
		return "float32"
	case "double":
		return "float64"
	case "bool":
		return "bool"
	case "bytes":
		return "[]byte"
	default:
		return protoType
	}
}

// mapKeyToStringExpr returns a Go expression that converts a map key variable
// to a string. JSON object keys are always strings, so non-string proto map
// key types need conversion when serializing to JSON.
func mapKeyToStringExpr(protoKeyType, varName string) string {
	switch protoKeyType {
	case "string":
		return varName
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf("strconv.FormatInt(int64(%s), 10)", varName)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf("strconv.FormatInt(%s, 10)", varName)
	case "uint32", "fixed32":
		return fmt.Sprintf("strconv.FormatUint(uint64(%s), 10)", varName)
	case "uint64", "fixed64":
		return fmt.Sprintf("strconv.FormatUint(%s, 10)", varName)
	case "bool":
		return fmt.Sprintf("strconv.FormatBool(%s)", varName)
	default:
		return fmt.Sprintf("fmt.Sprint(%s)", varName)
	}
}

// mapKeyFromStringBlock returns Go code that parses a string key back to the
// proto map key type. For string keys this is a simple assignment; for numeric
// keys it emits a parse+continue block. The parsed result is always in "mapKey".
func mapKeyFromStringBlock(protoKeyType string) string {
	switch protoKeyType {
	case "string":
		return "mapKey := k\n"
	case "int32", "sint32", "sfixed32":
		return `mapKeyI64, err := strconv.ParseInt(k, 10, 32)
if err != nil { continue }
mapKey := int32(mapKeyI64)
`
	case "int64", "sint64", "sfixed64":
		return `mapKey, err := strconv.ParseInt(k, 10, 64)
if err != nil { continue }
`
	case "uint32", "fixed32":
		return `mapKeyU64, err := strconv.ParseUint(k, 10, 32)
if err != nil { continue }
mapKey := uint32(mapKeyU64)
`
	case "uint64", "fixed64":
		return `mapKey, err := strconv.ParseUint(k, 10, 64)
if err != nil { continue }
`
	case "bool":
		return `mapKey, err := strconv.ParseBool(k)
if err != nil { continue }
`
	default:
		return "mapKey := k\n"
	}
}

// isScalarType returns true if the type is a scalar type that needs pointer handling
func isScalarType(goType string) bool {
	switch goType {
	case "string", "int32", "int64", "uint32", "uint64", "float32", "float64", "bool", "[]byte":
		return true
	default:
		return false
	}
}
