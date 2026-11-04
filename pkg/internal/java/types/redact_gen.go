// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// This file emits redaction for any type (tacplus#274) for Java: the
// deterministic redact<T> of every message with a sensitive leaf in its tree,
// and the validation of required / entropy-checked sensitive fields. Both come
// from the language-neutral plan in parser/sensitive.go, which Go and Rust emit
// from too, so the three give byte-identical output for the same input.
//
// Helpers.redact<T>(m) returns a redacted COPY (protobuf-java messages are
// immutable; the copy is built through toBuilder()): a set sensitive string
// becomes "REDACTED", a set sensitive bytes value the bytes of "REDACTED", an
// empty one stays empty, and <field>_redacted is set when the field held a
// value. Helpers.redactAll<T>(m) rewrites every string/bytes leaf of the tree (a
// (spec.sensitive) message field, or a (spec.sensitive_message)); it is
// package-private, as Go's redactAll<T> is unexported.

// writeRedact emits the Redact section of Helpers for every message of pf the
// index says needs it. ix nil means pf alone is the universe.
func writeRedact(b *strings.Builder, pf model.ProtoFile, outerClassName string, ix *parser.SensitiveIndex) {
	if ix == nil {
		files := []model.ProtoFile{pf}
		ix = parser.NewSensitiveIndex(files, parser.NewMessageResolver(files))
	}
	outers := map[string]string{}
	for _, m := range pf.Messages {
		outers[m.Name] = msgOuterClass(m, outerClassName)
	}
	header := false
	for mi := range pf.Messages {
		msg := &pf.Messages[mi]
		needs, needsAll := ix.NeedsRedact(pf.Package, msg.Name), ix.NeedsRedactAll(pf.Package, msg.Name)
		if !needs && !needsAll {
			continue
		}
		if !header {
			b.WriteString("// ============ Redact (spec.sensitive) ============\n")
			header = true
		}
		if needs {
			writeJavaRedactFunc(b, ix.Plan(msg, pf.Package, false), outers, false)
		}
		if needsAll {
			writeJavaRedactFunc(b, ix.Plan(msg, pf.Package, true), outers, true)
		}
	}
	if fileNeedsSensitiveVerdict(pf) {
		writeJavaSensitiveVerdictHelper(b)
	}
}

// javaRedactOuter returns the outer class of a same-package message type.
func javaRedactOuter(outers map[string]string, name string) string {
	if o, ok := outers[name]; ok {
		return o
	}
	return ""
}

func writeJavaRedactFunc(b *strings.Builder, p *parser.RedactPlan, outers map[string]string, all bool) {
	t := p.Message.Name
	full := javaRedactOuter(outers, t) + "." + t
	if all {
		fmt.Fprintf(b, "/**\n * redactAll%[1]s returns a copy of m with EVERY string and bytes leaf of its tree\n", t)
		b.WriteString(" * redacted (a (spec.sensitive) message field, or a (spec.sensitive_message)), except\n")
		b.WriteString(" * (spec.not_sensitive) fields and the injected trace_id.\n */\n")
		fmt.Fprintf(b, "static %s redactAll%s(%s m) {\n", full, t, full)
	} else {
		fmt.Fprintf(b, "/**\n * redact%[1]s returns a deterministically redacted COPY of m for review by a\n", t)
		b.WriteString(" * tool, a human or an LLM: every sensitive string becomes \"REDACTED\", every\n")
		b.WriteString(" * sensitive bytes value the bytes of \"REDACTED\" (empty stays empty), and each\n")
		b.WriteString(" * &lt;field&gt;_redacted is set where a value was replaced. No key, no randomness.\n */\n")
		fmt.Fprintf(b, "public static %s redact%s(%s m) {\n", full, t, full)
	}
	b.WriteString("if (m == null) { return null; }\n")
	if len(p.Fields) == 0 {
		b.WriteString("return m;\n}\n")
		return
	}
	fmt.Fprintf(b, "%s.Builder out = m.toBuilder();\n", full)
	for _, rf := range p.Fields {
		writeJavaRedactField(b, rf, outers)
	}
	b.WriteString("return out.build();\n}\n")
}

