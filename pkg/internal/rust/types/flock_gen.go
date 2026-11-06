// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// This file emits flock state (tacplus#206) for Rust: for every flock entry of a
// package, from the plan in parser/flock.go, the two-argument merge
// (flock_merge_<t>), the merge of two duplicate entries
// (flock_merge_entries_<t>), the writers that keep the merge metadata
// (flock_set/add/remove/put_<t>_<path>), the deletion mark helpers, the unique
// check and the canonical digest. The schema-independent half is
// rustFlockRuntime (flock_runtime.go). It is the Go emitter (go/types/
// flock_gen.go) function for function; Java emits the same from the same plan.
//
// Everything goes into a private module tacplus_flock of the package's
// helpers.rs, whose public items the package re-exports; the module's private
// helpers carry #[allow(dead_code)], as a package uses only some of them.

// writeFlock emits the flock module of pf when pf holds a flock entry.
func writeFlock(b *strings.Builder, pf model.ProtoFile, allFiles []model.ProtoFile) {
	entries := parser.NewFlockIndex(allFiles).EntriesIn(pf.Package)
	if len(entries) == 0 {
		return
	}
	protoByPkg := map[string]model.ProtoFile{}
	for _, f := range allFiles {
		if _, seen := protoByPkg[f.Package]; !seen {
			protoByPkg[f.Package] = f
		}
	}
	b.WriteString("\n// ---- flock state (tacplus#206) ----\n")
	b.WriteString("#[allow(dead_code, unused_imports, clippy::all)]\nmod tacplus_flock {\n")
	b.WriteString("    use super::*;\n    use ::prost::Message as _;\n")
	fmt.Fprintf(b, "    use %s as fpb;\n", rustFlockPkgPath(parser.FlockPackage, protoByPkg))
	runtime := strings.NewReplacer(
		"@MAX_SKEW_MS@", strconv.Itoa(parser.FlockMaxSkewMs),
		"@REBUILD_AFTER_MS@", strconv.Itoa(parser.FlockRebuildAfterMs),
		"@MAX_MESSAGE_AGE_MS@", strconv.Itoa(parser.FlockMaxMessageAgeMs),
	).Replace(rustFlockRuntime)
	b.WriteString(runtime)
	g := &rustFlockGen{b: b, pkg: pf.Package, protoByPkg: protoByPkg, joins: map[string]bool{}, sets: map[string]bool{}}
	for _, e := range entries {
		g.entry(e)
	}
	b.WriteString(g.tail.String())
	b.WriteString("}\n#[allow(unused_imports)]\npub use tacplus_flock::*;\n\n")
}

// rustFlockPkgPath is the crate path of a proto package's module.
func rustFlockPkgPath(pkg string, protoByPkg map[string]model.ProtoFile) string {
	pf, ok := protoByPkg[pkg]
	if !ok {
		return "crate::sys::" + pkg
	}
	return fmt.Sprintf("crate::%s::%s", strings.ReplaceAll(filepath.ToSlash(filepath.Dir(pf.RelPath)), "/", "::"), pkg)
}

type rustFlockGen struct {
	b *strings.Builder
	// tail holds the helpers (set materializers, element joins) emitted while
	// a function body is being written; it is appended at the end.
	tail       strings.Builder
	pkg        string
	protoByPkg map[string]model.ProtoFile
	joins      map[string]bool
	sets       map[string]bool
}

func (g *rustFlockGen) p(format string, args ...any) { fmt.Fprintf(g.b, format, args...) }

func (g *rustFlockGen) t(format string, args ...any) { fmt.Fprintf(&g.tail, format, args...) }

// qual is the Rust path of message name of package pkg, from this package.
func (g *rustFlockGen) qual(pkg, name string) string {
	t := toProstTypeName(name)
	switch {
	case pkg == "" || pkg == g.pkg:
		return t
	case pkg == parser.FlockPackage:
		return "fpb::" + t
	}
	return rustFlockPkgPath(pkg, g.protoByPkg) + "::" + t
}

// ident is the snake name of a message for helper names: unqualified in this
// package, prefixed with its package otherwise.
func (g *rustFlockGen) ident(pkg, name string) string {
	s := strutil.ToSnakeCase(toProstTypeName(name))
	if pkg == "" || pkg == g.pkg {
		return s
	}
	return strutil.ToSnakeCase(pkg) + "_" + s
}

// rustFlockField is the Rust field name of f.
func rustFlockField(name string) string { return escapeRustKeyword(strutil.ToSnakeCase(name)) }

// rustScalarType is the Rust type of one scalar value of kind proto type typ.
func rustScalarType(f model.Field) string {
	if f.IsEnum {
		return "i32"
	}
	switch f.Type {
	case "string":
		return "String"
	case "bytes":
		return "Vec<u8>"
	}
	return rustParseType(f)
}

// valueType is the Rust type of one value of f (its element, when repeated):
// a message value is an Option, as prost holds a singular one.
func (g *rustFlockGen) elemType(f model.Field) string {
	if f.IsMessage {
		return g.qual(f.TypePackage, f.Type)
	}
	return rustScalarType(f)
}

