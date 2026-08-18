// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// writeValidation writes static validation methods into the Helpers class.
// Validation helper methods and patterns live in the shared Validation class.
func writeValidation(b *strings.Builder, pf model.ProtoFile, outerClassName string, config *tactype.Config) error {
	crossHelpers := buildCrossPackageHelpersMap(pf, config)
	// Generate validation methods for each message
	for _, msg := range pf.Messages {
		if err := writeMessageValidation(b, msg, msgOuterClass(msg, outerClassName), crossHelpers); err != nil {
			return err
		}
	}
	return nil
}

// writeMessageValidation writes a validate method for a single message
func writeMessageValidation(b *strings.Builder, msg model.Message, outerClassName string, crossHelpers map[string]string) error {
	fullTypeName := outerClassName + "." + msg.Name

	// Emit JML formal verification specs as Java comments before the method
	writeJmlSpecs(b, msg)

	fmt.Fprintf(b, "public static void validate%s(%s msg) throws ProtoException {\n", msg.Name, fullTypeName)
	fmt.Fprintf(b, "validate%sDepth(msg, 0);\n", msg.Name)
	b.WriteString("}\n")
	fmt.Fprintf(b, "public static void validate%sDepth(%s msg, int depth) throws ProtoException {\n", msg.Name, fullTypeName)
	b.WriteString("if (msg == null) {\n")
	b.WriteString("return;\n")
	b.WriteString("}\n")
	b.WriteString("if (depth > 24) {\n")
	b.WriteString("throw new ProtoException(ProtoException.INVALID_ARGUMENT, \"message nesting exceeds max depth of 24\");\n")
	b.WriteString("}\n")

	hasValidations := false
	for _, field := range msg.Fields {
		if !field.IsOneOf && len(field.Annotations) > 0 {
			hasValidations = true
			break
		}
	}

	formatIntLiteral := func(value int64, fieldType string) string {
		if fieldType == "int64" || value > 2147483647 || value < -2147483648 {
			return fmt.Sprintf("%dL", value)
		}
		return fmt.Sprintf("%d", value)
	}

	for _, field := range msg.Fields {
		// Oneof members are inherently optional (only one case is active), but a
		// set string/bytes case must still honor its declared scalar byte bound —
		// matching the Go/Rust emitters. An unset case reads as empty through the
		// getter and trivially passes the max check.
		if field.IsOneOf {
			if len(field.Annotations) == 0 {
				continue
			}
			javaFieldName := toJavaFieldName(field.Name)
			getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
			for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
				annotationValue := field.Annotations[annotationName]
				if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
					continue
				}
				if annotationName != "assert_max_size" || annotationValue.Type != model.AnnotationTypeInt {
					continue
				}
				if field.Type != "string" && field.Type != "bytes" {
					continue
				}
				hasValidations = true
				fmt.Fprintf(b, "// Validate %s (assert_max_size: %d bytes)\n", javaFieldName, annotationValue.IntValue)
				if field.Type == "bytes" {
					fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName, annotationValue.IntValue)
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName)
				} else {
					fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", getterName, annotationValue.IntValue)
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, annotationValue.IntValue, getterName)
				}
				b.WriteString("}\n")
			}
			continue
		}
		if len(field.Annotations) == 0 {
			continue
		}

		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
				continue
			}

			switch annotationName {
			case "assert_required":
				// Shares javaRequiredZeroCheck with the conditional-requirement family so
				// that (spec.assert_required) and (spec.required_if_true) test the same
				// field the same way.
				fmt.Fprintf(b, "// Validate %s (assert_required)\n", javaFieldName)
				fmt.Fprintf(b, "if (%s) {\n", javaRequiredZeroCheck(field, javaFieldName, getterName))
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,\"field %s is required\");\n", javaFieldName)
				b.WriteString("}\n")

			case "assert_email":
				if field.Type != "string" {
					return fmt.Errorf("assert_email annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_email)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateEmail(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateEmail(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_fqdn":
				if field.Type != "string" {
					return fmt.Errorf("assert_fqdn annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_fqdn)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateFQDN(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateFQDN(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_host":
				if field.Type != "string" {
					return fmt.Errorf("assert_host annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_host: IP address or FQDN)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateHost(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateHost(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_base64":
				if field.Type != "string" {
					return fmt.Errorf("assert_base64 annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_base64)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateBase64(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateBase64(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_listen_addr":
				if field.Type != "string" {
					return fmt.Errorf("assert_listen_addr annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_listen_addr)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateListenAddr(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateListenAddr(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_max_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap {
						fmt.Fprintf(b, "// Validate %s (assert_max_size: %d)\n", javaFieldName, annotationValue.IntValue)
						fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName+"Map", annotationValue.IntValue)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName+"Map")
					} else if field.IsRepeated {
						fmt.Fprintf(b, "// Validate %s (assert_max_size: %d)\n", javaFieldName, annotationValue.IntValue)
						fmt.Fprintf(b, "if (msg.%s() > %d) {\n", getterName+"Count", annotationValue.IntValue)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName+"Count")
					} else {
						fmt.Fprintf(b, "// Validate %s (assert_max_size: %d bytes)\n", javaFieldName, annotationValue.IntValue)
						if field.Type == "bytes" {
							fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName)
						} else {
							fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, annotationValue.IntValue, getterName)
						}
					}
					b.WriteString("}\n")
				}

			case "assert_min_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap {
						fmt.Fprintf(b, "// Validate %s (assert_min_size: %d)\n", javaFieldName, annotationValue.IntValue)
						fmt.Fprintf(b, "if (msg.%s().size() < %d) {\n", getterName+"Map", annotationValue.IntValue)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName+"Map")
					} else if field.IsRepeated {
						fmt.Fprintf(b, "// Validate %s (assert_min_size: %d)\n", javaFieldName, annotationValue.IntValue)
						fmt.Fprintf(b, "if (msg.%s() < %d) {\n", getterName+"Count", annotationValue.IntValue)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName+"Count")
					} else {
						fmt.Fprintf(b, "// Validate %s (assert_min_size: %d bytes)\n", javaFieldName, annotationValue.IntValue)
						if field.Type == "bytes" {
							fmt.Fprintf(b, "if (msg.%s().size() < %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName)
						} else {
							fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length < %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, annotationValue.IntValue, getterName)
						}
					}
					b.WriteString("}\n")
				}

			case "assert_net_port":
				if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
					return fmt.Errorf("assert_net_port annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_net_port)\n", javaFieldName)
				fmt.Fprintf(b, "if (msg.%s() < 0 || msg.%s() > 65535) {\n", getterName, getterName)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid port number (0-65535), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
				b.WriteString("}\n")

			case "assert_ipaddr_type":
				if field.Type != "string" {
					return fmt.Errorf("assert_ipaddr_type annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					writeIPAddrValidation(b, javaFieldName, getterName, annotationValue.EnumValue, field.IsRepeated)
				}

			case "assert_duration":
				if annotationValue.Type == model.AnnotationTypeEnum {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return fmt.Errorf("assert_duration annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					switch annotationValue.EnumValue {
					case "DURATION_TYPE_POSITIVE":
						fmt.Fprintf(b, "// Validate %s (assert_duration: POSITIVE)\n", javaFieldName)
						fmt.Fprintf(b, "if (msg.%s() <= 0) {\n", getterName)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be positive (> 0), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
						b.WriteString("}\n")
					case "DURATION_TYPE_NON_NEGATIVE":
						fmt.Fprintf(b, "// Validate %s (assert_duration: NON_NEGATIVE)\n", javaFieldName)
						fmt.Fprintf(b, "if (msg.%s() < 0) {\n", getterName)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be non-negative (>= 0), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
						b.WriteString("}\n")
					}
				}

			case "assert_duration_min":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return fmt.Errorf("assert_duration_min annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_duration_min: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (msg.%s() < %s) {\n", getterName, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be >= %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_duration_max":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return fmt.Errorf("assert_duration_max annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_duration_max: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (msg.%s() > %s) {\n", getterName, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be <= %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_gt":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return fmt.Errorf("assert_gt annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					valueExpr := fmt.Sprintf("msg.%s()", getterName)
					if field.IsEnum {
						valueExpr = fmt.Sprintf("msg.%s().getNumber()", getterName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_gt: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (%s <= %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be > %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_ge":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return fmt.Errorf("assert_ge annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					valueExpr := fmt.Sprintf("msg.%s()", getterName)
					if field.IsEnum {
						valueExpr = fmt.Sprintf("msg.%s().getNumber()", getterName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_ge: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (%s < %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be >= %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_lt":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return fmt.Errorf("assert_lt annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					valueExpr := fmt.Sprintf("msg.%s()", getterName)
					if field.IsEnum {
						valueExpr = fmt.Sprintf("msg.%s().getNumber()", getterName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_lt: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (%s >= %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be < %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type) || field.IsEnum) {
						return fmt.Errorf("assert_le annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					valueExpr := fmt.Sprintf("msg.%s()", getterName)
					if field.IsEnum {
						valueExpr = fmt.Sprintf("msg.%s().getNumber()", getterName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_le: %d)\n", javaFieldName, annotationValue.IntValue)
					fmt.Fprintf(b, "if (%s > %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be <= %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
				}

			case "assert_semver":
				if field.Type != "string" {
					return fmt.Errorf("assert_semver annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_semver)\n", javaFieldName)
				if field.IsRepeated {
					listGetter := getterName + "List"
					fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
					b.WriteString("if (!val.isEmpty()) {\n")
					fmt.Fprintf(b, "Validation.validateSemver(val, \"%s\");\n", javaFieldName)
					b.WriteString("}\n")
					b.WriteString("}\n")
				} else {
					fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
					fmt.Fprintf(b, "Validation.validateSemver(msg.%s(), \"%s\");\n", getterName, javaFieldName)
					b.WriteString("}\n")
				}

			case "assert_sql":
				if field.Type != "string" {
					return fmt.Errorf("assert_sql annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				if annotationValue.Type == model.AnnotationTypeEnum {
					fmt.Fprintf(b, "// Validate %s (assert_sql: %s)\n", javaFieldName, annotationValue.EnumValue)
					if field.IsRepeated {
						listGetter := getterName + "List"
						fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
						b.WriteString("if (!val.isEmpty()) {\n")
						fmt.Fprintf(b, "Validation.validateSql(val, \"%s\", \"%s\");\n", annotationValue.EnumValue, javaFieldName)
						b.WriteString("}\n")
						b.WriteString("}\n")
					} else {
						fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
						fmt.Fprintf(b, "Validation.validateSql(msg.%s(), \"%s\", \"%s\");\n", getterName, annotationValue.EnumValue, javaFieldName)
						b.WriteString("}\n")
					}
				}

			case "assert_url":
				if field.Type != "string" {
					return fmt.Errorf("assert_url annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_url annotation on field %s.%s must be an enum value", msg.Name, javaFieldName)
				}
				writeURLValidation(b, javaFieldName, getterName, annotationValue.EnumValue, field.IsRepeated)

			case "assert_file_path":
				if field.Type != "string" {
					return fmt.Errorf("assert_file_path annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_file_path annotation on field %s.%s must be an enum value", msg.Name, javaFieldName)
				}
				writeFilePathValidation(b, javaFieldName, getterName, annotationValue.EnumValue, field.IsRepeated)

			case "assert_enc_type":
				if field.Type != "string" {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s requires string type", msg.Name, javaFieldName)
				}
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s must be an enum value", msg.Name, javaFieldName)
				}
				writeEncTypeValidation(b, javaFieldName, getterName, annotationValue.EnumValue, field.IsRepeated)

			case "assert_enum_defined":
				if err := writeJavaEnumDefinedValidation(b, msg, field, javaFieldName, getterName); err != nil {
					return err
				}

			case "assert_numeric":
				if err := writeJavaNumericConstraintValidation(b, msg, field, javaFieldName, getterName, annotationValue.NumericConstraint); err != nil {
					return err
				}

			case "assert_float":
				if err := writeJavaFloatConstraintValidation(b, msg, field, javaFieldName, getterName, annotationValue.FloatConstraint); err != nil {
					return err
				}

			case "required_if_true":
				writeJavaRequiredIfBool(b, msg, field, javaFieldName, getterName, annotationValue.StringValue, true)

			case "required_if_false":
				writeJavaRequiredIfBool(b, msg, field, javaFieldName, getterName, annotationValue.StringValue, false)

			case "required_if_eq":
				writeJavaRequiredIfEq(b, msg, field, javaFieldName, getterName, annotationValue.StringValue)

			case "assert_invariant":
				// Emitted as a formal //@ invariant by the JML annotation pass
				// (writeJmlSpecs); no runtime constraint, matching the Go emitter.

			case "epoch_expiration":
				fmt.Fprintf(b, "// Validate %s (epoch_expiration)\n", javaFieldName)
				fmt.Fprintf(b, "if (msg.%s() != 0) {\n", getterName)
				fmt.Fprintf(b, "long now = System.currentTimeMillis() / 1000;\n")
				fmt.Fprintf(b, "if (msg.%s() <= now) {\n", getterName)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a future epoch timestamp, got: %%d (now: %%d)\", msg.%s(), now));\n", javaFieldName, getterName)
				b.WriteString("}\n")
				fmt.Fprintf(b, "if (msg.%s() > now + 31536000L) {\n", getterName)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be within 1 year from now, got: %%d (max: %%d)\", msg.%s(), now + 31536000L));\n", javaFieldName, getterName)
				b.WriteString("}\n")
				b.WriteString("}\n")

			default:
				// An unknown VALIDATION annotation is a hard error: the generator has
				// no template for it, so the declared constraint would be silently
				// dropped (OF-26d). The conditional-requirement family
				// (required_if_*) counts as validation even though it carries no
				// assert_ prefix — it went unimplemented in Java precisely because
				// this guard only looked for that prefix. Non-validation annotations
				// fall through harmlessly.
				if strings.HasPrefix(annotationName, "assert_") || strings.HasPrefix(annotationName, "required_if_") {
					return fmt.Errorf("unsupported validation annotation %q on field %s.%s (no Java validation template)", annotationName, msg.Name, javaFieldName)
				}
			}
		}
	}

	// Default max size enforcement for repeated/map fields without explicit assert_max_size.
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

		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
		defaultCount := collection.DefaultCollCount

		hasValidations = true
		fmt.Fprintf(b, "// Validate %s (default max size: %d elements)\n", javaFieldName, defaultCount)
		if field.IsMap {
			fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName+"Map", defaultCount)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d elements, got: %%d\", msg.%s().size()));\n", javaFieldName, defaultCount, getterName+"Map")
		} else {
			fmt.Fprintf(b, "if (msg.%s() > %d) {\n", getterName+"Count", defaultCount)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d elements, got: %%d\", msg.%s()));\n", javaFieldName, defaultCount, getterName+"Count")
		}
		b.WriteString("}\n")
	}

	// Default per-element max size enforcement for repeated/map string/bytes values.
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
		defaultSize := collection.DefaultScalarBytes

		if field.IsRepeated && field.Type == "string" {
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s elements (default max size: %d bytes)\n", javaFieldName, defaultSize)
			fmt.Fprintf(b, "for (String val : msg.%s()) {\n", getterName+"List")
			fmt.Fprintf(b, "if (val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", defaultSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s element exceeds max size of %d bytes, got: %%d\", val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, defaultSize)
			b.WriteString("}\n")
			b.WriteString("}\n")
		} else if field.IsRepeated && field.Type == "bytes" {
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s elements (default max size: %d bytes)\n", javaFieldName, defaultSize)
			fmt.Fprintf(b, "for (com.google.protobuf.ByteString val : msg.%s()) {\n", getterName+"List")
			fmt.Fprintf(b, "if (val.size() > %d) {\n", defaultSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s element exceeds max size of %d bytes, got: %%d\", val.size()));\n", javaFieldName, defaultSize)
			b.WriteString("}\n")
			b.WriteString("}\n")
		}

		if field.IsMap {
			if field.MapKeyType == "string" {
				hasValidations = true
				fmt.Fprintf(b, "// Validate %s keys (default max size: %d bytes)\n", javaFieldName, defaultSize)
				fmt.Fprintf(b, "for (String key : msg.%s().keySet()) {\n", getterName+"Map")
				fmt.Fprintf(b, "if (key.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", defaultSize)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s key exceeds max size of %d bytes, got: %%d\", key.getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, defaultSize)
				b.WriteString("}\n")
				b.WriteString("}\n")
			}
			if field.MapValueType == "string" {
				hasValidations = true
				fmt.Fprintf(b, "// Validate %s values (default max size: %d bytes)\n", javaFieldName, defaultSize)
				fmt.Fprintf(b, "for (String val : msg.%s().values()) {\n", getterName+"Map")
				fmt.Fprintf(b, "if (val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", defaultSize)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s value exceeds max size of %d bytes, got: %%d\", val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, defaultSize)
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else if field.MapValueType == "bytes" {
				hasValidations = true
				fmt.Fprintf(b, "// Validate %s values (default max size: %d bytes)\n", javaFieldName, defaultSize)
				fmt.Fprintf(b, "for (com.google.protobuf.ByteString val : msg.%s().values()) {\n", getterName+"Map")
				fmt.Fprintf(b, "if (val.size() > %d) {\n", defaultSize)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s value exceeds max size of %d bytes, got: %%d\", val.size()));\n", javaFieldName, defaultSize)
				b.WriteString("}\n")
				b.WriteString("}\n")
			}
		}
	}

	// Default max size enforcement for singular string/bytes fields without
	// explicit assert_max_size. Oneof members are deliberately included: the
	// getter reads as empty when the case is not active, which trivially passes,
	// and enforces the bound when it is active — matching the Go/Rust emitters.
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

		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
		defaultSize := collection.DefaultScalarBytes

		hasValidations = true
		fmt.Fprintf(b, "// Validate %s (default max size: %d bytes)\n", javaFieldName, defaultSize)
		if field.Type == "bytes" {
			fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName, defaultSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, defaultSize, getterName)
		} else {
			fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", getterName, defaultSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, defaultSize, getterName)
		}
		b.WriteString("}\n")
	}

	for _, field := range msg.Fields {
		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]

		if field.MapValueIsEnum {
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s enum values are known\n", javaFieldName)
			fmt.Fprintf(b, "for (Object key : msg.%sMap().keySet()) {\n", getterName)
			fmt.Fprintf(b, "if (msg.%sMap().get(key).name().equals(\"UNRECOGNIZED\")) {\n", getterName)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s[%%s] has unknown enum value\", key));\n", javaFieldName)
			b.WriteString("}\n")
			b.WriteString("}\n")
			continue
		}

		if !field.IsEnum {
			continue
		}

		hasValidations = true
		fmt.Fprintf(b, "// Validate %s enum value is known\n", javaFieldName)
		if field.IsRepeated {
			countGetter := getterName + "Count"
			fmt.Fprintf(b, "for (int i = 0; i < msg.%s(); i++) {\n", countGetter)
			fmt.Fprintf(b, "if (msg.%s(i).name().equals(\"UNRECOGNIZED\")) {\n", getterName)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s[%%d] has unknown enum value\", i));\n", javaFieldName)
			b.WriteString("}\n")
			b.WriteString("}\n")
		} else if field.IsOptional {
			hasGetter := "has" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
			fmt.Fprintf(b, "if (msg.%s() && msg.%s().name().equals(\"UNRECOGNIZED\")) {\n", hasGetter, getterName)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,\"field %s has unknown enum value\");\n", javaFieldName)
			b.WriteString("}\n")
		} else {
			fmt.Fprintf(b, "if (msg.%s().name().equals(\"UNRECOGNIZED\")) {\n", getterName)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,\"field %s has unknown enum value\");\n", javaFieldName)
			b.WriteString("}\n")
		}
	}

	for _, field := range msg.Fields {
		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]

		if field.MapValueIsMessage {
			validateRef := msgValidationFunc(field.MapValueType, field.MapValueTypePackage, crossHelpers)
			if validateRef == "" {
				continue
			}
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s message values\n", javaFieldName)
			fmt.Fprintf(b, "for (Object key : msg.%sMap().keySet()) {\n", getterName)
			fmt.Fprintf(b, "%s(msg.%sMap().get(key), depth + 1);\n", validateRef, getterName)
			b.WriteString("}\n")
			continue
		}

		if !field.IsMessage {
			continue
		}

		validateRef := msgValidationFunc(field.Type, field.TypePackage, crossHelpers)
		if validateRef == "" {
			continue
		}

		hasValidations = true
		fmt.Fprintf(b, "// Validate %s nested message", javaFieldName)
		if field.IsRepeated {
			b.WriteString("s\n")
			countGetter := getterName + "Count"
			fmt.Fprintf(b, "for (int i = 0; i < msg.%s(); i++) {\n", countGetter)
			fmt.Fprintf(b, "%s(msg.%s(i), depth + 1);\n", validateRef, getterName)
			b.WriteString("}\n")
		} else {
			b.WriteString("\n")
			hasGetter := "has" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]
			fmt.Fprintf(b, "if (msg.%s()) {\n", hasGetter)
			fmt.Fprintf(b, "%s(msg.%s(), depth + 1);\n", validateRef, getterName)
			b.WriteString("}\n")
		}
	}

	if !hasValidations {
		b.WriteString("// No validations defined\n")
	}

	b.WriteString("}\n")
	return nil
}

