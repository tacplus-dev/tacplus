// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// This file emits redaction for any type (tacplus#274) for Rust: the
// deterministic redact_<t> of every message with a sensitive leaf in its tree,
// and the validation of required / entropy-checked sensitive fields. Both come
// from the language-neutral plan in parser/sensitive.go, which Go and Java emit
// from too, so the three give byte-identical output for the same input.
//
// redact_<t>(&m) returns a redacted COPY: a set sensitive string becomes
// "REDACTED", a set sensitive bytes value the bytes of "REDACTED", an empty one
// stays empty, and <field>_redacted is set when the field held a value. The
// in-place redact_<t>_in_place / redact_all_<t>_in_place do the work on the
// copy; redact_all_* rewrites every string/bytes leaf (a (spec.sensitive)
// message field, or a (spec.sensitive_message)), redact_<t>_in_place only the
// annotated ones.

// writeRedact emits redact_<t> and the in-place helpers for every message of pf
// the index says needs them, and the verdict helper when a validator calls it.
func writeRedact(b *strings.Builder, pf model.ProtoFile, allFiles []model.ProtoFile) {
	ix := parser.NewSensitiveIndex(allFiles, parser.NewMessageResolver(allFiles))
	for mi := range pf.Messages {
		msg := &pf.Messages[mi]
		if ix.NeedsRedact(pf.Package, msg.Name) {
			writeRustRedactFunc(b, ix.Plan(msg, pf.Package, false))
		}
		if ix.NeedsRedactAll(pf.Package, msg.Name) {
			writeRustRedactInPlace(b, ix.Plan(msg, pf.Package, true), rustRedactAllFn(msg.Name),
				"rewrites every string and bytes leaf of m in place (the value reached through a sensitive field)")
		}
	}
	if rustFileNeedsSensitiveVerdict(pf) {
		writeRustSensitiveVerdictHelper(b)
	}
}

func rustRedactFn(msg string) string        { return "redact_" + strutil.ToSnakeCase(msg) }
func rustRedactInPlaceFn(msg string) string { return rustRedactFn(msg) + "_in_place" }
func rustRedactAllFn(msg string) string {
	return "redact_all_" + strutil.ToSnakeCase(msg) + "_in_place"
}

func writeRustRedactFunc(b *strings.Builder, p *parser.RedactPlan) {
	t := toProstTypeName(p.Message.Name)
	fn := rustRedactFn(p.Message.Name)
	fmt.Fprintf(b, "/// %s returns a deterministically redacted COPY of m for review by a\n", fn)
	b.WriteString("/// tool, a human or an LLM: every sensitive string becomes \"REDACTED\", every\n")
	b.WriteString("/// sensitive bytes value the bytes of \"REDACTED\" (empty stays empty), and each\n")
	b.WriteString("/// <field>_redacted is set where a value was replaced. No key, no randomness.\n")
	fmt.Fprintf(b, "pub fn %s(m: &%s) -> %s {\n", fn, t, t)
	b.WriteString("    let mut out = m.clone();\n")
	fmt.Fprintf(b, "    %s(&mut out);\n    out\n}\n\n", rustRedactInPlaceFn(p.Message.Name))
	writeRustRedactInPlace(b, p, rustRedactInPlaceFn(p.Message.Name), "is "+fn+" in place")
}

// writeRustRedactInPlace emits one in-place redaction function from a plan.
func writeRustRedactInPlace(b *strings.Builder, p *parser.RedactPlan, fn, doc string) {
	t := toProstTypeName(p.Message.Name)
	fmt.Fprintf(b, "/// %s %s.\n", fn, doc)
	if len(p.Fields) == 0 {
		fmt.Fprintf(b, "pub fn %s(_m: &mut %s) {}\n\n", fn, t)
		return
	}
	fmt.Fprintf(b, "pub fn %s(m: &mut %s) {\n", fn, t)
	for _, rf := range p.Fields {
		writeRustRedactField(b, rf)
	}
	b.WriteString("}\n\n")
}

func writeRustRedactField(b *strings.Builder, rf parser.RedactField) {
	f := rf.Field
	rfn := escapeRustKeyword(strutil.ToSnakeCase(f.Name))
	set := func(indent string) string {
		if !rf.Indicator {
			return ""
		}
		return fmt.Sprintf("%sm.%s = true;\n", indent, escapeRustKeyword(strutil.ToSnakeCase(f.Name+"_redacted")))
	}
	if rf.Action == parser.RedactLeafValue {
		marker := fmt.Sprintf("%q.to_string()", parser.RedactMarker)
		if rf.Kind == parser.LeafBytes {
			marker = fmt.Sprintf("b%q.to_vec()", parser.RedactMarker)
		}
		var iter string
		switch {
		case f.IsMap:
			iter = fmt.Sprintf("for v in m.%s.values_mut() {\n", rfn)
		case f.IsRepeated:
			iter = fmt.Sprintf("for v in m.%s.iter_mut() {\n", rfn)
		case f.IsOptional:
			iter = fmt.Sprintf("if let Some(v) = m.%s.as_mut() {\n", rfn)
		default:
			fmt.Fprintf(b, "    if !m.%s.is_empty() {\n        m.%s = %s;\n%s    }\n", rfn, rfn, marker, set("        "))
			return
		}
		fmt.Fprintf(b, "    %s        if !v.is_empty() {\n            *v = %s;\n%s        }\n    }\n", iter, marker, set("            "))
		return
	}
	sub := rustRedactInPlaceFn(valueTypeName(f))
	if rf.Action == parser.RedactAllLeaves {
		sub = rustRedactAllFn(valueTypeName(f))
	}
	switch {
	case f.IsMap || f.IsRepeated:
		it := "iter_mut"
		if f.IsMap {
			it = "values_mut"
		}
		fmt.Fprintf(b, "    for v in m.%s.%s() {\n        %s(v);\n    }\n", rfn, it, sub)
		if rf.Indicator {
			fmt.Fprintf(b, "    if !m.%s.is_empty() {\n%s    }\n", rfn, set("        "))
		}
	default:
		fmt.Fprintf(b, "    if let Some(v) = m.%s.as_mut() {\n        %s(v);\n%s    }\n", rfn, sub, set("        "))
	}
}

