// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// This file emits flock state (tacplus#206) for Go: for every flock entry of a
// package, from the plan in parser/flock.go, the two-argument merge
// (FlockMerge<T>), the merge of two duplicate entries (FlockMergeEntries<T>),
// the writers that keep the merge metadata (FlockSet/Add/Remove/Put<T><Path>),
// the deletion mark helpers, the unique check and the canonical digest. The
// schema-independent half is goFlockRuntime (flock_runtime.go). Rust and Java
// emit the same functions from the same plan.

// generateGoFlock writes flock_helpers.go into the package directory of pf when pf
// holds a flock entry.
func generateGoFlock(pf model.ProtoFile, baseDir, subdir string, config *tactype.Config, ix *parser.FlockIndex) error {
	entries := ix.EntriesIn(pf.Package)
	if len(entries) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)
	b.WriteString("import (\n\"bytes\"\n\"crypto/rand\"\n\"crypto/sha256\"\n\"encoding/hex\"\n\"sort\"\n\"strconv\"\n\"strings\"\n\"time\"\n")
	b.WriteString("\"google.golang.org/protobuf/proto\"\n")
	fmt.Fprintf(&b, "flock \"%s/types/sys/flock\"\n", config.TypesModulePath())
	fmt.Fprintf(&b, "protoerrors \"%s\"\n", config.ErrorsPath())
	b.WriteString(")\n")
	fmt.Fprintf(&b, goFlockRuntime, parser.FlockMaxSkewMs, parser.FlockRebuildAfterMs, parser.FlockMaxMessageAgeMs)
	b.WriteString("\nvar _ = bytes.Compare\n\n")
	b.WriteString(`func flockIDText(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return ""
}

`)
	g := &goFlockGen{b: &b, pkg: pf.Package, joins: map[string]bool{}, sets: map[string]bool{}}
	for _, e := range entries {
		g.entry(e)
	}
	b.WriteString(g.tail.String())
	outputPath := filepath.Join(baseDir, "golang", subdir, "flock_helpers.go")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	return writeAndFormat(outputPath, b.String())
}

type goFlockGen struct {
	b *strings.Builder
	// tail holds the helpers (set materializers, element joins) emitted while
	// a function body is being written; it is appended at the end.
	tail  strings.Builder
	pkg   string
	joins map[string]bool
	sets  map[string]bool
}

func (g *goFlockGen) p(format string, args ...any) { fmt.Fprintf(g.b, format, args...) }

func (g *goFlockGen) t(format string, args ...any) { fmt.Fprintf(&g.tail, format, args...) }

// qual is a type name as this package refers to it.
func (g *goFlockGen) qual(pkg, name string) string {
	if pkg == "" || pkg == g.pkg {
		return name
	}
	return pkg + "." + name
}

// goType is the Go type of one value of f (its element, when repeated).
func (g *goFlockGen) goType(f model.Field) string {
	switch {
	case f.IsMessage:
		return "*" + g.qual(f.TypePackage, f.Type)
	case f.IsEnum:
		return g.qual(f.TypePackage, f.Type)
	}
	return protoScalarGoType(f.Type)
}

// idExpr is the canonical identity encoding of the scalar v of kind k.
func idExpr(k parser.FlockScalarKind, v string) string {
	switch k {
	case parser.FlockKindString:
		return "flockIDString(" + v + ")"
	case parser.FlockKindBytes:
		return "flockIDBytes(" + v + ")"
	case parser.FlockKindBool:
		return "flockIDBool(" + v + ")"
	case parser.FlockKindUnsigned:
		return "flockIDUint(uint64(" + v + "))"
	}
	return "flockIDInt(int64(" + v + "))"
}

// fromIDText decodes the identity text t back to a value of Go type typ.
func fromIDText(k parser.FlockScalarKind, typ, t string) string {
	switch k {
	case parser.FlockKindString:
		return t
	case parser.FlockKindBytes:
		return "func() []byte { v, _ := hex.DecodeString(" + t + "); return v }()"
	case parser.FlockKindBool:
		return t + ` == "true"`
	case parser.FlockKindUnsigned:
		return typ + "(func() uint64 { v, _ := strconv.ParseUint(" + t + ", 10, 64); return v }())"
	}
	return typ + "(func() int64 { v, _ := strconv.ParseInt(" + t + ", 10, 64); return v }())"
}

func pathName(path string) string {
	var out string
	for _, seg := range strings.Split(path, ".") {
		out += toGoFieldName(seg)
	}
	return out
}