// rustIDExpr is the canonical identity encoding of the scalar place v of kind k.
func rustIDExpr(k parser.FlockScalarKind, v string) string {
	switch k {
	case parser.FlockKindString:
		return "flock_id_string(&" + v + ")"
	case parser.FlockKindBytes:
		return "flock_id_bytes(&" + v + ")"
	case parser.FlockKindBool:
		return "flock_id_bool(" + v + ")"
	case parser.FlockKindUnsigned:
		return "flock_id_uint(" + v + " as u64)"
	}
	return "flock_id_int(" + v + " as i64)"
}

// rustFromIDText decodes the identity text t (a &str) back to a value of type typ.
func rustFromIDText(k parser.FlockScalarKind, typ, t string) string {
	switch k {
	case parser.FlockKindString:
		return t + ".to_string()"
	case parser.FlockKindBytes:
		return "::hex::decode(" + t + ").unwrap_or_default()"
	case parser.FlockKindBool:
		return t + ` == "true"`
	case parser.FlockKindUnsigned:
		return t + ".parse::<u64>().unwrap_or(0) as " + typ
	}
	return t + ".parse::<i64>().unwrap_or(0) as " + typ
}

func rustPathName(path string) string {
	var segs []string
	for _, seg := range strings.Split(path, ".") {
		segs = append(segs, strutil.ToSnakeCase(seg))
	}
	return strings.Join(segs, "_")
}

func (g *rustFlockGen) entry(e *parser.FlockEntry) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	what := e.Package + "." + e.Message.Name
	key, created, epoch, mark := rustFlockField(e.Key), rustFlockField(e.Created), rustFlockField(e.Epoch), rustFlockField(e.Mark)
	meta := rustFlockField(parser.FlockMetaFieldName)

	g.p("\n    // ---- flock entry %s ----\n\n", what)
	g.p("    /// flock_new_%[1]s returns a new %[2]s entry: flock creates its key\n    /// (flock_new_key) and stamps its creation, which never changes.\n", s, t)
	g.p("    pub fn flock_new_%[1]s(key: &str, created: &fpb::Hlc) -> %[2]s {\n        %[2]s { %[3]s: key.to_string(), %[4]s: flock_hlc_clone(Some(created)), %[5]s: flock_hlc_clone(Some(created)), ..Default::default() }\n    }\n\n", s, t, key, created, epoch)

	// Merge.
	g.p("    /// flock_merge_%[1]s merges two copies of one %[2]s entry (the same key)\n    /// field by field, by each field's (spec.merge) rule. It is commutative,\n    /// associative and idempotent; a deletion mark wins.\n", s, t)
	g.p("    pub fn flock_merge_%[1]s(a: &%[2]s, b: &%[2]s) -> Result<%[2]s, crate::sys::errors::Error> {\n", s, t)
	g.p("        if a.%[2]s != b.%[2]s {\n            return Err(flock_error(FLOCK_CODE_INVALID_ARGUMENT, format!(\"flock: flock_merge_%[1]s merges copies of one entry, not {} and {}; two entries merge with flock_merge_entries_%[1]s\", a.%[2]s, b.%[2]s)));\n        }\n", s, key)
	g.p("        let mut mo = flock_merge_meta(a.%[1]s.as_ref(), b.%[1]s.as_ref());\n", meta)
	if e.WholeEntry {
		g.p("        let w = match flock_hlc_compare(a.%[1]s.as_ref(), b.%[1]s.as_ref()) {\n            std::cmp::Ordering::Less => b,\n            std::cmp::Ordering::Equal if flock_body_%[2]s(b) > flock_body_%[2]s(a) => b,\n            _ => a,\n        };\n", epoch, s)
		g.p("        let mut out = w.clone();\n")
	} else {
		g.p("        let mut out = flock_join_%[1]s(Some(a), Some(b), a.%[2]s.as_ref(), b.%[2]s.as_ref(), &mut mo, \"\", false).unwrap_or_default();\n", s, meta)
		g.p("        out.%[1]s = a.%[1]s.clone();\n", key)
		g.p("        out.%[1]s = flock_hlc_max(a.%[1]s.as_ref(), b.%[1]s.as_ref());\n", epoch)
	}
	g.p("        out.%[1]s = a.%[1]s.clone();\n        if flock_hlc_zero(a.%[1]s.as_ref()) {\n            out.%[1]s = b.%[1]s.clone();\n        } else if !flock_hlc_zero(b.%[1]s.as_ref()) && flock_hlc_compare(a.%[1]s.as_ref(), b.%[1]s.as_ref()) != std::cmp::Ordering::Equal {\n            flock_add_conflict(&mut mo, %[2]q);\n            if flock_hlc_compare(b.%[1]s.as_ref(), a.%[1]s.as_ref()) == std::cmp::Ordering::Less {\n                out.%[1]s = b.%[1]s.clone();\n            }\n        }\n", created, e.Created)
	g.p("        out.%[1]s = flock_merge_mark(a.%[1]s.as_ref(), b.%[1]s.as_ref());\n", mark)
	g.p("        out.%s = Some(mo);\n        Ok(out)\n    }\n\n", meta)

	if e.WholeEntry {
		g.p("    fn flock_body_%[1]s(e: &%[2]s) -> Vec<u8> {\n        let mut c = e.clone();\n        c.%[3]s = None;\n        c.%[4]s = None;\n        flock_marshal(&c)\n    }\n\n", s, t, mark, meta)
		g.p("    /// flock_write_%[1]s stamps a write of the whole entry at at: the newer\n    /// write replaces the entry on every copy.\n", s)
		g.p("    pub fn flock_write_%[1]s(e: &mut %[2]s, at: &fpb::Hlc) {\n        e.%[3]s = flock_hlc_max(e.%[3]s.as_ref(), Some(at));\n    }\n\n", s, t, epoch)
	} else {
		g.mergeEntries(e)
		g.joinFn(e.Package, e.Message.Name, e.Fields)
		g.writers(e, e.Fields, e.Package, e.Message.Name, nil, "")
		g.unique(e)
	}
	g.marks(e)
	g.digest(e)
}

