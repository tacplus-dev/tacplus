// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// pyRequiredZeroCheck returns the Python expression that tests whether a field is
// at its zero value. It drives the whole requirement family — assert_required plus
// the conditional required_if_true / required_if_false / required_if_eq — so the
// same field shape yields the same test whichever annotation asks for it. A bare
// `not msg.<f>` reads as False for every singular message field (protobuf's Python
// messages have no __bool__), which silently drops the requirement.
func pyRequiredZeroCheck(field model.Field, pyFieldName string) string {
	switch {
	case field.IsMap || field.IsRepeated:
		return fmt.Sprintf("len(%s) == 0", pyFieldGet("msg", pyFieldName))
	case field.IsMessage || field.IsOptional:
		return fmt.Sprintf("not msg.HasField(\"%s\")", field.Name)
	default:
		return fmt.Sprintf("not %s", pyFieldGet("msg", pyFieldName))
	}
}

// pyValidationCode holds a pre-rendered validation code block.
type pyValidationCode struct {
	Code string
}

// pyValidationMessage holds the template data for a single message's validation.
type pyValidationMessage struct {
	TypeName       string
	FuncName       string // snake_case function name part
	HasValidations bool
	Validations    []pyValidationCode
}

// pyConvenienceFunction holds data for validate_from_bytes/validate_from_file.
type pyConvenienceFunction struct {
	TypeName   string
	FuncPrefix string
}

// pyValidationData holds the template data for all validation functions.
type pyValidationData struct {
	Messages             []pyValidationMessage
	ConvenienceFunctions []pyConvenienceFunction
}

// writeValidation writes validation functions to the combined helpers.py builder
func writeValidation(b *strings.Builder, pf model.ProtoFile) error {
	if len(pf.Messages) == 0 {
		return nil
	}

	var messages []pyValidationMessage
	var convenienceFunctions []pyConvenienceFunction

	for _, msg := range pf.Messages {
		funcName := strutil.ToSnakeCase(msg.Name)

		// Check if there are any validations needed
		hasValidations := false
		for _, field := range msg.Fields {
			if len(field.Annotations) > 0 {
				hasValidations = true
				break
			}
			// Collection fields always get default max count validation
			if !field.IsOneOf && (field.IsRepeated || field.IsMap) {
				hasValidations = true
				break
			}
			// String/bytes fields always get default max size validation
			if !field.IsRepeated && !field.IsMap && (field.Type == "string" || field.Type == "bytes") {
				hasValidations = true
				break
			}
			if pythonMessageValidationFunc(field.Type, field.TypePackage) != "" && field.IsMessage {
				hasValidations = true
				break
			}
			if pythonMessageValidationFunc(field.MapValueType, field.MapValueTypePackage) != "" && field.MapValueIsMessage {
				hasValidations = true
				break
			}
			if (field.IsEnum && field.TypePackage == "") || (field.MapValueIsEnum && field.MapValueTypePackage == "") {
				hasValidations = true
				break
			}
		}
		var validations []pyValidationCode
		if hasValidations {
			var err error
			validations, err = buildValidationBlocks(msg, pf.Enums)
			if err != nil {
				return err
			}
		}

		messages = append(messages, pyValidationMessage{
			TypeName:       msg.Name,
			FuncName:       funcName,
			HasValidations: hasValidations,
			Validations:    validations,
		})

		convenienceFunctions = append(convenienceFunctions, pyConvenienceFunction{
			TypeName:   msg.Name,
			FuncPrefix: funcName,
		})
	}

	data := pyValidationData{
		Messages:             messages,
		ConvenienceFunctions: convenienceFunctions,
	}

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	result, err := tmplutil.ExecuteTemplate(typeFS, "validation.py.tmpl", tmplutil.BaseFuncMap(), data)
	if err != nil {
		return fmt.Errorf("failed to execute validation template: %w", err)
	}

	b.Write(result)
	return nil
}

