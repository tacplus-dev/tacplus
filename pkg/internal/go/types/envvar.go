// Copyright (c) 2026 Second Star Cavalier LLC

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

// envVarTemplateData holds pre-computed data for the envvar template.
type envVarTemplateData struct {
	Messages []envVarMessageData
}

// envVarMessageData holds data for a single message's env var serialization.
type envVarMessageData struct {
	TypeName         string
	LowerTypeName    string
	ToEnvVarFields   []serialAssignment
	FromEnvVarFields []serialAssignment
}

func generateMessageEnvVar(b *strings.Builder, msg model.Message, relPath string) error {
	data := buildEnvVarMessageData(msg, relPath)

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "envvar.go.tmpl", funcMap, envVarTemplateData{
		Messages: []envVarMessageData{data},
	})
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

// computeEnvVarPrefix computes the env var key prefix for a message.
// e.g., relPath="sys/jwk.proto", msgName="Jwk" -> "PROTO_SYS_JWK_JWK"
func computeEnvVarPrefix(relPath string, msgName string) string {
	// Strip .proto extension
	pathPart := strings.TrimSuffix(relPath, ".proto")
	// Replace path separators with underscores
	pathPart = strings.ReplaceAll(pathPart, "/", "_")
	// Uppercase
	pathPart = strings.ToUpper(pathPart)

	// Convert PascalCase message name to UPPER_SNAKE_CASE
	msgPart := strings.ToUpper(strutil.ToSnakeCase(msgName))

	if pathPart == "" {
		return "PROTO_" + msgPart
	}
	return "PROTO_" + pathPart + "_" + msgPart
}

func buildEnvVarMessageData(msg model.Message, relPath string) envVarMessageData {
	prefix := computeEnvVarPrefix(relPath, msg.Name)

	data := envVarMessageData{
		TypeName:      msg.Name,
		LowerTypeName: strings.ToLower(msg.Name),
	}

	for _, field := range msg.Fields {
		if field.IsOneOf || field.IsMap {
			continue
		}

		goFieldName := toGoFieldName(field.Name)
		envKey := prefix + "_" + strings.ToUpper(field.Name)

		// Skip repeated messages and well-known types
		if field.IsMessage && field.IsRepeated {
			continue
		}
		if isWellKnownType(field.TypePackage) {
			continue
		}

		toAssignment := computeToEnvVarAssignment(field, goFieldName, envKey)
		if toAssignment != "" {
			data.ToEnvVarFields = append(data.ToEnvVarFields, serialAssignment{Assignment: toAssignment})
		}

		fromAssignment := computeFromEnvVarAssignment(field, goFieldName, envKey)
		if fromAssignment != "" {
			data.FromEnvVarFields = append(data.FromEnvVarFields, serialAssignment{Assignment: fromAssignment})
		}
	}

	return data
}

// isWellKnownType returns true if the type package is a protobuf well-known type.
func isWellKnownType(typePackage string) bool {
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb":
		return true
	}
	return false
}

func computeToEnvVarAssignment(field model.Field, goFieldName, envKey string) string {
	if field.IsEnum {
		return computeEnumToEnvVar(field, goFieldName, envKey)
	}
	if field.IsMessage {
		return computeMessageToEnvVar(field, goFieldName)
	}
	return computeScalarToEnvVar(field, goFieldName, envKey)
}

func computeFromEnvVarAssignment(field model.Field, goFieldName, envKey string) string {
	if field.IsEnum {
		return computeEnumFromEnvVar(field, goFieldName, envKey)
	}
	if field.IsMessage {
		return computeMessageFromEnvVar(field, goFieldName)
	}
	return computeScalarFromEnvVar(field, goFieldName, envKey)
}

