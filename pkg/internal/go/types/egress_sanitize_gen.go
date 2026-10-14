// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// This file is the emit-time side of RED-1: the generated service WRAPPER — not
// the handler — applies egress data-entitlement sanitization on every
// record-bearing egress path (unary/Sample, PAGE/Pull, WATCH snapshot+delta,
// Tier-2/Tier-3 SNAPSHOT). The handler returns raw records and is never trusted
// to redact; sanitization runs AFTER the handler so it physically cannot be
// bypassed.
//
// egressSanitizer answers, at wrapper-emit time, whether a proto type carries a
// generated Sanitize<T>/Build<T>Entitlement pair — the exact same predicate the
// helpers emitter uses (HasEntitlement + tree relevance), so wrapper and helpers
// can never disagree. Injected envelopes (Sample<T>Response, <T>WatchChange) are
// generated AFTER the entitlement pass and have no Sanitize of their own; for
// those the wrapper sanitizes each message-typed FIELD whose type is
// sanitizable (envelopeSanFields).

// egressSanitizer indexes every message of the (entitlement-augmented)
// descriptor set for the wrapper emitters.
type egressSanitizer struct {
	resolver *parser.MessageResolver
	msgs     map[string]*model.Message // key: pkg + "\x00" + name
}

// newEgressSanitizer builds the emit-time oracle over all parsed proto files.
func newEgressSanitizer(allFiles []model.ProtoFile) *egressSanitizer {
	es := &egressSanitizer{
		resolver: parser.NewMessageResolver(allFiles),
		msgs:     make(map[string]*model.Message),
	}
	for fi := range allFiles {
		pf := &allFiles[fi]
		for mi := range pf.Messages {
			es.msgs[pf.Package+"\x00"+pf.Messages[mi].Name] = &pf.Messages[mi]
		}
	}
	return es
}

// sanTarget is one sanitizable proto type at an egress seam, with the Go
// qualifier the wrapper package renders it under ("types" or "<pkg>_pb").
type sanTarget struct {
	Name string // simple message name (Sanitize<Name> / Build<Name>Entitlement)
	Qual string // Go package qualifier in the wrapper file
}

// entVar is the per-method local holding this type's built entitlement.
func (t sanTarget) entVar() string { return "__ent" + t.Name }

// sanitizeExpr renders the sanitize call for one item expression.
func (t sanTarget) sanitizeExpr(expr string) string {
	return fmt.Sprintf("%s.Sanitize%s(%s, %s, __hmacKey)", t.Qual, t.Name, expr, t.entVar())
}

// splitEgressFQN splits a proto type reference into (package, simple name); a
// bare name belongs to the service's own package.
func splitEgressFQN(fullType, servicePackage string) (string, string) {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[:i], fullType[i+1:]
	}
	return servicePackage, fullType
}

// sanitizableType reports whether fullType has a generated Sanitize<T> (same
// gate as the helpers emitter: entitlement message exists + tree is relevant).
func (es *egressSanitizer) sanitizableType(fullType, servicePackage string) (sanTarget, bool) {
	if es == nil || fullType == "" {
		return sanTarget{}, false
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok || !es.resolver.HasEntitlement(name) {
		return sanTarget{}, false
	}
	if !parser.BuildEntitlementTree(msg, pkg, es.resolver).IsRelevant() {
		return sanTarget{}, false
	}
	ref := goTypeRef(fullType, name, servicePackage)
	return sanTarget{Name: name, Qual: ref[:strings.LastIndex(ref, ".")]}, true
}

// envelopeSanField is a message-typed field of a non-sanitizable envelope whose
// own type is sanitizable (e.g. Sample<T>Response.items, <T>WatchChange.item).
type envelopeSanField struct {
	Field  model.Field
	Target sanTarget
}

// envelopeSanFields returns the sanitizable message-typed fields of fullType.
// Empty when fullType is itself sanitizable (callers sanitize it whole instead).
func (es *egressSanitizer) envelopeSanFields(fullType, servicePackage string) []envelopeSanField {
	if es == nil || fullType == "" {
		return nil
	}
	if _, ok := es.sanitizableType(fullType, servicePackage); ok {
		return nil
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []envelopeSanField
	for _, f := range msg.Fields {
		if !f.IsMessage || f.IsMap {
			continue
		}
		if f.IsOneOf {
			// A oneof envelope with sanitizable members is itself sanitizable
			// (oneof recursion) and takes the whole-sanitize path; the plain
			// field statements emitted here would not compile against the
			// oneof representation.
			continue
		}
		fpkg := f.TypePackage
		if fpkg == "" {
			fpkg = pkg
		}
		if t, ok := es.sanitizableType(fpkg+"."+f.Type, servicePackage); ok {
			out = append(out, envelopeSanField{Field: f, Target: t})
		}
	}
	return out
}

// egressSanitizePreamble renders the once-per-RPC setup: collect the caller's
// token data grants (fail-closed — nil caller or absent claim yields no grants,
// hence the most-protective view) and build each needed per-type entitlement.
// targets may contain duplicates; they are deduped preserving order.
func egressSanitizePreamble(targets []sanTarget) string {
	if len(targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
	// Egress data entitlement (RED-1): the wrapper — never the handler — sanitizes
	// every emitted record. Grants come from the caller's token (OPA-stamped);
	// absent caller/claim/grant is fail-closed to the most-protective view.
	__dataGrants := map[string]bool{}
	if caller != nil {
		for _, g := range caller.Token().GetServiceEntitlements().GetDataGrants() {
			__dataGrants[g] = true
		}
	}
	__hmacKey := p.anonymizeKey()
`)
	seen := map[string]bool{}
	for _, t := range targets {
		key := t.Qual + "." + t.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&b, "\t%s := %s.Build%sEntitlement(__dataGrants)\n", t.entVar(), t.Qual, t.Name)
	}
	return b.String()
}

// egressSanitizeSliceStmt renders sanitization of a whole slice into a FRESH
// slice (never mutating the handler-returned backing array, which may alias
// stored state). sliceExpr must be addressable; elemType is the rendered Go
// element type.
func egressSanitizeSliceStmt(t sanTarget, sliceExpr, elemType, indent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s{\n", indent)
	fmt.Fprintf(&b, "%s\t__san := make([]*%s, len(%s))\n", indent, elemType, sliceExpr)
	fmt.Fprintf(&b, "%s\tfor i := range %s {\n", indent, sliceExpr)
	fmt.Fprintf(&b, "%s\t\t__san[i] = %s\n", indent, t.sanitizeExpr(sliceExpr+"[i]"))
	fmt.Fprintf(&b, "%s\t}\n", indent)
	fmt.Fprintf(&b, "%s\t%s = __san\n", indent, sliceExpr)
	fmt.Fprintf(&b, "%s}\n", indent)
	return b.String()
}

// egressSanitizeEnvelopeStmts renders per-field sanitization of an envelope
// value held in varName. Sanitize<T> returns a copy, so a singular field is a
// plain reassignment; a repeated field goes through a fresh slice.
func egressSanitizeEnvelopeStmts(fields []envelopeSanField, varName, indent string) string {
	var b strings.Builder
	for _, ef := range fields {
		gf := toGoFieldName(ef.Field.Name)
		expr := varName + "." + gf
		if ef.Field.IsRepeated {
			b.WriteString(egressSanitizeSliceStmt(ef.Target, expr, ef.Target.Qual+"."+ef.Target.Name, indent))
			continue
		}
		fmt.Fprintf(&b, "%s%s = %s\n", indent, expr, ef.Target.sanitizeExpr(expr))
	}
	return b.String()
}