// buildValidationBlocks builds pre-rendered validation code blocks for a message.
func buildValidationBlocks(msg model.Message, enums []model.Enum) ([]pyValidationCode, error) {
	var blocks []pyValidationCode

	for _, field := range msg.Fields {
		if field.IsOneOf {
			if len(field.Annotations) == 0 {
				continue
			}
			pyFieldName := strutil.ToSnakeCase(field.Name)
			if annotationValue, ok := field.Annotations["assert_max_size"]; ok && annotationValue.Type == model.AnnotationTypeInt && (field.Type == "string" || field.Type == "bytes") {
				var code strings.Builder
				pyLinef(&code, 1, "# Validate %s (assert_max_size: %d bytes)", pyFieldName, annotationValue.IntValue)
				if field.Type == "bytes" {
					pyLinef(&code, 1, "if len(%s) > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
				} else {
					pyLinef(&code, 1, "if len(%s.encode('utf-8')) > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
				}
				pyLinef(&code, 2, "raise invalid_argument(f\"field %s exceeds max size of %d bytes\")", pyFieldName, annotationValue.IntValue)
				blocks = append(blocks, pyValidationCode{Code: code.String()})
			}
			continue
		}
		if len(field.Annotations) == 0 {
			continue
		}

		pyFieldName := strutil.ToSnakeCase(field.Name)

		// Check assert_required FIRST
		if annotationValue, hasRequired := field.Annotations["assert_required"]; hasRequired {
			if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
				var code strings.Builder
				pyLinef(&code, 1, "# Validate %s (assert_required)", pyFieldName)
				pyLinef(&code, 1, "if %s:", pyRequiredZeroCheck(field, pyFieldName))
				pyLinef(&code, 2, "raise invalid_argument(f\"field %s is required\")", pyFieldName)
				blocks = append(blocks, pyValidationCode{Code: code.String()})
			}
		}

		// Then process other annotations
		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
				continue
			}
			if annotationName == "assert_required" {
				continue
			}

			var code strings.Builder

			switch annotationName {
			case "assert_email":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_email annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_email)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_email(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_email(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_fqdn":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_fqdn annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_fqdn)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_fqdn(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_fqdn(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_host":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_host annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_host: IP address or FQDN)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_host(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_host(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_base64":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_base64 annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_base64)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_base64(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_base64(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_listen_addr":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_listen_addr annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_listen_addr)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_listen_addr(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_listen_addr(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_net_port":
				if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
					return nil, fmt.Errorf("assert_net_port annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_net_port)", pyFieldName)
				pyLinef(&code, 1, "if %s < 0 or %s > 65535:", pyFieldGet("msg", pyFieldName), pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be a valid port number (0-65535), got: {%s}\")", pyFieldName, pyFieldGet("msg", pyFieldName))

			case "assert_ipaddr_type":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_ipaddr_type annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					buildIPAddrValidation(&code, pyFieldName, annotationValue.EnumValue, field.IsRepeated)
				}

			case "assert_duration":
				if annotationValue.Type == model.AnnotationTypeEnum {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return nil, fmt.Errorf("assert_duration annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					switch annotationValue.EnumValue {
					case "DURATION_TYPE_POSITIVE":
						pyLinef(&code, 1, "# Validate %s (assert_duration: POSITIVE)", pyFieldName)
						pyLinef(&code, 1, "if %s <= 0:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be positive (> 0), got: {%s}\")", pyFieldName, pyFieldGet("msg", pyFieldName))
					case "DURATION_TYPE_NON_NEGATIVE":
						pyLinef(&code, 1, "# Validate %s (assert_duration: NON_NEGATIVE)", pyFieldName)
						pyLinef(&code, 1, "if %s < 0:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be non-negative (>= 0), got: {%s}\")", pyFieldName, pyFieldGet("msg", pyFieldName))
					}
				}

			case "assert_duration_min":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return nil, fmt.Errorf("assert_duration_min annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_duration_min: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s < %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be >= %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_duration_max":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return nil, fmt.Errorf("assert_duration_max annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_duration_max: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be <= %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_gt":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return nil, fmt.Errorf("assert_gt annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_gt: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s <= %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be > %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_ge":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return nil, fmt.Errorf("assert_ge annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_ge: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s < %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be >= %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_lt":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return nil, fmt.Errorf("assert_lt annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_lt: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s >= %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be < %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return nil, fmt.Errorf("assert_le annotation on field %s.%s requires a scalar numeric field", msg.Name, pyFieldName)
					}
					pyLinef(&code, 1, "# Validate %s (assert_le: %d)", pyFieldName, annotationValue.IntValue)
					pyLinef(&code, 1, "if %s > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
					pyLinef(&code, 2, "raise invalid_argument(f\"field %s must be <= %d, got: {%s}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
				}

			case "assert_semver":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_semver annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				pyLinef(&code, 1, "# Validate %s (assert_semver)", pyFieldName)
				if field.IsRepeated {
					pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
					pyLine(&code, 2, "if val:")
					pyLinef(&code, 3, "_validate_semver(val, '%s')", pyFieldName)
				} else {
					pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
					pyLinef(&code, 2, "_validate_semver(%s, '%s')", pyFieldGet("msg", pyFieldName), pyFieldName)
				}

			case "assert_sql":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_sql annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					pyLinef(&code, 1, "# Validate %s (assert_sql: %s)", pyFieldName, annotationValue.EnumValue)
					if field.IsRepeated {
						pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
						pyLine(&code, 2, "if val:")
						pyLinef(&code, 3, "_validate_sql(val, '%s', '%s')", annotationValue.EnumValue, pyFieldName)
					} else {
						pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "_validate_sql(%s, '%s', '%s')", pyFieldGet("msg", pyFieldName), annotationValue.EnumValue, pyFieldName)
					}
				}

			case "assert_url":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_url annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					pyLinef(&code, 1, "# Validate %s (assert_url: %s)", pyFieldName, annotationValue.EnumValue)
					if field.IsRepeated {
						pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
						pyLine(&code, 2, "if val:")
						pyLinef(&code, 3, "_validate_url(val, '%s', '%s')", annotationValue.EnumValue, pyFieldName)
					} else {
						pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "_validate_url(%s, '%s', '%s')", pyFieldGet("msg", pyFieldName), annotationValue.EnumValue, pyFieldName)
					}
				}

			case "assert_file_path":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_file_path annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					pyLinef(&code, 1, "# Validate %s (assert_file_path: %s)", pyFieldName, annotationValue.EnumValue)
					if field.IsRepeated {
						pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
						pyLine(&code, 2, "if val:")
						pyLinef(&code, 3, "_validate_file_path(val, '%s', '%s')", annotationValue.EnumValue, pyFieldName)
					} else {
						pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "_validate_file_path(%s, '%s', '%s')", pyFieldGet("msg", pyFieldName), annotationValue.EnumValue, pyFieldName)
					}
				}

			case "assert_enc_type":
				if field.Type != "string" {
					return nil, fmt.Errorf("assert_enc_type annotation on field %s.%s requires string type", msg.Name, pyFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					pyLinef(&code, 1, "# Validate %s (assert_enc_type: %s)", pyFieldName, annotationValue.EnumValue)
					if field.IsRepeated {
						pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
						pyLine(&code, 2, "if val:")
						pyLinef(&code, 3, "_validate_enc_type(val, '%s', '%s')", annotationValue.EnumValue, pyFieldName)
					} else {
						pyLinef(&code, 1, "if %s:", pyFieldGet("msg", pyFieldName))
						pyLinef(&code, 2, "_validate_enc_type(%s, '%s', '%s')", pyFieldGet("msg", pyFieldName), annotationValue.EnumValue, pyFieldName)
					}
				}

			case "assert_invariant":
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					pyLinef(&code, 1, "# Formal invariant for %s (assert_invariant): %s", pyFieldName, annotationValue.StringValue)
				}

			case "assert_enum_defined":
				if err := buildPyEnumDefinedValidation(&code, msg, field, pyFieldName); err != nil {
					return nil, err
				}

			case "assert_numeric":
				if err := buildPyNumericConstraintValidation(&code, msg, field, pyFieldName, annotationValue.NumericConstraint); err != nil {
					return nil, err
				}

			case "assert_float":
				if err := buildPyFloatConstraintValidation(&code, msg, field, pyFieldName, annotationValue.FloatConstraint); err != nil {
					return nil, err
				}

			case "epoch_expiration":
				pyLinef(&code, 1, "# Validate %s (epoch_expiration)", pyFieldName)
				pyLinef(&code, 1, "if %s != 0:", pyFieldGet("msg", pyFieldName))
				pyLine(&code, 2, "import time as _time")
				pyLine(&code, 2, "_now = int(_time.time())")
				pyLinef(&code, 2, "if %s <= _now:", pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 3, "raise invalid_argument(f\"field %s must be a future epoch timestamp, got: {%s} (now: {_now})\")", pyFieldName, pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 2, "if %s > _now + 31536000:", pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 3, "raise invalid_argument(f\"field %s must be within 1 year from now, got: {%s} (max: {_now + 31536000})\")", pyFieldName, pyFieldGet("msg", pyFieldName))

			case "required_if_true":
				condFieldPy := strutil.ToSnakeCase(annotationValue.StringValue)
				pyLinef(&code, 1, "# Validate %s (required_if_true: %s)", pyFieldName, condFieldPy)
				pyLinef(&code, 1, "if %s and %s:", pyFieldGet("msg", condFieldPy), pyRequiredZeroCheck(field, pyFieldName))
				pyLinef(&code, 2, "raise invalid_argument(\"field %s is required when %s is true\")", pyFieldName, condFieldPy)

			case "required_if_false":
				condFieldPy := strutil.ToSnakeCase(annotationValue.StringValue)
				pyLinef(&code, 1, "# Validate %s (required_if_false: %s)", pyFieldName, condFieldPy)
				pyLinef(&code, 1, "if not %s and %s:", pyFieldGet("msg", condFieldPy), pyRequiredZeroCheck(field, pyFieldName))
				pyLinef(&code, 2, "raise invalid_argument(\"field %s is required when %s is false\")", pyFieldName, condFieldPy)

			case "required_if_eq":
				parts := strings.SplitN(annotationValue.StringValue, "=", 2)
				if len(parts) == 2 {
					condFieldSnake := parts[0]
					condValue := parts[1]
					condFieldPy := strutil.ToSnakeCase(condFieldSnake)

					var siblingField *model.Field
					for i := range msg.Fields {
						if msg.Fields[i].Name == condFieldSnake {
							siblingField = &msg.Fields[i]
							break
						}
					}

					pyLinef(&code, 1, "# Validate %s (required_if_eq: %s=%s)", pyFieldName, condFieldSnake, condValue)
					if siblingField != nil && siblingField.IsEnum {
						enumIntVal := 0
						for _, e := range enums {
							if e.Name == siblingField.Type {
								for _, v := range e.Values {
									if v.Name == condValue {
										enumIntVal = v.Number
										break
									}
								}
								break
							}
						}
						pyLinef(&code, 1, "if %s == %d and %s:  # %s", pyFieldGet("msg", condFieldPy), enumIntVal, pyRequiredZeroCheck(field, pyFieldName), condValue)
					} else {
						pyLinef(&code, 1, "if %s == \"%s\" and %s:", pyFieldGet("msg", condFieldPy), condValue, pyRequiredZeroCheck(field, pyFieldName))
					}
					pyLinef(&code, 2, "raise invalid_argument(\"field %s is required when %s is %s\")", pyFieldName, condFieldSnake, condValue)
				}

			case "assert_max_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsRepeated || field.IsMap {
						pyLinef(&code, 1, "# Validate %s (assert_max_size: %d)", pyFieldName, annotationValue.IntValue)
						pyLinef(&code, 1, "if len(%s) > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s exceeds max size of %d, got: {len(%s)}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
					} else {
						pyLinef(&code, 1, "# Validate %s (assert_max_size: %d bytes)", pyFieldName, annotationValue.IntValue)
						if field.Type == "bytes" {
							pyLinef(&code, 1, "if len(%s) > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						} else {
							pyLinef(&code, 1, "if len(%s.encode('utf-8')) > %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						}
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s exceeds max size of %d bytes\")", pyFieldName, annotationValue.IntValue)
					}
				}

			case "assert_element_max_size":
				// Per-element byte bound. The emission lives in the per-element
				// loop below, which applies this bound in place of the default
				// one it already enforces on every repeated/map string/bytes
				// element; here we only reject a misapplied annotation.
				if err := field.ValidateElementMaxSize(msg.Name); err != nil {
					return nil, err
				}

			case "assert_min_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsRepeated || field.IsMap {
						pyLinef(&code, 1, "# Validate %s (assert_min_size: %d)", pyFieldName, annotationValue.IntValue)
						pyLinef(&code, 1, "if len(%s) < %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s below min size of %d, got: {len(%s)}\")", pyFieldName, annotationValue.IntValue, pyFieldGet("msg", pyFieldName))
					} else {
						pyLinef(&code, 1, "# Validate %s (assert_min_size: %d bytes)", pyFieldName, annotationValue.IntValue)
						if field.Type == "bytes" {
							pyLinef(&code, 1, "if len(%s) < %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						} else {
							pyLinef(&code, 1, "if len(%s.encode('utf-8')) < %d:", pyFieldGet("msg", pyFieldName), annotationValue.IntValue)
						}
						pyLinef(&code, 2, "raise invalid_argument(f\"field %s below min size of %d bytes\")", pyFieldName, annotationValue.IntValue)
					}
				}
			default:
				// Unknown validation annotation (assert_* or required_if_*) is a hard
				// error: no Python
				// template exists, so the declared constraint would be silently dropped
				// (OF-26d). Non-validation annotations fall through harmlessly.
				if strings.HasPrefix(annotationName, "assert_") || strings.HasPrefix(annotationName, "required_if_") {
					return nil, fmt.Errorf("unsupported validation annotation %q on field %s.%s (no Python validation template)", annotationName, msg.Name, pyFieldName)
				}
			}

			if code.Len() > 0 {
				blocks = append(blocks, pyValidationCode{Code: code.String()})
			}
		}
	}

	// Default max size enforcement for repeated/map fields without explicit assert_max_size
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}
		if !field.IsRepeated && !field.IsMap {
			continue
		}
		if _, hasMaxSize := field.Annotations["assert_max_size"]; hasMaxSize {
			continue
		}

		pyFieldName := strutil.ToSnakeCase(field.Name)
		defaultCount := collection.DefaultCollCount

		var code strings.Builder
		pyLinef(&code, 1, "# Validate %s (default max size: %d elements)", pyFieldName, defaultCount)
		pyLinef(&code, 1, "if len(%s) > %d:", pyFieldGet("msg", pyFieldName), defaultCount)
		pyLinef(&code, 2, "raise invalid_argument(f\"field %s exceeds max size of %d elements, got: {len(%s)}\")", pyFieldName, defaultCount, pyFieldGet("msg", pyFieldName))
		blocks = append(blocks, pyValidationCode{Code: code.String()})
	}

	// Default per-element max size enforcement for repeated/map string/bytes values.
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		pyFieldName := strutil.ToSnakeCase(field.Name)
		defaultSize := collection.DefaultScalarBytes

		// An explicit (spec.assert_element_max_size) replaces the default bound
		// for repeated elements and map values; map keys keep the default.
		elemSize, elemLabel := defaultSize, "default max size"
		if bound := field.ElementMaxSizeBytes(); bound > 0 {
			elemSize, elemLabel = bound, "assert_element_max_size"
		}

		var code strings.Builder
		if field.IsRepeated && field.Type == "string" {
			pyLinef(&code, 1, "# Validate %s elements (%s: %d bytes)", pyFieldName, elemLabel, elemSize)
			pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "if len(val.encode('utf-8')) > %d:", elemSize)
			pyLinef(&code, 3, "raise invalid_argument(f\"field %s element exceeds max size of %d bytes, got: {len(val.encode('utf-8'))}\")", pyFieldName, elemSize)
		} else if field.IsRepeated && field.Type == "bytes" {
			pyLinef(&code, 1, "# Validate %s elements (%s: %d bytes)", pyFieldName, elemLabel, elemSize)
			pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "if len(val) > %d:", elemSize)
			pyLinef(&code, 3, "raise invalid_argument(f\"field %s element exceeds max size of %d bytes, got: {len(val)}\")", pyFieldName, elemSize)
		}

		if field.IsMap {
			if field.MapKeyType == "string" {
				pyLinef(&code, 1, "# Validate %s keys (default max size: %d bytes)", pyFieldName, defaultSize)
				pyLinef(&code, 1, "for key in %s.keys():", pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 2, "if len(key.encode('utf-8')) > %d:", defaultSize)
				pyLinef(&code, 3, "raise invalid_argument(f\"field %s key exceeds max size of %d bytes, got: {len(key.encode('utf-8'))}\")", pyFieldName, defaultSize)
			}
			if field.MapValueType == "string" {
				pyLinef(&code, 1, "# Validate %s values (%s: %d bytes)", pyFieldName, elemLabel, elemSize)
				pyLinef(&code, 1, "for val in %s.values():", pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 2, "if len(val.encode('utf-8')) > %d:", elemSize)
				pyLinef(&code, 3, "raise invalid_argument(f\"field %s value exceeds max size of %d bytes, got: {len(val.encode('utf-8'))}\")", pyFieldName, elemSize)
			} else if field.MapValueType == "bytes" {
				pyLinef(&code, 1, "# Validate %s values (%s: %d bytes)", pyFieldName, elemLabel, elemSize)
				pyLinef(&code, 1, "for val in %s.values():", pyFieldGet("msg", pyFieldName))
				pyLinef(&code, 2, "if len(val) > %d:", elemSize)
				pyLinef(&code, 3, "raise invalid_argument(f\"field %s value exceeds max size of %d bytes, got: {len(val)}\")", pyFieldName, elemSize)
			}
		}

		if code.Len() > 0 {
			blocks = append(blocks, pyValidationCode{Code: code.String()})
		}
	}

	// Default max size enforcement for singular string/bytes fields without explicit assert_max_size
	for _, field := range msg.Fields {
		if field.Type != "string" && field.Type != "bytes" {
			continue
		}
		if field.IsRepeated || field.IsMap {
			continue
		}
		if _, hasMaxSize := field.Annotations["assert_max_size"]; hasMaxSize {
			continue
		}

		pyFieldName := strutil.ToSnakeCase(field.Name)
		defaultSize := collection.DefaultScalarBytes

		var code strings.Builder
		pyLinef(&code, 1, "# Validate %s (default max size: %d bytes)", pyFieldName, defaultSize)
		if field.Type == "bytes" {
			pyLinef(&code, 1, "if len(%s) > %d:", pyFieldGet("msg", pyFieldName), defaultSize)
		} else {
			pyLinef(&code, 1, "if len(%s.encode('utf-8')) > %d:", pyFieldGet("msg", pyFieldName), defaultSize)
		}
		pyLinef(&code, 2, "raise invalid_argument(f\"field %s exceeds max size of %d bytes\")", pyFieldName, defaultSize)
		blocks = append(blocks, pyValidationCode{Code: code.String()})
	}

	// Recursive validation for nested generated messages, including oneof variants.
	for _, field := range msg.Fields {
		pyFieldName := strutil.ToSnakeCase(field.Name)

		if field.MapValueIsEnum && field.MapValueTypePackage == "" {
			var code strings.Builder
			pyLinef(&code, 1, "# Validate %s enum values are known", pyFieldName)
			pyLinef(&code, 1, "for key, val in %s.items():", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "if int(val) not in %s.DESCRIPTOR.values_by_number:", field.MapValueType)
			pyLinef(&code, 3, "raise invalid_argument(f\"field %s[{key}] has unknown enum value {int(val)}\")", pyFieldName)
			blocks = append(blocks, pyValidationCode{Code: code.String()})
			continue
		}

		if !field.IsEnum || field.TypePackage != "" {
			continue
		}

		var code strings.Builder
		pyLinef(&code, 1, "# Validate %s enum value is known", pyFieldName)
		if field.IsRepeated {
			pyLinef(&code, 1, "for i, val in enumerate(%s):", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "if int(val) not in %s.DESCRIPTOR.values_by_number:", field.Type)
			pyLinef(&code, 3, "raise invalid_argument(f\"field %s[{i}] has unknown enum value {int(val)}\")", pyFieldName)
		} else if field.IsOptional {
			pyLinef(&code, 1, "if msg.HasField('%s') and int(%s) not in %s.DESCRIPTOR.values_by_number:", field.Name, pyFieldGet("msg", pyFieldName), field.Type)
			pyLinef(&code, 2, "raise invalid_argument(f\"field %s has unknown enum value {int(%s)}\")", pyFieldName, pyFieldGet("msg", pyFieldName))
		} else {
			pyLinef(&code, 1, "if int(%s) not in %s.DESCRIPTOR.values_by_number:", pyFieldGet("msg", pyFieldName), field.Type)
			pyLinef(&code, 2, "raise invalid_argument(f\"field %s has unknown enum value {int(%s)}\")", pyFieldName, pyFieldGet("msg", pyFieldName))
		}
		blocks = append(blocks, pyValidationCode{Code: code.String()})
	}

	// Recursively validate nested generated messages, including oneof variants.
	for _, field := range msg.Fields {
		pyFieldName := strutil.ToSnakeCase(field.Name)

		if field.MapValueIsMessage {
			validateFunc := pythonMessageValidationFunc(field.MapValueType, field.MapValueTypePackage)
			if validateFunc == "" {
				continue
			}

			var code strings.Builder
			pyLinef(&code, 1, "# Validate %s message values", pyFieldName)
			pyLinef(&code, 1, "for val in %s.values():", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "%s(val, _depth + 1)", validateFunc)
			blocks = append(blocks, pyValidationCode{Code: code.String()})
			continue
		}

		if !field.IsMessage {
			continue
		}

		validateFunc := pythonMessageValidationFunc(field.Type, field.TypePackage)
		if validateFunc == "" {
			continue
		}

		var code strings.Builder
		if field.IsOneOf {
			pyLinef(&code, 1, "# Validate %s nested message", pyFieldName)
			pyLinef(&code, 1, "if msg.WhichOneof('%s') == '%s':", field.OneOfName, field.Name)
			pyLinef(&code, 2, "%s(%s, _depth + 1)", validateFunc, pyFieldGet("msg", pyFieldName))
		} else if field.IsRepeated {
			pyLinef(&code, 1, "# Validate %s nested messages", pyFieldName)
			pyLinef(&code, 1, "for val in %s:", pyFieldGet("msg", pyFieldName))
			pyLinef(&code, 2, "%s(val, _depth + 1)", validateFunc)
		} else {
			pyLinef(&code, 1, "# Validate %s nested message", pyFieldName)
			pyLinef(&code, 1, "if msg.HasField('%s'):", field.Name)
			pyLinef(&code, 2, "%s(%s, _depth + 1)", validateFunc, pyFieldGet("msg", pyFieldName))
		}
		blocks = append(blocks, pyValidationCode{Code: code.String()})
	}

	return blocks, nil
}