// computeScalarToEnvVar generates the ToEnvVar assignment for scalar fields.
func computeScalarToEnvVar(field model.Field, goFieldName, envKey string) string {
	accessor := fmt.Sprintf("v.%s", goFieldName)

	if field.IsOptional {
		var conversion string
		switch field.Type {
		case "string":
			conversion = fmt.Sprintf("result[\"%s\"] = *%s", envKey, accessor)
		case "int32", "sint32", "sfixed32":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatInt(int64(*%s), 10)", envKey, accessor)
		case "int64", "sint64", "sfixed64":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatInt(*%s, 10)", envKey, accessor)
		case "uint32", "fixed32":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatUint(uint64(*%s), 10)", envKey, accessor)
		case "uint64", "fixed64":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatUint(*%s, 10)", envKey, accessor)
		case "float":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatFloat(float64(*%s), 'f', -1, 32)", envKey, accessor)
		case "double":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatFloat(*%s, 'f', -1, 64)", envKey, accessor)
		case "bool":
			conversion = fmt.Sprintf("result[\"%s\"] = strconv.FormatBool(*%s)", envKey, accessor)
		case "bytes":
			conversion = fmt.Sprintf("result[\"%s\"] = base64.StdEncoding.EncodeToString(*%s)", envKey, accessor)
		default:
			return ""
		}
		return fmt.Sprintf("if %s != nil {\n%s\n}", accessor, conversion)
	}

	if field.IsRepeated {
		return computeRepeatedScalarToEnvVar(field, goFieldName, envKey)
	}

	switch field.Type {
	case "string":
		return fmt.Sprintf("result[\"%s\"] = %s", envKey, accessor)
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatInt(int64(%s), 10)", envKey, accessor)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatInt(%s, 10)", envKey, accessor)
	case "uint32", "fixed32":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatUint(uint64(%s), 10)", envKey, accessor)
	case "uint64", "fixed64":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatUint(%s, 10)", envKey, accessor)
	case "float":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatFloat(float64(%s), 'f', -1, 32)", envKey, accessor)
	case "double":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatFloat(%s, 'f', -1, 64)", envKey, accessor)
	case "bool":
		return fmt.Sprintf("result[\"%s\"] = strconv.FormatBool(%s)", envKey, accessor)
	case "bytes":
		return fmt.Sprintf("result[\"%s\"] = base64.StdEncoding.EncodeToString(%s)", envKey, accessor)
	default:
		return ""
	}
}

// computeRepeatedScalarToEnvVar generates comma-separated string for repeated scalar fields.
func computeRepeatedScalarToEnvVar(field model.Field, goFieldName, envKey string) string {
	accessor := fmt.Sprintf("v.%s", goFieldName)

	switch field.Type {
	case "string":
		return fmt.Sprintf("if len(%s) > 0 {\nresult[\"%s\"] = strings.Join(%s, \",\")\n}", accessor, envKey, accessor)
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatInt(int64(val), 10)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatInt(val, 10)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "uint32", "fixed32":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatUint(uint64(val), 10)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "uint64", "fixed64":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatUint(val, 10)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "float":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatFloat(float64(val), 'f', -1, 32)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "double":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatFloat(val, 'f', -1, 64)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	case "bool":
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = strconv.FormatBool(val)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, envKey)
	default:
		return ""
	}
}

// computeEnumToEnvVar generates the ToEnvVar assignment for enum fields.
func computeEnumToEnvVar(field model.Field, goFieldName, envKey string) string {
	converterFunc := fmt.Sprintf("%sToString", field.Type)
	if field.TypePackage != "" {
		converterFunc = fmt.Sprintf("%s.%sToString", goImportAlias(field.TypePackage), field.Type)
	}

	accessor := fmt.Sprintf("v.%s", goFieldName)

	if field.IsOptional {
		return fmt.Sprintf("if %s != nil {\nresult[\"%s\"] = %s(*%s)\n}", accessor, envKey, converterFunc, accessor)
	}

	if field.IsRepeated {
		return fmt.Sprintf(`if len(%s) > 0 {
parts := make([]string, len(%s))
for i, val := range %s {
parts[i] = %s(val)
}
result["%s"] = strings.Join(parts, ",")
}`, accessor, accessor, accessor, converterFunc, envKey)
	}

	return fmt.Sprintf("result[\"%s\"] = %s(%s)", envKey, converterFunc, accessor)
}

