// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
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

// writeMessageValidation writes the validate methods for a single message: the
// strict validator, and the at-rest variant (validate<Type>AtRest) that runs over
// stored state on disk load.
func writeMessageValidation(b *strings.Builder, msg model.Message, outerClassName string, crossHelpers map[string]string) error {
	// The strict validator carries the message's JML specs, which `tacplus
	// verify` proves with OpenJML ESC (#257).
	specs := jmlSpecs(msg)
	var strict strings.Builder
	if err := writeValidateMethods(&strict, msg, outerClassName, crossHelpers, "", specs); err != nil {
		return err
	}
	if specs == "" {
		b.WriteString(strict.String())
	} else {
		b.WriteString(withJmlLoopFrames(strict.String()))
	}
	return writeValidateMethods(b, msg, outerClassName, crossHelpers, "AtRest", "")
}

// withJmlLoopFrames puts `loop_assigns \everything` before each loop of a
// validator ESC proves. ESC otherwise infers a loop's frame from the
// assignments in its body and refuses the calls a validator loop makes (an
// element getter, a nested validator), which may assign anything. The
// message's getters stay fixed across the loop (their results are final ghost
// fields), so the specs still prove.
func withJmlLoopFrames(code string) string {
	lines := strings.SplitAfter(code, "\n")
	var out strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "for (") {
			out.WriteString("//@ loop_assigns \\everything;\n")
		}
		out.WriteString(line)
	}
	return out.String()
}

