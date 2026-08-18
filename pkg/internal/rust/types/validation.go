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

// validatorUsage tracks which helper validators and imports are needed
type validatorUsage struct {
	needsEmail      bool
	needsFqdn       bool
	needsBase64     bool
	needsListenAddr bool
	needsCidr       bool
	needsIpv4Addr   bool
	needsIpv6Addr   bool
	needsIpAddr     bool
	needsSemver     bool
	needsHost       bool
	needsSql        bool
	needsUrl        bool
	needsFilePath   bool
	needsEncType    bool
}

// rustValidationTemplateData holds data for the validation template.
type rustValidationTemplateData struct {
	TypeName       string
	HasValidations bool
	ValidationCode []string // Pre-computed validation code lines
}

// rustValidationTestTemplateData holds data for the validation test template.
type rustValidationTestTemplateData struct {
	TypeName       string
	SnakeName      string
	HasValidations bool
}

// rustValidationHelpersData holds data for the validation helpers template.
type rustValidationHelpersData struct {
	NeedsEmail      bool
	NeedsFqdn       bool
	NeedsBase64     bool
	NeedsListenAddr bool
	NeedsCidr       bool
	NeedsSemver     bool
	NeedsHost       bool
	NeedsSql        bool
	NeedsUrl        bool
	NeedsFilePath   bool
	NeedsEncType    bool
}

// rustRequiredZeroCheck returns the Rust expression that tests whether a field is
// at its zero value. It drives the whole requirement family — assert_required
// plus the conditional required_if_true / required_if_false / required_if_eq — and
// must be type-correct: `.is_empty()` compiles only for String / Vec / HashMap, so
// a requirement on a numeric, bool, enum, message or optional field used to emit
// generated Rust that does not build.
func rustRequiredZeroCheck(field model.Field, rustFieldName string) string {
	switch {
	case field.IsRepeated || field.IsMap:
		return fmt.Sprintf("self.%s.is_empty()", rustFieldName)
	case field.IsOptional || field.IsMessage:
		// prost renders singular message fields and proto3 `optional` scalars as Option<T>.
		return fmt.Sprintf("self.%s.is_none()", rustFieldName)
	case field.Type == "string" || field.Type == "bytes":
		return fmt.Sprintf("self.%s.is_empty()", rustFieldName)
	case field.Type == "bool":
		return fmt.Sprintf("!self.%s", rustFieldName)
	default:
		// Numeric scalars and enums (prost renders enums as i32) compare against 0.
		return fmt.Sprintf("self.%s == 0", rustFieldName)
	}
}

// writeValidation writes validation functions to the shared builder
func writeValidation(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	b.WriteString("// ---- Validation ----\n")

	// Track which validators are actually used
	usage := &validatorUsage{}
	for _, msg := range pf.Messages {
		trackValidatorUsage(msg, usage)
	}

	// Generate validation for each message
	for _, msg := range pf.Messages {
		if err := generateMessageValidation(b, msg, pf.Enums); err != nil {
			fmt.Fprintf(b, "// ERROR generating validation for %s: %s\n", msg.Name, err)
		}
	}

	// Generate only the helper validation functions that are actually used
	if err := generateHelperValidators(b, usage); err != nil {
		fmt.Fprintf(b, "// ERROR generating helper validators: %s\n", err)
	}
}

// trackValidatorUsage analyzes a message to determine which helpers are needed
func trackValidatorUsage(msg model.Message, usage *validatorUsage) {
	for _, field := range msg.Fields {
		for annotationName, annotationValue := range field.Annotations {
			switch annotationName {
			case "assert_email":
				usage.needsEmail = true
			case "assert_fqdn":
				usage.needsFqdn = true
			case "assert_base64":
				usage.needsBase64 = true
			case "assert_listen_addr":
				usage.needsListenAddr = true
			case "assert_ipaddr_type":
				if annotationValue.Type == model.AnnotationTypeEnum {
					switch annotationValue.EnumValue {
					case "IP_ADDR_TYPE_IP_V4":
						usage.needsIpv4Addr = true
					case "IP_ADDR_TYPE_IP_V6":
						usage.needsIpv6Addr = true
					case "IP_ADDR_TYPE_IP_ANY":
						usage.needsIpAddr = true
					case "IP_ADDR_TYPE_NET_V4", "IP_ADDR_TYPE_NET_V6", "IP_ADDR_TYPE_NET_ANY":
						usage.needsCidr = true
						usage.needsIpAddr = true
					}
				}
			case "assert_semver":
				usage.needsSemver = true
			case "assert_host":
				usage.needsHost = true
				usage.needsFqdn = true
			case "assert_sql":
				usage.needsSql = true
			case "assert_url":
				usage.needsUrl = true
			case "assert_file_path":
				usage.needsFilePath = true
			case "assert_enc_type":
				usage.needsEncType = true
			}
		}
	}
}