func (g *rustFlockGen) mergeEntries(e *parser.FlockEntry) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	key, created, epoch, mark := rustFlockField(e.Key), rustFlockField(e.Created), rustFlockField(e.Epoch), rustFlockField(e.Mark)
	meta := rustFlockField(parser.FlockMetaFieldName)
	g.p("    /// flock_merge_entries_%[1]s merges two different %[2]s entries that are one:\n    /// the older (by creation stamp, then key) survives and absorbs the other in a\n    /// full join, removals included; the other is returned marked MERGED into it,\n    /// as (survivor, loser). Called in either order, on any copy, it picks the\n    /// same survivor. at is the clock time of the merge, kept in the mark.\n    /// Re-apply it whenever new state for the loser arrives: it is idempotent, and\n    /// neither entry's store epoch moves.\n", s, t)
	g.p("    pub fn flock_merge_entries_%[1]s(a: &%[2]s, b: &%[2]s, at: &fpb::Hlc) -> Result<(%[2]s, %[2]s), crate::sys::errors::Error> {\n", s, t)
	g.p("        if a.%[2]s == b.%[2]s {\n            return Err(flock_error(FLOCK_CODE_INVALID_ARGUMENT, format!(\"flock: flock_merge_entries_%[1]s merges two entries; two copies of {} merge with flock_merge_%[1]s\", a.%[2]s)));\n        }\n", s, key)
	g.p("        if flock_hlc_zero(a.%[1]s.as_ref()) || flock_hlc_zero(b.%[1]s.as_ref()) {\n            return Err(flock_error(FLOCK_CODE_FAILED_PRECONDITION, \"flock: flock_merge_entries_%[2]s: an entry has no creation stamp\".to_string()));\n        }\n", created, s)
	g.p("        let c = flock_hlc_compare(b.%[1]s.as_ref(), a.%[1]s.as_ref());\n        let (s, l) = if c == std::cmp::Ordering::Less || (c == std::cmp::Ordering::Equal && b.%[2]s < a.%[2]s) { (b, a) } else { (a, b) };\n        let mut l = l.clone();\n", created, key)
	g.p("        let mut mo = flock_merge_meta(s.%[1]s.as_ref(), l.%[1]s.as_ref());\n", meta)
	g.p("        let mut survivor = flock_join_%[1]s(Some(s), Some(&l), s.%[2]s.as_ref(), l.%[2]s.as_ref(), &mut mo, \"\", true).unwrap_or_default();\n", s, meta)
	g.p("        survivor.%[1]s = s.%[1]s.clone();\n        survivor.%[2]s = s.%[2]s.clone();\n", key, created)
	g.p("        survivor.%[1]s = flock_hlc_max(s.%[1]s.as_ref(), l.%[1]s.as_ref());\n", epoch)
	g.p("        survivor.%[1]s = s.%[1]s.clone();\n        survivor.%[2]s = Some(mo);\n", mark, meta)
	g.p("        let m = fpb::DeletionMark {\n            reason: fpb::DeleteReason::Merged as i32,\n            merged_into: s.%[2]s.clone(),\n            merged_into_created: flock_hlc_clone(s.%[3]s.as_ref()),\n            at: flock_hlc_clone(Some(at)),\n            ..Default::default()\n        };\n        l.%[1]s = flock_merge_mark(l.%[1]s.as_ref(), Some(&m));\n", mark, key, created)
	g.p("        Ok((survivor, l))\n    }\n\n")
}

