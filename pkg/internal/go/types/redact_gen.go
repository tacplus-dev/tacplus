// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// This file emits redaction for any type (tacplus#274) for Go: the
// deterministic Redact<T> of every message with a sensitive leaf in its tree,
// and the validation of required / entropy-checked sensitive fields. Both come
// from the language-neutral plan in parser/sensitive.go, which Rust and Java
// emit from too, so the three give byte-identical output for the same input.
//
// Redact<T>(m) returns a redacted COPY: a set sensitive string becomes
// "REDACTED", a set sensitive bytes value the bytes of "REDACTED", an empty one
// stays empty, and <field>_redacted is set when the field held a value. The
// unexported in-place redact<T> / redactAll<T> do the work on the copy;
// redactAll<T> rewrites every string/bytes leaf (a (spec.sensitive) message
// field, or a (spec.sensitive_message)), redact<T> only the annotated ones.

// generateRedactHelpers emits Redact<T> and the in-place helpers for every
// message of pf the index says needs them.
func generateRedactHelpers(b *strings.Builder, pf model.ProtoFile, ix *parser.SensitiveIndex) {
	for mi := range pf.Messages {
		msg := &pf.Messages[mi]
		needs, needsAll := ix.NeedsRedact(pf.Package, msg.Name), ix.NeedsRedactAll(pf.Package, msg.Name)
		if needs {
			writeGoRedactFunc(b, ix.Plan(msg, pf.Package, false))
		}
		if needsAll {
			writeGoRedactInPlace(b, ix.Plan(msg, pf.Package, true), "redactAll"+msg.Name)
		}
	}
}

func writeGoRedactFunc(b *strings.Builder, p *parser.RedactPlan) {
	t := p.Message.Name
	fmt.Fprintf(b, "// Redact%[1]s returns a deterministically redacted COPY of m for review by a\n", t)
	b.WriteString("// tool, a human or an LLM: every sensitive string becomes \"REDACTED\", every\n")
	b.WriteString("// sensitive bytes value the bytes of \"REDACTED\" (empty stays empty), and each\n")
	b.WriteString("// <field>_redacted is set where a value was replaced. No key, no randomness.\n")
	fmt.Fprintf(b, "func Redact%[1]s(m *%[1]s) *%[1]s {\n", t)
	b.WriteString("\tif m == nil {\n\t\treturn nil\n\t}\n")
	fmt.Fprintf(b, "\tout := proto.Clone(m).(*%s)\n", t)
	fmt.Fprintf(b, "\tredact%s(out)\n\treturn out\n}\n\n", t)
	writeGoRedactInPlace(b, p, "redact"+t)
}

// writeGoRedactInPlace emits one in-place redaction function from a plan.
func writeGoRedactInPlace(b *strings.Builder, p *parser.RedactPlan, fn string) {
	t := p.Message.Name
	fmt.Fprintf(b, "func %s(m *%s) {\n", fn, t)
	if len(p.Fields) == 0 {
		b.WriteString("\t_ = m\n}\n\n")
		return
	}
	b.WriteString("\tif m == nil {\n\t\treturn\n\t}\n")
	for _, rf := range p.Fields {
		writeGoRedactField(b, rf)
	}
	b.WriteString("}\n\n")
}

func writeGoRedactField(b *strings.Builder, rf parser.RedactField) {
	f := rf.Field
	gf := toGoFieldName(f.Name)
	set := ""
	if rf.Indicator {
		set = fmt.Sprintf("\t\tm.%s = true\n", toGoFieldName(f.Name+"_redacted"))
	}
	if rf.Action == parser.RedactLeafValue {
		marker := `"` + parser.RedactMarker + `"`
		if rf.Kind == parser.LeafBytes {
			marker = "[]byte(" + marker + ")"
		}
		switch {
		case f.IsMap || f.IsRepeated:
			fmt.Fprintf(b, "\tfor i, v := range m.%s {\n\t\tif len(v) > 0 {\n\t\t\tm.%s[i] = %s\n", gf, gf, marker)
			if rf.Indicator {
				fmt.Fprintf(b, "\t\t\tm.%s = true\n", toGoFieldName(f.Name+"_redacted"))
			}
			b.WriteString("\t\t}\n\t}\n")
		case f.IsOptional && rf.Kind == parser.LeafString:
			fmt.Fprintf(b, "\tif m.%s != nil && *m.%s != \"\" {\n\t\tv := %s\n\t\tm.%s = &v\n%s\t}\n", gf, gf, marker, gf, set)
		default:
			fmt.Fprintf(b, "\tif len(m.%s) > 0 {\n\t\tm.%s = %s\n%s\t}\n", gf, gf, marker, set)
		}
		return
	}
	sub := "redact" + valueTypeName(f)
	if rf.Action == parser.RedactAllLeaves {
		sub = "redactAll" + valueTypeName(f)
	}
	switch {
	case f.IsMap || f.IsRepeated:
		fmt.Fprintf(b, "\tfor _, v := range m.%s {\n\t\t%s(v)\n\t}\n", gf, sub)
		if rf.Indicator {
			fmt.Fprintf(b, "\tif len(m.%s) > 0 {\n%s\t}\n", gf, set)
		}
	default:
		fmt.Fprintf(b, "\tif m.%s != nil {\n\t\t%s(m.%s)\n%s\t}\n", gf, sub, gf, set)
	}
}