func (g *goFlockGen) entry(e *parser.FlockEntry) {
	t := e.Message.Name
	what := e.Package + "." + t
	key, created, epoch, mark := toGoFieldName(e.Key), toGoFieldName(e.Created), toGoFieldName(e.Epoch), toGoFieldName(e.Mark)
	meta := toGoFieldName(parser.FlockMetaFieldName)

	g.p("// ---- flock entry %s ----\n\n", what)
	g.p("// FlockNew%[1]s returns a new %[1]s entry: flock creates its key (FlockNewKey)\n// and stamps its creation, which never changes.\n", t)
	g.p("func FlockNew%[1]s(key string, created *flock.Hlc) *%[1]s {\n\treturn &%[1]s{%[2]s: key, %[3]s: flockHlcClone(created), %[4]s: flockHlcClone(created)}\n}\n\n", t, key, created, epoch)

	// Merge.
	g.p("// FlockMerge%[1]s merges two copies of one %[1]s entry (the same key) field by\n// field, by each field's (spec.merge) rule. It is commutative, associative and\n// idempotent; a deletion mark wins.\n", t)
	g.p("func FlockMerge%[1]s(a, b *%[1]s) (*%[1]s, error) {\n", t)
	g.p("\tif a == nil || b == nil {\n\t\treturn nil, protoerrors.NewErrMissingInvalidData(nil, \"flock: FlockMerge%s needs two entries\")\n\t}\n", t)
	g.p("\tif a.Get%[2]s() != b.Get%[2]s() {\n\t\treturn nil, protoerrors.NewErrMissingInvalidData(nil, \"flock: FlockMerge%[1]s merges copies of one entry, not %%s and %%s; two entries merge with FlockMergeEntries%[1]s\", a.Get%[2]s(), b.Get%[2]s())\n\t}\n", t, key)
	g.p("\ta, b = proto.Clone(a).(*%[1]s), proto.Clone(b).(*%[1]s)\n", t)
	g.p("\tmo := flockMergeMeta(a.Get%[1]s(), b.Get%[1]s())\n", meta)
	if e.WholeEntry {
		g.p("\tw := a\n\tswitch c := FlockHlcCompare(a.Get%[1]s(), b.Get%[1]s()); {\n\tcase c < 0:\n\t\tw = b\n\tcase c == 0 && bytes.Compare(flockBody%[2]s(b), flockBody%[2]s(a)) > 0:\n\t\tw = b\n\t}\n", epoch, t)
		g.p("\tout := proto.Clone(w).(*%s)\n", t)
	} else {
		g.p("\tout := flockJoin%[1]s(a, b, a.Get%[2]s(), b.Get%[2]s(), mo, \"\", false)\n", t, meta)
		g.p("\tout.%[1]s = a.Get%[1]s()\n", key)
		g.p("\tout.%[1]s = flockHlcMax(a.Get%[1]s(), b.Get%[1]s())\n", epoch)
	}
	g.p("\tout.%[1]s = a.Get%[1]s()\n\tswitch {\n\tcase flockHlcZero(a.Get%[1]s()):\n\t\tout.%[1]s = b.Get%[1]s()\n\tcase !flockHlcZero(b.Get%[1]s()) && FlockHlcCompare(a.Get%[1]s(), b.Get%[1]s()) != 0:\n\t\tflockAddConflict(mo, %[2]q)\n\t\tif FlockHlcCompare(b.Get%[1]s(), a.Get%[1]s()) < 0 {\n\t\t\tout.%[1]s = b.Get%[1]s()\n\t\t}\n\t}\n", created, e.Created)
	g.p("\tout.%[1]s = flockMergeMark(a.Get%[1]s(), b.Get%[1]s())\n", mark)
	g.p("\tout.%s = mo\n\treturn out, nil\n}\n\n", meta)

	if e.WholeEntry {
		g.p("func flockBody%[1]s(e *%[1]s) []byte {\n\tc := proto.Clone(e).(*%[1]s)\n\tc.%[2]s = nil\n\tc.%[3]s = nil\n\treturn flockMarshal(c)\n}\n\n", t, mark, meta)
		g.p("// FlockWrite%[1]s stamps a write of the whole entry at at: the newer write\n// replaces the entry on every copy.\n", t)
		g.p("func FlockWrite%[1]s(e *%[1]s, at *flock.Hlc) {\n\te.%[2]s = flockHlcMax(e.Get%[2]s(), at)\n}\n\n", t, epoch)
	} else {
		g.mergeEntries(e)
		g.joinFn(t, e.Message.Name, e.Fields)
		g.writers(e, e.Fields, "e", "")
		g.unique(e)
	}
	g.marks(e)
	g.digest(e)
}