// joinFn emits flock_join_<name>, the field-by-field join of two values of a
// message (an entry, or a nested message reached by a FlockRuleNested field).
// entries: merging two entries (merge_entries): an IMMUTABLE field keeps a's.
func (g *rustFlockGen) joinFn(pkg, msgName string, fields []parser.FlockField) {
	name := g.ident(pkg, msgName)
	if g.joins[name] {
		return
	}
	g.joins[name] = true
	typ := g.qual(pkg, msgName)
	g.p("    fn flock_join_%[1]s(a: Option<&%[2]s>, b: Option<&%[2]s>, ma: Option<&fpb::Meta>, mb: Option<&fpb::Meta>, mo: &mut fpb::Meta, prefix: &str, entries: bool) -> Option<%[2]s> {\n", name, typ)
	g.p("        let _ = (&ma, &mb, &mo, &prefix, &entries);\n        if a.is_none() && b.is_none() {\n            return None;\n        }\n        let zero = %[1]s::default();\n        let a = a.unwrap_or(&zero);\n        let b = b.unwrap_or(&zero);\n        let mut out = %[1]s::default();\n", typ)
	var nested []parser.FlockField
	for _, f := range fields {
		rf := rustFlockField(f.Field.Name)
		switch f.Rule {
		case parser.FlockRuleImmutable:
			zero, less, ne := g.scalarOps(f, "a."+rf, "b."+rf)
			g.p("        out.%[1]s = a.%[1]s.clone();\n        if %[2]s {\n            out.%[1]s = b.%[1]s.clone();\n        } else if !entries && !(%[3]s) && %[4]s {\n            flock_add_conflict(mo, &format!(\"{}{}\", prefix, %[5]q));\n            if %[6]s {\n                out.%[1]s = b.%[1]s.clone();\n            }\n        }\n",
				rf, zero("a."+rf), zero("b."+rf), ne, f.Field.Name, less)
		case parser.FlockRuleMin:
			g.p("        out.%[1]s = flock_min_non_zero(&a.%[1]s, &b.%[1]s);\n", rf)
		case parser.FlockRuleMax:
			g.p("        out.%[1]s = flock_max_non_zero(&a.%[1]s, &b.%[1]s);\n", rf)
		case parser.FlockRuleLastWrite:
			g.p("        {\n            let p = format!(\"{}{}\", prefix, %[2]q);\n            match flock_hlc_compare(flock_stamp(ma, &p), flock_stamp(mb, &p)) {\n                std::cmp::Ordering::Greater => out.%[1]s = a.%[1]s.clone(),\n                std::cmp::Ordering::Less => out.%[1]s = b.%[1]s.clone(),\n                std::cmp::Ordering::Equal => {\n                    out.%[1]s = a.%[1]s.clone();\n                    if flock_marshal(&%[3]s { %[1]s: b.%[1]s.clone(), ..Default::default() }) > flock_marshal(&%[3]s { %[1]s: a.%[1]s.clone(), ..Default::default() }) {\n                        out.%[1]s = b.%[1]s.clone();\n                    }\n                }\n            }\n        }\n",
				rf, f.Field.Name, typ)
		case parser.FlockRuleNested:
			jn := g.ident(f.Nested.Package, f.Nested.Message.Name)
			g.p("        out.%[1]s = flock_join_%[2]s(a.%[1]s.as_ref(), b.%[1]s.as_ref(), ma, mb, mo, &format!(\"{}{}\", prefix, %[3]q), entries);\n", rf, jn, f.Field.Name+".")
			nested = append(nested, f)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			g.p("        out.%[1]s = %[2]s(Some(&*mo), &format!(\"{}{}\", prefix, %[3]q));\n", rf, g.setFn(pkg, msgName, f), f.Field.Name)
		}
	}
	g.p("        Some(out)\n    }\n\n")
	for _, f := range nested {
		g.joinFn(f.Nested.Package, f.Nested.Message.Name, f.Nested.Fields)
	}
}

// scalarOps returns, for an IMMUTABLE field, its zero test, a "b is less"
// test and an "a differs from b" test, over Rust places a and b.
func (g *rustFlockGen) scalarOps(f parser.FlockField, a, b string) (zero func(string) string, less, ne string) {
	switch {
	case f.HlcValue:
		return func(v string) string { return "flock_hlc_zero(" + v + ".as_ref())" },
			"flock_hlc_compare(" + b + ".as_ref(), " + a + ".as_ref()) == std::cmp::Ordering::Less",
			"flock_hlc_compare(" + a + ".as_ref(), " + b + ".as_ref()) != std::cmp::Ordering::Equal"
	case f.Kind == parser.FlockKindBytes || f.Kind == parser.FlockKindString:
		return func(v string) string { return v + ".is_empty()" }, b + " < " + a, a + " != " + b
	case f.Kind == parser.FlockKindBool:
		return func(v string) string { return "!" + v }, "false", a + " != " + b
	case f.Kind == parser.FlockKindFloat:
		return func(v string) string { return v + " == 0.0" }, b + " < " + a, a + " != " + b
	}
	return func(v string) string { return v + " == 0" }, b + " < " + a, a + " != " + b
}