func pythonMessageValidationFunc(typeName, typePackage string) string {
	if typeName == "" || typePackage != "" {
		return ""
	}
	return "validate_" + strutil.ToSnakeCase(typeName)
}

func buildPyEnumDefinedValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string) error {
	if !field.IsEnum {
		return fmt.Errorf("assert_enum_defined annotation on field %s.%s requires an enum field", msg.Name, fieldName)
	}

	pyLinef(b, 1, "# Validate %s (assert_enum_defined)", fieldName)
	if field.IsRepeated {
		pyLinef(b, 1, "for i, val in enumerate(%s):", pyFieldGet("msg", fieldName))
		pyLine(b, 2, "if val == 0:")
		pyLinef(b, 3, "raise invalid_argument(f\"field %s[{i}] must be defined\")", fieldName)
	} else if field.IsOptional {
		pyLinef(b, 1, "if msg.HasField('%s') and %s == 0:", fieldName, pyFieldGet("msg", fieldName))
		pyLinef(b, 2, "raise invalid_argument(\"field %s must be defined\")", fieldName)
	} else {
		pyLinef(b, 1, "if %s == 0:", pyFieldGet("msg", fieldName))
		pyLinef(b, 2, "raise invalid_argument(\"field %s must be defined\")", fieldName)
	}

	return nil
}

func buildPyNumericConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.NumericConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isIntegerType(field.Type) {
		return fmt.Errorf("assert_numeric annotation on field %s.%s requires an integer field", msg.Name, fieldName)
	}
	if constraint.MultipleOf != nil && *constraint.MultipleOf == 0 {
		return fmt.Errorf("assert_numeric multiple_of annotation on field %s.%s must be non-zero", msg.Name, fieldName)
	}

	pyLinef(b, 1, "# Validate %s (assert_numeric)", fieldName)
	if field.IsRepeated {
		pyLinef(b, 1, "for i, val in enumerate(%s):", pyFieldGet("msg", fieldName))
		writePyNumericConstraintChecks(b, fieldName+"[{i}]", "val", constraint, 2)
		return nil
	}
	if field.IsOptional {
		pyLinef(b, 1, "if msg.HasField('%s'):", fieldName)
		pyLinef(b, 2, "val = %s", pyFieldGet("msg", fieldName))
		writePyNumericConstraintChecks(b, fieldName, "val", constraint, 2)
		return nil
	}

	pyLinef(b, 1, "val = %s", pyFieldGet("msg", fieldName))
	writePyNumericConstraintChecks(b, fieldName, "val", constraint, 1)
	return nil
}

func writePyNumericConstraintChecks(b *strings.Builder, fieldLabel, valueExpr string, constraint *model.NumericConstraint, level int) {
	if constraint.Gt != nil {
		pyLinef(b, level, "if %s <= %d:", valueExpr, *constraint.Gt)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be > %d, got: {%s}\")", fieldLabel, *constraint.Gt, valueExpr)
	}
	if constraint.Ge != nil {
		pyLinef(b, level, "if %s < %d:", valueExpr, *constraint.Ge)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be >= %d, got: {%s}\")", fieldLabel, *constraint.Ge, valueExpr)
	}
	if constraint.Lt != nil {
		pyLinef(b, level, "if %s >= %d:", valueExpr, *constraint.Lt)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be < %d, got: {%s}\")", fieldLabel, *constraint.Lt, valueExpr)
	}
	if constraint.Le != nil {
		pyLinef(b, level, "if %s > %d:", valueExpr, *constraint.Le)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be <= %d, got: {%s}\")", fieldLabel, *constraint.Le, valueExpr)
	}
	if constraint.Eq != nil {
		pyLinef(b, level, "if %s != %d:", valueExpr, *constraint.Eq)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must equal %d, got: {%s}\")", fieldLabel, *constraint.Eq, valueExpr)
	}
	if constraint.Ne != nil {
		pyLinef(b, level, "if %s == %d:", valueExpr, *constraint.Ne)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must not equal %d, got: {%s}\")", fieldLabel, *constraint.Ne, valueExpr)
	}
	if len(constraint.In) > 0 {
		values := pyIntListLiteral(constraint.In)
		pyLinef(b, level, "if %s not in %s:", valueExpr, values)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be one of %s, got: {%s}\")", fieldLabel, values, valueExpr)
	}
	if len(constraint.NotIn) > 0 {
		values := pyIntListLiteral(constraint.NotIn)
		pyLinef(b, level, "if %s in %s:", valueExpr, values)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must not be one of %s, got: {%s}\")", fieldLabel, values, valueExpr)
	}
	if constraint.MultipleOf != nil {
		pyLinef(b, level, "if %s %% %d != 0:", valueExpr, *constraint.MultipleOf)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be a multiple of %d, got: {%s}\")", fieldLabel, *constraint.MultipleOf, valueExpr)
	}
}

func buildPyFloatConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.FloatConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isFloatType(field.Type) {
		return fmt.Errorf("assert_float annotation on field %s.%s requires a float or double field", msg.Name, fieldName)
	}

	epsilon := 1e-9
	if constraint.Epsilon != nil {
		epsilon = *constraint.Epsilon
	}

	pyLinef(b, 1, "# Validate %s (assert_float)", fieldName)
	if field.IsRepeated {
		pyLinef(b, 1, "for i, val in enumerate(%s):", pyFieldGet("msg", fieldName))
		writePyFloatConstraintChecks(b, fieldName+"[{i}]", "val", constraint, epsilon, 2)
		return nil
	}
	if field.IsOptional {
		pyLinef(b, 1, "if msg.HasField('%s'):", fieldName)
		pyLinef(b, 2, "val = %s", pyFieldGet("msg", fieldName))
		writePyFloatConstraintChecks(b, fieldName, "val", constraint, epsilon, 2)
		return nil
	}

	pyLinef(b, 1, "val = %s", pyFieldGet("msg", fieldName))
	writePyFloatConstraintChecks(b, fieldName, "val", constraint, epsilon, 1)
	return nil
}

func writePyFloatConstraintChecks(b *strings.Builder, fieldLabel, valueExpr string, constraint *model.FloatConstraint, epsilon float64, level int) {
	if constraint.Gt != nil {
		threshold := pyFloatLiteral(*constraint.Gt)
		pyLinef(b, level, "if %s <= %s:", valueExpr, threshold)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be > %s, got: {%s}\")", fieldLabel, threshold, valueExpr)
	}
	if constraint.Ge != nil {
		threshold := pyFloatLiteral(*constraint.Ge)
		pyLinef(b, level, "if %s < %s:", valueExpr, threshold)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be >= %s, got: {%s}\")", fieldLabel, threshold, valueExpr)
	}
	if constraint.Lt != nil {
		threshold := pyFloatLiteral(*constraint.Lt)
		pyLinef(b, level, "if %s >= %s:", valueExpr, threshold)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be < %s, got: {%s}\")", fieldLabel, threshold, valueExpr)
	}
	if constraint.Le != nil {
		threshold := pyFloatLiteral(*constraint.Le)
		pyLinef(b, level, "if %s > %s:", valueExpr, threshold)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be <= %s, got: {%s}\")", fieldLabel, threshold, valueExpr)
	}
	if len(constraint.In) > 0 {
		values := pyFloatListLiteral(constraint.In)
		epsilonLiteral := pyFloatLiteral(epsilon)
		pyLinef(b, level, "if not any(abs(%s - candidate) <= %s for candidate in %s):", valueExpr, epsilonLiteral, values)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must be one of %s, got: {%s}\")", fieldLabel, values, valueExpr)
	}
	if len(constraint.NotIn) > 0 {
		values := pyFloatListLiteral(constraint.NotIn)
		epsilonLiteral := pyFloatLiteral(epsilon)
		pyLinef(b, level, "if any(abs(%s - candidate) <= %s for candidate in %s):", valueExpr, epsilonLiteral, values)
		pyLinef(b, level+1, "raise invalid_argument(f\"field %s must not be one of %s, got: {%s}\")", fieldLabel, values, valueExpr)
	}
}