// computeMessageToEnvVar generates the ToEnvVar assignment for sub-message fields.
// Sub-messages use their own keys (not prefixed by parent).
func computeMessageToEnvVar(field model.Field, goFieldName string) string {
	toEnvVarFunc := fmt.Sprintf("%sToEnvVar", field.Type)
	if field.TypePackage != "" {
		toEnvVarFunc = fmt.Sprintf("%s.%sToEnvVar", goImportAlias(field.TypePackage), field.Type)
	}

	accessor := fmt.Sprintf("v.%s", goFieldName)

	return fmt.Sprintf(`if %s != nil {
for k, val := range %s(%s) {
result[k] = val
}
}`, accessor, toEnvVarFunc, accessor)
}

// computeScalarFromEnvVar generates the FromEnvVar assignment for scalar fields.
func computeScalarFromEnvVar(field model.Field, goFieldName, envKey string) string {
	if field.IsOptional {
		return computeOptionalScalarFromEnvVar(field, goFieldName, envKey)
	}
	if field.IsRepeated {
		return computeRepeatedScalarFromEnvVar(field, goFieldName, envKey)
	}

	valCheck := fmt.Sprintf("if val, ok := env[\"%s\"]; ok && val != \"\" {", envKey)

	switch field.Type {
	case "string":
		return fmt.Sprintf("%s\nresult.%s = val\n}", valCheck, goFieldName)
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseInt(val, 10, 32)
if err != nil { return nil, err }
result.%s = int32(parsed)
}`, valCheck, goFieldName)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseInt(val, 10, 64)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, goFieldName)
	case "uint32", "fixed32":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseUint(val, 10, 32)
if err != nil { return nil, err }
result.%s = uint32(parsed)
}`, valCheck, goFieldName)
	case "uint64", "fixed64":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseUint(val, 10, 64)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, goFieldName)
	case "float":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseFloat(val, 32)
if err != nil { return nil, err }
result.%s = float32(parsed)
}`, valCheck, goFieldName)
	case "double":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseFloat(val, 64)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, goFieldName)
	case "bool":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseBool(val)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, goFieldName)
	case "bytes":
		return fmt.Sprintf(`%s
parsed, err := base64.StdEncoding.DecodeString(val)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, goFieldName)
	default:
		return ""
	}
}

// computeOptionalScalarFromEnvVar generates FromEnvVar for optional scalar fields (pointer types).
func computeOptionalScalarFromEnvVar(field model.Field, goFieldName, envKey string) string {
	valCheck := fmt.Sprintf("if val, ok := env[\"%s\"]; ok && val != \"\" {", envKey)

	switch field.Type {
	case "string":
		return fmt.Sprintf("%s\nresult.%s = &val\n}", valCheck, goFieldName)
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseInt(val, 10, 32)
if err != nil { return nil, err }
v := int32(parsed)
result.%s = &v
}`, valCheck, goFieldName)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseInt(val, 10, 64)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, goFieldName)
	case "uint32", "fixed32":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseUint(val, 10, 32)
if err != nil { return nil, err }
v := uint32(parsed)
result.%s = &v
}`, valCheck, goFieldName)
	case "uint64", "fixed64":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseUint(val, 10, 64)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, goFieldName)
	case "float":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseFloat(val, 32)
if err != nil { return nil, err }
v := float32(parsed)
result.%s = &v
}`, valCheck, goFieldName)
	case "double":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseFloat(val, 64)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, goFieldName)
	case "bool":
		return fmt.Sprintf(`%s