func (g *goFlockGen) mergeEntries(e *parser.FlockEntry) {
	t := e.Message.Name
	key, created, epoch, mark := toGoFieldName(e.Key), toGoFieldName(e.Created), toGoFieldName(e.Epoch), toGoFieldName(e.Mark)
	meta := toGoFieldName(parser.FlockMetaFieldName)
	g.p("// FlockMergeEntries%[1]s merges two different %[1]s entries that are one: the\n// older (by creation stamp, then key) survives and absorbs the other in a full\n// join, removals included; the other is returned marked MERGED into it. Called\n// in either order, on any copy, it picks the same survivor. at is the clock\n// time of the merge, kept in the mark. Re-apply it whenever new state for the\n// loser arrives: it is idempotent, and neither entry's store epoch moves.\n", t)
	g.p("func FlockMergeEntries%[1]s(a, b *%[1]s, at *flock.Hlc) (survivor, loser *%[1]s, err error) {\n", t)
	g.p("\tif a == nil || b == nil {\n\t\treturn nil, nil, protoerrors.NewErrMissingInvalidData(nil, \"flock: FlockMergeEntries%s needs two entries\")\n\t}\n", t)
	g.p("\tif a.Get%[2]s() == b.Get%[2]s() {\n\t\treturn nil, nil, protoerrors.NewErrMissingInvalidData(nil, \"flock: FlockMergeEntries%[1]s merges two entries; two copies of %%s merge with FlockMerge%[1]s\", a.Get%[2]s())\n\t}\n", t, key)
	g.p("\tif flockHlcZero(a.Get%[1]s()) || flockHlcZero(b.Get%[1]s()) {\n\t\treturn nil, nil, protoerrors.NewErrFailedPrecondition(\"flock: FlockMergeEntries%[2]s: an entry has no creation stamp\")\n\t}\n", created, t)
	g.p("\ts, l := a, b\n\tif c := FlockHlcCompare(b.Get%[1]s(), a.Get%[1]s()); c < 0 || c == 0 && b.Get%[2]s() < a.Get%[2]s() {\n\t\ts, l = b, a\n\t}\n", created, key)
	g.p("\ts, l = proto.Clone(s).(*%[1]s), proto.Clone(l).(*%[1]s)\n", t)
	g.p("\tmo := flockMergeMeta(s.Get%[1]s(), l.Get%[1]s())\n", meta)
	g.p("\tsurvivor = flockJoin%[1]s(s, l, s.Get%[2]s(), l.Get%[2]s(), mo, \"\", true)\n", t, meta)
	g.p("\tsurvivor.%[1]s, survivor.%[2]s = s.Get%[1]s(), s.Get%[2]s()\n", key, created)
	g.p("\tsurvivor.%[1]s = flockHlcMax(s.Get%[1]s(), l.Get%[1]s())\n", epoch)
	g.p("\tsurvivor.%[1]s = s.Get%[1]s()\n\tsurvivor.%[2]s = mo\n", mark, meta)
	g.p("\tl.%[1]s = flockMergeMark(l.Get%[1]s(), &flock.DeletionMark{Reason: flock.DeleteReason_DELETE_REASON_MERGED, MergedInto: s.Get%[2]s(), MergedIntoCreated: flockHlcClone(s.Get%[3]s()), At: flockHlcClone(at)})\n", mark, key, created)
	g.p("\treturn survivor, l, nil\n}\n\n")
}