// writeIPAddrValidation writes IP address validation code (helper for writeMessageValidation)
func writeIPAddrValidation(b *strings.Builder, fieldName, getterName, ipType string, isRepeated bool) {
	fmt.Fprintf(b, "// Validate %s (assert_ipaddr_type: %s)\n", fieldName, ipType)

	var valExpr string
	if isRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
		b.WriteString("if (!val.isEmpty()) {\n")
		valExpr = "val"
	} else {
		fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
		valExpr = fmt.Sprintf("msg.%s()", getterName)
	}

	indent := ""
	if isRepeated {
		indent = ""
	}

	switch ipType {
	case "IP_ADDR_TYPE_IP_V4", "IP_ADDR_TYPE_IP_V6", "IP_ADDR_TYPE_IP_ANY":
		fmt.Fprintf(b, "%sValidation.validateIPAddress(%s, \"%s\", \"%s\");\n", indent, valExpr, ipType, fieldName)

	case "IP_ADDR_TYPE_NET_V4", "IP_ADDR_TYPE_NET_V6", "IP_ADDR_TYPE_NET_ANY":
		fmt.Fprintf(b, "%sValidation.validateCIDR(%s, \"%s\", \"%s\");\n", indent, valExpr, ipType, fieldName)

	case "IP_ADDR_TYPE_MASK_V4":
		fmt.Fprintf(b, "%stry {\n", indent)
		fmt.Fprintf(b, "%s int mask = Integer.parseInt(%s);\n", indent, valExpr)
		fmt.Fprintf(b, "%s if (mask < 0 || mask > 32) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IPv4 mask (0-32), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s }\n", indent)
		fmt.Fprintf(b, "%s} catch (NumberFormatException e) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IPv4 mask (0-32), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s}\n", indent)

	case "IP_ADDR_TYPE_MASK_V6":
		fmt.Fprintf(b, "%stry {\n", indent)
		fmt.Fprintf(b, "%s int mask = Integer.parseInt(%s);\n", indent, valExpr)
		fmt.Fprintf(b, "%s if (mask < 0 || mask > 128) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IPv6 mask (0-128), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s }\n", indent)
		fmt.Fprintf(b, "%s} catch (NumberFormatException e) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IPv6 mask (0-128), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s}\n", indent)

	case "IP_ADDR_TYPE_MASK_ANY":
		fmt.Fprintf(b, "%stry {\n", indent)
		fmt.Fprintf(b, "%s int mask = Integer.parseInt(%s);\n", indent, valExpr)
		fmt.Fprintf(b, "%s if (mask < 0 || mask > 128) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IP mask (0-128), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s }\n", indent)
		fmt.Fprintf(b, "%s} catch (NumberFormatException e) {\n", indent)
		fmt.Fprintf(b, "%s throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid IP mask (0-128), got: %%s\", %s));\n", indent, fieldName, valExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}

	b.WriteString("}\n") // close if
	if isRepeated {
		b.WriteString("}\n") // close for
	}
}