parsed, err := strconv.ParseBool(val)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, goFieldName)
	case "bytes":
		return fmt.Sprintf(`%s
parsed, err := base64.StdEncoding.DecodeString(val)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, goFieldName)
	default:
		return ""
	}
}

// computeRepeatedScalarFromEnvVar generates FromEnvVar for repeated scalar fields.
func computeRepeatedScalarFromEnvVar(field model.Field, goFieldName, envKey string) string {
	valCheck := fmt.Sprintf("if val, ok := env[\"%s\"]; ok && val != \"\" {", envKey)

	switch field.Type {
	case "string":
		return fmt.Sprintf("%s\nresult.%s = strings.Split(val, \",\")\n}", valCheck, goFieldName)
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]int32, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseInt(strings.TrimSpace(p), 10, 32)
if err != nil { return nil, err }
result.%s = append(result.%s, int32(parsed))
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]int64, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
if err != nil { return nil, err }
result.%s = append(result.%s, parsed)
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "uint32", "fixed32":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]uint32, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
if err != nil { return nil, err }
result.%s = append(result.%s, uint32(parsed))
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "uint64", "fixed64":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]uint64, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
if err != nil { return nil, err }
result.%s = append(result.%s, parsed)
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "float":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]float32, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
if err != nil { return nil, err }
result.%s = append(result.%s, float32(parsed))
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "double":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]float64, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
if err != nil { return nil, err }
result.%s = append(result.%s, parsed)
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	case "bool":
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]bool, 0, len(parts))
for _, p := range parts {
parsed, err := strconv.ParseBool(strings.TrimSpace(p))
if err != nil { return nil, err }
result.%s = append(result.%s, parsed)
}
}`, valCheck, goFieldName, goFieldName, goFieldName)
	default:
		return ""
	}
}

// computeEnumFromEnvVar generates the FromEnvVar assignment for enum fields.
func computeEnumFromEnvVar(field model.Field, goFieldName, envKey string) string {
	parserFunc := fmt.Sprintf("Parse%s", field.Type)
	if field.TypePackage != "" {
		parserFunc = fmt.Sprintf("%s.Parse%s", goImportAlias(field.TypePackage), field.Type)
	}

	fullType := field.Type
	if field.TypePackage != "" {
		fullType = goImportAlias(field.TypePackage) + "." + field.Type
	}

	valCheck := fmt.Sprintf("if val, ok := env[\"%s\"]; ok && val != \"\" {", envKey)

	if field.IsOptional {
		return fmt.Sprintf(`%s
parsed, err := %s(val)
if err != nil { return nil, err }
result.%s = &parsed
}`, valCheck, parserFunc, goFieldName)
	}

	if field.IsRepeated {
		return fmt.Sprintf(`%s
parts := strings.Split(val, ",")
result.%s = make([]%s, 0, len(parts))
for _, p := range parts {
parsed, err := %s(strings.TrimSpace(p))
if err != nil { return nil, err }
result.%s = append(result.%s, parsed)
}
}`, valCheck, goFieldName, fullType, parserFunc, goFieldName, goFieldName)
	}

	return fmt.Sprintf(`%s
parsed, err := %s(val)
if err != nil { return nil, err }
result.%s = parsed
}`, valCheck, parserFunc, goFieldName)
}

// computeMessageFromEnvVar generates the FromEnvVar assignment for sub-message fields.
// Sub-messages use their own keys (not prefixed by parent).
func computeMessageFromEnvVar(field model.Field, goFieldName string) string {
	fromEnvVarFunc := fmt.Sprintf("%sFromEnvVar", field.Type)
	if field.TypePackage != "" {
		fromEnvVarFunc = fmt.Sprintf("%s.%sFromEnvVar", goImportAlias(field.TypePackage), field.Type)
	}

	// For sub-messages, just call their FromEnvVar with the same env map.
	// They will pick out their own keys.
	return fmt.Sprintf(`if sub, err := %s(env); err != nil {
return nil, err
} else if sub != nil {
result.%s = sub
}`, fromEnvVarFunc, goFieldName)
}