func generateMessageValidation(b *strings.Builder, msg model.Message, enums []model.Enum) error {
	typeName := toProstTypeName(msg.Name)

	// Check if there are any validations needed.
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
		if field.IsMessage && rustMessageHasValidate(field.Type, field.TypePackage) {
			hasValidations = true
			break
		}
		if field.MapValueIsMessage && rustMessageHasValidate(field.MapValueType, field.MapValueTypePackage) {
			hasValidations = true
			break
		}
		if (field.IsEnum && field.TypePackage == "") || (field.MapValueIsEnum && field.MapValueTypePackage == "") {
			hasValidations = true
			break
		}
	}

	// Pre-compute validation code lines
	var validationCode []string
	if hasValidations {
		var vb strings.Builder
		for _, field := range msg.Fields {
			if field.IsOneOf {
				generateRustOneofScalarMaxSize(&vb, msg, field)
				continue
			}
			if len(field.Annotations) == 0 {
				continue
			}
			if err := generateFieldValidation(&vb, field, msg, enums); err != nil {
				return err
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

			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
			defaultCount := collection.DefaultCollCount

			fmt.Fprintf(&vb, "// Validate %s (default max size: %d elements)\n", rustFieldName, defaultCount)
			fmt.Fprintf(&vb, "if self.%s.len() > %d {\n", rustFieldName, defaultCount)
			fmt.Fprintf(&vb, "return Err(format!(\"field %s exceeds max size of %d elements, got: {}\", self.%s.len()));\n", rustFieldName, defaultCount, rustFieldName)
			vb.WriteString("}\n")
		}

		// Default per-element max size enforcement for repeated/map string/bytes values.
		for _, field := range msg.Fields {
			if field.IsOneOf {
				continue
			}

			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
			defaultSize := collection.DefaultScalarBytes

			if field.IsRepeated && (field.Type == "string" || field.Type == "bytes") {
				fmt.Fprintf(&vb, "// Validate %s elements (default max size: %d bytes)\n", rustFieldName, defaultSize)
				fmt.Fprintf(&vb, "for (i, val) in self.%s.iter().enumerate() {\n", rustFieldName)
				fmt.Fprintf(&vb, "if val.len() > %d {\n", defaultSize)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s[{}] exceeds max size of %d bytes, got: {}\", i, val.len()));\n", rustFieldName, defaultSize)
				vb.WriteString("}\n")
				vb.WriteString("}\n")
			}

			if field.IsMap {
				if field.MapKeyType == "string" {
					fmt.Fprintf(&vb, "// Validate %s keys (default max size: %d bytes)\n", rustFieldName, defaultSize)
					fmt.Fprintf(&vb, "for key in self.%s.keys() {\n", rustFieldName)
					fmt.Fprintf(&vb, "if key.len() > %d {\n", defaultSize)
					fmt.Fprintf(&vb, "return Err(format!(\"field %s key exceeds max size of %d bytes, got: {}\", key.len()));\n", rustFieldName, defaultSize)
					vb.WriteString("}\n")
					vb.WriteString("}\n")
				}
				if field.MapValueType == "string" || field.MapValueType == "bytes" {
					fmt.Fprintf(&vb, "// Validate %s values (default max size: %d bytes)\n", rustFieldName, defaultSize)
					fmt.Fprintf(&vb, "for (key, val) in self.%s.iter() {\n", rustFieldName)
					fmt.Fprintf(&vb, "if val.len() > %d {\n", defaultSize)
					fmt.Fprintf(&vb, "return Err(format!(\"field %s[{:?}] exceeds max size of %d bytes, got: {}\", key, val.len()));\n", rustFieldName, defaultSize)
					vb.WriteString("}\n")
					vb.WriteString("}\n")
				}
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

			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
			defaultSize := collection.DefaultScalarBytes

			fmt.Fprintf(&vb, "// Validate %s (default max size: %d bytes)\n", rustFieldName, defaultSize)
			if field.IsOneOf {
				writeRustOneofScalarMaxSize(&vb, msg, field, defaultSize)
			} else if field.IsOptional {
				fmt.Fprintf(&vb, "if let Some(ref val) = self.%s {\n", rustFieldName)
				fmt.Fprintf(&vb, "if val.len() > %d {\n", defaultSize)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s exceeds max size of %d bytes, got: {}\", val.len()));\n", rustFieldName, defaultSize)
				vb.WriteString("}\n")
				vb.WriteString("}\n")
			} else {
				fmt.Fprintf(&vb, "if self.%s.len() > %d {\n", rustFieldName, defaultSize)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s exceeds max size of %d bytes, got: {}\", self.%s.len()));\n", rustFieldName, defaultSize, rustFieldName)
				vb.WriteString("}\n")
			}
		}

		// Recursive validation for nested generated messages, including oneof variants.
		for _, field := range msg.Fields {
			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

			if field.MapValueIsEnum && field.MapValueTypePackage == "" {
				enumType := toProstEnumName(field.MapValueType)
				fmt.Fprintf(&vb, "// Validate %s enum values are known\n", rustFieldName)
				fmt.Fprintf(&vb, "for (key, val) in self.%s.iter() {\n", rustFieldName)
				fmt.Fprintf(&vb, "if %s::try_from(*val).is_err() {\n", enumType)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s[{:?}] has unknown enum value {}\", key, val));\n", rustFieldName)
				vb.WriteString("}\n")
				vb.WriteString("}\n")
				continue
			}

			if !field.IsEnum || field.TypePackage != "" {
				continue
			}

			enumType := toProstEnumName(field.Type)
			fmt.Fprintf(&vb, "// Validate %s enum value is known\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(&vb, "for (i, val) in self.%s.iter().enumerate() {\n", rustFieldName)
				fmt.Fprintf(&vb, "if %s::try_from(*val).is_err() {\n", enumType)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s[{}] has unknown enum value {}\", i, val));\n", rustFieldName)
				vb.WriteString("}\n")
				vb.WriteString("}\n")
			} else if field.IsOptional {
				fmt.Fprintf(&vb, "if let Some(val) = self.%s {\n", rustFieldName)
				fmt.Fprintf(&vb, "if %s::try_from(val).is_err() {\n", enumType)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s has unknown enum value {}\", val));\n", rustFieldName)
				vb.WriteString("}\n")
				vb.WriteString("}\n")
			} else {
				fmt.Fprintf(&vb, "if %s::try_from(self.%s).is_err() {\n", enumType, rustFieldName)
				fmt.Fprintf(&vb, "return Err(format!(\"field %s has unknown enum value {}\", self.%s));\n", rustFieldName, rustFieldName)
				vb.WriteString("}\n")
			}
		}

		// Recursive validation for nested generated messages, including oneof variants.
		for _, field := range msg.Fields {
			rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

			if field.MapValueIsMessage {
				if !rustMessageHasValidate(field.MapValueType, field.MapValueTypePackage) {
					continue
				}
				fmt.Fprintf(&vb, "// Validate %s message values\n", rustFieldName)
				fmt.Fprintf(&vb, "for (key, val) in self.%s.iter() {\n", rustFieldName)
				fmt.Fprintf(&vb, "val.validate_with_depth(depth + 1).map_err(|e| format!(\"field %s[{:?}] validation failed: {}\", key, e))?;\n", rustFieldName)
				vb.WriteString("}\n")
				continue
			}

			if !field.IsMessage || !rustMessageHasValidate(field.Type, field.TypePackage) {
				continue
			}

			fmt.Fprintf(&vb, "// Validate %s nested message", rustFieldName)
			if field.IsOneOf {
				vb.WriteString("\n")
				groupField := escapeRustKeyword(strutil.ToSnakeCase(field.OneOfName))
				moduleName := escapeRustKeyword(strutil.ToSnakeCase(msg.Name))
				enumName := strutil.ToCamelCase(field.OneOfName)
				variantName := strutil.ToCamelCase(field.Name)
				fmt.Fprintf(&vb, "if let Some(%s::%s::%s(val)) = &self.%s {\n", moduleName, enumName, variantName, groupField)
				fmt.Fprintf(&vb, "val.validate_with_depth(depth + 1).map_err(|e| format!(\"field %s validation failed: {}\", e))?;\n", rustFieldName)
				vb.WriteString("}\n")
			} else if field.IsRepeated {
				vb.WriteString("s\n")
				fmt.Fprintf(&vb, "for (i, val) in self.%s.iter().enumerate() {\n", rustFieldName)
				fmt.Fprintf(&vb, "val.validate_with_depth(depth + 1).map_err(|e| format!(\"field %s[{}] validation failed: {}\", i, e))?;\n", rustFieldName)
				vb.WriteString("}\n")
			} else {
				vb.WriteString("\n")
				fmt.Fprintf(&vb, "if let Some(val) = &self.%s {\n", rustFieldName)
				fmt.Fprintf(&vb, "val.validate_with_depth(depth + 1).map_err(|e| format!(\"field %s validation failed: {}\", e))?;\n", rustFieldName)
				vb.WriteString("}\n")
			}
		}

		for _, line := range strings.Split(vb.String(), "\n") {
			if line != "" {
				validationCode = append(validationCode, line)
			}
		}
	}

	data := rustValidationTemplateData{
		TypeName:       typeName,
		HasValidations: hasValidations,
		ValidationCode: validationCode,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "validation.rs.tmpl", funcMap, data)
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