// writeURLValidation writes URL validation code (helper for writeMessageValidation).
// Empty values are skipped — presence is assert_required's concern (matches Go/Rust).
func writeURLValidation(b *strings.Builder, fieldName, getterName, scheme string, isRepeated bool) {
	fmt.Fprintf(b, "// Validate %s (assert_url: %s)\n", fieldName, scheme)
	if isRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
		b.WriteString("if (!val.isEmpty()) {\n")
		fmt.Fprintf(b, "Validation.validateUrl(val, \"%s\", \"%s\");\n", scheme, fieldName)
		b.WriteString("}\n")
		b.WriteString("}\n")
		return
	}

	fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
	fmt.Fprintf(b, "Validation.validateUrl(msg.%s(), \"%s\", \"%s\");\n", getterName, scheme, fieldName)
	b.WriteString("}\n")
}

// writeFilePathValidation writes file path validation code (helper for writeMessageValidation).
// Empty values are skipped — presence is assert_required's concern (matches Go/Rust).
func writeFilePathValidation(b *strings.Builder, fieldName, getterName, pathType string, isRepeated bool) {
	fmt.Fprintf(b, "// Validate %s (assert_file_path: %s)\n", fieldName, pathType)
	if isRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
		b.WriteString("if (!val.isEmpty()) {\n")
		fmt.Fprintf(b, "Validation.validateFilePath(val, \"%s\", \"%s\");\n", pathType, fieldName)
		b.WriteString("}\n")
		b.WriteString("}\n")
		return
	}

	fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
	fmt.Fprintf(b, "Validation.validateFilePath(msg.%s(), \"%s\", \"%s\");\n", getterName, pathType, fieldName)
	b.WriteString("}\n")
}

