// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// This file emits flock state (tacplus#206) for Java: for every flock entry of
// a package, from the plan in parser/flock.go, the static methods of the
// package's generated FlockHelpers class. It is a port of the Go emitter
// (pkg/internal/go/types/flock_gen.go), function for function; the Java names
// are the Go names with a lower-case first letter (FlockMergePerson ->
// flockMergePerson). The schema-independent half is javaFlockRuntime
// (flock_runtime.go).
//
// protobuf-java messages are immutable, so where Go changes an entry in place
// (the writers, FlockMark, FlockAck, FlockWrite) Java takes the entry's
// Builder; the merges take messages and return new ones. Where Go returns
// (value, bool), Java returns an Optional; where Go returns an error, Java
// throws the generated ProtoException.

// javaTypeResolver returns the Java class (fully qualified, or as the
// generated package refers to it) of the message or enum pkg.name.
type javaTypeResolver func(pkg, name string) (string, bool)

// generateJavaFlock writes FlockHelpers.java into outputDir (the Java package
// directory of javaPackage) when pf holds a flock entry. resolve finds types of
// other packages (sys/flock.proto's included); nil falls back to the standard
// location of sys/flock.proto only.
func generateJavaFlock(pf model.ProtoFile, outputDir, javaPackage, defaultOuter string, config *tactype.Config, ix *parser.FlockIndex, resolve javaTypeResolver) error {
	entries := ix.EntriesIn(pf.Package)
	if len(entries) == 0 {
		return nil
	}
	g := &javaFlockGen{
		pkg:     pf.Package,
		outers:  map[string]string{},
		resolve: resolve,
		joins:   map[string]bool{},
		sets:    map[string]bool{},
	}
	for _, m := range pf.Messages {
		g.outers[m.Name] = msgOuterClass(m, defaultOuter)
	}
	for _, e := range pf.Enums {
		g.outers[e.Name] = enumOuterClass(e, defaultOuter)
	}
	g.flock = config.JavaPackageBase + ".types.sys.flock.Flock"
	if resolve != nil {
		if hlc, ok := resolve(parser.FlockPackage, "Hlc"); ok {
			g.flock = strings.TrimSuffix(hlc, ".Hlc")
		}
	}
	for _, e := range entries {
		g.entry(e)
	}
	if g.err != nil {
		return g.err
	}

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangJava, pf.RelPath))
	fmt.Fprintf(&b, "package %s;\n\n", javaPackage)
	fmt.Fprintf(&b, "import %s.types.sys.errors.ProtoException;\n\n", config.JavaPackageBase)
	b.WriteString("/**\n * FlockHelpers holds the generated merge of every flock entry of this package\n")
	b.WriteString(" * (tacplus#206): the hybrid clock, the per-entry merge, merge of duplicate\n")
	b.WriteString(" * entries, the writers that keep the merge metadata, the deletion mark\n")
	b.WriteString(" * helpers, the unique check and the canonical digest. Go and Rust emit the\n")
	b.WriteString(" * same functions from the same plan.\n */\n")
	b.WriteString("public final class FlockHelpers {\n")
	b.WriteString(strings.NewReplacer(
		"$F", g.flock,
		"$SKEW", strconv.Itoa(parser.FlockMaxSkewMs),
		"$REBUILD", strconv.Itoa(parser.FlockRebuildAfterMs),
		"$MAXAGE", strconv.Itoa(parser.FlockMaxMessageAgeMs),
	).Replace(javaFlockRuntime))
	b.WriteString("\n")
	b.WriteString(g.b.String())
	b.WriteString(g.tail.String())
	b.WriteString("}\n")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, "FlockHelpers.java"), []byte(b.String()), 0644)
}

// newJavaFlockResolver resolves a message or enum of any parsed file to its
// Java class: the file's java_package and outer class.
func newJavaFlockResolver(allFiles []model.ProtoFile, protoDir string) javaTypeResolver {
	return func(pkg, name string) (string, bool) {
		for _, pf := range allFiles {
			if pf.Package != pkg {
				continue
			}
			found := false
			for _, m := range pf.Messages {
				found = found || m.Name == name
			}
			for _, e := range pf.Enums {
				found = found || e.Name == name
			}
			if !found {
				continue
			}
			path := filepath.Join(protoDir, pf.RelPath)
			jp := extractJavaPackageName(path)
			if jp == "" {
				return "", false
			}
			outer := extractJavaOuterClassName(path)
			if outer == "" {
				def := toCamelOuter(filepath.Base(pf.RelPath))
				outer = def
				if hasNamingConflict(path, def) {
					outer = def + "OuterClass"
				}
			}
			return jp + "." + outer + "." + name, true
		}
		return "", false
	}
}

// toCamelOuter is protoc's default outer class name of a proto file name.
func toCamelOuter(file string) string {
	stem := strings.TrimSuffix(file, ".proto")
	var out strings.Builder
	up := true
	for _, r := range stem {
		switch {
		case r == '_' || r == '-' || r == '.':
			up = true
		case up && r >= 'a' && r <= 'z':
			out.WriteRune(r - 'a' + 'A')
			up = false
		default:
			out.WriteRune(r)
			up = r >= '0' && r <= '9'
		}
	}
	return out.String()
}

type javaFlockGen struct {
	b strings.Builder
	// tail holds the helpers (set materializers, element joins) emitted while
	// a method body is being written; it is appended at the end.
	tail    strings.Builder
	pkg     string
	flock   string // the Java outer class of sys/flock.proto
	outers  map[string]string
	resolve javaTypeResolver
	joins   map[string]bool
	sets    map[string]bool
	err     error
}

func (g *javaFlockGen) p(format string, args ...any) { fmt.Fprintf(&g.b, format, args...) }

func (g *javaFlockGen) t(format string, args ...any) { fmt.Fprintf(&g.tail, format, args...) }