// setFn emits (once) the materializer of a SET_* field of the message
// ownerPkg.ownerMsg: its live elements, from the op set at a path.
func (g *rustFlockGen) setFn(ownerPkg, ownerMsg string, f parser.FlockField) string {
	name := "flock_mat_" + g.ident(ownerPkg, ownerMsg) + "_" + strutil.ToSnakeCase(f.Field.Name)
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	el := f.Element
	rw := f.SetRemoveWins()
	switch {
	case el.Scalar:
		typ := rustScalarType(f.Field)
		g.t("    fn %[1]s(m: Option<&fpb::Meta>, path: &str) -> Vec<%[2]s> {\n        flock_live(m, path, %[3]v).keys().map(|id| %[4]s).collect()\n    }\n\n",
			name, typ, rw, rustFromIDText(el.Kind, typ, "flock_id_text(id)"))
	case el.Map:
		vt := g.qual(el.Package, el.Message.Name)
		kt := rustScalarType(model.Field{Type: f.Field.MapKeyType})
		g.t("    fn %[1]s(m: Option<&fpb::Meta>, path: &str) -> ::std::collections::HashMap<%[2]s, %[3]s> {\n        let mut out = ::std::collections::HashMap::new();\n        for (id, ops) in flock_live(m, path, %[4]v) {\n            if let Some(v) = %[5]s(&ops) {\n                out.insert(%[6]s, v);\n            }\n        }\n        out\n    }\n\n",
			name, kt, vt, rw, g.elemFn(el), rustFromIDText(el.KeyKind, kt, "flock_id_text(&id)"))
	default:
		vt := g.qual(el.Package, el.Message.Name)
		g.t("    fn %[1]s(m: Option<&fpb::Meta>, path: &str) -> Vec<%[2]s> {\n        flock_live(m, path, %[3]v).values().filter_map(|ops| %[4]s(ops)).collect()\n    }\n\n",
			name, vt, rw, g.elemFn(el))
	}
	return name
}

// elemFn emits (once) the join of an element's live adds: their payloads, in
// dot order; MIN and MAX over all, LAST_WRITE from the latest.
func (g *rustFlockGen) elemFn(el *parser.FlockElement) string {
	vt := g.qual(el.Package, el.Message.Name)
	name := "flock_elem_" + g.ident(el.Package, el.Message.Name)
	if g.sets[name] {
		return name
	}
	g.sets[name] = true
	g.t("    fn %[1]s(ops: &[&fpb::SetOp]) -> Option<%[2]s> {\n        let mut v: Option<%[2]s> = None;\n        for op in ops {\n            let Ok(e) = <%[2]s as ::prost::Message>::decode(op.payload.as_slice()) else {\n                continue;\n            };\n            let Some(cur) = v.as_mut() else {\n                v = Some(e);\n                continue;\n            };\n            let _ = (&cur, &e);\n", name, vt)
	for _, f := range el.Fields {
		rf := rustFlockField(f.Field.Name)
		switch f.Rule {
		case parser.FlockRuleMin:
			g.t("            cur.%[1]s = flock_min_non_zero(&cur.%[1]s, &e.%[1]s);\n", rf)
		case parser.FlockRuleMax:
			g.t("            cur.%[1]s = flock_max_non_zero(&cur.%[1]s, &e.%[1]s);\n", rf)
		case parser.FlockRuleLastWrite:
			g.t("            cur.%[1]s = e.%[1]s.clone();\n", rf)
		}
	}
	g.t("        }\n        v\n    }\n\n")
	return name
}

