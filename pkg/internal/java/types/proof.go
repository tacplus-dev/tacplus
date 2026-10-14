// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// jbmcProofField represents a single field that needs nondeterministic assignment
// and associated CProver.assume() constraints in a JBMC proof harness.
type jbmcProofField struct {
	JavaFieldName string   // camelCase field name
	SetterName    string   // e.g., "setPort"
	NondetCall    string   // e.g., "CProver.nondetInt()"
	Assumes       []string // CProver.assume() expressions for this field
	IsEnum        bool     // true if the field is a protobuf enum
	EnumType      string   // fully qualified enum type (e.g., "Eventbus.DeliveryMode")
}

// jbmcProofMessage holds data for generating a JBMC proof harness for one message.
type jbmcProofMessage struct {
	MessageName string
	OuterClass  string
	Fields      []jbmcProofField
	Invariants  []string
}

// writeJbmcProofHarnesses generates a Proof.java file with JBMC proof harnesses
// for messages that have provable annotations.
func writeJbmcProofHarnesses(pf model.ProtoFile, baseDir, subdir, outerClassName string) error {
	if len(pf.Messages) == 0 {
		return nil
	}

	// Collect messages that have provable annotations
	var proofMsgs []jbmcProofMessage
	for _, msg := range pf.Messages {
		pm := buildJbmcProofMessage(msg, msgOuterClass(msg, outerClassName))
		if len(pm.Fields) > 0 || len(pm.Invariants) > 0 {
			proofMsgs = append(proofMsgs, pm)
		}
	}

	if len(proofMsgs) == 0 {
		return nil
	}

	packageName := strings.ReplaceAll(subdir, string(filepath.Separator), ".")

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangJava, ""))
	fmt.Fprintf(&b, "package %s;\n\n", packageName)
	b.WriteString("import org.cprover.CProver;\n\n")

	b.WriteString("/**\n")
	b.WriteString(" * JBMC proof harnesses for bounded model checking of validate() methods.\n")
	b.WriteString(" * Each harness constructs a message with nondeterministic field values,\n")
	b.WriteString(" * constrains them via CProver.assume(), and asserts validate() succeeds.\n")
	b.WriteString(" */\n")
	b.WriteString("public class Proof {\n\n")

	for _, pm := range proofMsgs {
		writeJbmcHarnessMethod(&b, pm)
	}

	b.WriteString("}\n")

	outputDir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "Proof.java"), []byte(b.String()), 0644)
}

// writeJbmcHarnessMethod writes a single static harness method for one message.
func writeJbmcHarnessMethod(b *strings.Builder, pm jbmcProofMessage) {
	snakeName := strutil.ToSnakeCase(pm.MessageName)
	fullType := pm.OuterClass + "." + pm.MessageName

	fmt.Fprintf(b, "    // JBMC harness: verify %s.validate() accepts all valid inputs\n", pm.MessageName)
	fmt.Fprintf(b, "    public static void verify_%s_validate() {\n", snakeName)
	fmt.Fprintf(b, "        %s.Builder builder = %s.newBuilder();\n\n", fullType, fullType)

	// Assign nondeterministic values and assumptions per field
	for _, field := range pm.Fields {
		// Determine the Java type for the local variable from the nondet call
		javaType := nondetCallToJavaType(field.NondetCall)
		fmt.Fprintf(b, "        %s %s = %s;\n", javaType, field.JavaFieldName, field.NondetCall)

		for _, assume := range field.Assumes {
			fmt.Fprintf(b, "        CProver.assume(%s);\n", assume)
		}

		if field.IsEnum {
			fmt.Fprintf(b, "        builder.%s(%s.forNumber(%s));\n\n", field.SetterName, field.EnumType, field.JavaFieldName)
		} else {
			fmt.Fprintf(b, "        builder.%s(%s);\n\n", field.SetterName, field.JavaFieldName)
		}
	}

	// Emit CProver.assume() for each explicit invariant
	for _, inv := range pm.Invariants {
		fmt.Fprintf(b, "        CProver.assume(%s);\n", inv)
	}

	fmt.Fprintf(b, "        %s msg = builder.build();\n\n", fullType)
	b.WriteString("        try {\n")
	fmt.Fprintf(b, "            Helpers.validate%s(msg);\n", pm.MessageName)
	b.WriteString("        } catch (Exception e) {\n")
	fmt.Fprintf(b, "            assert false : \"validate threw for valid input: \" + e.getMessage();\n")
	b.WriteString("        }\n")
	b.WriteString("    }\n\n")
}