// typ is the Java class of the message or enum pkg.name.
func (g *javaFlockGen) typ(pkg, name string) string {
	if pkg == parser.FlockPackage {
		return g.flock + "." + name
	}
	if pkg == "" || pkg == g.pkg {
		if o, ok := g.outers[name]; ok && o != "" {
			return o + "." + name
		}
	} else if g.resolve != nil {
		if t, ok := g.resolve(pkg, name); ok {
			return t
		}
	}
	if g.err == nil {
		g.err = fmt.Errorf("flock (java): cannot resolve the Java class of %s.%s", pkg, name)
	}
	return name
}

// hlc is the Java class of flock.Hlc; meta of flock.Meta.
func (g *javaFlockGen) hlc() string  { return g.flock + ".Hlc" }
func (g *javaFlockGen) meta() string { return g.flock + ".Meta" }

// scalarType is the Java type of one value of f (its element, when repeated).
func (g *javaFlockGen) scalarType(f model.Field) string {
	switch {
	case f.IsMessage, f.IsEnum:
		return g.typ(f.TypePackage, f.Type)
	case f.Type == "bytes":
		return "com.google.protobuf.ByteString"
	}
	return protoTypeToJavaType(f.Type)
}

// boxed is the Java list element type of a repeated scalar f's values as the
// set materializer returns them (an enum's numbers).
func (g *javaFlockGen) boxed(f model.Field) string {
	switch {
	case f.IsMessage:
		return g.typ(f.TypePackage, f.Type)
	case f.IsEnum:
		return "Integer"
	case f.Type == "bytes":
		return "com.google.protobuf.ByteString"
	case f.Type == "bool":
		return "Boolean"
	}
	return protoTypeToJavaBoxed(f.Type)
}

func jName(f model.Field) string { return javaPascalField(f.Name) }

// get reads a singular field of the message or builder x (an enum by number).
func get(f model.Field, x string) string {
	if f.IsEnum {
		return x + ".get" + jName(f) + "Value()"
	}
	return x + ".get" + jName(f) + "()"
}

// opt reads a singular message field of x as Go does: null when absent.
func opt(f model.Field, x string) string {
	return "(" + x + ".has" + jName(f) + "() ? " + x + ".get" + jName(f) + "() : null)"
}

// copyField sets field f of the builder to to its value in from, presence
// included: Go's out.X = from.X.
func copyField(f model.Field, from, to string) string {
	n := jName(f)
	switch {
	case f.IsMap:
		return fmt.Sprintf("%[2]s.clear%[3]s().putAll%[3]s(%[1]s.get%[3]sMap());\n", from, to, n)
	case f.IsRepeated && f.IsEnum:
		return fmt.Sprintf("%[2]s.clear%[3]s().addAll%[3]sValue(%[1]s.get%[3]sValueList());\n", from, to, n)
	case f.IsRepeated:
		return fmt.Sprintf("%[2]s.clear%[3]s().addAll%[3]s(%[1]s.get%[3]sList());\n", from, to, n)
	case f.IsMessage:
		return fmt.Sprintf("if (%[1]s.has%[3]s()) {\n%[2]s.set%[3]s(%[1]s.get%[3]s());\n} else {\n%[2]s.clear%[3]s();\n}\n", from, to, n)
	case f.IsEnum:
		return fmt.Sprintf("%[2]s.set%[3]sValue(%[1]s.get%[3]sValue());\n", from, to, n)
	}
	return fmt.Sprintf("%[2]s.set%[3]s(%[1]s.get%[3]s());\n", from, to, n)
}

// setNullable sets the message field n of builder x to the nullable value v.
func setNullable(x, n, v string) string {
	return fmt.Sprintf("if (%[3]s == null) {\n%[1]s.clear%[2]s();\n} else {\n%[1]s.set%[2]s(%[3]s);\n}\n", x, n, v)
}

// idExpr is the canonical identity encoding of the Java value v of kind k
// (an enum by number), of Java type jt.
func idExpr(k parser.FlockScalarKind, jt, v string) string {
	switch k {
	case parser.FlockKindString:
		return "flockIDString(" + v + ")"
	case parser.FlockKindBytes:
		return "flockIDBytes(" + v + ")"
	case parser.FlockKindBool:
		return "flockIDBool(" + v + ")"
	case parser.FlockKindUnsigned:
		if jt == "int" {
			return "flockIDUint(Integer.toUnsignedLong(" + v + "))"
		}
		return "flockIDUint(" + v + ")"
	}
	return "flockIDInt(" + v + ")"
}

// fromIDText decodes the identity text t back to a Java value of type jt (an
// enum as its number).
func fromIDText(k parser.FlockScalarKind, jt, t string) string {
	switch k {
	case parser.FlockKindString:
		return t
	case parser.FlockKindBytes:
		return "flockUnhex(" + t + ")"
	case parser.FlockKindBool:
		return t + `.equals("true")`
	case parser.FlockKindUnsigned:
		if jt == "int" || jt == "Integer" {
			return "(int) flockParseUint(" + t + ")"
		}
		return "flockParseUint(" + t + ")"
	}
	if jt == "int" || jt == "Integer" || jt == "" {
		return "(int) flockParseInt(" + t + ")"
	}
	return "flockParseInt(" + t + ")"
}

func pathName(path string) string {
	var out string
	for _, seg := range strings.Split(path, ".") {
		out += javaPascalField(seg)
	}
	return out
}

// minMax is the runtime MIN or MAX (op) of a field's Java type and kind.
func minMax(op string, f parser.FlockField, jt string) string {
	switch {
	case f.Kind == parser.FlockKindString:
		return "flock" + op + "NonZeroString"
	case f.Kind == parser.FlockKindUnsigned && jt == "int":
		return "flock" + op + "NonZeroUint"
	case f.Kind == parser.FlockKindUnsigned:
		return "flock" + op + "NonZeroUlong"
	case jt == "int":
		return "flock" + op + "NonZeroInt"
	}
	return "flock" + op + "NonZeroLong"
}