func rustMessageHasValidate(typeName, typePackage string) bool {
	if typeName == "" {
		return false
	}
	switch typePackage {
	case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb",
		"google.protobuf":
		return false
	}
	return true
}

func generateRustOneofScalarMaxSize(b *strings.Builder, msg model.Message, field model.Field) {
	if field.Type != "string" && field.Type != "bytes" {
		return
	}
	annotationValue, ok := field.Annotations["assert_max_size"]
	if !ok || annotationValue.Type != model.AnnotationTypeInt {
		return
	}

	rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
	fmt.Fprintf(b, "// Validate %s (assert_max_size: %d bytes)\n", rustFieldName, annotationValue.IntValue)
	writeRustOneofScalarMaxSize(b, msg, field, annotationValue.IntValue)
}

func writeRustOneofScalarMaxSize(b *strings.Builder, msg model.Message, field model.Field, maxSize int64) {
	groupField := escapeRustKeyword(strutil.ToSnakeCase(field.OneOfName))
	moduleName := escapeRustKeyword(strutil.ToSnakeCase(msg.Name))
	enumName := strutil.ToCamelCase(field.OneOfName)
	variantName := strutil.ToCamelCase(field.Name)
	rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

	fmt.Fprintf(b, "if let Some(%s::%s::%s(val)) = &self.%s {\n", moduleName, enumName, variantName, groupField)
	fmt.Fprintf(b, "if val.len() > %d {\n", maxSize)
	fmt.Fprintf(b, "return Err(format!(\"field %s exceeds max size of %d bytes, got: {}\", val.len()));\n", rustFieldName, maxSize)
	b.WriteString("}\n")
	b.WriteString("}\n")
}