// writeEncTypeValidation writes PEM key/certificate validation code (helper for
// writeMessageValidation). PEM normalization (raw or base64-wrapped input) happens
// inside Validation.validateEncType. Empty values are skipped (matches Go/Rust).
func writeEncTypeValidation(b *strings.Builder, fieldName, getterName, encType string, isRepeated bool) {
	fmt.Fprintf(b, "// Validate %s (assert_enc_type: %s)\n", fieldName, encType)
	if isRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (String val : msg.%s()) {\n", listGetter)
		b.WriteString("if (!val.isEmpty()) {\n")
		fmt.Fprintf(b, "Validation.validateEncType(val, \"%s\", \"%s\");\n", encType, fieldName)
		b.WriteString("}\n")
		b.WriteString("}\n")
		return
	}

	fmt.Fprintf(b, "if (!msg.%s().isEmpty()) {\n", getterName)
	fmt.Fprintf(b, "Validation.validateEncType(msg.%s(), \"%s\", \"%s\");\n", getterName, encType, fieldName)
	b.WriteString("}\n")
}

// toJavaFieldName converts a proto field name to the lowerCamelCase name
// protobuf-java derives its accessors from. It mirrors protobuf's
// UnderscoresToCamelCase exactly — including the rule that a letter immediately
// following a digit is capitalized (so "requests_per_second_5m" becomes
// "requestsPerSecond5M", whose getter is getRequestsPerSecond5M). A naive
// split-on-underscore misses the digit boundary and emits getRequestsPerSecond5m,
// which protobuf never generates — that mismatch is a compile error in the
// generated Helpers (cannot find symbol).
func toJavaFieldName(s string) string {
	var b strings.Builder
	capNext := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			if capNext {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)
			capNext = false
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c)
			capNext = false
		case c >= '0' && c <= '9':
			b.WriteByte(c)
			capNext = true // protobuf: a letter after a digit is capitalized
		default: // '_' or any separator
			capNext = true
		}
	}
	return b.String()
}