// elemID is the canonical identity of the element value v of a set.
func (g *rustFlockGen) elemID(el *parser.FlockElement, v string) string {
	if el.Scalar {
		return rustIDExpr(el.Kind, v)
	}
	var parts []string
	for _, f := range el.Identity {
		parts = append(parts, rustIDExpr(parser.FlockKind(f), v+"."+rustFlockField(f.Name)))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "[" + strings.Join(parts, ", ") + "].concat()"
}

// rustFlockHop is one nested message on the way from an entry to a field.
type rustFlockHop struct {
	field string // the Rust field name
}

// chain is the Rust expression that creates, as Go's ensure does, the nested
// messages on the way and borrows the innermost one mutably.
func chain(hops []rustFlockHop) string {
	if len(hops) == 0 {
		return "&mut *e"
	}
	expr := "e"
	for _, h := range hops {
		expr += "." + h.field + ".get_or_insert_with(Default::default)"
	}
	return expr
}

// writers emits the writers of fields (an entry's, or a nested message's at path
// prefix pre, of message ownerPkg.ownerMsg, reached from the entry e through hops).
func (g *rustFlockGen) writers(e *parser.FlockEntry, fields []parser.FlockField, ownerPkg, ownerMsg string, hops []rustFlockHop, pre string) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	epoch := rustFlockField(e.Epoch)
	meta := rustFlockField(parser.FlockMetaFieldName)
	bump := fmt.Sprintf("        e.%[1]s = flock_hlc_max(e.%[1]s.as_ref(), Some(at));\n", epoch)
	for _, f := range fields {
		rf := rustFlockField(f.Field.Name)
		path := pre + f.Field.Name
		fn := s + "_" + rustPathName(path)
		target := chain(hops)
		switch f.Rule {
		case parser.FlockRuleNested:
			next := append(append([]rustFlockHop(nil), hops...), rustFlockHop{field: rf})
			g.writers(e, f.Nested.Fields, f.Nested.Package, f.Nested.Message.Name, next, path+".")
		case parser.FlockRuleImmutable:
			typ := g.valueType(f.Field)
			zero, _, ne := g.scalarOps(f, "t."+rf, "v")
			g.p("    /// flock_set_%[1]s sets the IMMUTABLE %[2]s once; setting it again to\n    /// another value is refused.\n", fn, path)
			g.p("    pub fn flock_set_%[1]s(e: &mut %[2]s, v: %[3]s, at: &fpb::Hlc) -> Result<(), crate::sys::errors::Error> {\n        {\n            let t = %[4]s;\n            if !(%[5]s) && %[6]s {\n                return Err(flock_error(FLOCK_CODE_FAILED_PRECONDITION, \"flock: %[7]s is immutable\".to_string()));\n            }\n            t.%[8]s = v;\n        }\n%[9]s        Ok(())\n    }\n\n",
				fn, t, typ, target, zero("t."+rf), ne, path, rf, bump)
		case parser.FlockRuleMin, parser.FlockRuleMax:
			op := "flock_min_non_zero"
			if f.Rule == parser.FlockRuleMax {
				op = "flock_max_non_zero"
			}
			g.p("    /// flock_set_%[1]s merges v into %[2]s by its rule.\n", fn, path)
			g.p("    pub fn flock_set_%[1]s(e: &mut %[2]s, v: %[3]s, at: &fpb::Hlc) {\n        {\n            let t = %[4]s;\n            t.%[5]s = %[6]s(&t.%[5]s, &v);\n        }\n%[7]s    }\n\n", fn, t, g.valueType(f.Field), target, rf, op, bump)
		case parser.FlockRuleLastWrite:
			g.p("    /// flock_set_%[1]s writes %[2]s at at: the newest write wins.\n", fn, path)
			g.p("    pub fn flock_set_%[1]s(e: &mut %[2]s, v: %[3]s, at: &fpb::Hlc) {\n        if flock_hlc_compare(Some(at), flock_stamp(e.%[4]s.as_ref(), %[5]q)) != std::cmp::Ordering::Greater {\n            return;\n        }\n        {\n            let t = %[6]s;\n            t.%[7]s = v;\n        }\n        let mut m = flock_meta_clone(e.%[4]s.as_ref());\n        flock_set_stamp(&mut m, %[5]q, Some(at));\n        e.%[4]s = Some(m);\n%[8]s    }\n\n",
				fn, t, g.valueType(f.Field), meta, path, target, rf, bump)
		case parser.FlockRuleSetAddWins, parser.FlockRuleSetRemoveWins:
			el := f.Element
			mat := g.setFn(ownerPkg, ownerMsg, f)
			remat := fmt.Sprintf("        let live = %[1]s(Some(&m), %[2]q);\n        e.%[3]s = Some(m);\n        {\n            let t = %[4]s;\n            t.%[5]s = live;\n        }\n%[6]s", mat, path, meta, target, rf, bump)
			switch {
			case el.Map:
				kt := rustScalarType(model.Field{Type: f.Field.MapKeyType})
				vt := g.qual(el.Package, el.Message.Name)
				g.p("    /// flock_put_%[1]s adds or updates %[2]s[k] at at.\n", fn, path)
				g.p("    pub fn flock_put_%[1]s(e: &mut %[2]s, k: %[3]s, v: &%[4]s, at: &fpb::Hlc) {\n        let mut m = flock_meta_clone(e.%[5]s.as_ref());\n        flock_add_op(&mut m, %[6]q, %[7]s, flock_marshal(v), at);\n%[8]s    }\n\n",
					fn, t, kt, vt, meta, path, rustIDExpr(el.KeyKind, "k"), remat)
				g.p("    /// flock_remove_%[1]s removes %[2]s[k] at at.\n", fn, path)
				g.p("    pub fn flock_remove_%[1]s(e: &mut %[2]s, k: %[3]s, at: &fpb::Hlc) {\n        let mut m = flock_meta_clone(e.%[4]s.as_ref());\n        flock_remove_op(&mut m, %[5]q, %[6]s, at);\n%[7]s    }\n\n",
					fn, t, kt, meta, path, rustIDExpr(el.KeyKind, "k"), remat)
			default:
				vt := g.elemType(f.Field)
				payload := "flock_marshal(v)"
				arg := "&" + vt
				if el.Scalar {
					payload = "Vec::new()"
					arg = vt
				}
				g.p("    /// flock_add_%[1]s adds (or updates) an element of %[2]s at at.\n", fn, path)
				g.p("    pub fn flock_add_%[1]s(e: &mut %[2]s, v: %[3]s, at: &fpb::Hlc) {\n        let mut m = flock_meta_clone(e.%[4]s.as_ref());\n        flock_add_op(&mut m, %[5]q, %[6]s, %[7]s, at);\n%[8]s    }\n\n",
					fn, t, arg, meta, path, g.elemID(el, "v"), payload, remat)
				g.p("    /// flock_remove_%[1]s removes an element (by identity) of %[2]s at at.\n", fn, path)
				g.p("    pub fn flock_remove_%[1]s(e: &mut %[2]s, v: %[3]s, at: &fpb::Hlc) {\n        let mut m = flock_meta_clone(e.%[4]s.as_ref());\n        flock_remove_op(&mut m, %[5]q, %[6]s, at);\n%[7]s    }\n\n",
					fn, t, arg, meta, path, g.elemID(el, "v"), remat)
			}
		}
	}
}