// generateFieldValidation generates validation code for a single field's annotations
func generateFieldValidation(b *strings.Builder, field model.Field, msg model.Message, _enums []model.Enum) error {
	rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))

	for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
		annotationValue := field.Annotations[annotationName]
		// Skip boolean annotations that are set to false
		if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
			continue
		}

		switch annotationName {
		case "assert_required":
			// Shares rustRequiredZeroCheck with the conditional-requirement family so
			// that (spec.assert_required) and (spec.required_if_true) test the same
			// field the same way.
			fmt.Fprintf(b, "// Validate %s (assert_required)\n", rustFieldName)
			fmt.Fprintf(b, "if %s {\n", rustRequiredZeroCheck(field, rustFieldName))
			fmt.Fprintf(b, "return Err(format!(\"field %s is required\"));\n", rustFieldName)
			b.WriteString("}\n")

		case "assert_email":
			fmt.Fprintf(b, "// Validate %s (assert_email)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_email(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_email(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_fqdn":
			fmt.Fprintf(b, "// Validate %s (assert_fqdn)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_fqdn(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_fqdn(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_host":
			fmt.Fprintf(b, "// Validate %s (assert_host: IP address or FQDN)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_host(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_host(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_base64":
			fmt.Fprintf(b, "// Validate %s (assert_base64)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_base64(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_base64(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_listen_addr":
			fmt.Fprintf(b, "// Validate %s (assert_listen_addr)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_listen_addr(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_listen_addr(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_net_port":
			if err := generateRustDurationBoundValidation(b, msg, field, rustFieldName, "assert_net_port", "assert_net_port", "%[1]s < 0 || %[1]s > 65535", "a valid port number (0-65535)"); err != nil {
				return err
			}

		case "assert_ipaddr_type":
			if annotationValue.Type == model.AnnotationTypeEnum {
				generateIPAddrValidation(b, rustFieldName, annotationValue.EnumValue, field.IsRepeated)
			}

		case "assert_duration":
			if annotationValue.Type == model.AnnotationTypeEnum {
				switch annotationValue.EnumValue {
				case "DURATION_TYPE_POSITIVE":
					if err := generateRustDurationBoundValidation(b, msg, field, rustFieldName, "assert_duration", "assert_duration: POSITIVE", "%[1]s <= 0", "positive (> 0)"); err != nil {
						return err
					}
				case "DURATION_TYPE_NON_NEGATIVE":
					if err := generateRustDurationBoundValidation(b, msg, field, rustFieldName, "assert_duration", "assert_duration: NON_NEGATIVE", "%[1]s < 0", "non-negative (>= 0)"); err != nil {
						return err
					}
				}
			}

		case "assert_duration_min":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustDurationBoundValidation(b, msg, field, rustFieldName, "assert_duration_min",
					fmt.Sprintf("assert_duration_min: %d", annotationValue.IntValue),
					fmt.Sprintf("%%[1]s < %d", annotationValue.IntValue),
					fmt.Sprintf(">= %d", annotationValue.IntValue)); err != nil {
					return err
				}
			}

		case "assert_duration_max":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustDurationBoundValidation(b, msg, field, rustFieldName, "assert_duration_max",
					fmt.Sprintf("assert_duration_max: %d", annotationValue.IntValue),
					fmt.Sprintf("%%[1]s > %d", annotationValue.IntValue),
					fmt.Sprintf("<= %d", annotationValue.IntValue)); err != nil {
					return err
				}
			}

		case "assert_gt":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustComparisonValidation(b, msg, field, rustFieldName, "assert_gt", "<=", ">", annotationValue.IntValue); err != nil {
					return err
				}
			}

		case "assert_ge":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustComparisonValidation(b, msg, field, rustFieldName, "assert_ge", "<", ">=", annotationValue.IntValue); err != nil {
					return err
				}
			}

		case "assert_lt":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustComparisonValidation(b, msg, field, rustFieldName, "assert_lt", ">=", "<", annotationValue.IntValue); err != nil {
					return err
				}
			}

		case "assert_le":
			if annotationValue.Type == model.AnnotationTypeInt {
				if err := generateRustComparisonValidation(b, msg, field, rustFieldName, "assert_le", ">", "<=", annotationValue.IntValue); err != nil {
					return err
				}
			}

		case "assert_semver":
			fmt.Fprintf(b, "// Validate %s (assert_semver)\n", rustFieldName)
			if field.IsRepeated {
				fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
				b.WriteString("if !val.is_empty() {\n")
				b.WriteString("validate_semver(val)?;\n")
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else {
				fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
				fmt.Fprintf(b, "validate_semver(&self.%s)?;\n", rustFieldName)
				b.WriteString("}\n")
			}

		case "assert_sql":
			if annotationValue.Type == model.AnnotationTypeEnum {
				fmt.Fprintf(b, "// Validate %s (assert_sql: %s)\n", rustFieldName, annotationValue.EnumValue)
				if field.IsRepeated {
					fmt.Fprintf(b, "for val in &self.%s {\n", rustFieldName)
					b.WriteString("if !val.is_empty() {\n")
					fmt.Fprintf(b, "validate_sql(val, \"%s\")?;\n", annotationValue.EnumValue)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if !self.%s.is_empty() {\n", rustFieldName)
					fmt.Fprintf(b, "validate_sql(&self.%s, \"%s\")?;\n", rustFieldName, annotationValue.EnumValue)
					b.WriteString("}\n")
				}
			}

		case "assert_url":
			if field.Type != "string" {
				return fmt.Errorf("assert_url annotation on field %s.%s requires string type", msg.Name, rustFieldName)
			}
			if annotationValue.Type == model.AnnotationTypeEnum {
				generateRustStringHelperValidation(b, rustFieldName, "assert_url", annotationValue.EnumValue, field.IsRepeated, "validate_url", "URL validation failed")
			}

		case "assert_file_path":
			if field.Type != "string" {
				return fmt.Errorf("assert_file_path annotation on field %s.%s requires string type", msg.Name, rustFieldName)
			}
			if annotationValue.Type == model.AnnotationTypeEnum {
				generateRustStringHelperValidation(b, rustFieldName, "assert_file_path", annotationValue.EnumValue, field.IsRepeated, "validate_file_path", "file path validation failed")
			}

		case "assert_enc_type":
			if field.Type != "string" {
				return fmt.Errorf("assert_enc_type annotation on field %s.%s requires string type", msg.Name, rustFieldName)
			}
			if annotationValue.Type == model.AnnotationTypeEnum {
				generateRustStringHelperValidation(b, rustFieldName, "assert_enc_type", annotationValue.EnumValue, field.IsRepeated, "validate_enc_type", "encryption validation failed")
			}

		case "assert_enum_defined":
			if err := generateRustEnumDefinedValidation(b, msg, field, rustFieldName); err != nil {
				return err
			}

		case "assert_numeric":
			if err := generateRustNumericConstraintValidation(b, msg, field, rustFieldName, annotationValue.NumericConstraint); err != nil {
				return err
			}

		case "assert_float":
			if err := generateRustFloatConstraintValidation(b, msg, field, rustFieldName, annotationValue.FloatConstraint); err != nil {
				return err
			}

		case "epoch_expiration":
			fmt.Fprintf(b, "// Validate %s (epoch_expiration)\n", rustFieldName)
			fmt.Fprintf(b, "if self.%s != 0 {\n", rustFieldName)
			b.WriteString("let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() as i64;\n")
			fmt.Fprintf(b, "if self.%s <= now {\n", rustFieldName)
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a future epoch timestamp, got: {} (now: {})\", self.%s, now));\n", rustFieldName, rustFieldName)
			b.WriteString("}\n")
			fmt.Fprintf(b, "if self.%s > now + 31536000 {\n", rustFieldName)
			fmt.Fprintf(b, "return Err(format!(\"field %s must be within 1 year from now, got: {} (max: {})\", self.%s, now + 31536000));\n", rustFieldName, rustFieldName)
			b.WriteString("}\n")
			b.WriteString("}\n")

		case "required_if_true":
			condFieldRust := strutil.ToSnakeCase(annotationValue.StringValue)
			fmt.Fprintf(b, "// Validate %s (required_if_true: %s)\n", rustFieldName, condFieldRust)
			fmt.Fprintf(b, "if self.%s && %s {\n", condFieldRust, rustRequiredZeroCheck(field, rustFieldName))
			fmt.Fprintf(b, "return Err(\"field %s is required when %s is true\".to_string());\n", rustFieldName, condFieldRust)
			b.WriteString("}\n")

		case "required_if_false":
			condFieldRust := strutil.ToSnakeCase(annotationValue.StringValue)
			fmt.Fprintf(b, "// Validate %s (required_if_false: %s)\n", rustFieldName, condFieldRust)
			fmt.Fprintf(b, "if !self.%s && %s {\n", condFieldRust, rustRequiredZeroCheck(field, rustFieldName))
			fmt.Fprintf(b, "return Err(\"field %s is required when %s is false\".to_string());\n", rustFieldName, condFieldRust)
			b.WriteString("}\n")

		case "required_if_eq":
			parts := strings.SplitN(annotationValue.StringValue, "=", 2)
			if len(parts) == 2 {
				condFieldSnake := parts[0]
				condValue := parts[1]
				condFieldRust := strutil.ToSnakeCase(condFieldSnake)

				// Find the sibling field to determine if it's an enum
				var siblingField *model.Field
				for i := range msg.Fields {
					if msg.Fields[i].Name == condFieldSnake {
						siblingField = &msg.Fields[i]
						break
					}
				}

				fmt.Fprintf(b, "// Validate %s (required_if_eq: %s=%s)\n", rustFieldName, condFieldSnake, condValue)
				zeroCheck := rustRequiredZeroCheck(field, rustFieldName)
				if siblingField != nil && siblingField.IsEnum {
					enumType := toProstEnumName(siblingField.Type)
					variantName := toProstEnumVariant(condValue, enumType)
					fmt.Fprintf(b, "if self.%s == %s::%s as i32 && %s {\n", condFieldRust, enumType, variantName, zeroCheck)
				} else {
					fmt.Fprintf(b, "if self.%s == \"%s\" && %s {\n", condFieldRust, condValue, zeroCheck)
				}
				fmt.Fprintf(b, "return Err(\"field %s is required when %s is %s\".to_string());\n", rustFieldName, condFieldSnake, condValue)
				b.WriteString("}\n")
			}

		case "assert_max_size":
			if annotationValue.Type == model.AnnotationTypeInt {
				if field.IsRepeated || field.IsMap {
					fmt.Fprintf(b, "// Validate %s (assert_max_size: %d)\n", rustFieldName, annotationValue.IntValue)
				} else {
					fmt.Fprintf(b, "// Validate %s (assert_max_size: %d bytes)\n", rustFieldName, annotationValue.IntValue)
				}
				if field.IsOptional {
					// Option<String>/Option<Vec<u8>>: unwrap before measuring len.
					fmt.Fprintf(b, "if let Some(ref val) = self.%s {\n", rustFieldName)
					fmt.Fprintf(b, "if val.len() > %d {\n", annotationValue.IntValue)
					fmt.Fprintf(b, "return Err(format!(\"field %s exceeds max size of %d, got: {}\", val.len()));\n", rustFieldName, annotationValue.IntValue)
					b.WriteString("}\n}\n")
				} else {
					fmt.Fprintf(b, "if self.%s.len() > %d {\n", rustFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "return Err(format!(\"field %s exceeds max size of %d, got: {}\", self.%s.len()));\n", rustFieldName, annotationValue.IntValue, rustFieldName)
					b.WriteString("}\n")
				}
			}

		case "assert_min_size":
			if annotationValue.Type == model.AnnotationTypeInt {
				if field.IsRepeated || field.IsMap {
					fmt.Fprintf(b, "// Validate %s (assert_min_size: %d)\n", rustFieldName, annotationValue.IntValue)
				} else {
					fmt.Fprintf(b, "// Validate %s (assert_min_size: %d bytes)\n", rustFieldName, annotationValue.IntValue)
				}
				if field.IsOptional {
					// Option<String>/Option<Vec<u8>>: unwrap before measuring len.
					fmt.Fprintf(b, "if let Some(ref val) = self.%s {\n", rustFieldName)
					fmt.Fprintf(b, "if val.len() < %d {\n", annotationValue.IntValue)
					fmt.Fprintf(b, "return Err(format!(\"field %s below min size of %d, got: {}\", val.len()));\n", rustFieldName, annotationValue.IntValue)
					b.WriteString("}\n}\n")
				} else {
					fmt.Fprintf(b, "if self.%s.len() < %d {\n", rustFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "return Err(format!(\"field %s below min size of %d, got: {}\", self.%s.len()));\n", rustFieldName, annotationValue.IntValue, rustFieldName)
					b.WriteString("}\n")
				}
			}

		case "assert_invariant":
			if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
				fmt.Fprintf(b, "// Formal invariant for %s\n", rustFieldName)
				fmt.Fprintf(b, "debug_assert!(%s, \"invariant violated: %s\");\n",
					annotationValue.StringValue, rustFieldName)
			}
		default:
			// An unknown VALIDATION annotation (assert_* or required_if_*) is a hard
			// error: the
			// generator has no template for it, so the declared constraint would
			// be silently dropped (OF-26d). Non-validation annotations fall
			// through harmlessly.
			if strings.HasPrefix(annotationName, "assert_") || strings.HasPrefix(annotationName, "required_if_") {
				return fmt.Errorf("unsupported validation annotation %q on field %s.%s (no Rust validation template)", annotationName, msg.Name, rustFieldName)
			}
		}
	}

	return nil
}

func generateRustEnumDefinedValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string) error {
	if !field.IsEnum {
		return fmt.Errorf("assert_enum_defined annotation on field %s.%s requires an enum field", msg.Name, fieldName)
	}

	fmt.Fprintf(b, "// Validate %s (assert_enum_defined)\n", fieldName)
	if field.IsRepeated {
		fmt.Fprintf(b, "for (i, val) in self.%s.iter().enumerate() {\n", fieldName)
		b.WriteString("if *val == 0 {\n")
		fmt.Fprintf(b, "return Err(format!(\"field %s[{}] must be defined\", i));\n", fieldName)
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = self.%s {\n", fieldName)
		b.WriteString("if val == 0 {\n")
		fmt.Fprintf(b, "return Err(\"field %s must be defined\".to_string());\n", fieldName)
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if self.%s == 0 {\n", fieldName)
		fmt.Fprintf(b, "return Err(\"field %s must be defined\".to_string());\n", fieldName)
		b.WriteString("}\n")
	}

	return nil
}

// generateRustComparisonValidation emits one of the simple bound annotations
// (assert_gt / assert_ge / assert_lt / assert_le). failOp is the operator that
// makes the generated `if` fire and wantOp the one quoted back in the error
// message. spec.proto scopes the family to scalar numeric fields, so a map,
// repeated or non-numeric field is an authoring mistake: emitting the
// comparison anyway produced Rust that does not build (`if self.name <= 5`
// against a String, `if self.ports <= 5` against a Vec<i32>). Proto3-optional
// fields render as Option<T>, so the value has to be unwrapped the way
// generateRustNumericConstraintValidation already does.
func generateRustComparisonValidation(b *strings.Builder, msg model.Message, field model.Field, rustFieldName, annotation, failOp, wantOp string, bound int64) error {
	if field.IsMap || field.IsRepeated || !(isNumericType(field.Type) || field.IsEnum) {
		return fmt.Errorf("%s annotation on field %s.%s requires a scalar numeric field", annotation, msg.Name, rustFieldName)
	}

	valueStr := fmt.Sprintf("%d", bound)
	if isFloatType(field.Type) {
		valueStr = fmt.Sprintf("%d.0", bound)
	}

	valueExpr := "self." + rustFieldName
	fmt.Fprintf(b, "// Validate %s (%s: %s)\n", rustFieldName, annotation, valueStr)
	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = self.%s {\n", rustFieldName)
		valueExpr = "val"
	}
	fmt.Fprintf(b, "if %s %s %s {\n", valueExpr, failOp, valueStr)
	fmt.Fprintf(b, "return Err(format!(\"field %s must be %s %s, got: {}\", %s));\n", rustFieldName, wantOp, valueStr, valueExpr)
	b.WriteString("}\n")
	if field.IsOptional {
		b.WriteString("}\n")
	}

	return nil
}

// generateRustDurationBoundValidation emits one of the duration/port bound
// annotations (assert_net_port / assert_duration / assert_duration_min /
// assert_duration_max). condFmt is the failing comparison with %[1]s standing
// for the value expression, and want is the phrase quoted back in the error
// message. All four compare the field against an integer, so a map, repeated or
// non-numeric field is an authoring mistake: emitting the comparison anyway
// produced Rust that does not build (`if self.name < 0` against a String,
// `if self.ports < 0` against a Vec<i32>). Enums are rejected too, because java
// renders this family through the bare getter, which does not compile against a
// protobuf enum. Proto3-optional fields render as Option<T>, so the value has
// to be unwrapped the way generateRustNumericConstraintValidation already does.
func generateRustDurationBoundValidation(b *strings.Builder, msg model.Message, field model.Field, rustFieldName, annotation, comment, condFmt, want string) error {
	if field.IsMap || field.IsRepeated || !isNumericType(field.Type) {
		return fmt.Errorf("%s annotation on field %s.%s requires a scalar numeric field", annotation, msg.Name, rustFieldName)
	}

	valueExpr := "self." + rustFieldName
	fmt.Fprintf(b, "// Validate %s (%s)\n", rustFieldName, comment)
	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = self.%s {\n", rustFieldName)
		valueExpr = "val"
	}
	fmt.Fprintf(b, "if "+condFmt+" {\n", valueExpr)
	fmt.Fprintf(b, "return Err(format!(\"field %s must be %s, got: {}\", %s));\n", rustFieldName, want, valueExpr)
	b.WriteString("}\n")
	if field.IsOptional {
		b.WriteString("}\n")
	}

	return nil
}

func generateRustNumericConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.NumericConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isIntegerType(field.Type) {
		return fmt.Errorf("assert_numeric annotation on field %s.%s requires an integer field", msg.Name, fieldName)
	}
	if constraint.MultipleOf != nil && *constraint.MultipleOf == 0 {
		return fmt.Errorf("assert_numeric multiple_of annotation on field %s.%s must be non-zero", msg.Name, fieldName)
	}

	fmt.Fprintf(b, "// Validate %s (assert_numeric)\n", fieldName)
	if field.IsRepeated {
		fmt.Fprintf(b, "for (i, val) in self.%s.iter().enumerate() {\n", fieldName)
		b.WriteString("let val = *val;\n")
		writeRustNumericConstraintChecks(b, fieldName, "val", "i", constraint)
		b.WriteString("}\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = self.%s {\n", fieldName)
		writeRustNumericConstraintChecks(b, fieldName, "val", "", constraint)
		b.WriteString("}\n")
		return nil
	}

	fmt.Fprintf(b, "let val = self.%s;\n", fieldName)
	writeRustNumericConstraintChecks(b, fieldName, "val", "", constraint)
	return nil
}

func writeRustNumericConstraintChecks(b *strings.Builder, fieldName, valueExpr, indexVar string, constraint *model.NumericConstraint) {
	if constraint.Gt != nil {
		fmt.Fprintf(b, "if %s <= %d {\n", valueExpr, *constraint.Gt)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must be > %d", *constraint.Gt), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		fmt.Fprintf(b, "if %s < %d {\n", valueExpr, *constraint.Ge)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must be >= %d", *constraint.Ge), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		fmt.Fprintf(b, "if %s >= %d {\n", valueExpr, *constraint.Lt)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must be < %d", *constraint.Lt), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		fmt.Fprintf(b, "if %s > %d {\n", valueExpr, *constraint.Le)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must be <= %d", *constraint.Le), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Eq != nil {
		fmt.Fprintf(b, "if %s != %d {\n", valueExpr, *constraint.Eq)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must equal %d", *constraint.Eq), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ne != nil {
		fmt.Fprintf(b, "if %s == %d {\n", valueExpr, *constraint.Ne)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must not equal %d", *constraint.Ne), valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		values := rustIntArrayLiteral(constraint.In)
		fmt.Fprintf(b, "if !%s.contains(&%s) {\n", values, valueExpr)
		writeRustConstraintErr(b, fieldName, indexVar, "must be one of "+values, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.NotIn) > 0 {
		values := rustIntArrayLiteral(constraint.NotIn)
		fmt.Fprintf(b, "if %s.contains(&%s) {\n", values, valueExpr)
		writeRustConstraintErr(b, fieldName, indexVar, "must not be one of "+values, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.MultipleOf != nil {
		fmt.Fprintf(b, "if %s %% %d != 0 {\n", valueExpr, *constraint.MultipleOf)
		writeRustConstraintErr(b, fieldName, indexVar, fmt.Sprintf("must be a multiple of %d", *constraint.MultipleOf), valueExpr)
		b.WriteString("}\n")
	}
}

func generateRustFloatConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.FloatConstraint) error {
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

	fmt.Fprintf(b, "// Validate %s (assert_float)\n", fieldName)
	if field.IsRepeated {
		fmt.Fprintf(b, "for (i, val) in self.%s.iter().enumerate() {\n", fieldName)
		b.WriteString("let val = *val as f64;\n")
		writeRustFloatConstraintChecks(b, fieldName, "val", "i", constraint, epsilon)
		b.WriteString("}\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "if let Some(val) = self.%s {\n", fieldName)
		b.WriteString("let val = val as f64;\n")
		writeRustFloatConstraintChecks(b, fieldName, "val", "", constraint, epsilon)
		b.WriteString("}\n")
		return nil
	}

	fmt.Fprintf(b, "let val = self.%s as f64;\n", fieldName)
	writeRustFloatConstraintChecks(b, fieldName, "val", "", constraint, epsilon)
	return nil
}

func writeRustFloatConstraintChecks(b *strings.Builder, fieldName, valueExpr, indexVar string, constraint *model.FloatConstraint, epsilon float64) {
	if constraint.Gt != nil {
		threshold := rustFloatLiteral(*constraint.Gt)
		fmt.Fprintf(b, "if %s <= %s {\n", valueExpr, threshold)
		writeRustConstraintErr(b, fieldName, indexVar, "must be > "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		threshold := rustFloatLiteral(*constraint.Ge)
		fmt.Fprintf(b, "if %s < %s {\n", valueExpr, threshold)
		writeRustConstraintErr(b, fieldName, indexVar, "must be >= "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		threshold := rustFloatLiteral(*constraint.Lt)
		fmt.Fprintf(b, "if %s >= %s {\n", valueExpr, threshold)
		writeRustConstraintErr(b, fieldName, indexVar, "must be < "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		threshold := rustFloatLiteral(*constraint.Le)
		fmt.Fprintf(b, "if %s > %s {\n", valueExpr, threshold)
		writeRustConstraintErr(b, fieldName, indexVar, "must be <= "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		values := rustFloatArrayLiteral(constraint.In)
		epsilonLiteral := rustFloatLiteral(epsilon)
		fmt.Fprintf(b, "if !%s.iter().any(|candidate| (%s - *candidate).abs() <= %s) {\n", values, valueExpr, epsilonLiteral)
		writeRustConstraintErr(b, fieldName, indexVar, "must be one of "+values, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.NotIn) > 0 {
		values := rustFloatArrayLiteral(constraint.NotIn)
		epsilonLiteral := rustFloatLiteral(epsilon)
		fmt.Fprintf(b, "if %s.iter().any(|candidate| (%s - *candidate).abs() <= %s) {\n", values, valueExpr, epsilonLiteral)
		writeRustConstraintErr(b, fieldName, indexVar, "must not be one of "+values, valueExpr)
		b.WriteString("}\n")
	}
}

func writeRustConstraintErr(b *strings.Builder, fieldName, indexVar, message, valueExpr string) {
	if indexVar != "" {
		fmt.Fprintf(b, "return Err(format!(\"field %s[{}] %s, got: {}\", %s, %s));\n", fieldName, message, indexVar, valueExpr)
		return
	}
	fmt.Fprintf(b, "return Err(format!(\"field %s %s, got: {}\", %s));\n", fieldName, message, valueExpr)
}

func rustIntArrayLiteral(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func rustFloatArrayLiteral(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, rustFloatLiteral(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func rustFloatLiteral(value float64) string {
	s := fmt.Sprintf("%g", value)
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}

func generateRustStringHelperValidation(b *strings.Builder, fieldName, annotationName, annotationValue string, isRepeated bool, helperName, failure string) {
	fmt.Fprintf(b, "// Validate %s (%s: %s)\n", fieldName, annotationName, annotationValue)
	if isRepeated {
		fmt.Fprintf(b, "for (i, val) in self.%s.iter().enumerate() {\n", fieldName)
		b.WriteString("if !val.is_empty() {\n")
		fmt.Fprintf(b, "%s(val, \"%s\").map_err(|e| format!(\"field %s[{}] %s: {}\", i, e))?;\n", helperName, annotationValue, fieldName, failure)
		b.WriteString("}\n")
		b.WriteString("}\n")
		return
	}

	fmt.Fprintf(b, "if !self.%s.is_empty() {\n", fieldName)
	fmt.Fprintf(b, "%s(&self.%s, \"%s\").map_err(|e| format!(\"field %s %s: {}\", e))?;\n", helperName, fieldName, annotationValue, fieldName, failure)
	b.WriteString("}\n")
}

func generateIPAddrValidation(b *strings.Builder, fieldName, ipType string, isRepeated bool) {
	fmt.Fprintf(b, "// Validate %s (assert_ipaddr_type: %s)\n", fieldName, ipType)

	if isRepeated {
		fmt.Fprintf(b, "for val in &self.%s {\n", fieldName)
		b.WriteString("if !val.is_empty() {\n")

		switch ipType {
		case "IP_ADDR_TYPE_IP_V4":
			b.WriteString("if val.parse::<Ipv4Addr>().is_err() {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 address, got: {}\", val));\n", fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_IP_V6":
			b.WriteString("if val.parse::<Ipv6Addr>().is_err() {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 address, got: {}\", val));\n", fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_IP_ANY":
			b.WriteString("if val.parse::<IpAddr>().is_err() {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP address, got: {}\", val));\n", fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_NET_V4", "IP_ADDR_TYPE_NET_V6", "IP_ADDR_TYPE_NET_ANY":
			fmt.Fprintf(b, "validate_cidr(val, \"%s\")?;\n", ipType)

		case "IP_ADDR_TYPE_MASK_V4":
			b.WriteString("if let Ok(mask) = val.parse::<u8>() {\n")
			b.WriteString("if mask > 32 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 mask (0-32), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 mask (0-32), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_MASK_V6":
			b.WriteString("if let Ok(mask) = val.parse::<u8>() {\n")
			b.WriteString("if mask > 128 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 mask (0-128), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 mask (0-128), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_MASK_ANY":
			b.WriteString("if let Ok(mask) = val.parse::<u8>() {\n")
			b.WriteString("if mask > 128 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP mask (0-128), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP mask (0-128), got: {}\", val));\n", fieldName)
			b.WriteString("}\n")
		}

		b.WriteString("}\n") // close if !val.is_empty()
		b.WriteString("}\n") // close for loop
	} else {
		fmt.Fprintf(b, "if !self.%s.is_empty() {\n", fieldName)

		switch ipType {
		case "IP_ADDR_TYPE_IP_V4":
			fmt.Fprintf(b, "if self.%s.parse::<Ipv4Addr>().is_err() {\n", fieldName)
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 address, got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_IP_V6":
			fmt.Fprintf(b, "if self.%s.parse::<Ipv6Addr>().is_err() {\n", fieldName)
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 address, got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_IP_ANY":
			fmt.Fprintf(b, "if self.%s.parse::<IpAddr>().is_err() {\n", fieldName)
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP address, got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_NET_V4", "IP_ADDR_TYPE_NET_V6", "IP_ADDR_TYPE_NET_ANY":
			fmt.Fprintf(b, "validate_cidr(&self.%s, \"%s\")?;\n", fieldName, ipType)

		case "IP_ADDR_TYPE_MASK_V4":
			fmt.Fprintf(b, "if let Ok(mask) = self.%s.parse::<u8>() {\n", fieldName)
			b.WriteString("if mask > 32 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 mask (0-32), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv4 mask (0-32), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_MASK_V6":
			fmt.Fprintf(b, "if let Ok(mask) = self.%s.parse::<u8>() {\n", fieldName)
			b.WriteString("if mask > 128 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 mask (0-128), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IPv6 mask (0-128), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")

		case "IP_ADDR_TYPE_MASK_ANY":
			fmt.Fprintf(b, "if let Ok(mask) = self.%s.parse::<u8>() {\n", fieldName)
			b.WriteString("if mask > 128 {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP mask (0-128), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")
			b.WriteString("} else {\n")
			fmt.Fprintf(b, "return Err(format!(\"field %s must be a valid IP mask (0-128), got: {}\", self.%s));\n", fieldName, fieldName)
			b.WriteString("}\n")
		}

		b.WriteString("}\n")
	}
}

func generateHelperValidators(b *strings.Builder, usage *validatorUsage) error {
	data := rustValidationHelpersData{
		NeedsEmail:      usage.needsEmail,
		NeedsFqdn:       usage.needsFqdn,
		NeedsBase64:     usage.needsBase64,
		NeedsListenAddr: usage.needsListenAddr,
		NeedsCidr:       usage.needsCidr,
		NeedsSemver:     usage.needsSemver,
		NeedsHost:       usage.needsHost,
		NeedsSql:        usage.needsSql,
		NeedsUrl:        usage.needsUrl,
		NeedsFilePath:   usage.needsFilePath,
		NeedsEncType:    usage.needsEncType,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "validation_helpers.rs.tmpl", funcMap, data)
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

// writeValidationTestContent writes validation test functions into the combined test module (no #[cfg(test)] wrapper)
func writeValidationTestContent(b *strings.Builder, msg model.Message) {
	generateValidationTestsForMessage(b, msg)
}

// generateValidationTestsForMessage generates validation tests for a single message
func generateValidationTestsForMessage(b *strings.Builder, msg model.Message) {
	typeName := toProstTypeName(msg.Name)
	snakeName := strutil.ToSnakeCase(typeName)

	// Check if message has any validations
	hasValidations := false
	for _, field := range msg.Fields {
		if len(field.Annotations) > 0 {
			hasValidations = true
			break
		}
	}

	data := rustValidationTestTemplateData{
		TypeName:       typeName,
		SnakeName:      snakeName,
		HasValidations: hasValidations,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "validation_test.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing validation test template: %s\n", err)
		return
	}

	b.Write(result)
}