// joinFn emits flockJoin<name>, the field-by-field join of two values of a
// message (an entry, or a nested message reached by a FlockRuleNested field).
// entries: merging two entries (merge_entries): an IMMUTABLE field keeps a's.
func (g *goFlockGen) joinFn(name, typ string, fields []parser.FlockField) {
	if g.joins[name] {
		return
	}
	g.joins[name] = true
	g.p("func flockJoin%[1]s(a, b *%[2]s, ma, mb, mo *flock.Meta, prefix string, entries bool) *%[2]s {\n", name, typ)
	g.p("\t_ = entries\n\tif a == nil && b == nil {\n\t\treturn nil\n\t}\n\tif a == nil {\n\t\ta = &%[1]s{}\n\t}\n\tif b == nil {\n\t\tb = &%[1]s{}\n\t}\n\tout := &%[1]s{}\n", typ)
	var nested []parser.FlockField
	for _, f := range fields {
		gf := toGoFieldName(f.Field.Name)
		switch f.Rule {
		case parser.FlockRuleImmutable:
			zero, less, ne := g.scalarOps(f, "a.Get"+gf+"()", "b.Get"+gf+"()")
			g.p("\tout.%[1]s = a.Get%[1]s()\n\tswitch {\n\tcase %[2]s:\n\t\tout.%[1]s = b.Get%[1]s()\n\tcase !entries && !(%[3]s) && %[4]s:\n\t\tflockAddConflict(mo, prefix+%[5]q)\n\t\tif %[6]s {\n\t\t\tout.%[1]s = b.Get%[1]s()\n\t\t}\n\t}\n",
				gf, zero("a.Get"+gf+"()"), zero("b.Get"+gf+"()"), ne, f.Field.Name, less)
		case parser.FlockRuleMin:
			g.p("\tout.%[1]s = flockMinNonZero(a.Get%[1]s(), b.Get%[1]s())\n", gf)
		case parser.FlockRuleMax:
			g.p("\tout.%[1]s = flockMaxNonZero(a.Get%[1]s(), b.Get%[1]s())\n", gf)
		case parser.FlockRuleLastWrite:
			g.p("\tswitch c := FlockHlcCompare(flockStamp(ma, prefix+%[2]q), flockStamp(mb, prefix+%[2]q)); {\n\tcase c > 0:\n\t\tout.%[1]s = a.%[1]s\n\tcase c < 0:\n\t\tout.%[1]s = b.%[1]s\n\tdefault:\n\t\tout.%[1]s = a.%[1]s\n\t\tif bytes.Compare(flockMarshal(&%[3]s{%[1]s: b.%[1]s}), flockMarshal(&%[3]s{%[1]s: a.%[1]s})) > 0 {\n\t\t\tout.%[1]s = b.%[1]s\n\t\t}\n\t}\n",
				gf, f.Field.Name, typ)
		case parser.FlockRuleNested:
			jn := g.nestedName(f)
			g.p("\tout.%[1]s = flockJoin%[2]s(a.%[1]s, b.%[1]s, ma, mb, mo, prefix+%[3]q, entries)\n", gf, jn, f.Field.Name+".")
			nested = append(nested, f)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			g.p("\tout.%[1]s = %[2]s(mo, prefix+%[3]q)\n", gf, g.setFn(typ, f), f.Field.Name)
		}
	}
	g.p("\treturn out\n}\n\n")
	for _, f := range nested {
		g.joinFn(g.nestedName(f), g.qual(f.Nested.Package, f.Nested.Message.Name), f.Nested.Fields)
	}
}

func (g *goFlockGen) nestedName(f parser.FlockField) string {
	if f.Nested.Package == g.pkg {
		return f.Nested.Message.Name
	}
	return toGoFieldName(f.Nested.Package) + f.Nested.Message.Name
}

// scalarOps returns, for an IMMUTABLE field, its zero test, a "b is less"
// test and an "a differs from b" test.
func (g *goFlockGen) scalarOps(f parser.FlockField, a, b string) (zero func(string) string, less, ne string) {
	switch {
	case f.HlcValue:
		return func(v string) string { return "flockHlcZero(" + v + ")" }, "FlockHlcCompare(" + b + ", " + a + ") < 0", "FlockHlcCompare(" + a + ", " + b + ") != 0"
	case f.Kind == parser.FlockKindBytes:
		return func(v string) string { return "len(" + v + ") == 0" }, "bytes.Compare(" + b + ", " + a + ") < 0", "!bytes.Equal(" + a + ", " + b + ")"
	case f.Kind == parser.FlockKindBool:
		return func(v string) string { return "!" + v }, "false", a + " != " + b
	case f.Kind == parser.FlockKindString:
		return func(v string) string { return v + ` == ""` }, b + " < " + a, a + " != " + b
	}
	return func(v string) string { return v + " == 0" }, b + " < " + a, a + " != " + b
}