func (g *javaFlockGen) entry(e *parser.FlockEntry) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	what := e.Package + "." + t
	key, created, epoch, mark := javaPascalField(e.Key), javaPascalField(e.Created), javaPascalField(e.Epoch), javaPascalField(e.Mark)
	meta := javaPascalField(parser.FlockMetaFieldName)
	H, F := g.hlc(), g.flock

	g.p("// ---- flock entry %s ----\n\n", what)
	g.p("/** Returns a new %[1]s entry: flock creates its key (flockNewKey) and stamps its creation, which never changes. */\n", t)
	g.p("public static %[1]s flockNew%[2]s(String key, %[3]s created) {\n%[1]s.Builder b = %[1]s.newBuilder().set%[4]s(key);\n%[3]s c = flockHlcClone(created);\nif (c != null) {\nb.set%[5]s(c).set%[6]s(c);\n}\nreturn b.build();\n}\n\n", jt, t, H, key, created, epoch)

	// The store epoch bump every write makes, and the mark setter.
	g.p("static void flockBump%[1]s(%[2]s.Builder e, %[3]s at) {\n%[3]s w = flockHlcMax(e.get%[4]s(), at);\n%[5]s}\n\n", t, jt, H, epoch, setNullable("e", epoch, "w"))

	// Merge.
	g.p("/**\n * Merges two copies of one %[1]s entry (the same key) field by field, by each\n * field's (spec.merge) rule. It is commutative, associative and idempotent; a\n * deletion mark wins.\n */\n", t)
	g.p("public static %[1]s flockMerge%[2]s(%[1]s a, %[1]s b) {\n", jt, t)
	g.p("if (a == null || b == null) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"flock: flockMerge%s needs two entries\");\n}\n", t)
	g.p("if (!a.get%[2]s().equals(b.get%[2]s())) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"flock: flockMerge%[1]s merges copies of one entry, not \" + a.get%[2]s() + \" and \" + b.get%[2]s() + \"; two entries merge with flockMergeEntries%[1]s\");\n}\n", t, key)
	g.p("%[1]s.Meta.Builder mo = flockMergeMeta(a.get%[2]s(), b.get%[2]s());\n", F, meta)
	if e.WholeEntry {
		g.p("%[1]s w = a;\nint c = flockHlcCompare(a.get%[2]s(), b.get%[2]s());\nif (c < 0) {\nw = b;\n} else if (c == 0 && flockBytesCompare(flockBody%[3]s(b), flockBody%[3]s(a)) > 0) {\nw = b;\n}\n", jt, epoch, t)
		g.p("%[1]s.Builder out = w.toBuilder();\n", jt)
	} else {
		g.p("%[1]s.Builder out = flockJoin%[2]s(a, b, a.get%[3]s(), b.get%[3]s(), mo, \"\", false);\n", jt, t, meta)
		g.p("out.set%[1]s(a.get%[1]s());\n", key)
		g.p("%[1]s w = flockHlcMax(a.get%[2]s(), b.get%[2]s());\n%[3]s", H, epoch, setNullable("out", epoch, "w"))
	}
	g.p("%[1]s cr = (a.has%[2]s() ? a.get%[2]s() : null);\nif (flockHlcZero(a.get%[2]s())) {\ncr = (b.has%[2]s() ? b.get%[2]s() : null);\n} else if (!flockHlcZero(b.get%[2]s()) && flockHlcCompare(a.get%[2]s(), b.get%[2]s()) != 0) {\nflockAddConflict(mo, %[3]q);\nif (flockHlcCompare(b.get%[2]s(), a.get%[2]s()) < 0) {\ncr = (b.has%[2]s() ? b.get%[2]s() : null);\n}\n}\n%[4]s", H, created, e.Created, setNullable("out", created, "cr"))
	g.p("%[1]s.DeletionMark mk = flockMergeMark((a.has%[2]s() ? a.get%[2]s() : null), (b.has%[2]s() ? b.get%[2]s() : null));\n%[3]s", F, mark, setNullable("out", mark, "mk"))
	g.p("out.set%s(mo);\nreturn out.build();\n}\n\n", meta)

	if e.WholeEntry {
		g.p("static byte[] flockBody%[1]s(%[2]s e) {\nreturn flockMarshal(e.toBuilder().clear%[3]s().clear%[4]s().build());\n}\n\n", t, jt, mark, meta)
		g.p("/** Stamps a write of the whole entry at at: the newer write replaces the entry on every copy. */\n")
		g.p("public static void flockWrite%[1]s(%[2]s.Builder e, %[3]s at) {\nflockBump%[1]s(e, at);\n}\n\n", t, jt, H)
	} else {
		g.mergeEntries(e)
		g.joinFn(t, jt, e.Fields)
		g.writers(e, e.Fields, "e", "")
		g.unique(e)
	}
	g.marks(e)
	g.digest(e)
}