func writeJavaRedactField(b *strings.Builder, rf parser.RedactField, outers map[string]string) {
	f := rf.Field
	p := javaPascalField(f.Name)
	set := ""
	if rf.Indicator {
		set = fmt.Sprintf("out.set%s(true);\n", javaPascalField(f.Name+"_redacted"))
	}
	if rf.Action == parser.RedactLeafValue {
		marker := `"` + parser.RedactMarker + `"`
		valType := "String"
		if rf.Kind == parser.LeafBytes {
			marker = "com.google.protobuf.ByteString.copyFromUtf8(" + marker + ")"
			valType = "com.google.protobuf.ByteString"
		}
		switch {
		case f.IsMap:
			key := protoTypeToJavaBoxed(f.MapKeyType)
			fmt.Fprintf(b, "for (java.util.Map.Entry<%[1]s, %[2]s> e : new java.util.ArrayList<>(out.get%[3]sMap().entrySet())) {\n", key, valType, p)
			fmt.Fprintf(b, "if (!e.getValue().isEmpty()) {\nout.put%s(e.getKey(), %s);\n%s}\n}\n", p, marker, set)
		case f.IsRepeated:
			fmt.Fprintf(b, "for (int i = 0; i < out.get%[1]sCount(); i++) {\nif (!out.get%[1]s(i).isEmpty()) {\nout.set%[1]s(i, %[2]s);\n%[3]s}\n}\n", p, marker, set)
		case f.IsOptional && rf.Kind == parser.LeafString:
			fmt.Fprintf(b, "if (out.has%[1]s() && !out.get%[1]s().isEmpty()) {\nout.set%[1]s(%[2]s);\n%[3]s}\n", p, marker, set)
		default:
			fmt.Fprintf(b, "if (!out.get%[1]s().isEmpty()) {\nout.set%[1]s(%[2]s);\n%[3]s}\n", p, marker, set)
		}
		return
	}
	vt := valueTypeName(f)
	sub := "redact" + vt
	if rf.Action == parser.RedactAllLeaves {
		sub = "redactAll" + vt
	}
	switch {
	case f.IsMap:
		key := protoTypeToJavaBoxed(f.MapKeyType)
		full := javaRedactOuter(outers, vt) + "." + vt
		fmt.Fprintf(b, "for (java.util.Map.Entry<%[1]s, %[2]s> e : new java.util.ArrayList<>(out.get%[3]sMap().entrySet())) {\n", key, full, p)
		fmt.Fprintf(b, "out.put%s(e.getKey(), %s(e.getValue()));\n}\n", p, sub)
		if rf.Indicator {
			fmt.Fprintf(b, "if (out.get%sCount() > 0) {\n%s}\n", p, set)
		}
	case f.IsRepeated:
		fmt.Fprintf(b, "for (int i = 0; i < out.get%[1]sCount(); i++) {\nout.set%[1]s(i, %[2]s(out.get%[1]s(i)));\n}\n", p, sub)
		if rf.Indicator {
			fmt.Fprintf(b, "if (out.get%sCount() > 0) {\n%s}\n", p, set)
		}
	default:
		fmt.Fprintf(b, "if (out.has%[1]s()) {\nout.set%[1]s(%[2]s(out.get%[1]s()));\n%[3]s}\n", p, sub, set)
	}
}

// valueTypeName is a message field's type, or a map's message value type.
func valueTypeName(f model.Field) string {
	if f.IsMap {
		return f.MapValueType
	}
	return f.Type
}

// writeJavaSensitiveChecks emits the validate body lines of every required /
// entropy-checked sensitive field of msg (Java twin of writeGoSensitiveChecks,
// same message text), and reports whether it emitted any.
func writeJavaSensitiveChecks(b *strings.Builder, msg model.Message) bool {
	any := false
	for _, f := range msg.Fields {
		c, ok := parser.FieldSensitiveCheck(msg.Annotations, f)
		if !ok {
			continue
		}
		any = true
		getter := "msg.get" + javaFieldNameUpper(f.Name) + "()"
		name := msg.Name + "." + f.Name
		isBytes := parser.FieldLeafKind(f) == parser.LeafBytes
		val := getter
		if !isBytes {
			val = "com.google.protobuf.ByteString.copyFromUtf8(" + getter + ")"
		}
		fmt.Fprintf(b, "// Validate %s (sensitive)\n", toJavaFieldName(f.Name))
		if c.Required {
			fmt.Fprintf(b, "if (%s.isEmpty()) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, %q);\n}\n", getter, sensitiveRequiredMsgJava(name))
		}
		if c.CheckValue {
			fmt.Fprintf(b, "switch (tacplusSensitiveVerdict(%s, %v, %dL)) {\n", val, isBytes, c.MinEntropyBits)
			fmt.Fprintf(b, "case 1:\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, %q);\n", sensitivePlaceholderMsgJava(name))
			fmt.Fprintf(b, "case 2:\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, %q);\n", sensitiveEntropyMsgJava(name, c.MinEntropyBits))
			b.WriteString("default:\nbreak;\n}\n")
		}
	}
	return any
}

