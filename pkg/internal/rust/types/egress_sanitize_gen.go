// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
)

// Rust twin of the Go egress-sanitize emit support (RED-1): the generated
// service WRAPPER — not the handler — applies egress data-entitlement
// sanitization on every record-bearing egress path. rustEgressSanitizer is the
// emit-time oracle (same predicate as the helpers emitter: entitlement message
// exists + tree is relevant); rustSanTarget renders the sanitize/builder calls
// with the module qualification derived from the rendered type path (the
// sanitize/builder functions live in the same module as the type, re-exported
// from helpers.rs by the package mod).

// rustEgressSanitizer indexes every message of the (entitlement-augmented)
// descriptor set for the wrapper emitters.
type rustEgressSanitizer struct {
	resolver *parser.MessageResolver
	msgs     map[string]*model.Message // key: pkg + "\x00" + name
}

// newRustEgressSanitizer builds the emit-time oracle over all parsed files.
func newRustEgressSanitizer(allFiles []model.ProtoFile) *rustEgressSanitizer {
	es := &rustEgressSanitizer{
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

// rustSanTarget is one sanitizable proto type at an egress seam.
type rustSanTarget struct {
	Name  string // simple message name
	Snake string // snake_case name used in fn names and the __ent local
	Qual  string // module qualifier derived from the rendered type path ("" = same module)
}

// entVar is the per-method local holding this type's built entitlement.
func (t rustSanTarget) entVar() string { return "__ent_" + t.Snake }

// sanitizeExpr renders the sanitize call for one item expression (a reference).
func (t rustSanTarget) sanitizeExpr(refExpr string) string {
	return fmt.Sprintf("%ssanitize_%s(%s, Some(&%s), &__hmac_key)", t.Qual, t.Snake, refExpr, t.entVar())
}

// builderCall renders the Build<T>Entitlement call.
func (t rustSanTarget) builderCall() string {
	return fmt.Sprintf("%sbuild_%s_entitlement(&__data_grants)", t.Qual, t.Snake)
}

// splitRustEgressFQN splits a proto type reference into (package, simple name);
// a bare name belongs to the service's own package.
func splitRustEgressFQN(fullType, servicePkg string) (string, string) {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[:i], fullType[i+1:]
	}
	return servicePkg, fullType
}

// sanitizableType reports whether fullType has a generated sanitize_<t> (same
// gate as the helpers emitter). The Qual comes from the rendered Rust type path
// so the fn resolves in the type's own module.
func (es *rustEgressSanitizer) sanitizableType(fullType, servicePkg string, crossPkgTypes map[string]string) (rustSanTarget, bool) {
	if es == nil || fullType == "" || fullType == "google.protobuf.Empty" {
		return rustSanTarget{}, false
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok || !es.resolver.HasEntitlement(name) {
		return rustSanTarget{}, false
	}
	if !parser.BuildEntitlementTree(msg, pkg, es.resolver).IsRelevant() {
		return rustSanTarget{}, false
	}
	rendered := resolveOneRustTypeQualified(name, fullType, servicePkg, crossPkgTypes)
	qual := ""
	if i := strings.LastIndex(rendered, "::"); i >= 0 {
		qual = rendered[:i+2]
	}
	return rustSanTarget{Name: name, Snake: strutil.ToSnakeCase(name), Qual: qual}, true
}

// rustEnvelopeSanField is a message-typed field of a non-sanitizable envelope
// whose own type is sanitizable (e.g. Sample<T>Response.items, <T>WatchChange.item).
type rustEnvelopeSanField struct {
	Field  model.Field
	Target rustSanTarget
}

// envelopeSanFields returns the sanitizable message-typed fields of fullType.
// Empty when fullType is itself sanitizable (callers sanitize it whole instead).
func (es *rustEgressSanitizer) envelopeSanFields(fullType, servicePkg string, crossPkgTypes map[string]string) []rustEnvelopeSanField {
	if es == nil || fullType == "" {
		return nil
	}
	if _, ok := es.sanitizableType(fullType, servicePkg, crossPkgTypes); ok {
		return nil
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []rustEnvelopeSanField
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
		if t, ok := es.sanitizableType(fpkg+"."+f.Type, servicePkg, crossPkgTypes); ok {
			out = append(out, rustEnvelopeSanField{Field: f, Target: t})
		}
	}
	return out
}

// rustEgressSanitizePreamble renders the once-per-RPC setup: collect the
// caller's token data grants (fail-closed — a missing caller/claim yields no
// grants, hence the most-protective view), snapshot the ANONYMIZE key, and
// build each needed per-type entitlement. targets may contain duplicates; they
// are deduped preserving order.
func rustEgressSanitizePreamble(targets []rustSanTarget) string {
	if len(targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
        // Egress data entitlement (RED-1): the wrapper — never the handler — sanitizes
        // every emitted record. Grants come from the caller's token (OPA-stamped);
        // absent caller/claim/grant is fail-closed to the most-protective view.
        let mut __data_grants: std::collections::HashSet<String> = std::collections::HashSet::new();
        if let Some(c) = caller.as_ref() {
            if let Some(se) = c.token().service_entitlements() {
                if let Some(arr) = se.get("data_grants").and_then(|v| v.as_array()) {
                    for g in arr {
                        if let Some(s) = g.as_str() { __data_grants.insert(s.to_string()); }
                    }
                }
            }
        }
        let __hmac_key: Vec<u8> = self.anonymize_hmac_key.read().await.clone();
`)
	seen := map[string]bool{}
	for _, t := range targets {
		key := t.Qual + t.Snake
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&b, "        let %s = %s;\n", t.entVar(), t.builderCall())
	}
	return b.String()
}

// rustEgressEnvelopeStmts renders per-field sanitization of an owned envelope
// value held in varName (the caller emits `let mut <varName> = <varName>;`
// first). A singular field is Option<T>; a repeated field is Vec<T>.
func rustEgressEnvelopeStmts(fields []rustEnvelopeSanField, varName, indent string) string {
	var b strings.Builder
	for _, ef := range fields {
		rf := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name))
		if ef.Field.IsRepeated {
			fmt.Fprintf(&b, "%sfor v in %s.%s.iter_mut() {\n", indent, varName, rf)
			fmt.Fprintf(&b, "%s    *v = %s;\n%s}\n", indent, ef.Target.sanitizeExpr("v"), indent)
			continue
		}
		fmt.Fprintf(&b, "%s%s.%s = %s.%s.as_ref().map(|v| %s);\n", indent, varName, rf, varName, rf, ef.Target.sanitizeExpr("v"))
	}
	return b.String()
}