func (g *javaFlockGen) mergeEntries(e *parser.FlockEntry) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	key, created, mark := javaPascalField(e.Key), javaPascalField(e.Created), javaPascalField(e.Mark)
	meta := javaPascalField(parser.FlockMetaFieldName)
	H, F := g.hlc(), g.flock
	g.p("/**\n * Merges two different %[1]s entries that are one: the older (by creation\n * stamp, then key) survives and absorbs the other in a full join, removals\n * included; the other is returned marked MERGED into it. Called in either\n * order, on any copy, it picks the same survivor. at is the clock time of the\n * merge, kept in the mark. Re-apply it whenever new state for the loser\n * arrives: it is idempotent, and neither entry's store epoch moves.\n */\n", t)
	g.p("public static FlockEntries<%[1]s> flockMergeEntries%[2]s(%[1]s a, %[1]s b, %[3]s at) {\n", jt, t, H)
	g.p("if (a == null || b == null) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"flock: flockMergeEntries%s needs two entries\");\n}\n", t)
	g.p("if (a.get%[2]s().equals(b.get%[2]s())) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"flock: flockMergeEntries%[1]s merges two entries; two copies of \" + a.get%[2]s() + \" merge with flockMerge%[1]s\");\n}\n", t, key)
	g.p("if (flockHlcZero(a.get%[1]s()) || flockHlcZero(b.get%[1]s())) {\nthrow new ProtoException(ProtoException.FAILED_PRECONDITION, \"flock: flockMergeEntries%[2]s: an entry has no creation stamp\");\n}\n", created, t)
	g.p("%[1]s s = a;\n%[1]s l = b;\nint c = flockHlcCompare(b.get%[2]s(), a.get%[2]s());\nif (c < 0 || c == 0 && flockStrCmp(b.get%[3]s(), a.get%[3]s()) < 0) {\ns = b;\nl = a;\n}\n", jt, created, key)
	g.p("%[1]s.Meta.Builder mo = flockMergeMeta(s.get%[2]s(), l.get%[2]s());\n", F, meta)
	g.p("%[1]s.Builder sv = flockJoin%[2]s(s, l, s.get%[3]s(), l.get%[3]s(), mo, \"\", true);\n", jt, t, meta)
	g.p("sv.set%[1]s(s.get%[1]s());\n", key)
	g.p("%s", copyField(model.Field{Name: e.Created, IsMessage: true}, "s", "sv"))
	g.p("%[1]s w = flockHlcMax(s.get%[2]s(), l.get%[2]s());\n%[3]s", H, javaPascalField(e.Epoch), setNullable("sv", javaPascalField(e.Epoch), "w"))
	g.p("%s", copyField(model.Field{Name: e.Mark, IsMessage: true}, "s", "sv"))
	g.p("sv.set%s(mo);\n", meta)
	g.p("%[1]s.DeletionMark.Builder nm = %[1]s.DeletionMark.newBuilder().setReason(%[1]s.DeleteReason.DELETE_REASON_MERGED).setMergedInto(s.get%[2]s());\n", F, key)
	g.p("%[1]s mic = flockHlcClone(s.get%[2]s());\nif (mic != null) {\nnm.setMergedIntoCreated(mic);\n}\n%[1]s mat = flockHlcClone(at);\nif (mat != null) {\nnm.setAt(mat);\n}\n", H, created)
	g.p("%[1]s.DeletionMark lm = flockMergeMark((l.has%[2]s() ? l.get%[2]s() : null), nm.build());\n%[3]s.Builder lb = l.toBuilder();\n%[4]s", F, mark, jt, setNullable("lb", mark, "lm"))
	g.p("return new FlockEntries<>(sv.build(), lb.build());\n}\n\n")
}

// joinFn emits flockJoin<name>, the field-by-field join of two values of a
// message (an entry, or a nested message reached by a FlockRuleNested field),
// as a new Builder (null when both are absent). entries: merging two entries
// (merge_entries): an IMMUTABLE field keeps a's.
func (g *javaFlockGen) joinFn(name, jt string, fields []parser.FlockField) {
	if g.joins[name] {
		return
	}
	g.joins[name] = true
	F := g.flock
	g.p("static %[1]s.Builder flockJoin%[2]s(%[1]s a, %[1]s b, %[3]s.MetaOrBuilder ma, %[3]s.MetaOrBuilder mb, %[3]s.Meta.Builder mo, String prefix, boolean entries) {\n", jt, name, F)
	g.p("if (a == null && b == null) {\nreturn null;\n}\nif (a == null) {\na = %[1]s.getDefaultInstance();\n}\nif (b == null) {\nb = %[1]s.getDefaultInstance();\n}\n%[1]s.Builder out = %[1]s.newBuilder();\n", jt)
	var nested []parser.FlockField
	for _, f := range fields {
		n := jName(f.Field)
		switch f.Rule {
		case parser.FlockRuleImmutable:
			zero, less, ne := g.scalarOps(f, get(f.Field, "a"), get(f.Field, "b"))
			g.p("{\n%[1]s w = a;\nif (%[2]s) {\nw = b;\n} else if (!entries && !(%[3]s) && %[4]s) {\nflockAddConflict(mo, prefix + %[5]q);\nif (%[6]s) {\nw = b;\n}\n}\n%[7]s}\n",
				jt, zero(get(f.Field, "a")), zero(get(f.Field, "b")), ne, f.Field.Name, less, copyField(f.Field, "w", "out"))
		case parser.FlockRuleMin, parser.FlockRuleMax:
			op := "Min"
			if f.Rule == parser.FlockRuleMax {
				op = "Max"
			}
			g.p("out.set%[1]s(%[2]s(a.get%[1]s(), b.get%[1]s()));\n", n, minMax(op, f, g.scalarType(f.Field)))
		case parser.FlockRuleLastWrite:
			g.p("{\n%[1]s w = a;\nint c = flockHlcCompare(flockStamp(ma, prefix + %[2]q), flockStamp(mb, prefix + %[2]q));\nif (c < 0) {\nw = b;\n} else if (c == 0) {\n%[1]s.Builder ta = %[1]s.newBuilder();\n%[3]s%[1]s.Builder tb = %[1]s.newBuilder();\n%[4]sif (flockBytesCompare(flockMarshal(tb.build()), flockMarshal(ta.build())) > 0) {\nw = b;\n}\n}\n%[5]s}\n",
				jt, f.Field.Name, copyField(f.Field, "a", "ta"), copyField(f.Field, "b", "tb"), copyField(f.Field, "w", "out"))
		case parser.FlockRuleNested:
			jn := g.nestedName(f)
			g.p("{\n%[1]s.Builder nj = flockJoin%[2]s(%[3]s, %[4]s, ma, mb, mo, prefix + %[5]q, entries);\nif (nj != null) {\nout.set%[6]s(nj);\n}\n}\n",
				g.typ(f.Nested.Package, f.Nested.Message.Name), jn, opt(f.Field, "a"), opt(f.Field, "b"), f.Field.Name+".", n)
			nested = append(nested, f)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			g.p("%s", g.assignSet(f, "out", g.setFn(name, f)+"(mo, prefix + "+strconv.Quote(f.Field.Name)+")"))
		}
	}
	g.p("return out;\n}\n\n")
	for _, f := range nested {
		g.joinFn(g.nestedName(f), g.typ(f.Nested.Package, f.Nested.Message.Name), f.Nested.Fields)
	}
}