// javaFieldNameUpper is toJavaFieldName with a capitalized first letter — the
// PascalCase stem protobuf-java uses to form get/set/has/add/put accessors
// (getFoo, setFoo, hasFoo). Use this (not strutil.ToCamelCase) for any generated
// reference to a protobuf accessor, so the digit-boundary rule is honored.
func javaFieldNameUpper(s string) string {
	n := toJavaFieldName(s)
	if n == "" {
		return n
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

// writeJmlSpecs emits JML formal verification specs as Java comments
// before a validate method. JML annotations are pure comments (//@ ...)
// with zero runtime impact — no build changes needed.
func writeJmlSpecs(b *strings.Builder, msg model.Message) {
	var specs []string

	// Universal precondition: message must not be null
	specs = append(specs, "//@ requires msg != null;")

	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		javaFieldName := toJavaFieldName(field.Name)
		getterName := "get" + strings.ToUpper(javaFieldName[:1]) + javaFieldName[1:]

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			switch annotationName {
			case "assert_required":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					if field.Type == "string" {
						specs = append(specs,
							fmt.Sprintf("//@ ensures !msg.%s().isEmpty();", getterName))
					}
				}

			case "assert_max_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.Type == "string" || field.Type == "bytes" {
						specs = append(specs,
							fmt.Sprintf("//@ ensures msg.%s().size() <= %d;", getterName, annotationValue.IntValue))
					} else if field.IsRepeated {
						specs = append(specs,
							fmt.Sprintf("//@ ensures msg.%sCount() <= %d;", getterName, annotationValue.IntValue))
					}
				}

			case "assert_gt":
				if annotationValue.Type == model.AnnotationTypeInt {
					specs = append(specs,
						fmt.Sprintf("//@ ensures msg.%s() > %d;", getterName, annotationValue.IntValue))
				}

			case "assert_ge":
				if annotationValue.Type == model.AnnotationTypeInt {
					specs = append(specs,
						fmt.Sprintf("//@ ensures msg.%s() >= %d;", getterName, annotationValue.IntValue))
				}

			case "assert_lt":
				if annotationValue.Type == model.AnnotationTypeInt {
					specs = append(specs,
						fmt.Sprintf("//@ ensures msg.%s() < %d;", getterName, annotationValue.IntValue))
				}

			case "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt {
					specs = append(specs,
						fmt.Sprintf("//@ ensures msg.%s() <= %d;", getterName, annotationValue.IntValue))
				}

			case "assert_net_port":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					specs = append(specs,
						fmt.Sprintf("//@ ensures msg.%s() >= 0 && msg.%s() <= 65535;", getterName, getterName))
				}

			case "assert_invariant":
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					specs = append(specs,
						fmt.Sprintf("//@ invariant %s;", annotationValue.StringValue))
				}
			}
		}
	}

	// Only emit specs if we have more than just the null precondition
	if len(specs) > 1 {
		for _, spec := range specs {
			b.WriteString(spec)
			b.WriteString("\n")
		}
	}
}