// valueType is the Rust type a writer of f takes: the field's own type (an
// Option for a singular message, a Vec for a repeated field).
func (g *rustFlockGen) valueType(f model.Field) string {
	typ := g.elemType(f)
	switch {
	case f.IsRepeated:
		return "Vec<" + typ + ">"
	case f.IsMessage:
		return "Option<" + typ + ">"
	}
	return typ
}

func (g *rustFlockGen) unique(e *parser.FlockEntry) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	key, created, mark := rustFlockField(e.Key), rustFlockField(e.Created), rustFlockField(e.Mark)
	meta := rustFlockField(parser.FlockMetaFieldName)
	g.p("    /// flock_unique_keys_%[1]s lists the (spec.unique) elements of a live %[2]s\n    /// entry, each as \"<path>\\x00<element>\": the keys of the unique index.\n", s, t)
	mut := ""
	if len(e.UniqueFields()) > 0 {
		mut = "mut "
	}
	g.p("    pub fn flock_unique_keys_%[1]s(e: &%[2]s) -> Vec<String> {\n        if flock_marked(e.%[3]s.as_ref()) {\n            return Vec::new();\n        }\n        let %[4]sout: Vec<String> = Vec::new();\n", s, t, mark, mut)
	for _, f := range e.UniqueFields() {
		g.p("        for id in flock_live(e.%[1]s.as_ref(), %[2]q, %[3]v).keys() {\n            out.push(format!(\"{}\\x00{}\", %[2]q, id));\n        }\n", meta, f.Path, f.SetRemoveWins())
	}
	g.p("        out\n    }\n\n")
	g.p("    /// flock_check_unique_%[1]s refuses a local write of e that would share a\n    /// (spec.unique) element with another live entry; owner returns the key of\n    /// the live entry that holds a unique key, if any.\n", s)
	g.p("    pub fn flock_check_unique_%[1]s(e: &%[2]s, owner: impl Fn(&str) -> Option<String>) -> Result<(), crate::sys::errors::Error> {\n        for k in flock_unique_keys_%[1]s(e) {\n            if let Some(other) = owner(&k) {\n                if other != e.%[3]s {\n                    return Err(flock_error(FLOCK_CODE_ALREADY_EXISTS, format!(\"flock: %[4]s {} shares {} with {}\", e.%[3]s, k.replace('\\x00', \" \"), other)));\n                }\n            }\n        }\n        Ok(())\n    }\n\n", s, t, key, e.Package+"."+e.Message.Name)
	g.p("    /// flock_overlap_%[1]s decides what to do with two live %[2]s entries that\n    /// gossip brought together sharing the unique key k: merge them\n    /// (flock_merge_entries_%[1]s) only in the partition race, both created with\n    /// the element within window_ms of each other; quarantine anything else.\n", s, t)
	g.p("    pub fn flock_overlap_%[1]s(a: &%[2]s, b: &%[2]s, k: &str, window_ms: i64) -> FlockOverlap {\n        let Some((path, element)) = k.split_once('\\x00') else {\n            return FlockOverlap::Quarantine;\n        };\n", s, t)
	g.p("        if !flock_born_with(a.%[1]s.as_ref(), path, element, a.%[2]s.as_ref()) || !flock_born_with(b.%[1]s.as_ref(), path, element, b.%[2]s.as_ref()) {\n            return FlockOverlap::Quarantine;\n        }\n", meta, created)
	g.p("        let wall = |h: &Option<fpb::Hlc>| h.as_ref().map(|h| h.wall_ms).unwrap_or(0);\n        let mut d = wall(&a.%[1]s).wrapping_sub(wall(&b.%[1]s));\n        if d < 0 {\n            d = d.wrapping_neg();\n        }\n        if d > window_ms {\n            return FlockOverlap::Quarantine;\n        }\n        FlockOverlap::AutoMerge\n    }\n\n", created)
}