// writeValidateMethods writes one validator family. suffix is "" for the strict
// validator and "AtRest" for the at-rest one, which is the same validator without
// the time-relative assertions: a stored value ages. specs (JML, see jmlSpecs)
// sits on both methods: ESC proves the Depth method, which does the checking,
// from its body, and the public one from the Depth method's spec.
func writeValidateMethods(b *strings.Builder, msg model.Message, outerClassName string, crossHelpers map[string]string, suffix, specs string) error {
	fullTypeName := outerClassName + "." + msg.Name

	b.WriteString(specs)
	fmt.Fprintf(b, "public static void validate%s%s(%s msg) throws ProtoException {\n", msg.Name, suffix, fullTypeName)
	fmt.Fprintf(b, "validate%s%sDepth(msg, 0);\n", msg.Name, suffix)
	b.WriteString("}\n")
	b.WriteString(specs)
	fmt.Fprintf(b, "public static void validate%s%sDepth(%s msg, int depth) throws ProtoException {\n", msg.Name, suffix, fullTypeName)
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
			getterName := "get" + javaFieldNameUpper(field.Name)
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
		getterName := "get" + javaFieldNameUpper(field.Name)

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
					presenceGuarded := false
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
						presenceGuarded = javaOpenOptionalPresenceGuard(b, field, javaFieldName)
						if field.Type == "bytes" {
							fmt.Fprintf(b, "if (msg.%s().size() > %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName)
						} else {
							fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s exceeds max size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, annotationValue.IntValue, getterName)
						}
					}
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
				}

			case "assert_element_max_size":
				// Per-element byte bound. The emission lives in the per-element
				// loop below, which applies this bound in place of the default
				// one it already enforces on every repeated/map string/bytes
				// element; here we only reject a misapplied annotation.
				if err := field.ValidateElementMaxSize(msg.Name); err != nil {
					return err
				}

			case "assert_min_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					presenceGuarded := false
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
						presenceGuarded = javaOpenOptionalPresenceGuard(b, field, javaFieldName)
						if field.Type == "bytes" {
							fmt.Fprintf(b, "if (msg.%s().size() < %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d bytes, got: %%d\", msg.%s().size()));\n", javaFieldName, annotationValue.IntValue, getterName)
						} else {
							fmt.Fprintf(b, "if (msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length < %d) {\n", getterName, annotationValue.IntValue)
							fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s below min size of %d bytes, got: %%d\", msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, annotationValue.IntValue, getterName)
						}
					}
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
				}

			case "assert_net_port":
				if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
					return fmt.Errorf("assert_net_port annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
				}
				fmt.Fprintf(b, "// Validate %s (assert_net_port)\n", javaFieldName)
				presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
				fmt.Fprintf(b, "if (msg.%s() < 0 || msg.%s() > 65535) {\n", getterName, getterName)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be a valid port number (0-65535), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
				b.WriteString("}\n")
				if presenceGuarded {
					b.WriteString("}\n")
				}

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
						presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
						fmt.Fprintf(b, "if (msg.%s() <= 0) {\n", getterName)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be positive (> 0), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
						b.WriteString("}\n")
						if presenceGuarded {
							b.WriteString("}\n")
						}
					case "DURATION_TYPE_NON_NEGATIVE":
						fmt.Fprintf(b, "// Validate %s (assert_duration: NON_NEGATIVE)\n", javaFieldName)
						presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
						fmt.Fprintf(b, "if (msg.%s() < 0) {\n", getterName)
						fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be non-negative (>= 0), got: %%d\", msg.%s()));\n", javaFieldName, getterName)
						b.WriteString("}\n")
						if presenceGuarded {
							b.WriteString("}\n")
						}
					}
				}

			case "assert_duration_min":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return fmt.Errorf("assert_duration_min annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_duration_min: %d)\n", javaFieldName, annotationValue.IntValue)
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (msg.%s() < %s) {\n", getterName, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be >= %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
				}

			case "assert_duration_max":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsMap || field.IsRepeated || !(isIntegerType(field.Type) || isFloatType(field.Type)) {
						return fmt.Errorf("assert_duration_max annotation on field %s.%s requires a scalar numeric field", msg.Name, javaFieldName)
					}
					fmt.Fprintf(b, "// Validate %s (assert_duration_max: %d)\n", javaFieldName, annotationValue.IntValue)
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (msg.%s() > %s) {\n", getterName, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be <= %d, got: %%d\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
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
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (%s <= %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be > %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
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
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (%s < %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be >= %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
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
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (%s >= %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be < %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
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
					presenceGuarded := javaOpenOptionalPresenceGuard(b, field, javaFieldName)
					fmt.Fprintf(b, "if (%s > %s) {\n", valueExpr, formatIntLiteral(annotationValue.IntValue, field.Type))
					fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s must be <= %d, got: %%s\", msg.%s()));\n", javaFieldName, annotationValue.IntValue, getterName)
					b.WriteString("}\n")
					if presenceGuarded {
						b.WriteString("}\n")
					}
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
				// Emitted as a comment by jmlSpecs; no runtime constraint,
				// matching the Go emitter.

			case "epoch_expiration":
				if suffix != "" {
					break
				}
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
		getterName := "get" + javaFieldNameUpper(field.Name)
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
		getterName := "get" + javaFieldNameUpper(field.Name)
		defaultSize := collection.DefaultScalarBytes

		// An explicit (spec.assert_element_max_size) replaces the default bound
		// for repeated elements and map values; map keys keep the default.
		elemSize, elemLabel := defaultSize, "default max size"
		if bound := field.ElementMaxSizeBytes(); bound > 0 {
			elemSize, elemLabel = bound, "assert_element_max_size"
		}

		if field.IsRepeated && field.Type == "string" {
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s elements (%s: %d bytes)\n", javaFieldName, elemLabel, elemSize)
			fmt.Fprintf(b, "for (String val : msg.%s()) {\n", getterName+"List")
			fmt.Fprintf(b, "if (val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", elemSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s element exceeds max size of %d bytes, got: %%d\", val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, elemSize)
			b.WriteString("}\n")
			b.WriteString("}\n")
		} else if field.IsRepeated && field.Type == "bytes" {
			hasValidations = true
			fmt.Fprintf(b, "// Validate %s elements (%s: %d bytes)\n", javaFieldName, elemLabel, elemSize)
			fmt.Fprintf(b, "for (com.google.protobuf.ByteString val : msg.%s()) {\n", getterName+"List")
			fmt.Fprintf(b, "if (val.size() > %d) {\n", elemSize)
			fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s element exceeds max size of %d bytes, got: %%d\", val.size()));\n", javaFieldName, elemSize)
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
				fmt.Fprintf(b, "// Validate %s values (%s: %d bytes)\n", javaFieldName, elemLabel, elemSize)
				fmt.Fprintf(b, "for (String val : msg.%s().values()) {\n", getterName+"Map")
				fmt.Fprintf(b, "if (val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > %d) {\n", elemSize)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s value exceeds max size of %d bytes, got: %%d\", val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length));\n", javaFieldName, elemSize)
				b.WriteString("}\n")
				b.WriteString("}\n")
			} else if field.MapValueType == "bytes" {
				hasValidations = true
				fmt.Fprintf(b, "// Validate %s values (%s: %d bytes)\n", javaFieldName, elemLabel, elemSize)
				fmt.Fprintf(b, "for (com.google.protobuf.ByteString val : msg.%s().values()) {\n", getterName+"Map")
				fmt.Fprintf(b, "if (val.size() > %d) {\n", elemSize)
				fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,String.format(\"field %s value exceeds max size of %d bytes, got: %%d\", val.size()));\n", javaFieldName, elemSize)
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
		getterName := "get" + javaFieldNameUpper(field.Name)
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
		getterName := "get" + javaFieldNameUpper(field.Name)

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
			hasGetter := "has" + javaFieldNameUpper(field.Name)
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
		getterName := "get" + javaFieldNameUpper(field.Name)

		if field.MapValueIsMessage {
			validateRef := msgValidationFunc(field.MapValueType, field.MapValueTypePackage, crossHelpers, suffix)
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

		validateRef := msgValidationFunc(field.Type, field.TypePackage, crossHelpers, suffix)
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
			hasGetter := "has" + javaFieldNameUpper(field.Name)
			fmt.Fprintf(b, "if (msg.%s()) {\n", hasGetter)
			fmt.Fprintf(b, "%s(msg.%s(), depth + 1);\n", validateRef, getterName)
			b.WriteString("}\n")
		}
	}

	// Sensitive fields (tacplus#274): required, placeholders, entropy floor.
	if writeJavaSensitiveChecks(b, msg) {
		hasValidations = true
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

// protobufJavaReservedNames are the PascalCase field names protobuf-java
// suffixes with "_" because their accessors would collide with methods inherited
// from Object, MessageLite or MessageOrBuilder: a field `class` gets getClass_(),
// setClass_(), hasClass_(). Mirrors kForbiddenWordList in protoc's
// java/names.cc (checked against protoc 36.2).
var protobufJavaReservedNames = map[string]bool{
	"Class":                     true,
	"DefaultInstanceForType":    true,
	"ParserForType":             true,
	"SerializedSize":            true,
	"AllFields":                 true,
	"DescriptorForType":         true,
	"InitializationErrorString": true,
	"UnknownFields":             true,
	"CachedSize":                true,
}

// javaFieldNameUpper is toJavaFieldName with a capitalized first letter — the
// PascalCase stem protobuf-java uses to form get/set/has/add/put accessors
// (getFoo, setFoo, hasFoo), including its "_" suffix on reserved names. Use this
// (not strutil.ToCamelCase) for any generated reference to a protobuf accessor,
// so the digit-boundary and reserved-name rules are honored.
func javaFieldNameUpper(s string) string {
	n := toJavaFieldName(s)
	if n == "" {
		return n
	}
	n = strings.ToUpper(n[:1]) + n[1:]
	if protobufJavaReservedNames[n] {
		n += "_"
	}
	return n
}

// jmlSpecs returns the JML specs of a message's strict validator, one comment
// line each, or "" when it has none beyond msg != null. Each `ensures` states a
// bound the validator enforces, in the terms its code uses, so that OpenJML ESC
// (`tacplus verify`, #257) proves it from the method body: a validator that
// returns normally accepted only values within the declared bounds. ESC treats
// the protobuf getters as pure functions of the message (the spec stubs
// pkg/verification writes), so the proof holds for every value they can return.
// A bound is left out when no pure expression states it: a string's UTF-8 byte
// length (the validator measures String.getBytes, which allocates).
func jmlSpecs(msg model.Message) string {
	var specs []string
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}
		getter := "msg.get" + javaFieldNameUpper(field.Name)
		guard := func(spec string) string {
			if field.IsOptional {
				return fmt.Sprintf("!msg.has%s() || (%s)", javaFieldNameUpper(field.Name), spec)
			}
			return spec
		}
		scalar := !field.IsRepeated && !field.IsMap
		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			switch annotationName {
			case "assert_required":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue && scalar && field.Type == "string" {
					specs = append(specs, "//@ ensures "+guard(fmt.Sprintf("!%s().isEmpty()", getter))+";")
				}

			case "assert_max_size":
				if annotationValue.Type != model.AnnotationTypeInt {
					break
				}
				switch {
				case field.IsMap:
					specs = append(specs, fmt.Sprintf("//@ ensures %sMap().size() <= %d;", getter, annotationValue.IntValue))
				case field.IsRepeated:
					specs = append(specs, fmt.Sprintf("//@ ensures %sCount() <= %d;", getter, annotationValue.IntValue))
				case field.Type == "bytes":
					specs = append(specs, "//@ ensures "+guard(fmt.Sprintf("%s().size() <= %d", getter, annotationValue.IntValue))+";")
				}

			case "assert_gt", "assert_ge", "assert_lt", "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt && scalar {
					op := map[string]string{"assert_gt": ">", "assert_ge": ">=", "assert_lt": "<", "assert_le": "<="}[annotationName]
					if spec := jmlIntegerBound(getter+"()", field.Type, op, annotationValue.IntValue); spec != "" {
						specs = append(specs, "//@ ensures "+guard(spec)+";")
					}
				}

			case "assert_enum_defined":
				if field.IsEnum && scalar {
					specs = append(specs, "//@ ensures "+guard(fmt.Sprintf("%sValue() != 0", getter))+";")
				}

			case "assert_numeric":
				if c := annotationValue.NumericConstraint; c != nil && scalar {
					if spec := jmlNumericConstraint(getter+"()", field.Type, c); spec != "" {
						specs = append(specs, "//@ ensures "+guard(spec)+";")
					}
				}

			case "assert_net_port":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue && scalar {
					lo := jmlIntegerBound(getter+"()", field.Type, ">=", 0)
					hi := jmlIntegerBound(getter+"()", field.Type, "<=", 65535)
					if lo != "" && hi != "" {
						specs = append(specs, "//@ ensures "+guard(lo+" && "+hi)+";")
					}
				}

			case "assert_invariant":
				// The proto's expression, not Java: kept as a comment, not a
				// JML clause OpenJML would have to resolve.
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					specs = append(specs, fmt.Sprintf("// invariant (%s): %s", field.Name, annotationValue.StringValue))
				}
			}
		}
	}
	if len(specs) == 0 {
		return ""
	}
	return "//@ requires msg != null;\n" + strings.Join(specs, "\n") + "\n"
}