// buildJbmcProofMessage extracts provable assumptions from a message's field annotations.
// Only primitive numeric/boolean fields are assigned nondeterministic values.
// Strings, bytes, nested messages, maps, and repeated fields are skipped
// (JBMC's string reasoning is limited).
func buildJbmcProofMessage(msg model.Message, outerClassName string) jbmcProofMessage {
	pm := jbmcProofMessage{
		MessageName: msg.Name,
		OuterClass:  outerClassName,
	}

	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		// Skip types JBMC can't handle well
		if field.IsMessage || field.IsMap || field.IsRepeated {
			continue
		}

		// Skip string and bytes — JBMC's string support is limited
		if field.Type == "string" || field.Type == "bytes" {
			continue
		}

		// Determine nondet call and any unsigned assumptions
		nondetCall, unsignedAssumes := protoTypeToNondet(field)
		if nondetCall == "" {
			continue
		}

		javaFieldName := toJavaFieldName(field.Name)
		setterName := "set" + javaFieldNameUpper(field.Name)
		pf := jbmcProofField{
			JavaFieldName: javaFieldName,
			SetterName:    setterName,
			NondetCall:    nondetCall,
			Assumes:       unsignedAssumes,
			IsEnum:        field.IsEnum,
		}
		if field.IsEnum {
			pf.EnumType = outerClassName + "." + field.Type
		}

		hasAnnotation := false

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			switch annotationName {
			case "assert_gt":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("%s > %s", javaFieldName, formatJavaLiteral(annotationValue.IntValue, field.Type)))
					hasAnnotation = true
				}

			case "assert_ge":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("%s >= %s", javaFieldName, formatJavaLiteral(annotationValue.IntValue, field.Type)))
					hasAnnotation = true
				}

			case "assert_lt":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("%s < %s", javaFieldName, formatJavaLiteral(annotationValue.IntValue, field.Type)))
					hasAnnotation = true
				}

			case "assert_le":
				if annotationValue.Type == model.AnnotationTypeInt {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("%s <= %s", javaFieldName, formatJavaLiteral(annotationValue.IntValue, field.Type)))
					hasAnnotation = true
				}

			case "assert_net_port":
				if annotationValue.Type == model.AnnotationTypeBool && annotationValue.BoolValue {
					pf.Assumes = append(pf.Assumes,
						fmt.Sprintf("%s >= 0 && %s <= 65535", javaFieldName, javaFieldName))
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

// protoTypeToNondet maps a proto field type to the appropriate CProver.nondetX() call
// and returns any additional unsigned-type assumptions.
func protoTypeToNondet(field model.Field) (nondetCall string, assumes []string) {
	javaFieldName := toJavaFieldName(field.Name)

	if field.IsEnum {
		return "CProver.nondetInt()", []string{
			fmt.Sprintf("%s >= 0", javaFieldName),
		}
	}

	switch field.Type {
	case "int32", "sint32", "sfixed32":
		return "CProver.nondetInt()", nil
	case "int64", "sint64", "sfixed64":
		return "CProver.nondetLong()", nil
	case "uint32", "fixed32":
		return "CProver.nondetInt()", []string{
			fmt.Sprintf("%s >= 0", javaFieldName),
		}
	case "uint64", "fixed64":
		return "CProver.nondetLong()", []string{
			fmt.Sprintf("%s >= 0L", javaFieldName),
		}
	case "bool":
		return "CProver.nondetBoolean()", nil
	case "float":
		return "CProver.nondetFloat()", nil
	case "double":
		return "CProver.nondetDouble()", nil
	default:
		return "", nil
	}
}

// nondetCallToJavaType maps a CProver.nondetX() call to the Java primitive type.
func nondetCallToJavaType(nondetCall string) string {
	switch nondetCall {
	case "CProver.nondetInt()":
		return "int"
	case "CProver.nondetLong()":
		return "long"
	case "CProver.nondetBoolean()":
		return "boolean"
	case "CProver.nondetFloat()":
		return "float"
	case "CProver.nondetDouble()":
		return "double"
	default:
		return "int"
	}
}

// formatJavaLiteral formats an int64 annotation value as a Java literal,
// using the L suffix for long types.
func formatJavaLiteral(value int64, protoType string) string {
	switch protoType {
	case "int64", "sint64", "sfixed64", "uint64", "fixed64":
		return fmt.Sprintf("%dL", value)
	default:
		return fmt.Sprintf("%d", value)
	}
}