// assignSet replaces the SET_* field f of builder x with the materialized v.
func (g *javaFlockGen) assignSet(f parser.FlockField, x, v string) string {
	n := jName(f.Field)
	switch {
	case f.Element.Map:
		return fmt.Sprintf("%[1]s.clear%[2]s().putAll%[2]s(%[3]s);\n", x, n, v)
	case f.Element.Scalar && f.Field.IsEnum:
		return fmt.Sprintf("%[1]s.clear%[2]s().addAll%[2]sValue(%[3]s);\n", x, n, v)
	}
	return fmt.Sprintf("%[1]s.clear%[2]s().addAll%[2]s(%[3]s);\n", x, n, v)
}

func (g *javaFlockGen) nestedName(f parser.FlockField) string {
	if f.Nested.Package == g.pkg {
		return f.Nested.Message.Name
	}
	return javaPascalField(f.Nested.Package) + f.Nested.Message.Name
}

// scalarOps returns, for an IMMUTABLE field, its zero test, a "b is less"
// test and an "a differs from b" test, over Java values a and b (an enum by
// number).
func (g *javaFlockGen) scalarOps(f parser.FlockField, a, b string) (zero func(string) string, less, ne string) {
	jt := g.scalarType(f.Field)
	switch {
	case f.HlcValue:
		return func(v string) string { return "flockHlcZero(" + v + ")" }, "flockHlcCompare(" + b + ", " + a + ") < 0", "flockHlcCompare(" + a + ", " + b + ") != 0"
	case f.Kind == parser.FlockKindBytes:
		return func(v string) string { return v + ".isEmpty()" }, "flockBytesCompare(" + b + ".toByteArray(), " + a + ".toByteArray()) < 0", "!" + a + ".equals(" + b + ")"
	case f.Kind == parser.FlockKindBool:
		return func(v string) string { return "!" + v }, "false", a + " != " + b
	case f.Kind == parser.FlockKindString:
		return func(v string) string { return v + ".isEmpty()" }, "flockStrCmp(" + b + ", " + a + ") < 0", "!" + a + ".equals(" + b + ")"
	case f.Kind == parser.FlockKindUnsigned && jt == "int":
		return func(v string) string { return v + " == 0" }, "Integer.compareUnsigned(" + b + ", " + a + ") < 0", a + " != " + b
	case f.Kind == parser.FlockKindUnsigned:
		return func(v string) string { return v + " == 0" }, "Long.compareUnsigned(" + b + ", " + a + ") < 0", a + " != " + b
	}
	return func(v string) string { return v + " == 0" }, b + " < " + a, a + " != " + b
}

// setFn emits (once) the materializer of a SET_* field of owner: its live
// elements, from the op set at a path.
func (g *javaFlockGen) setFn(owner string, f parser.FlockField) string {
	name := "flockSet" + strings.ReplaceAll(owner, ".", "") + "_" + f.Field.Name
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	el := f.Element
	rw := f.SetRemoveWins()
	F := g.flock
	switch {
	case el.Scalar:
		bt := g.boxed(f.Field)
		g.t("static java.util.List<%[2]s> %[1]s(%[3]s.MetaOrBuilder m, String path) {\njava.util.List<%[2]s> out = new java.util.ArrayList<>();\nfor (String id : flockLive(m, path, %[4]v).keySet()) {\nout.add(%[5]s);\n}\nreturn out;\n}\n\n",
			name, bt, F, rw, fromIDText(el.Kind, bt, "flockIDText(id)"))
	case el.Map:
		vt := g.typ(el.Package, el.Message.Name)
		kt := protoTypeToJavaBoxed(f.Field.MapKeyType)
		g.t("static java.util.Map<%[2]s, %[3]s> %[1]s(%[4]s.MetaOrBuilder m, String path) {\njava.util.Map<%[2]s, %[3]s> out = new java.util.LinkedHashMap<>();\nfor (java.util.Map.Entry<String, java.util.List<%[4]s.SetOp>> en : flockLive(m, path, %[5]v).entrySet()) {\n%[3]s v = %[6]s(en.getValue());\nif (v != null) {\nout.put(%[7]s, v);\n}\n}\nreturn out;\n}\n\n",
			name, kt, vt, F, rw, g.elemFn(el), fromIDText(el.KeyKind, kt, "flockIDText(en.getKey())"))
	default:
		vt := g.typ(el.Package, el.Message.Name)
		g.t("static java.util.List<%[2]s> %[1]s(%[3]s.MetaOrBuilder m, String path) {\njava.util.List<%[2]s> out = new java.util.ArrayList<>();\nfor (java.util.List<%[3]s.SetOp> ops : flockLive(m, path, %[4]v).values()) {\n%[2]s v = %[5]s(ops);\nif (v != null) {\nout.add(v);\n}\n}\nreturn out;\n}\n\n",
			name, vt, F, rw, g.elemFn(el))
	}
	return name
}