// jmlNumericConstraint states an assert_numeric constraint on value, a getter
// call of a protobuf integer type, in the comparisons the validator makes; ""
// for a type that is not an integer.
func jmlNumericConstraint(value, protoType string, c *model.NumericConstraint) string {
	if jmlIntegerBound(value, protoType, "==", 0) == "" {
		return ""
	}
	// v is the value as the validator's long holds it; lit renders a bound.
	v, lit := value, func(n int64) string { return fmt.Sprintf("%dL", n) }
	switch protoType {
	case "int32", "sint32", "sfixed32":
		lit = func(n int64) string { return fmt.Sprintf("%d", n) }
	case "uint32", "fixed32":
		v = "Integer.toUnsignedLong(" + value + ")"
	}
	var parts []string
	for _, bound := range []struct {
		op string
		n  *int64
	}{{">", c.Gt}, {">=", c.Ge}, {"<", c.Lt}, {"<=", c.Le}} {
		if bound.n != nil {
			parts = append(parts, jmlIntegerBound(value, protoType, bound.op, *bound.n))
		}
	}
	if c.Eq != nil {
		parts = append(parts, v+" == "+lit(*c.Eq))
	}
	if c.Ne != nil {
		parts = append(parts, v+" != "+lit(*c.Ne))
	}
	if len(c.In) > 0 {
		terms := make([]string, len(c.In))
		for i, n := range c.In {
			terms[i] = v + " == " + lit(n)
		}
		in := strings.Join(terms, " || ")
		if len(c.In) > 1 && (len(parts) > 0 || len(c.NotIn) > 0 || c.MultipleOf != nil) {
			in = "(" + in + ")"
		}
		parts = append(parts, in)
	}
	for _, n := range c.NotIn {
		parts = append(parts, v+" != "+lit(n))
	}
	if c.MultipleOf != nil {
		if protoType == "uint64" || protoType == "fixed64" {
			// Long.remainderUnsigned has no ESC spec: left out.
		} else {
			parts = append(parts, fmt.Sprintf("%s %% %dL == 0L", v, *c.MultipleOf))
		}
	}
	return strings.Join(parts, " && ")
}