// valueTypeName is a message field's type, or a map's message value type.
func valueTypeName(f model.Field) string {
	if f.IsMap {
		return f.MapValueType
	}
	return f.Type
}

// writeGoSensitiveChecks emits the Validate body lines of every required /
// entropy-checked sensitive field of msg, and reports whether it emitted any.
func writeGoSensitiveChecks(b *strings.Builder, msg model.Message) bool {
	any := false
	for _, f := range msg.Fields {
		c, ok := parser.FieldSensitiveCheck(msg.Annotations, f)
		if !ok {
			continue
		}
		any = true
		gf := toGoFieldName(f.Name)
		name := msg.Name + "." + f.Name
		val := "m.Get" + gf + "()"
		if parser.FieldLeafKind(f) == parser.LeafBytes {
			val = "string(m.Get" + gf + "())"
		}
		fmt.Fprintf(b, "\t// Validate %s (sensitive)\n", gf)
		if c.Required {
			fmt.Fprintf(b, "\tif len(m.Get%s()) == 0 {\n\t\treturn protoerrors.NewErrMissingInvalidData(nil, %q)\n\t}\n", gf, sensitiveRequiredMsg(name))
		}
		if c.CheckValue {
			fmt.Fprintf(b, "\tswitch tacplusSensitiveVerdict(%s, %v, %d) {\n", val, parser.FieldLeafKind(f) == parser.LeafBytes, c.MinEntropyBits)
			fmt.Fprintf(b, "\tcase 1:\n\t\treturn protoerrors.NewErrMissingInvalidData(nil, %q)\n", sensitivePlaceholderMsg(name))
			fmt.Fprintf(b, "\tcase 2:\n\t\treturn protoerrors.NewErrMissingInvalidData(nil, %q)\n", sensitiveEntropyMsg(name, c.MinEntropyBits))
			b.WriteString("\t}\n")
		}
		b.WriteString("\n")
	}
	return any
}

// The three sensitive-validation messages name the field and the rule, never
// the value; Rust and Java emit the same text.
func sensitiveRequiredMsg(name string) string {
	return "field " + name + " (sensitive): required and empty"
}

func sensitivePlaceholderMsg(name string) string {
	return "field " + name + " (sensitive): a known placeholder value"
}

func sensitiveEntropyMsg(name string, bits int64) string {
	return fmt.Sprintf("field %s (sensitive): estimated entropy below %d bits", name, bits)
}

// fileNeedsSensitiveVerdict reports whether any message of pf validates a
// sensitive value, so the verdict helper is emitted once for the file.
func fileNeedsSensitiveVerdict(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, f := range msg.Fields {
			if c, ok := parser.FieldSensitiveCheck(msg.Annotations, f); ok && c.CheckValue {
				return true
			}
		}
	}
	return false
}

// generateSensitiveVerdictHelper emits tacplusSensitiveVerdict: 0 passes, 1 a
// known placeholder, 2 below the entropy floor. The entropy table is the
// parser's, as integer micro-bits, so no language computes a logarithm.
func generateSensitiveVerdictHelper(b *strings.Builder) {
	b.WriteString(`// tacplusSensitiveVerdict checks a set sensitive value: 0 passes, 1 is a known
// placeholder ("REDACTED", "changeme", ASCII case-insensitive, or one character
// repeated), 2 is below minBits of estimated entropy (bytes: length x 8;
// strings: code points x log2 of the character classes present). An empty value
// passes: required is checked on its own.
func tacplusSensitiveVerdict(v string, isBytes bool, minBits int64) int {
	if len(v) == 0 {
		return 0
	}
	lower := []byte(v)
	for i, c := range lower {
		if c >= 'A' && c <= 'Z' {
			lower[i] = c + ('a' - 'A')
		}
	}
	switch string(lower) {
`)
	quoted := make([]string, len(parser.SensitivePlaceholders))
	for i, p := range parser.SensitivePlaceholders {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	fmt.Fprintf(b, "\tcase %s:\n\t\treturn 1\n\t}\n", strings.Join(quoted, ", "))
	b.WriteString(`	var count int64
	var lo, up, dg, ot bool
	repeated := true
	if isBytes {
		count = int64(len(v))
		for i := 1; i < len(v); i++ {
			if v[i] != v[0] {
				repeated = false
			}
		}
	} else {
		var first rune
		for _, r := range v {
			if count == 0 {
				first = r
			} else if r != first {
				repeated = false
			}
			count++
			switch {
			case r >= 'a' && r <= 'z':
				lo = true
			case r >= 'A' && r <= 'Z':
				up = true
			case r >= '0' && r <= '9':
				dg = true
			default:
				ot = true
			}
		}
	}
	if repeated {
		return 1
	}
	var est int64
	if isBytes {
		est = count * 8000000
	} else {
		pool := 0
		if lo {
			pool += 26
		}
		if up {
			pool += 26
		}
		if dg {
			pool += 10
		}
		if ot {
			pool += 33
		}
		var perChar int64
		switch pool {
`)
	tab := parser.EntropyMicroBits()
	for _, p := range parser.EntropyPools() {
		fmt.Fprintf(b, "\t\tcase %d:\n\t\t\tperChar = %d\n", p, tab[p])
	}
	b.WriteString(`		}
		est = count * perChar
	}
	if est < minBits*1000000 {
		return 2
	}
	return 0
}

`)
}