// valueTypeName is a message field's type, or a map's message value type.
func valueTypeName(f model.Field) string {
	if f.IsMap {
		return f.MapValueType
	}
	return f.Type
}

// writeRustSensitiveChecks emits the validate body lines of every required /
// entropy-checked sensitive field of msg, and reports whether it emitted any.
// The messages are the Go and Java validators' text: they name the field and
// the rule, never the value.
func writeRustSensitiveChecks(b *strings.Builder, msg model.Message) bool {
	found := false
	for _, f := range msg.Fields {
		c, ok := parser.FieldSensitiveCheck(msg.Annotations, f)
		if !ok {
			continue
		}
		found = true
		rfn := escapeRustKeyword(strutil.ToSnakeCase(f.Name))
		name := msg.Name + "." + f.Name
		isBytes := parser.FieldLeafKind(f) == parser.LeafBytes
		// val is the field's value as &[u8]; an unset optional reads as empty.
		val := "self." + rfn
		if f.IsOptional {
			val += ".as_deref().unwrap_or_default()"
		}
		empty := val + ".is_empty()"
		if isBytes {
			if !f.IsOptional {
				val += ".as_slice()"
			}
		} else {
			val += ".as_bytes()"
		}
		fmt.Fprintf(b, "// Validate %s (sensitive)\n", rfn)
		if c.Required {
			fmt.Fprintf(b, "if %s {\nreturn Err(%q.to_string());\n}\n", empty,
				"field "+name+" (sensitive): required and empty")
		}
		if c.CheckValue {
			fmt.Fprintf(b, "match tacplus_sensitive_verdict(%s, %v, %d) {\n", val, isBytes, c.MinEntropyBits)
			fmt.Fprintf(b, "1 => return Err(%q.to_string()),\n", "field "+name+" (sensitive): a known placeholder value")
			fmt.Fprintf(b, "2 => return Err(%q.to_string()),\n",
				fmt.Sprintf("field %s (sensitive): estimated entropy below %d bits", name, c.MinEntropyBits))
			b.WriteString("_ => {}\n}\n")
		}
	}
	return found
}

// rustMessageHasSensitiveCheck reports whether msg validates any sensitive field.
func rustMessageHasSensitiveCheck(msg model.Message) bool {
	for _, f := range msg.Fields {
		if _, ok := parser.FieldSensitiveCheck(msg.Annotations, f); ok {
			return true
		}
	}
	return false
}

// rustFileNeedsSensitiveVerdict reports whether any message of pf validates a
// sensitive value, so the verdict helper is emitted once for the file.
func rustFileNeedsSensitiveVerdict(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, f := range msg.Fields {
			if c, ok := parser.FieldSensitiveCheck(msg.Annotations, f); ok && c.CheckValue {
				return true
			}
		}
	}
	return false
}

// writeRustSensitiveVerdictHelper emits tacplus_sensitive_verdict: 0 passes, 1 a
// known placeholder, 2 below the entropy floor. The entropy table is the
// parser's, as integer micro-bits, so no language computes a logarithm.
func writeRustSensitiveVerdictHelper(b *strings.Builder) {
	b.WriteString(`/// tacplus_sensitive_verdict checks a set sensitive value: 0 passes, 1 is a known
/// placeholder ("REDACTED", "changeme", ASCII case-insensitive, or one character
/// repeated), 2 is below min_bits of estimated entropy (bytes: length x 8;
/// strings: code points x log2 of the character classes present). An empty value
/// passes: required is checked on its own.
fn tacplus_sensitive_verdict(v: &[u8], is_bytes: bool, min_bits: i64) -> u8 {
    if v.is_empty() {
        return 0;
    }
`)
	conds := make([]string, len(parser.SensitivePlaceholders))
	for i, p := range parser.SensitivePlaceholders {
		conds[i] = fmt.Sprintf("v.eq_ignore_ascii_case(b%q)", p)
	}
	fmt.Fprintf(b, "    if %s {\n        return 1;\n    }\n", strings.Join(conds, " || "))
	b.WriteString(`    let (repeated, est) = if is_bytes {
        (v.iter().all(|c| *c == v[0]), v.len() as i64 * 8_000_000)
    } else {
        let s = String::from_utf8_lossy(v);
        let first = s.chars().next();
        let (mut lo, mut up, mut dg, mut ot) = (false, false, false, false);
        let mut n: i64 = 0;
        let mut same = true;
        for r in s.chars() {
            if Some(r) != first {
                same = false;
            }
            n += 1;
            match r {
                'a'..='z' => lo = true,
                'A'..='Z' => up = true,
                '0'..='9' => dg = true,
                _ => ot = true,
            }
        }
        let mut pool = 0;
        if lo {
            pool += 26;
        }
        if up {
            pool += 26;
        }
        if dg {
            pool += 10;
        }
        if ot {
            pool += 33;
        }
        let per_char: i64 = match pool {
`)
	tab := parser.EntropyMicroBits()
	for _, p := range parser.EntropyPools() {
		fmt.Fprintf(b, "            %d => %d,\n", p, tab[p])
	}
	b.WriteString(`            _ => 0,
        };
        (same, n * per_char)
    };
    if repeated {
        return 1;
    }
    if est < min_bits * 1_000_000 {
        return 2;
    }
    0
}

`)
}