// jmlIntegerBound states `value op bound` for a protobuf integer type, in the
// comparison the validator makes: a uint32 or uint64 is held in a signed int or
// long and ordered as unsigned. "" for a type that is not an integer.
func jmlIntegerBound(value, protoType, op string, bound int64) string {
	switch protoType {
	case "int32", "sint32", "sfixed32":
		return fmt.Sprintf("%s %s %d", value, op, bound)
	case "int64", "sint64", "sfixed64":
		return fmt.Sprintf("%s %s %dL", value, op, bound)
	case "uint32", "fixed32":
		return fmt.Sprintf("Integer.toUnsignedLong(%s) %s %dL", value, op, bound)
	case "uint64", "fixed64":
		// A long holding a uint64 is negative from 2^63 up, above every
		// non-negative bound. Split on the sign, which ESC reads; it has no
		// spec of Long.compareUnsigned. A negative bound (2^63 and up) is left out.
		if bound < 0 {
			return ""
		}
		switch op {
		case ">", ">=":
			return fmt.Sprintf("(%s < 0 || %s %s %dL)", value, value, op, bound)
		case "<", "<=":
			return fmt.Sprintf("(%s >= 0 && %s %s %dL)", value, value, op, bound)
		}
		return fmt.Sprintf("%s %s %dL", value, op, bound)
	}
	return ""
}