// elemFn emits (once) the join of an element's live adds: their payloads, in
// dot order; MIN and MAX over all, LAST_WRITE from the latest.
func (g *javaFlockGen) elemFn(el *parser.FlockElement) string {
	vt := g.typ(el.Package, el.Message.Name)
	short := el.Message.Name
	if el.Package != g.pkg {
		short = javaPascalField(el.Package) + short
	}
	name := "flockElem" + short
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	g.t("static %[2]s %[1]s(java.util.List<%[3]s.SetOp> ops) {\n%[2]s.Builder v = null;\nfor (%[3]s.SetOp op : ops) {\n%[2]s e;\ntry {\ne = %[2]s.parseFrom(op.getPayload());\n} catch (com.google.protobuf.InvalidProtocolBufferException ex) {\ncontinue;\n}\nif (v == null) {\nv = e.toBuilder();\ncontinue;\n}\n", name, vt, g.flock)
	for _, f := range el.Fields {
		n := jName(f.Field)
		switch f.Rule {
		case parser.FlockRuleMin:
			g.t("v.set%[1]s(%[2]s(v.get%[1]s(), e.get%[1]s()));\n", n, minMax("Min", f, g.scalarType(f.Field)))
		case parser.FlockRuleMax:
			g.t("v.set%[1]s(%[2]s(v.get%[1]s(), e.get%[1]s()));\n", n, minMax("Max", f, g.scalarType(f.Field)))
		case parser.FlockRuleLastWrite:
			g.t("%s", copyField(f.Field, "e", "v"))
		}
	}
	g.t("}\nreturn v == null ? null : v.build();\n}\n\n")
	return name
}

// elemID is the canonical identity of the element value v of a set.
func (g *javaFlockGen) elemID(f parser.FlockField, v string) string {
	el := f.Element
	if el.Scalar {
		if f.Field.IsEnum {
			return idExpr(el.Kind, "int", "flockEnumNumber("+v+")")
		}
		return idExpr(el.Kind, g.scalarType(f.Field), v)
	}
	var parts []string
	for _, idf := range el.Identity {
		parts = append(parts, idExpr(parser.FlockKind(idf), g.scalarType(idf), get(idf, v)))
	}
	return strings.Join(parts, " + ")
}

// writers emits the writers of fields (an entry's, or a nested message's at
// path prefix pre, reached from the entry builder e through the Java builder
// expression target).
func (g *javaFlockGen) writers(e *parser.FlockEntry, fields []parser.FlockField, target, pre string) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	meta := javaPascalField(parser.FlockMetaFieldName)
	H, F := g.hlc(), g.flock
	bump := fmt.Sprintf("flockBump%s(e, at);\n", t)
	for _, f := range fields {
		n := jName(f.Field)
		path := pre + f.Field.Name
		fn := t + pathName(path)
		ensure := ""
		if target != "e" {
			ensure = target + ";\n"
		}
		switch f.Rule {
		case parser.FlockRuleNested:
			g.writers(e, f.Nested.Fields, target+".get"+n+"Builder()", path+".")
		case parser.FlockRuleImmutable:
			vt := g.scalarType(f.Field)
			val, set := "v", fmt.Sprintf("%s.set%s(v);\n", target, n)
			switch {
			case f.Field.IsEnum:
				val, set = "vn", fmt.Sprintf("%s.set%sValue(vn);\n", target, n)
				ensure += "int vn = flockEnumNumber(v);\n"
			case f.HlcValue:
				set = setNullable(target, n, "v")
			}
			zero, _, ne := g.scalarOps(f, get(f.Field, target), val)
			g.p("/** Sets the IMMUTABLE %[1]s once; setting it again to another value is refused. */\n", path)
			g.p("public static void flockSet%[1]s(%[2]s.Builder e, %[3]s v, %[4]s at) {\n%[5]sif (!(%[6]s) && %[7]s) {\nthrow new ProtoException(ProtoException.FAILED_PRECONDITION, \"flock: %[8]s is immutable\");\n}\n%[9]s%[10]s}\n\n",
				fn, jt, vt, H, ensure, zero(get(f.Field, target)), ne, path, set, bump)
		case parser.FlockRuleMin, parser.FlockRuleMax:
			op := "Min"
			if f.Rule == parser.FlockRuleMax {
				op = "Max"
			}
			vt := g.scalarType(f.Field)
			g.p("/** Merges v into %[1]s by its rule. */\n", path)
			g.p("public static void flockSet%[1]s(%[2]s.Builder e, %[3]s v, %[4]s at) {\n%[5]s%[6]s.set%[7]s(%[8]s(%[6]s.get%[7]s(), v));\n%[9]s}\n\n",
				fn, jt, vt, H, ensure, target, n, minMax(op, f, vt), bump)
		case parser.FlockRuleLastWrite:
			vt := g.scalarType(f.Field)
			var set string
			switch {
			case f.Field.IsRepeated:
				vt = "java.util.List<" + g.boxedParam(f.Field) + ">"
				set = fmt.Sprintf("%[1]s.clear%[2]s().addAll%[2]s(v);\n", target, n)
			case f.Field.IsMessage:
				set = setNullable(target, n, "v")
			case f.Field.IsEnum:
				set = fmt.Sprintf("%s.set%sValue(flockEnumNumber(v));\n", target, n)
			default:
				set = fmt.Sprintf("%s.set%s(v);\n", target, n)
			}
			g.p("/** Writes %[1]s at at: the newest write wins. */\n", path)
			g.p("public static void flockSet%[1]s(%[2]s.Builder e, %[3]s v, %[4]s at) {\nif (flockHlcCompare(at, flockStamp(e.get%[5]s(), %[6]q)) <= 0) {\nreturn;\n}\n%[7]s%[8]s%[9]s.Meta.Builder m = flockMetaClone(e.get%[5]s());\nflockSetStamp(m, %[6]q, at);\ne.set%[5]s(m);\n%[10]s}\n\n",
				fn, jt, vt, H, meta, path, ensure, set, F, bump)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			el := f.Element
			owner := g.ownerName(e, path)
			mat := g.setFn(owner, f)
			remat := fmt.Sprintf("%[1]s.Meta mm = m.build();\ne.set%[2]s(mm);\n%[3]s%[4]s%[5]s", F, meta, ensure, g.assignSet(f, target, mat+"(mm, "+strconv.Quote(path)+")"), bump)
			switch {
			case el.Map:
				kt := protoTypeToJavaType(f.Field.MapKeyType)
				vt := g.typ(el.Package, el.Message.Name)
				g.p("/** Adds or updates %[1]s[k] at at. */\n", path)
				g.p("public static void flockPut%[1]s(%[2]s.Builder e, %[3]s k, %[4]s v, %[5]s at) {\n%[6]s.Meta.Builder m = flockMetaClone(e.get%[7]s());\nflockAddOp(m, %[8]q, %[9]s, com.google.protobuf.ByteString.copyFrom(flockMarshal(v)), at);\n%[10]s}\n\n",
					fn, jt, kt, vt, H, F, meta, path, idExpr(el.KeyKind, kt, "k"), remat)
				g.p("/** Removes %[1]s[k] at at. */\n", path)
				g.p("public static void flockRemove%[1]s(%[2]s.Builder e, %[3]s k, %[4]s at) {\n%[5]s.Meta.Builder m = flockMetaClone(e.get%[6]s());\nflockRemoveOp(m, %[7]q, %[8]s, at);\n%[9]s}\n\n",
					fn, jt, kt, H, F, meta, path, idExpr(el.KeyKind, kt, "k"), remat)
			default:
				vt := g.scalarType(f.Field)
				payload := "com.google.protobuf.ByteString.copyFrom(flockMarshal(v))"
				if el.Scalar {
					payload = "com.google.protobuf.ByteString.EMPTY"
				}
				g.p("/** Adds (or updates) an element of %[1]s at at. */\n", path)
				g.p("public static void flockAdd%[1]s(%[2]s.Builder e, %[3]s v, %[4]s at) {\n%[5]s.Meta.Builder m = flockMetaClone(e.get%[6]s());\nflockAddOp(m, %[7]q, %[8]s, %[9]s, at);\n%[10]s}\n\n",
					fn, jt, vt, H, F, meta, path, g.elemID(f, "v"), payload, remat)
				g.p("/** Removes an element (by identity) of %[1]s at at. */\n", path)
				g.p("public static void flockRemove%[1]s(%[2]s.Builder e, %[3]s v, %[4]s at) {\n%[5]s.Meta.Builder m = flockMetaClone(e.get%[6]s());\nflockRemoveOp(m, %[7]q, %[8]s, at);\n%[9]s}\n\n",
					fn, jt, vt, H, F, meta, path, g.elemID(f, "v"), remat)
			}
		}
	}
}

