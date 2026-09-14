// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func writeJavaEnumDefinedValidation(b *strings.Builder, msg model.Message, field model.Field, javaFieldName, getterName string) error {
	if !field.IsEnum {
		return fmt.Errorf("assert_enum_defined annotation on field %s.%s requires an enum field", msg.Name, javaFieldName)
	}

	fmt.Fprintf(b, "// Validate %s (assert_enum_defined)\n", javaFieldName)
	valueGetter := getterName + "Value"
	if field.IsRepeated {
		listGetter := valueGetter + "List"
		fmt.Fprintf(b, "for (int i = 0; i < msg.%s().size(); i++) {\n", listGetter)
		fmt.Fprintf(b, "int val = msg.%s().get(i);\n", listGetter)
		b.WriteString("if (val == 0) {\n")
		writeJavaValidationThrow(b, fmt.Sprintf("field %s[%%d] must be defined", javaFieldName), "i")
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else if field.IsOptional {
		fmt.Fprintf(b, "if (msg.has%s()) {\n", upperFirst(javaFieldName))
		fmt.Fprintf(b, "if (msg.%s() == 0) {\n", valueGetter)
		writeJavaValidationThrow(b, fmt.Sprintf("field %s must be defined", javaFieldName))
		b.WriteString("}\n")
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(b, "if (msg.%s() == 0) {\n", valueGetter)
		writeJavaValidationThrow(b, fmt.Sprintf("field %s must be defined", javaFieldName))
		b.WriteString("}\n")
	}

	return nil
}

func writeJavaNumericConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, javaFieldName, getterName string, constraint *model.NumericConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isIntegerType(field.Type) {
		return fmt.Errorf("assert_numeric annotation on field %s.%s requires an integer field", msg.Name, javaFieldName)
	}
	if constraint.MultipleOf != nil && *constraint.MultipleOf == 0 {
		return fmt.Errorf("assert_numeric multiple_of annotation on field %s.%s must be non-zero", msg.Name, javaFieldName)
	}

	fmt.Fprintf(b, "// Validate %s (assert_numeric)\n", javaFieldName)
	if field.IsRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (int i = 0; i < msg.%s().size(); i++) {\n", listGetter)
		fmt.Fprintf(b, "long val = msg.%s().get(i);\n", listGetter)
		writeJavaNumericConstraintChecks(b, javaFieldName, "val", "i", constraint)
		b.WriteString("}\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "if (msg.has%s()) {\n", upperFirst(javaFieldName))
		fmt.Fprintf(b, "long val = msg.%s();\n", getterName)
		writeJavaNumericConstraintChecks(b, javaFieldName, "val", "", constraint)
		b.WriteString("}\n")
		return nil
	}

	b.WriteString("{\n")
	fmt.Fprintf(b, "long val = msg.%s();\n", getterName)
	writeJavaNumericConstraintChecks(b, javaFieldName, "val", "", constraint)
	b.WriteString("}\n")
	return nil
}

func writeJavaNumericConstraintChecks(b *strings.Builder, fieldName, valueExpr, indexExpr string, constraint *model.NumericConstraint) {
	if constraint.Gt != nil {
		fmt.Fprintf(b, "if (%s <= %s) {\n", valueExpr, javaLongLiteral(*constraint.Gt))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must be > %d", *constraint.Gt), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		fmt.Fprintf(b, "if (%s < %s) {\n", valueExpr, javaLongLiteral(*constraint.Ge))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must be >= %d", *constraint.Ge), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		fmt.Fprintf(b, "if (%s >= %s) {\n", valueExpr, javaLongLiteral(*constraint.Lt))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must be < %d", *constraint.Lt), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		fmt.Fprintf(b, "if (%s > %s) {\n", valueExpr, javaLongLiteral(*constraint.Le))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must be <= %d", *constraint.Le), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Eq != nil {
		fmt.Fprintf(b, "if (%s != %s) {\n", valueExpr, javaLongLiteral(*constraint.Eq))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must equal %d", *constraint.Eq), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ne != nil {
		fmt.Fprintf(b, "if (%s == %s) {\n", valueExpr, javaLongLiteral(*constraint.Ne))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must not equal %d", *constraint.Ne), valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		values := javaLongArrayLiteral(constraint.In)
		fmt.Fprintf(b, "if (!java.util.Arrays.stream(%s).anyMatch(candidate -> val == candidate)) {\n", values)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be one of "+javaLongMessageList(constraint.In), valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.NotIn) > 0 {
		values := javaLongArrayLiteral(constraint.NotIn)
		fmt.Fprintf(b, "if (java.util.Arrays.stream(%s).anyMatch(candidate -> val == candidate)) {\n", values)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must not be one of "+javaLongMessageList(constraint.NotIn), valueExpr)
		b.WriteString("}\n")
	}
	if constraint.MultipleOf != nil {
		fmt.Fprintf(b, "if (%s %% %s != 0L) {\n", valueExpr, javaLongLiteral(*constraint.MultipleOf))
		writeJavaConstraintThrow(b, fieldName, indexExpr, fmt.Sprintf("must be a multiple of %d", *constraint.MultipleOf), valueExpr)
		b.WriteString("}\n")
	}
}

func writeJavaFloatConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, javaFieldName, getterName string, constraint *model.FloatConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isFloatType(field.Type) {
		return fmt.Errorf("assert_float annotation on field %s.%s requires a float or double field", msg.Name, javaFieldName)
	}

	epsilon := 1e-9
	if constraint.Epsilon != nil {
		epsilon = *constraint.Epsilon
	}

	fmt.Fprintf(b, "// Validate %s (assert_float)\n", javaFieldName)
	if field.IsRepeated {
		listGetter := getterName + "List"
		fmt.Fprintf(b, "for (int i = 0; i < msg.%s().size(); i++) {\n", listGetter)
		fmt.Fprintf(b, "double val = msg.%s().get(i);\n", listGetter)
		writeJavaFloatConstraintChecks(b, javaFieldName, "val", "i", constraint, epsilon)
		b.WriteString("}\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "if (msg.has%s()) {\n", upperFirst(javaFieldName))
		fmt.Fprintf(b, "double val = msg.%s();\n", getterName)
		writeJavaFloatConstraintChecks(b, javaFieldName, "val", "", constraint, epsilon)
		b.WriteString("}\n")
		return nil
	}

	b.WriteString("{\n")
	fmt.Fprintf(b, "double val = msg.%s();\n", getterName)
	writeJavaFloatConstraintChecks(b, javaFieldName, "val", "", constraint, epsilon)
	b.WriteString("}\n")
	return nil
}

func writeJavaFloatConstraintChecks(b *strings.Builder, fieldName, valueExpr, indexExpr string, constraint *model.FloatConstraint, epsilon float64) {
	if constraint.Gt != nil {
		threshold := javaDoubleLiteral(*constraint.Gt)
		fmt.Fprintf(b, "if (%s <= %s) {\n", valueExpr, threshold)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be > "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		threshold := javaDoubleLiteral(*constraint.Ge)
		fmt.Fprintf(b, "if (%s < %s) {\n", valueExpr, threshold)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be >= "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		threshold := javaDoubleLiteral(*constraint.Lt)
		fmt.Fprintf(b, "if (%s >= %s) {\n", valueExpr, threshold)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be < "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		threshold := javaDoubleLiteral(*constraint.Le)
		fmt.Fprintf(b, "if (%s > %s) {\n", valueExpr, threshold)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be <= "+threshold, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		values := javaDoubleArrayLiteral(constraint.In)
		epsilonLiteral := javaDoubleLiteral(epsilon)
		fmt.Fprintf(b, "if (!java.util.Arrays.stream(%s).anyMatch(candidate -> Math.abs(val - candidate) <= %s)) {\n", values, epsilonLiteral)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must be one of "+javaDoubleMessageList(constraint.In), valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.NotIn) > 0 {
		values := javaDoubleArrayLiteral(constraint.NotIn)
		epsilonLiteral := javaDoubleLiteral(epsilon)
		fmt.Fprintf(b, "if (java.util.Arrays.stream(%s).anyMatch(candidate -> Math.abs(val - candidate) <= %s)) {\n", values, epsilonLiteral)
		writeJavaConstraintThrow(b, fieldName, indexExpr, "must not be one of "+javaDoubleMessageList(constraint.NotIn), valueExpr)
		b.WriteString("}\n")
	}
}

func writeJavaConstraintThrow(b *strings.Builder, fieldName, indexExpr, message, valueExpr string) {
	if indexExpr != "" {
		writeJavaValidationThrow(b, fmt.Sprintf("field %s[%%d] %s, got: %%s", fieldName, message), indexExpr, valueExpr)
		return
	}
	writeJavaValidationThrow(b, fmt.Sprintf("field %s %s, got: %%s", fieldName, message), valueExpr)
}

func writeJavaValidationThrow(b *strings.Builder, message string, args ...string) {
	messageExpr := fmt.Sprintf("%q", message)
	if len(args) > 0 {
		messageExpr = fmt.Sprintf("String.format(%s, %s)", messageExpr, strings.Join(args, ", "))
	}

	fmt.Fprintf(b, "throw new ProtoException(ProtoException.INVALID_ARGUMENT,%s);\n", messageExpr)
}

func javaLongLiteral(value int64) string {
	return fmt.Sprintf("%dL", value)
}

func javaLongArrayLiteral(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, javaLongLiteral(value))
	}
	return "new long[] {" + strings.Join(parts, ", ") + "}"
}

func javaLongMessageList(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func javaDoubleLiteral(value float64) string {
	s := fmt.Sprintf("%g", value)
	if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
		s += ".0"
	}
	return s
}

func javaDoubleArrayLiteral(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, javaDoubleLiteral(value))
	}
	return "new double[] {" + strings.Join(parts, ", ") + "}"
}

