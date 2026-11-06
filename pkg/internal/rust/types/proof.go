// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// proofHarness is one Kani proof: a field of a message given a nondeterministic
// value outside an annotated bound, and validate() asserted to refuse it.
type proofHarness struct {
	Name     string // verify_<message>_<field>_<annotation>
	TypeName string
	Field    string // snake_case Rust field name
	Optional bool   // proto3 optional: the field is an Option<T>
	Refused  string // the value outside the bound, %[1]s standing for it
}

// proofMessage holds the harnesses of one message and the invariants every one
// of them assumes.
type proofMessage struct {
	Harnesses  []proofHarness
	Invariants []string // from assert_invariant, self. rewritten to msg.
}

// selfFieldRe is the receiver an assert_invariant names its fields through
// (`self.ttl > 0`): validate() checks it as written, the free harness function
// has `msg` in its place.
var selfFieldRe = regexp.MustCompile(`\bself\.`)

// writeProofHarnesses writes #[cfg(kani)] proof harnesses into the helpers builder.
//
// Each harness proves that validate() refuses every value outside one annotated
// integer or enum bound (tacplus#211): it leaves the message at its default,
// gives the field kani::any() constrained to the out-of-bound values, and
// asserts validate().is_err(). That claim holds whatever the message's other
// fields hold, because the bound's check is unconditional; "validate() accepts
// every value inside the bounds" does not (an unmodelled check on another field
// can refuse the default), and a Kani harness must state only what is true.
// Strings, bytes, repeated, map, oneof, float and message fields get none:
// Kani has no Arbitrary for String or Vec, and a float bound would have to
// model NaN. Every harness stubs std::fmt::format, which the crate's Cargo.toml
// enables for cargo kani (types_cargo.toml.tmpl): the error message is the only
// thing it changes, and the generated validators never branch on one.
func writeProofHarnesses(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	var proofMsgs []proofMessage
	for _, msg := range pf.Messages {
		if !rustProofReachable(msg) {
			continue
		}
		if pm := buildProofMessage(msg); len(pm.Harnesses) > 0 {
			proofMsgs = append(proofMsgs, pm)
		}
	}

	if len(proofMsgs) == 0 {
		return
	}

	b.WriteString("\n// ---- Kani Formal Verification Harnesses ----\n")
	b.WriteString("#[cfg(kani)]\n")
	b.WriteString("mod proof {\n")
	b.WriteString("use super::*;\n\n")
	b.WriteString("// validate() formats the value it refuses into its error, and Kani cannot\n")
	b.WriteString("// finish core::fmt; is_err() does not depend on the message's text.\n")
	b.WriteString("fn format_stub(_args: std::fmt::Arguments<'_>) -> String {\n")
	b.WriteString("String::new()\n")
	b.WriteString("}\n\n")

	for _, pm := range proofMsgs {
		for _, h := range pm.Harnesses {
			b.WriteString("#[kani::proof]\n")
			b.WriteString("#[kani::stub(std::fmt::format, format_stub)]\n")
			fmt.Fprintf(b, "fn %s() {\n", h.Name)
			fmt.Fprintf(b, "let mut msg = %s::default();\n", h.TypeName)
			value := "msg." + h.Field
			if h.Optional {
				b.WriteString("let v = kani::any();\n")
				fmt.Fprintf(b, "msg.%s = Some(v);\n", h.Field)
				value = "v"
			} else {
				fmt.Fprintf(b, "msg.%s = kani::any();\n", h.Field)
			}
			fmt.Fprintf(b, "kani::assume("+h.Refused+");\n", value)
			for _, inv := range pm.Invariants {
				fmt.Fprintf(b, "kani::assume(%s);\n", inv)
			}
			b.WriteString("// validate() refuses every value outside the bound\n")
			b.WriteString("assert!(msg.validate().is_err());\n")
			b.WriteString("}\n\n")
		}
	}

	b.WriteString("}\n")
}