func pyIntListLiteral(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pyFloatListLiteral(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, pyFloatLiteral(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pyFloatLiteral(value float64) string {
	s := fmt.Sprintf("%g", value)
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}

func buildIPAddrValidation(b *strings.Builder, fieldName, ipType string, isRepeated bool) {
	pyLinef(b, 1, "# Validate %s (assert_ipaddr_type: %s)", fieldName, ipType)

	var baseLevel int
	if isRepeated {
		pyLinef(b, 1, "for val in %s:", pyFieldGet("msg", fieldName))
		pyLine(b, 2, "if val:")
		baseLevel = 3
	} else {
		pyLinef(b, 1, "if %s:", pyFieldGet("msg", fieldName))
		baseLevel = 2
	}
	varRef := pyFieldGet("msg", fieldName)
	if isRepeated {
		varRef = "val"
	}

	switch ipType {
	case "IP_ADDR_TYPE_IP_V4":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "addr = ipaddress.IPv4Address(%s)", varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IPv4 address, got: {%s}\")", fieldName, varRef)

	case "IP_ADDR_TYPE_IP_V6":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "addr = ipaddress.IPv6Address(%s)", varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IPv6 address, got: {%s}\")", fieldName, varRef)

	case "IP_ADDR_TYPE_IP_ANY":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "addr = ipaddress.ip_address(%s)", varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IP address, got: {%s}\")", fieldName, varRef)

	case "IP_ADDR_TYPE_NET_V4", "IP_ADDR_TYPE_NET_V6", "IP_ADDR_TYPE_NET_ANY":
		pyLinef(b, baseLevel, "_validate_cidr(%s, '%s', '%s')", varRef, ipType, fieldName)

	case "IP_ADDR_TYPE_MASK_V4":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "mask = int(%s)", varRef)
		pyLine(b, baseLevel+1, "if mask < 0 or mask > 32:")
		pyLinef(b, baseLevel+2, "raise invalid_argument(f\"field %s must be a valid IPv4 mask (0-32), got: {%s}\")", fieldName, varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IPv4 mask (0-32), got: {%s}\")", fieldName, varRef)

	case "IP_ADDR_TYPE_MASK_V6":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "mask = int(%s)", varRef)
		pyLine(b, baseLevel+1, "if mask < 0 or mask > 128:")
		pyLinef(b, baseLevel+2, "raise invalid_argument(f\"field %s must be a valid IPv6 mask (0-128), got: {%s}\")", fieldName, varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IPv6 mask (0-128), got: {%s}\")", fieldName, varRef)

	case "IP_ADDR_TYPE_MASK_ANY":
		pyLine(b, baseLevel, "try:")
		pyLinef(b, baseLevel+1, "mask = int(%s)", varRef)
		pyLine(b, baseLevel+1, "if mask < 0 or mask > 128:")
		pyLinef(b, baseLevel+2, "raise invalid_argument(f\"field %s must be a valid IP mask (0-128), got: {%s}\")", fieldName, varRef)
		pyLine(b, baseLevel, "except ValueError:")
		pyLinef(b, baseLevel+1, "raise invalid_argument(f\"field %s must be a valid IP mask (0-128), got: {%s}\")", fieldName, varRef)
	}
}