// setFn emits (once) the materializer of a SET_* field of owner: its live
// elements, from the op set at a path.
func (g *goFlockGen) setFn(owner string, f parser.FlockField) string {
	name := "flockSet" + strings.ReplaceAll(owner, ".", "") + "_" + f.Field.Name
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	el := f.Element
	rw := f.SetRemoveWins()
	switch {
	case el.Scalar:
		typ := g.goType(f.Field)
		g.t("func %[1]s(m *flock.Meta, path string) []%[2]s {\n\t_, ids := flockLive(m, path, %[3]v)\n\tvar out []%[2]s\n\tfor _, id := range ids {\n\t\tout = append(out, %[4]s)\n\t}\n\treturn out\n}\n\n",
			name, typ, rw, fromIDText(el.Kind, typ, "flockIDText(id)"))
	case el.Map:
		vt := g.qual(el.Package, el.Message.Name)
		kt := protoScalarGoType(f.Field.MapKeyType)
		g.t("func %[1]s(m *flock.Meta, path string) map[%[2]s]*%[3]s {\n\tlive, ids := flockLive(m, path, %[4]v)\n\tif len(ids) == 0 {\n\t\treturn nil\n\t}\n\tout := map[%[2]s]*%[3]s{}\n\tfor _, id := range ids {\n\t\tif v := %[5]s(live[id]); v != nil {\n\t\t\tout[%[6]s] = v\n\t\t}\n\t}\n\treturn out\n}\n\n",
			name, kt, vt, rw, g.elemFn(el), fromIDText(el.KeyKind, kt, "flockIDText(id)"))
	default:
		vt := g.qual(el.Package, el.Message.Name)
		g.t("func %[1]s(m *flock.Meta, path string) []*%[2]s {\n\tlive, ids := flockLive(m, path, %[3]v)\n\tvar out []*%[2]s\n\tfor _, id := range ids {\n\t\tif v := %[4]s(live[id]); v != nil {\n\t\t\tout = append(out, v)\n\t\t}\n\t}\n\treturn out\n}\n\n",
			name, vt, rw, g.elemFn(el))
	}
	return name
}

// elemFn emits (once) the join of an element's live adds: their payloads, in
// dot order; MIN and MAX over all, LAST_WRITE from the latest.
func (g *goFlockGen) elemFn(el *parser.FlockElement) string {
	vt := g.qual(el.Package, el.Message.Name)
	name := "flockElem" + strings.ReplaceAll(vt, ".", "")
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	g.t("func %[1]s(ops []*flock.SetOp) *%[2]s {\n\tvar v *%[2]s\n\tfor _, op := range ops {\n\t\te := &%[2]s{}\n\t\tif err := proto.Unmarshal(op.GetPayload(), e); err != nil {\n\t\t\tcontinue\n\t\t}\n\t\tif v == nil {\n\t\t\tv = e\n\t\t\tcontinue\n\t\t}\n", name, vt)
	for _, f := range el.Fields {
		gf := toGoFieldName(f.Field.Name)
		switch f.Rule {
		case parser.FlockRuleMin:
			g.t("\t\tv.%[1]s = flockMinNonZero(v.%[1]s, e.%[1]s)\n", gf)
		case parser.FlockRuleMax:
			g.t("\t\tv.%[1]s = flockMaxNonZero(v.%[1]s, e.%[1]s)\n", gf)
		case parser.FlockRuleLastWrite:
			g.t("\t\tv.%[1]s = e.%[1]s\n", gf)
		}
	}
	g.t("\t}\n\treturn v\n}\n\n")
	return name
}

// elemID is the canonical identity of the element value v of a set.
func (g *goFlockGen) elemID(el *parser.FlockElement, v string) string {
	if el.Scalar {
		return idExpr(el.Kind, v)
	}
	var parts []string
	for _, f := range el.Identity {
		parts = append(parts, idExpr(parser.FlockKind(f), v+".Get"+toGoFieldName(f.Name)+"()"))
	}
	return strings.Join(parts, " + ")
}