// rustProofReachable reports whether a harness may call msg.validate(): Kani
// compiles all the code a harness reaches, run or not, and it cannot compile
// everything validate() can reach. It panics generating the regex crate
// (TryFromIntError in its codegen, Kani 0.68), which the email, fqdn, host and
// semver validators use; a nested message's validator can reach anything,
// across packages this file does not see; and a sensitive-field check reaches
// the entropy estimate, which no harness needs. Nor can Kani run the default
// of a message with a map field: prost's HashMap draws its hash keys from the
// OS, a syscall Kani does not model.
func rustProofReachable(msg model.Message) bool {
	if rustMessageHasSensitiveCheck(msg) {
		return false
	}
	for _, field := range msg.Fields {
		if field.IsMap {
			return false
		}
	}
	var usage validatorUsage
	trackValidatorUsage(msg, &usage)
	if usage.needsEmail || usage.needsFqdn || usage.needsHost || usage.needsSemver {
		return false
	}
	for _, field := range msg.Fields {
		if field.IsMessage && rustMessageHasValidate(field.Type, field.TypePackage) {
			return false
		}
	}
	return true
}

// buildProofMessage collects a message's harnesses: one per integer or enum
// field and bound annotation, with the bound's out-of-range values as the
// validator's generator writes its failing comparison.
func buildProofMessage(msg model.Message) proofMessage {
	typeName := toProstTypeName(msg.Name)
	msgSnake := strutil.ToSnakeCase(typeName)
	var pm proofMessage

	for _, field := range msg.Fields {
		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			v := field.Annotations[annotationName]
			if annotationName == "assert_invariant" && v.Type == model.AnnotationTypeString && v.StringValue != "" {
				pm.Invariants = append(pm.Invariants, selfFieldRe.ReplaceAllString(v.StringValue, "msg."))
			}
		}

		if field.IsOneOf || field.IsRepeated || field.IsMap || field.IsMessage {
			continue
		}
		if !isIntegerType(field.Type) && !field.IsEnum {
			continue
		}

		rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			refused, ok := rustProofRefusedBound(field, annotationName, field.Annotations[annotationName])
			if !ok {
				continue
			}
			pm.Harnesses = append(pm.Harnesses, proofHarness{
				Name:     fmt.Sprintf("verify_%s_%s_%s", msgSnake, strings.TrimPrefix(rustFieldName, "r#"), annotationName),
				TypeName: typeName,
				Field:    rustFieldName,
				Optional: field.IsOptional,
				Refused:  refused,
			})
		}
	}

	return pm
}

// rustProofRefusedBound returns the values an integer bound annotation refuses,
// %[1]s standing for the value: the failing comparison validation.go writes for
// it (generateRustComparisonValidation, generateRustDurationBoundValidation).
// The duration and port family is not generated for an enum.
func rustProofRefusedBound(field model.Field, annotationName string, v model.AnnotationValue) (string, bool) {
	switch annotationName {
	case "assert_gt", "assert_ge", "assert_lt", "assert_le":
		if v.Type != model.AnnotationTypeInt {
			return "", false
		}
		op := map[string]string{"assert_gt": "<=", "assert_ge": "<", "assert_lt": ">=", "assert_le": ">"}[annotationName]
		return fmt.Sprintf("%%[1]s %s %d", op, v.IntValue), true
	}
	if field.IsEnum {
		return "", false
	}
	switch annotationName {
	case "assert_net_port":
		if v.Type == model.AnnotationTypeBool && v.BoolValue {
			return "%[1]s < 0 || %[1]s > 65535", true
		}
	case "assert_duration":
		if v.Type == model.AnnotationTypeEnum {
			switch v.EnumValue {
			case "DURATION_TYPE_POSITIVE":
				return "%[1]s <= 0", true
			case "DURATION_TYPE_NON_NEGATIVE":
				return "%[1]s < 0", true
			}
		}
	case "assert_duration_min":
		if v.Type == model.AnnotationTypeInt {
			return fmt.Sprintf("%%[1]s < %d", v.IntValue), true
		}
	case "assert_duration_max":
		if v.Type == model.AnnotationTypeInt {
			return fmt.Sprintf("%%[1]s > %d", v.IntValue), true
		}
	}
	return "", false
}