// The three sensitive-validation messages name the field and the rule, never
// the value; Go and Rust emit the same text.
func sensitiveRequiredMsgJava(name string) string {
	return "field " + name + " (sensitive): required and empty"
}

func sensitivePlaceholderMsgJava(name string) string {
	return "field " + name + " (sensitive): a known placeholder value"
}

func sensitiveEntropyMsgJava(name string, bits int64) string {
	return fmt.Sprintf("field %s (sensitive): estimated entropy below %d bits", name, bits)
}

// fileNeedsSensitiveVerdict reports whether any message of pf validates a
// sensitive value, so the verdict helper is emitted once per Helpers class.
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

// writeJavaSensitiveVerdictHelper emits tacplusSensitiveVerdict: 0 passes, 1 a
// known placeholder, 2 below the entropy floor. The entropy table is the
// parser's, as integer micro-bits, so no language computes a logarithm.
func writeJavaSensitiveVerdictHelper(b *strings.Builder) {
	b.WriteString(`/**
 * tacplusSensitiveVerdict checks a set sensitive value (a string field passes its
 * UTF-8 bytes): 0 passes, 1 is a known placeholder ("REDACTED", "changeme", ASCII
 * case-insensitive, or one character repeated), 2 is below minBits of estimated
 * entropy (bytes: length x 8; strings: code points x log2 of the character
 * classes present). An empty value passes: required is checked on its own.
 */
static int tacplusSensitiveVerdict(com.google.protobuf.ByteString value, boolean isBytes, long minBits) {
byte[] v = value.toByteArray();
if (v.length == 0) { return 0; }
byte[] lower = v.clone();
for (int i = 0; i < lower.length; i++) {
if (lower[i] >= 'A' && lower[i] <= 'Z') { lower[i] = (byte) (lower[i] + ('a' - 'A')); }
}
String folded = new String(lower, java.nio.charset.StandardCharsets.ISO_8859_1);
`)
	conds := make([]string, len(parser.SensitivePlaceholders))
	for i, p := range parser.SensitivePlaceholders {
		conds[i] = fmt.Sprintf("folded.equals(%q)", p)
	}
	fmt.Fprintf(b, "if (%s) { return 1; }\n", strings.Join(conds, " || "))
	b.WriteString(`long count;
boolean lo = false, up = false, dg = false, ot = false;
boolean repeated = true;
if (isBytes) {
count = v.length;
for (int i = 1; i < v.length; i++) {
if (v[i] != v[0]) { repeated = false; }
}
} else {
int[] cps = value.toStringUtf8().codePoints().toArray();
count = cps.length;
for (int i = 0; i < cps.length; i++) {
int r = cps[i];
if (i > 0 && r != cps[0]) { repeated = false; }
if (r >= 'a' && r <= 'z') { lo = true; }
else if (r >= 'A' && r <= 'Z') { up = true; }
else if (r >= '0' && r <= '9') { dg = true; }
else { ot = true; }
}
}
if (repeated) { return 1; }
long est;
if (isBytes) {
est = count * 8000000L;
} else {
int pool = 0;
if (lo) { pool += 26; }
if (up) { pool += 26; }
if (dg) { pool += 10; }
if (ot) { pool += 33; }
long perChar;
switch (pool) {
`)
	tab := parser.EntropyMicroBits()
	for _, p := range parser.EntropyPools() {
		fmt.Fprintf(b, "case %d:\nperChar = %dL;\nbreak;\n", p, tab[p])
	}
	b.WriteString(`default:
perChar = 0L;
break;
}
est = count * perChar;
}
if (est < minBits * 1000000L) { return 2; }
return 0;
}
`)
}

// sensitiveIndex builds the redaction index over every parsed file, so a
// module's redact<T> sees the same plan Go and Rust do.
func sensitiveIndex(allFiles []model.ProtoFile) *parser.SensitiveIndex {
	return parser.NewSensitiveIndex(allFiles, parser.NewMessageResolver(allFiles))
}