func (g *rustFlockGen) marks(e *parser.FlockEntry) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	key, epoch, mark := rustFlockField(e.Key), rustFlockField(e.Epoch), rustFlockField(e.Mark)
	what := e.Package + "." + e.Message.Name
	g.p("    /// flock_live_%[1]s refuses a marked %[2]s entry at once: a MERGED one names\n    /// its survivor, a USER or EXPIRED one is gone.\n", s, t)
	g.p("    pub fn flock_live_%[1]s(e: &%[2]s) -> Result<(), crate::sys::errors::Error> {\n        if flock_marked(e.%[3]s.as_ref()) {\n            return Err(flock_mark_error(%[4]q, &e.%[5]s, e.%[3]s.as_ref()));\n        }\n        Ok(())\n    }\n\n", s, t, mark, what, key)
	g.p("    /// flock_mark_%[1]s marks a %[2]s entry deleted at at, for a person (USER) or\n    /// by age (EXPIRED). A MERGED mark is made only by flock_merge_entries_%[1]s.\n", s, t)
	g.p("    pub fn flock_mark_%[1]s(e: &mut %[2]s, reason: fpb::DeleteReason, at: &fpb::Hlc) -> Result<(), crate::sys::errors::Error> {\n        if reason != fpb::DeleteReason::User && reason != fpb::DeleteReason::Expired {\n            return Err(flock_error(FLOCK_CODE_INVALID_ARGUMENT, format!(\"flock: flock_mark_%[1]s marks USER or EXPIRED, not {}\", reason.as_str_name())));\n        }\n        let m = fpb::DeletionMark { reason: reason as i32, at: flock_hlc_clone(Some(at)), ..Default::default() };\n        e.%[3]s = flock_merge_mark(e.%[3]s.as_ref(), Some(&m));\n        e.%[4]s = flock_hlc_max(e.%[4]s.as_ref(), Some(at));\n        Ok(())\n    }\n\n", s, t, mark, epoch)
	g.p("    /// flock_ack_%[1]s records that copy holds the entry's mark.\n", s)
	g.p("    pub fn flock_ack_%[1]s(e: &mut %[2]s, copy: &str, at: &fpb::Hlc) {\n        e.%[3]s = flock_ack(e.%[3]s.as_ref(), copy, at);\n    }\n\n", s, t, mark)
	g.p("    /// flock_purge_ready_%[1]s reports whether a marked entry may be purged:\n    /// every copy in members has acknowledged its mark, FLOCK_MAX_MESSAGE_AGE_MS ago.\n", s)
	g.p("    pub fn flock_purge_ready_%[1]s(e: &%[2]s, members: &[String], now_ms: i64) -> bool {\n        flock_purge_ready(e.%[3]s.as_ref(), members, now_ms)\n    }\n\n", s, t, mark)
	g.p("    /// flock_redirect_%[1]s returns the survivor a MERGED entry redirects to.\n", s)
	g.p("    pub fn flock_redirect_%[1]s(e: &%[2]s) -> Option<String> {\n        match e.%[3]s.as_ref() {\n            Some(m) if m.reason == fpb::DeleteReason::Merged as i32 => Some(m.merged_into.clone()),\n            _ => None,\n        }\n    }\n\n", s, t, mark)
	g.p("    /// flock_stub_expired_%[1]s reports whether a MERGED entry's redirect stub\n    /// has outlived lifetime_ms (the longest credential lifetime) and may go.\n", s)
	g.p("    pub fn flock_stub_expired_%[1]s(e: &%[2]s, now_ms: i64, lifetime_ms: i64) -> bool {\n        match e.%[3]s.as_ref() {\n            Some(m) if m.reason == fpb::DeleteReason::Merged as i32 => {\n                now_ms.wrapping_sub(m.at.as_ref().map(|h| h.wall_ms).unwrap_or(0)) >= lifetime_ms\n            }\n            _ => false,\n        }\n    }\n\n", s, t, mark)
}

func (g *rustFlockGen) digest(e *parser.FlockEntry) {
	t := toProstTypeName(e.Message.Name)
	s := strutil.ToSnakeCase(t)
	g.p("    /// flock_digest_%[1]s is the SHA-256 of the entry's canonical encoding (its\n    /// set fields, which the op sets determine, left out): equal on two copies\n    /// exactly when they hold the same entry, in every language.\n", s)
	var body strings.Builder
	if !e.WholeEntry && len(e.FlockSetFields()) > 0 {
		var clear func(fields []parser.FlockField, expr, indent string, depth int)
		clear = func(fields []parser.FlockField, expr, indent string, depth int) {
			for _, f := range fields {
				rf := rustFlockField(f.Field.Name)
				switch {
				case f.IsSet():
					fmt.Fprintf(&body, "%s%s.%s = Default::default();\n", indent, expr, rf)
				case f.Nested != nil:
					n := fmt.Sprintf("n%d", depth)
					fmt.Fprintf(&body, "%sif let Some(%s) = %s.%s.as_mut() {\n", indent, n, expr, rf)
					clear(f.Nested.Fields, n, indent+"    ", depth+1)
					fmt.Fprintf(&body, "%s}\n", indent)
				}
			}
		}
		clear(e.Fields, "c", "        ", 0)
	}
	if body.Len() == 0 {
		g.p("    pub fn flock_digest_%[1]s(e: &%[2]s) -> Vec<u8> {\n        flock_digest(e)\n    }\n\n", s, t)
		return
	}
	g.p("    pub fn flock_digest_%[1]s(e: &%[2]s) -> Vec<u8> {\n        let mut c = e.clone();\n%[3]s        flock_digest(&c)\n    }\n\n", s, t, body.String())
}
