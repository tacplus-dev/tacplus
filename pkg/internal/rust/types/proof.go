// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// proofField represents a single field that needs nondeterministic assignment
// and associated kani::assume() constraints.
type proofField struct {
	RustName string   // snake_case field name
	Assumes  []string // kani::assume(msg.field ...) expressions
}

// proofMessage holds data for generating a Kani proof harness for one message.
type proofMessage struct {
	TypeName   string
	Fields     []proofField // fields needing kani::any() + constraints
	Invariants []string     // from assert_invariant
}

// writeProofHarnesses writes #[cfg(kani)] proof harnesses into the helpers builder.
// Harnesses construct messages field-by-field using kani::any() on individual
// primitive fields, avoiding the need for Arbitrary on the whole struct (which
// fails for messages containing prost-wkt well-known types).
func writeProofHarnesses(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Messages) == 0 {
		return
	}

	// Collect messages that have any provable annotations
	var proofMsgs []proofMessage
	for _, msg := range pf.Messages {
		pm := buildProofMessage(msg)
		if len(pm.Fields) > 0 || len(pm.Invariants) > 0 {
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

	for _, pm := range proofMsgs {
		snakeName := strutil.ToSnakeCase(pm.TypeName)
		fmt.Fprintf(b, "#[kani::proof]\n")
		fmt.Fprintf(b, "fn verify_%s_validate() {\n", snakeName)
		fmt.Fprintf(b, "let mut msg = %s::default();\n", pm.TypeName)

		// Assign nondeterministic values to annotated fields
		for _, field := range pm.Fields {
			fmt.Fprintf(b, "msg.%s = kani::any();\n", field.RustName)
			for _, assume := range field.Assumes {
				fmt.Fprintf(b, "kani::assume(%s);\n", assume)
			}
		}

		// Emit kani::assume() for each explicit invariant
		for _, inv := range pm.Invariants {
			fmt.Fprintf(b, "kani::assume(%s);\n", inv)
		}

		b.WriteString("// validate() should succeed for all inputs satisfying invariants\n")
		b.WriteString("assert!(msg.validate().is_ok());\n")
		b.WriteString("}\n\n")
	}

	b.WriteString("}\n")
}

// buildProofMessage extracts provable assumptions from a message's field annotations.
// Only primitive fields (string, bytes, integers, floats, bool) are assigned
// nondeterministic values — nested messages, maps, and repeated fields of
// complex types are left at their defaults.
func buildProofMessage(msg model.Message) proofMessage {
	typeName := toProstTypeName(msg.Name)
	pm := proofMessage{TypeName: typeName}

	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		// Skip fields that can't be used with kani::any() directly:
		// nested messages, maps, and repeated complex types.
		if field.IsMessage || field.IsMap {
			continue
		}

		rustFieldName := escapeRustKeyword(strutil.ToSnakeCase(field.Name))
		pf := proofField{RustName: rustFieldName}
		hasAnnotation := false

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			switch annotationName {
			case "assert_max_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					if field.IsRepeated {
						pf.Assumes = append(pf.Assumes,
							fmt.Sprintf("msg.%s.len() <= %d", rustFieldName, annotationValue.IntValue))
					} else if field.Type == "string" || field.Type == "bytes" {
						pf.Assumes = append(pf.Assumes,
							fmt.Sprintf("msg.%s.len() <= %d", rustFieldName, annotationValue.IntValue))
					}
					hasAnnotation = true
				}

			case "assert_min_size":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s.len() >= %d", rustFieldName, annotationValue.IntValue))
					hasAnnotation = true
				}

			case "assert_gt":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s > %d", rustFieldName, annotationValue.IntValue))
					hasAnnotation = true
				}

			case "assert_ge":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s >= %d", rustFieldName, annotationValue.IntValue))
					hasAnnotation = true
				}

			case "assert_lt":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s < %d", rustFieldName, annotationValue.IntValue))
					hasAnnotation = true
				}

			case "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s <= %d", rustFieldName, annotationValue.IntValue))
					hasAnnotation = true
				}

			case "assert_net_port":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("msg.%s >= 0 && msg.%s <= 65535", rustFieldName, rustFieldName))
					hasAnnotation = true
				}

			case "assert_required":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					if field.Type == "string" {
						pf.Assumes = append(pf.Assumes,
							fmt.Sprintf("!msg.%s.is_empty()", rustFieldName))
					}
					hasAnnotation = true
				}

			case "assert_non_empty":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("!msg.%s.is_empty()", rustFieldName))
					hasAnnotation = true
				}

			case "assert_invariant":
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					pm.Invariants = append(pm.Invariants, annotationValue.StringValue)
					hasAnnotation = true
				}
			}
		}

		if hasAnnotation {
			pm.Fields = append(pm.Fields, pf)
		}
	}

	return pm
}