// writers emits the writers of fields (an entry's, or a nested message's at
// path prefix pre, reached from the entry e through the Go expression at).
func (g *goFlockGen) writers(e *parser.FlockEntry, fields []parser.FlockField, at, pre string) {
	t := e.Message.Name
	epoch := toGoFieldName(e.Epoch)
	meta := toGoFieldName(parser.FlockMetaFieldName)
	bump := fmt.Sprintf("\te.%[1]s = flockHlcMax(e.Get%[1]s(), at)\n", epoch)
	for _, f := range fields {
		gf := toGoFieldName(f.Field.Name)
		path := pre + f.Field.Name
		fn := t + pathName(path)
		target := at + "." + gf
		ensure := g.ensure(e, path)
		switch f.Rule {
		case parser.FlockRuleNested:
			g.writers(e, f.Nested.Fields, target, path+".")
		case parser.FlockRuleImmutable:
			typ := g.goType(f.Field)
			zero, _, ne := g.scalarOps(f, target, "v")
			g.p("// FlockSet%[1]s sets the IMMUTABLE %[2]s once; setting it again to another\n// value is refused.\n", fn, path)
			g.p("func FlockSet%[1]s(e *%[2]s, v %[3]s, at *flock.Hlc) error {\n%[4]s\tif !(%[5]s) && %[6]s {\n\t\treturn protoerrors.NewErrFailedPrecondition(\"flock: %[7]s is immutable\")\n\t}\n\t%[8]s = v\n%[9]s\treturn nil\n}\n\n",
				fn, t, typ, ensure, zero(target), ne, path, target, bump)
		case parser.FlockRuleMin, parser.FlockRuleMax:
			op := "flockMinNonZero"
			if f.Rule == parser.FlockRuleMax {
				op = "flockMaxNonZero"
			}
			g.p("// FlockSet%[1]s merges v into %[2]s by its rule.\n", fn, path)
			g.p("func FlockSet%[1]s(e *%[2]s, v %[3]s, at *flock.Hlc) {\n%[4]s\t%[5]s = %[6]s(%[5]s, v)\n%[7]s}\n\n", fn, t, g.goType(f.Field), ensure, target, op, bump)
		case parser.FlockRuleLastWrite:
			typ := g.goType(f.Field)
			if f.Field.IsRepeated {
				typ = "[]" + typ
			}
			g.p("// FlockSet%[1]s writes %[2]s at at: the newest write wins.\n", fn, path)
			g.p("func FlockSet%[1]s(e *%[2]s, v %[3]s, at *flock.Hlc) {\n\tif FlockHlcCompare(at, flockStamp(e.Get%[4]s(), %[5]q)) <= 0 {\n\t\treturn\n\t}\n%[6]s\t%[7]s = v\n\tm := flockMetaClone(e.Get%[4]s())\n\tflockSetStamp(m, %[5]q, at)\n\te.%[4]s = m\n%[8]s}\n\n",
				fn, t, typ, meta, path, ensure, target, bump)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			el := f.Element
			owner := g.ownerType(e, path)
			mat := g.setFn(owner, f)
			remat := fmt.Sprintf("\te.%[1]s = m\n%[2]s\t%[3]s = %[4]s(m, %[5]q)\n%[6]s", meta, ensure, target, mat, path, bump)
			switch {
			case el.Map:
				kt := protoScalarGoType(f.Field.MapKeyType)
				vt := g.qual(el.Package, el.Message.Name)
				g.p("// FlockPut%[1]s adds or updates %[2]s[k] at at.\n", fn, path)
				g.p("func FlockPut%[1]s(e *%[2]s, k %[3]s, v *%[4]s, at *flock.Hlc) {\n\tm := flockMetaClone(e.Get%[5]s())\n\tflockAddOp(m, %[6]q, %[7]s, flockMarshal(v), at)\n%[8]s}\n\n",
					fn, t, kt, vt, meta, path, idExpr(el.KeyKind, "k"), remat)
				g.p("// FlockRemove%[1]s removes %[2]s[k] at at.\n", fn, path)
				g.p("func FlockRemove%[1]s(e *%[2]s, k %[3]s, at *flock.Hlc) {\n\tm := flockMetaClone(e.Get%[4]s())\n\tflockRemoveOp(m, %[5]q, %[6]s, at)\n%[7]s}\n\n",
					fn, t, kt, meta, path, idExpr(el.KeyKind, "k"), remat)
			default:
				vt := g.goType(f.Field)
				payload := "flockMarshal(v)"
				if el.Scalar {
					payload = "nil"
				}
				g.p("// FlockAdd%[1]s adds (or updates) an element of %[2]s at at.\n", fn, path)
				g.p("func FlockAdd%[1]s(e *%[2]s, v %[3]s, at *flock.Hlc) {\n\tm := flockMetaClone(e.Get%[4]s())\n\tflockAddOp(m, %[5]q, %[6]s, %[7]s, at)\n%[8]s}\n\n",
					fn, t, vt, meta, path, g.elemID(el, "v"), payload, remat)
				g.p("// FlockRemove%[1]s removes an element (by identity) of %[2]s at at.\n", fn, path)
				g.p("func FlockRemove%[1]s(e *%[2]s, v %[3]s, at *flock.Hlc) {\n\tm := flockMetaClone(e.Get%[4]s())\n\tflockRemoveOp(m, %[5]q, %[6]s, at)\n%[7]s}\n\n",
					fn, t, vt, meta, path, g.elemID(el, "v"), remat)
			}
		}
	}
}