func javaDoubleMessageList(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, javaDoubleLiteral(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// javaRequiredZeroCheck returns the Java expression that tests whether a field is
// at its zero value. It drives the whole requirement family — assert_required plus
// the conditional required_if_true / required_if_false / required_if_eq — and
// mirrors the type ladder the Go emitter uses, so the same annotation means the
// same thing in both server languages.
func javaRequiredZeroCheck(field model.Field, javaFieldName, getterName string) string {
	switch {
	case field.IsMap:
		return fmt.Sprintf("msg.%sMap().isEmpty()", getterName)
	case field.IsRepeated:
		return fmt.Sprintf("msg.%sCount() == 0", getterName)
	case field.IsOptional || field.IsMessage:
		return fmt.Sprintf("!msg.has%s()", upperFirst(javaFieldName))
	case field.Type == "string" || field.Type == "bytes":
		return fmt.Sprintf("msg.%s().isEmpty()", getterName)
	case field.Type == "bool":
		return fmt.Sprintf("!msg.%s()", getterName)
	case field.IsEnum:
		// Enum getters return the enum; the numeric accessor avoids naming the
		// generated Java enum class (see writeJavaEnumDefinedValidation).
		return fmt.Sprintf("msg.%sValue() == 0", getterName)
	default:
		return fmt.Sprintf("msg.%s() == 0", getterName)
	}
}

// javaOpenOptionalPresenceGuard opens `if (msg.has<Field>()) {` for a proto3
// optional field and reports whether it wrote anything, so the caller closes the
// brace it opened. A proto3 `optional` scalar reads back as the type default
// when absent, so a bound check emitted against the bare getter (min size,
// numeric comparison, port/duration range) fires on a field the caller
// legitimately omitted — Java rejected requests Go and Rust accept, because
// those render the field as a pointer / Option and test presence first.
// assert_numeric, assert_float and assert_enum_defined already emit this guard
// themselves; presence-independent checks (assert_required and the
// required_if_* family) must keep firing on an absent field and are not guarded.
func javaOpenOptionalPresenceGuard(b *strings.Builder, field model.Field, javaFieldName string) bool {
	if !field.IsOptional || field.IsRepeated || field.IsMap {
		return false
	}
	fmt.Fprintf(b, "if (msg.has%s()) {\n", upperFirst(javaFieldName))
	return true
}

// javaSiblingGetter returns the getter name for a sibling field referenced by a
// conditional-requirement annotation (e.g. "enable_tls" -> "getEnableTls").
func javaSiblingGetter(siblingName string) string {
	return "get" + upperFirst(toJavaFieldName(siblingName))
}

// writeJavaRequiredIfBool emits the required_if_true / required_if_false
// constraint: the annotated field must be set whenever the named boolean sibling
// holds wantTrue.
func writeJavaRequiredIfBool(b *strings.Builder, msg model.Message, field model.Field, javaFieldName, getterName, siblingName string, wantTrue bool) {
	if siblingName == "" {
		return
	}
	annotation, state, negate := "required_if_true", "true", ""
	if !wantTrue {
		annotation, state, negate = "required_if_false", "false", "!"
	}
	fmt.Fprintf(b, "// Validate %s (%s: %s)\n", javaFieldName, annotation, siblingName)
	fmt.Fprintf(b, "if (%smsg.%s() && %s) {\n", negate, javaSiblingGetter(siblingName), javaRequiredZeroCheck(field, javaFieldName, getterName))
	writeJavaValidationThrow(b, fmt.Sprintf("field %s is required when %s is %s", javaFieldName, siblingName, state))
	b.WriteString("}\n")
}

// writeJavaRequiredIfEq emits the required_if_eq constraint: the annotated field
// must be set whenever the named sibling equals the given value. The annotation
// value arrives as "field=value" (see the descriptor parser).
func writeJavaRequiredIfEq(b *strings.Builder, msg model.Message, field model.Field, javaFieldName, getterName, spec string) {
	parts := strings.SplitN(spec, "=", 2)
	if len(parts) != 2 || parts[0] == "" {
		return
	}
	condFieldName, condValue := parts[0], parts[1]

	var sibling *model.Field
	for i := range msg.Fields {
		if msg.Fields[i].Name == condFieldName {
			sibling = &msg.Fields[i]
			break
		}
	}

	// An enum sibling is compared by variant NAME so the emitter never has to
	// resolve the generated Java enum class; a string sibling by value.
	condExpr := fmt.Sprintf("%q.equals(msg.%s())", condValue, javaSiblingGetter(condFieldName))
	if sibling != nil && sibling.IsEnum {
		condExpr = fmt.Sprintf("%q.equals(msg.%s().name())", condValue, javaSiblingGetter(condFieldName))
	}

	fmt.Fprintf(b, "// Validate %s (required_if_eq: %s=%s)\n", javaFieldName, condFieldName, condValue)
	fmt.Fprintf(b, "if (%s && %s) {\n", condExpr, javaRequiredZeroCheck(field, javaFieldName, getterName))
	writeJavaValidationThrow(b, fmt.Sprintf("field %s is required when %s is %s", javaFieldName, condFieldName, condValue))
	b.WriteString("}\n")
}