// boxedParam is the list element type a LAST_WRITE repeated field's writer
// takes (an enum as its Java enum).
func (g *javaFlockGen) boxedParam(f model.Field) string {
	if f.IsEnum {
		return g.typ(f.TypePackage, f.Type)
	}
	return g.boxed(f)
}

// ownerName names the message that holds the field at path (the entry, or the
// nested message on the way), as the materializer names use it.
func (g *javaFlockGen) ownerName(e *parser.FlockEntry, path string) string {
	segs := strings.Split(path, ".")
	owner := e.Message.Name
	fields := e.Fields
	for _, seg := range segs[:len(segs)-1] {
		for _, f := range fields {
			if f.Field.Name == seg && f.Nested != nil {
				owner = g.nestedName(f)
				fields = f.Nested.Fields
			}
		}
	}
	return owner
}

func (g *javaFlockGen) unique(e *parser.FlockEntry) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	key, created, mark := javaPascalField(e.Key), javaPascalField(e.Created), javaPascalField(e.Mark)
	meta := javaPascalField(parser.FlockMetaFieldName)
	g.p("/** Lists the (spec.unique) elements of a live %[1]s entry, each as \"&lt;path&gt;\\0&lt;element&gt;\": the keys of the unique index. */\n", t)
	g.p("public static java.util.List<String> flockUniqueKeys%[1]s(%[2]s e) {\njava.util.List<String> out = new java.util.ArrayList<>();\nif (flockMarked(e.get%[3]s())) {\nreturn out;\n}\n", t, jt, mark)
	for _, f := range e.UniqueFields() {
		g.p("for (String id : flockLive(e.get%[1]s(), %[2]q, %[3]v).keySet()) {\nout.add(%[2]q + \"\\0\" + id);\n}\n", meta, f.Path, f.SetRemoveWins())
	}
	g.p("return out;\n}\n\n")
	g.p("/**\n * Refuses a local write of e that would share a (spec.unique) element with\n * another live entry; owner returns the key of the live entry that holds a\n * unique key, if any.\n */\n")
	g.p("public static void flockCheckUnique%[1]s(%[2]s e, java.util.function.Function<String, java.util.Optional<String>> owner) {\nfor (String k : flockUniqueKeys%[1]s(e)) {\njava.util.Optional<String> other = owner.apply(k);\nif (other.isPresent() && !other.get().equals(e.get%[3]s())) {\nthrow new ProtoException(ProtoException.ALREADY_EXISTS, \"flock: %[4]s \" + e.get%[3]s() + \" shares \" + k.replace('\\0', ' ') + \" with \" + other.get());\n}\n}\n}\n\n", t, jt, key, e.Package+"."+t)
	g.p("/**\n * Decides what to do with two live %[1]s entries that gossip brought together\n * sharing the unique key k: merge them (flockMergeEntries%[1]s) only in the\n * partition race, both created with the element within windowMs of each\n * other; quarantine anything else.\n */\n", t)
	g.p("public static FlockOverlap flockOverlap%[1]s(%[2]s a, %[2]s b, String k, long windowMs) {\nint i = k.indexOf('\\0');\nif (i < 0) {\nreturn FlockOverlap.QUARANTINE;\n}\nString path = k.substring(0, i);\nString element = k.substring(i + 1);\n", t, jt)
	g.p("if (!flockBornWith(a.get%[1]s(), path, element, a.get%[2]s()) || !flockBornWith(b.get%[1]s(), path, element, b.get%[2]s())) {\nreturn FlockOverlap.QUARANTINE;\n}\n", meta, created)
	g.p("long d = a.get%[1]s().getWallMs() - b.get%[1]s().getWallMs();\nif (d < 0) {\nd = -d;\n}\nif (d > windowMs) {\nreturn FlockOverlap.QUARANTINE;\n}\nreturn FlockOverlap.AUTO_MERGE;\n}\n\n", created)
}