// ownerType is the Go type that holds the field at path (the entry, or the
// nested message on the way).
func (g *goFlockGen) ownerType(e *parser.FlockEntry, path string) string {
	segs := strings.Split(path, ".")
	owner := e.Message.Name
	fields := e.Fields
	for _, seg := range segs[:len(segs)-1] {
		for _, f := range fields {
			if f.Field.Name == seg && f.Nested != nil {
				owner = g.qual(f.Nested.Package, f.Nested.Message.Name)
				fields = f.Nested.Fields
			}
		}
	}
	return owner
}

// ensure creates the nested messages on the way to path.
func (g *goFlockGen) ensure(e *parser.FlockEntry, path string) string {
	segs := strings.Split(path, ".")
	var out strings.Builder
	expr := "e"
	fields := e.Fields
	for _, seg := range segs[:len(segs)-1] {
		for _, f := range fields {
			if f.Field.Name == seg && f.Nested != nil {
				expr += "." + toGoFieldName(seg)
				fmt.Fprintf(&out, "\tif %[1]s == nil {\n\t\t%[1]s = &%[2]s{}\n\t}\n", expr, g.qual(f.Nested.Package, f.Nested.Message.Name))
				fields = f.Nested.Fields
			}
		}
	}
	return out.String()
}

func (g *goFlockGen) unique(e *parser.FlockEntry) {
	t := e.Message.Name
	key, created, mark := toGoFieldName(e.Key), toGoFieldName(e.Created), toGoFieldName(e.Mark)
	meta := toGoFieldName(parser.FlockMetaFieldName)
	uf := e.UniqueFields()
	g.p("// FlockUniqueKeys%[1]s lists the (spec.unique) elements of a live %[1]s entry,\n// each as \"<path>\\x00<element>\": the keys of the unique index.\n", t)
	g.p("func FlockUniqueKeys%[1]s(e *%[1]s) []string {\n\tif flockMarked(e.Get%[2]s()) {\n\t\treturn nil\n\t}\n\tvar out []string\n", t, mark)
	for _, f := range uf {
		g.p("\tif _, ids := flockLive(e.Get%[1]s(), %[2]q, %[3]v); len(ids) > 0 {\n\t\tfor _, id := range ids {\n\t\t\tout = append(out, %[2]q+\"\\x00\"+id)\n\t\t}\n\t}\n", meta, f.Path, f.SetRemoveWins())
	}
	g.p("\treturn out\n}\n\n")
	g.p("// FlockCheckUnique%[1]s refuses a local write of e that would share a\n// (spec.unique) element with another live entry; owner returns the key of the\n// live entry that holds a unique key, if any.\n", t)
	g.p("func FlockCheckUnique%[1]s(e *%[1]s, owner func(uniqueKey string) (string, bool)) error {\n\tfor _, k := range FlockUniqueKeys%[1]s(e) {\n\t\tif other, ok := owner(k); ok && other != e.Get%[2]s() {\n\t\t\treturn protoerrors.NewErrAlreadyExists(\"flock: %[3]s %%s shares %%s with %%s\", e.Get%[2]s(), strings.ReplaceAll(k, \"\\x00\", \" \"), other)\n\t\t}\n\t}\n\treturn nil\n}\n\n", t, key, e.Package+"."+t)
	g.p("// FlockOverlap%[1]s decides what to do with two live %[1]s entries that gossip\n// brought together sharing the unique key k: merge them (FlockMergeEntries%[1]s)\n// only in the partition race, both created with the element within windowMs\n// of each other; quarantine anything else.\n", t)
	g.p("func FlockOverlap%[1]s(a, b *%[1]s, k string, windowMs int64) FlockOverlap {\n\tpath, element, ok := strings.Cut(k, \"\\x00\")\n\tif !ok {\n\t\treturn FlockOverlapQuarantine\n\t}\n", t)
	g.p("\tif !flockBornWith(a.Get%[1]s(), path, element, a.Get%[2]s()) || !flockBornWith(b.Get%[1]s(), path, element, b.Get%[2]s()) {\n\t\treturn FlockOverlapQuarantine\n\t}\n", meta, created)
	g.p("\td := a.Get%[1]s().GetWallMs() - b.Get%[1]s().GetWallMs()\n\tif d < 0 {\n\t\td = -d\n\t}\n\tif d > windowMs {\n\t\treturn FlockOverlapQuarantine\n\t}\n\treturn FlockOverlapAutoMerge\n}\n\n", created)
}

