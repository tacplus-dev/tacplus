// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// Java twin of the Go/Rust egress-sanitize emit support (RED-1): the generated
// service WRAPPER — not the handler — applies egress data-entitlement
// sanitization on every record-bearing egress path. javaEgressSanitizer is the
// emit-time oracle (same predicate as the helpers emitter: entitlement message
// exists + tree is relevant); javaSanTarget renders the sanitize/builder calls
// against the right Helpers class (local or fully-qualified cross-package).

// javaEgressSanitizer indexes every message of the (entitlement-augmented)
// descriptor set for the wrapper emitters.
type javaEgressSanitizer struct {
	resolver *parser.MessageResolver
	msgs     map[string]*model.Message // key: pkg + "\x00" + name
}

// newJavaEgressSanitizer builds the emit-time oracle over all parsed files.
func newJavaEgressSanitizer(allFiles []model.ProtoFile) *javaEgressSanitizer {
	es := &javaEgressSanitizer{
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

// javaSanTarget is one sanitizable proto type at an egress seam.
type javaSanTarget struct {
	Name    string // simple message name
	Helpers string // Helpers class qualifier ("Helpers" or the cross-package FQN)
}

// entVar is the per-method local holding this type's built entitlement.
func (t javaSanTarget) entVar() string { return "__ent" + t.Name }

// sanitizeExpr renders the sanitize call for one item expression.
func (t javaSanTarget) sanitizeExpr(expr string) string {
	return fmt.Sprintf("%s.sanitize%s(%s, %s, __hmacKey)", t.Helpers, t.Name, expr, t.entVar())
}

// builderCall renders the build<T>Entitlement call.
func (t javaSanTarget) builderCall() string {
	return fmt.Sprintf("%s.build%sEntitlement(__dataGrants)", t.Helpers, t.Name)
}

// splitJavaEgressFQN splits a proto type reference into (package, simple name);
// a bare name belongs to the service's own package.
func splitJavaEgressFQN(fullType, servicePackage string) (string, string) {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[:i], fullType[i+1:]
	}
	return servicePackage, fullType
}

// sanitizableType reports whether fullType has a generated sanitize<T> (same
// gate as the helpers emitter). NOTE the Java helpers emitter resolves nested
// recursion with a same-FILE resolver, but the existence gate (HasEntitlement +
// relevance) matches because entitlement messages are appended to the same file
// as their source message.
func (es *javaEgressSanitizer) sanitizableType(fullType, servicePackage string, crossPkgHelpers map[string]string) (javaSanTarget, bool) {
	if es == nil || fullType == "" || strings.HasPrefix(fullType, "google.protobuf.") {
		return javaSanTarget{}, false
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok || !es.resolver.HasEntitlement(name) {
		return javaSanTarget{}, false
	}
	if !parser.BuildEntitlementTree(msg, pkg, es.resolver).IsRelevant() {
		return javaSanTarget{}, false
	}
	return javaSanTarget{Name: name, Helpers: resolveValidationHelpers(fullType, servicePackage, crossPkgHelpers)}, true
}

// javaEnvelopeSanField is a message-typed field of a non-sanitizable envelope
// whose own type is sanitizable (e.g. Sample<T>Response.items, <T>WatchChange.item).
type javaEnvelopeSanField struct {
	Field  model.Field
	Target javaSanTarget
}

// envelopeSanFields returns the sanitizable message-typed fields of fullType.
// Empty when fullType is itself sanitizable (callers sanitize it whole instead).
func (es *javaEgressSanitizer) envelopeSanFields(fullType, servicePackage string, crossPkgHelpers map[string]string) []javaEnvelopeSanField {
	if es == nil || fullType == "" {
		return nil
	}
	if _, ok := es.sanitizableType(fullType, servicePackage, crossPkgHelpers); ok {
		return nil
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []javaEnvelopeSanField
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
		if t, ok := es.sanitizableType(fpkg+"."+f.Type, servicePackage, crossPkgHelpers); ok {
			out = append(out, javaEnvelopeSanField{Field: f, Target: t})
		}
	}
	return out
}

// javaEgressSanitizePreamble renders the once-per-RPC setup: collect the
// caller's token data grants from the caller's VERIFIED token (fail-closed — an
// RPC where nothing verified a token has no caller and therefore no grants,
// hence the most-protective view), snapshot the ANONYMIZE key, and build each
// needed per-type entitlement. targets may contain duplicates; deduped
// preserving order.
func javaEgressSanitizePreamble(targets []javaSanTarget) string {
	if len(targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
            // Egress data entitlement (RED-1): the wrapper — never the handler — sanitizes
            // every emitted record. Grants come from the caller's VERIFIED token
            // (OPA-stamped); absent caller/claim/grant is fail-closed to the
            // most-protective view.
            java.util.Set<String> __dataGrants = new java.util.HashSet<>();
            TokenWrapper __callerToken = Auth.verifiedTokenFromContext();
            if (__callerToken != null) {
                var __serviceEntitlements = __callerToken.getServiceEntitlements();
                if (__serviceEntitlements != null) {
                    __dataGrants.addAll(__serviceEntitlements.getDataGrantsList());
                }
            }
            final byte[] __hmacKey = this.anonymizeHmacKey;
`)
	seen := map[string]bool{}
	for _, t := range targets {
		key := t.Helpers + "." + t.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(&b, "            final var %s = %s;\n", t.entVar(), t.builderCall())
	}
	return b.String()
}