func (g *javaFlockGen) marks(e *parser.FlockEntry) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	key, mark := javaPascalField(e.Key), javaPascalField(e.Mark)
	what := e.Package + "." + t
	H, F := g.hlc(), g.flock
	g.p("/** Refuses a marked %[1]s entry at once (NOT_FOUND): a MERGED one names its survivor, a USER or EXPIRED one is gone. */\n", t)
	g.p("public static void flockLive%[1]s(%[2]s e) {\nif (flockMarked(e.get%[3]s())) {\nthrow flockMarkError(%[4]q, e.get%[5]s(), e.get%[3]s());\n}\n}\n\n", t, jt, mark, what, key)
	g.p("/** Marks a %[1]s entry deleted at at, for a person (USER) or by age (EXPIRED). A MERGED mark is made only by flockMergeEntries%[1]s. */\n", t)
	g.p("public static void flockMark%[1]s(%[2]s.Builder e, %[3]s.DeleteReason reason, %[4]s at) {\nif (reason != %[3]s.DeleteReason.DELETE_REASON_USER && reason != %[3]s.DeleteReason.DELETE_REASON_EXPIRED) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"flock: flockMark%[1]s marks USER or EXPIRED, not \" + reason);\n}\n", t, jt, F, H)
	g.p("%[1]s.DeletionMark.Builder nm = %[1]s.DeletionMark.newBuilder().setReason(reason);\n%[2]s a = flockHlcClone(at);\nif (a != null) {\nnm.setAt(a);\n}\n%[1]s.DeletionMark m = flockMergeMark((e.has%[3]s() ? e.get%[3]s() : null), nm.build());\n%[4]sflockBump%[5]s(e, at);\n}\n\n", F, H, mark, setNullable("e", mark, "m"), t)
	g.p("/** Records that copy holds the entry's mark. */\n")
	g.p("public static void flockAck%[1]s(%[2]s.Builder e, String copy, %[3]s at) {\n%[4]s.DeletionMark m = flockAck((e.has%[5]s() ? e.get%[5]s() : null), copy, at);\n%[6]s}\n\n", t, jt, H, F, mark, setNullable("e", mark, "m"))
	g.p("/** Reports whether a marked entry may be purged: every copy in members has acknowledged its mark, FLOCK_MAX_MESSAGE_AGE_MS ago. */\n")
	g.p("public static boolean flockPurgeReady%[1]s(%[2]s e, java.util.Collection<String> members, long nowMs) {\nreturn flockPurgeReady((e.has%[3]s() ? e.get%[3]s() : null), members, nowMs);\n}\n\n", t, jt, mark)
	g.p("/** Returns the survivor a MERGED entry redirects to. */\n")
	g.p("public static java.util.Optional<String> flockRedirect%[1]s(%[2]s e) {\nif (e.get%[3]s().getReasonValue() != %[4]s.DeleteReason.DELETE_REASON_MERGED_VALUE) {\nreturn java.util.Optional.empty();\n}\nreturn java.util.Optional.of(e.get%[3]s().getMergedInto());\n}\n\n", t, jt, mark, F)
	g.p("/** Reports whether a MERGED entry's redirect stub has outlived lifetimeMs (the longest credential lifetime) and may go. */\n")
	g.p("public static boolean flockStubExpired%[1]s(%[2]s e, long nowMs, long lifetimeMs) {\nreturn e.get%[3]s().getReasonValue() == %[4]s.DeleteReason.DELETE_REASON_MERGED_VALUE && nowMs - e.get%[3]s().getAt().getWallMs() >= lifetimeMs;\n}\n\n", t, jt, mark, F)
}

func (g *javaFlockGen) digest(e *parser.FlockEntry) {
	t := e.Message.Name
	jt := g.typ(e.Package, t)
	g.p("/**\n * The SHA-256 of the entry's canonical encoding (its set fields, which the\n * op sets determine, left out): equal on two copies exactly when they hold\n * the same entry, in every language.\n */\n")
	g.p("public static byte[] flockDigest%[1]s(%[2]s e) {\n%[2]s.Builder c = e.toBuilder();\n", t, jt)
	if !e.WholeEntry {
		var clear func(fields []parser.FlockField, expr string)
		clear = func(fields []parser.FlockField, expr string) {
			for _, f := range fields {
				n := jName(f.Field)
				switch {
				case f.IsSet():
					g.p("%s.clear%s();\n", expr, n)
				case f.Nested != nil:
					g.p("if (%s.has%s()) {\n", expr, n)
					clear(f.Nested.Fields, expr+".get"+n+"Builder()")
					g.p("}\n")
				}
			}
		}
		clear(e.Fields, "c")
	}
	g.p("return flockDigest(c.build());\n}\n\n")
}

// javaFlockIndex plans every flock root of allFiles (generateModule's local
// parser variable shadows the package there).
func javaFlockIndex(allFiles []model.ProtoFile) *parser.FlockIndex {
	return parser.NewFlockIndex(allFiles)
}