func (g *goFlockGen) marks(e *parser.FlockEntry) {
	t := e.Message.Name
	key, epoch, mark := toGoFieldName(e.Key), toGoFieldName(e.Epoch), toGoFieldName(e.Mark)
	what := e.Package + "." + t
	g.p("// FlockLive%[1]s refuses a marked %[1]s entry at once: a MERGED one names its\n// survivor, a USER or EXPIRED one is gone.\n", t)
	g.p("func FlockLive%[1]s(e *%[1]s) error {\n\tif flockMarked(e.Get%[2]s()) {\n\t\treturn flockMarkError(%[3]q, e.Get%[4]s(), e.Get%[2]s())\n\t}\n\treturn nil\n}\n\n", t, mark, what, key)
	g.p("// FlockMark%[1]s marks a %[1]s entry deleted at at, for a person (USER) or by\n// age (EXPIRED). A MERGED mark is made only by FlockMergeEntries%[1]s.\n", t)
	g.p("func FlockMark%[1]s(e *%[1]s, reason flock.DeleteReason, at *flock.Hlc) error {\n\tif reason != flock.DeleteReason_DELETE_REASON_USER && reason != flock.DeleteReason_DELETE_REASON_EXPIRED {\n\t\treturn protoerrors.NewErrMissingInvalidData(nil, \"flock: FlockMark%[1]s marks USER or EXPIRED, not %%s\", reason)\n\t}\n\te.%[2]s = flockMergeMark(e.Get%[2]s(), &flock.DeletionMark{Reason: reason, At: flockHlcClone(at)})\n\te.%[3]s = flockHlcMax(e.Get%[3]s(), at)\n\treturn nil\n}\n\n", t, mark, epoch)
	g.p("// FlockAck%[1]s records that copy holds the entry's mark.\n", t)
	g.p("func FlockAck%[1]s(e *%[1]s, copy string, at *flock.Hlc) {\n\te.%[2]s = flockAck(e.Get%[2]s(), copy, at)\n}\n\n", t, mark)
	g.p("// FlockPurgeReady%[1]s reports whether a marked entry may be purged: every\n// copy in members has acknowledged its mark, FlockMaxMessageAgeMs ago.\n", t)
	g.p("func FlockPurgeReady%[1]s(e *%[1]s, members []string, nowMs int64) bool {\n\treturn flockPurgeReady(e.Get%[2]s(), members, nowMs)\n}\n\n", t, mark)
	g.p("// FlockRedirect%[1]s returns the survivor a MERGED entry redirects to.\n", t)
	g.p("func FlockRedirect%[1]s(e *%[1]s) (string, bool) {\n\tif e.Get%[2]s().GetReason() != flock.DeleteReason_DELETE_REASON_MERGED {\n\t\treturn \"\", false\n\t}\n\treturn e.Get%[2]s().GetMergedInto(), true\n}\n\n", t, mark)
	g.p("// FlockStubExpired%[1]s reports whether a MERGED entry's redirect stub has\n// outlived lifetimeMs (the longest credential lifetime) and may go.\n", t)
	g.p("func FlockStubExpired%[1]s(e *%[1]s, nowMs, lifetimeMs int64) bool {\n\treturn e.Get%[2]s().GetReason() == flock.DeleteReason_DELETE_REASON_MERGED && nowMs-e.Get%[2]s().GetAt().GetWallMs() >= lifetimeMs\n}\n\n", t, mark)
}

func (g *goFlockGen) digest(e *parser.FlockEntry) {
	t := e.Message.Name
	g.p("// FlockDigest%[1]s is the SHA-256 of the entry's canonical encoding (its\n// set fields, which the op sets determine, left out): equal on two copies\n// exactly when they hold the same entry, in every language.\n", t)
	g.p("func FlockDigest%[1]s(e *%[1]s) []byte {\n\tc := proto.Clone(e).(*%[1]s)\n", t)
	if !e.WholeEntry {
		var clear func(fields []parser.FlockField, expr string)
		clear = func(fields []parser.FlockField, expr string) {
			for _, f := range fields {
				gf := toGoFieldName(f.Field.Name)
				switch {
				case f.IsSet():
					g.p("\t%s.%s = nil\n", expr, gf)
				case f.Nested != nil:
					g.p("\tif %s.%s != nil {\n", expr, gf)
					clear(f.Nested.Fields, expr+"."+gf)
					g.p("\t}\n")
				}
			}
		}
		clear(e.Fields, "c")
	}
	g.p("\treturn flockDigest(c)\n}\n\n")
}
