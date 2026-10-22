// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// This file emits the recursive data-entitlement Sanitize<T> transforms for Go.
// The <T>Entitlement / EntitlementLevel TYPES are generated as proto (see
// AppendEntitlementMessagesToProtos + sys/entitlement.proto) and compiled by
// protoc-gen-go; this emits only the BEHAVIOR. Hard-floor fields (DROP /
// PSEUDONYMIZE) always apply, caller-independent. Caller-decided (MAY_*) fields
// are FAIL-CLOSED: absent an explicit level (UNSPECIFIED = 0, the proto3
// default) the field gets its MOST-PROTECTIVE default — MAY_REDACT redacts,
// MAY_ANONYMIZE anonymizes; only an explicit RAW (3, set by the generated
// Build<T>Entitlement from a matching token data grant) egresses the value raw.
// REDACT clears, ANONYMIZE keyed-HMACs a string/bytes value — each setting the
// injected <field>_redacted indicator. A message-typed field redacted whole
// clears the entire subtree; under RAW it recurses (pulling the sub-entitlement
// from the parent and calling Sanitize<Sub>), just like the serialization helpers.
//
// EntitlementLevel is compared via int32() literals (UNSPECIFIED=0, ANONYMIZE=1,
// REDACT=2, RAW=3) so this hand-emitted helper needs no import of the
// entitlement package. Proto nil-safe getters return 0 on a nil entitlement, so
// nil is fully protective automatically.

// generateSanitizeHelpers emits Sanitize<T> for every entitlement-relevant message
// in pf. resolver resolves message-typed fields. It records which HMAC primitives
// the emitted code needs into n so the caller emits exactly those helpers.
func generateSanitizeHelpers(b *strings.Builder, pf model.ProtoFile, resolver *parser.MessageResolver, n *sanitizeNeeds) error {
	for mi := range pf.Messages {
		msg := &pf.Messages[mi]
		if !resolver.HasEntitlement(msg.Name) {
			continue // no generated <T>Entitlement (e.g. an infra wrapper) → no Sanitize<T>
		}
		tree := parser.BuildEntitlementTree(msg, pf.Package, resolver)
		if !tree.IsRelevant() {
			continue
		}
		writeGoSanitizeFunc(b, tree, n)
	}
	return nil
}

// sanitizeNeeds records which once-per-file HMAC primitives the generated Sanitize
// code references, so only those helpers are emitted.
type sanitizeNeeds struct {
	str, bytes, mac, email, ipv4, ipv6, fqdn bool
}

// writeGoSanitizeFunc emits one Sanitize<T> function.
func writeGoSanitizeFunc(b *strings.Builder, node *parser.EntitlementNode, n *sanitizeNeeds) {
	t := node.Message.Name
	fmt.Fprintf(b, "// Sanitize%[1]s returns a data-entitlement–sanitized COPY of m under ent. FAIL-CLOSED:\n", t)
	fmt.Fprintf(b, "// a nil ent (or an unset level) applies each MAY_* field's most-protective default;\n")
	fmt.Fprintf(b, "// only an explicit RAW level egresses a field raw. hmacKey is the per-deployment\n")
	fmt.Fprintf(b, "// ANONYMIZE secret, propagated to nested sanitizers so a value pseudonymizes to the\n")
	fmt.Fprintf(b, "// same surrogate at any depth.\n")
	fmt.Fprintf(b, "func Sanitize%[1]s(m *%[1]s, ent *%[1]sEntitlement, hmacKey []byte) *%[1]s {\n", t)
	b.WriteString("\tif m == nil {\n\t\treturn nil\n\t}\n")
	fmt.Fprintf(b, "\tout := proto.Clone(m).(*%s)\n", t)
	for _, ef := range node.Fields {
		writeGoSanitizeField(b, ef, t, node.Package, n)
	}
	b.WriteString("\treturn out\n}\n\n")
}

func writeGoSanitizeField(b *strings.Builder, ef parser.EntitlementField, msgName, pkg string, n *sanitizeNeeds) {
	gf := toGoFieldName(ef.Field.Name)
	getter := "Get" + gf
	indicator := toGoFieldName(ef.Field.Name + "_redacted")
	callerDecided := ef.Mode == "may_redact" || ef.Mode == "may_anonymize"

	if ef.Field.IsMessage {
		recurses := ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == pkg)
		if ef.Field.IsOneOf {
			// A message-typed ONEOF member can never carry a redact mode
			// (ValidateDataEntitlementAnnotations rejects it) — only field-by-field
			// recursion applies. The generated struct stores the active variant
			// behind the oneof interface, so the sanitized copy is re-wrapped into
			// the member's wrapper struct; the getter is nil for any other variant.
			if recurses {
				oneofField := toGoFieldName(ef.Field.OneOfName)
				sub := "ent.Get" + gf + "Sub()"
				fmt.Fprintf(b, "\tif v := out.%s(); v != nil {\n", getter)
				fmt.Fprintf(b, "\t\tout.%s = &%s_%s{%s: Sanitize%s(v, %s, hmacKey)}\n", oneofField, msgName, gf, gf, ef.Field.Type, sub)
				b.WriteString("\t}\n")
			}
			return
		}
		switch {
		case ef.Mode == "drop":
			// is-redact whole message/list: always clear (nil for a slice too).
			fmt.Fprintf(b, "\tout.%s = nil\n\tout.%s = true\n", gf, indicator)
		case callerDecided:
			// Fail-closed: only an explicit RAW keeps the subtree; anything else
			// (UNSPECIFIED/ANONYMIZE/REDACT) clears it whole.
			fmt.Fprintf(b, "\tif int32(ent.%s()) != 3 {\n\t\tout.%s = nil\n\t\tout.%s = true\n", getter, gf, indicator)
			if recurses {
				b.WriteString("\t} else {\n")
				writeGoMsgRecurse(b, ef, gf, getter, "\t\t")
			}
			b.WriteString("\t}\n")
		case recurses:
			writeGoMsgRecurse(b, ef, gf, getter, "\t")
		}
		return
	}

	// Scalar leaf.
	switch ef.Mode {
	case "drop":
		fmt.Fprintf(b, "\tout.%s = %s\n\tout.%s = true\n", gf, goZeroExpr(ef.Field), indicator)
	case "pseudonymize":
		writeGoAnonymize(b, ef, gf, indicator, "\t", n)
	case "may_redact":
		// Fail-closed: UNSPECIFIED (and REDACT) redact; ANONYMIZE loosens to a
		// surrogate; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "\tswitch lvl := int32(ent.%s()); {\n", getter)
		b.WriteString("\tcase lvl == 3: // RAW — explicit caller grant\n")
		b.WriteString("\tcase lvl == 1:\n")
		writeGoAnonymize(b, ef, gf, indicator, "\t\t", n)
		fmt.Fprintf(b, "\tdefault:\n\t\tout.%s = %s\n\t\tout.%s = true\n", gf, goZeroExpr(ef.Field), indicator)
		b.WriteString("\t}\n")
	case "may_anonymize":
		// Fail-closed: UNSPECIFIED (and ANONYMIZE) anonymize; REDACT tightens to a
		// clear; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "\tswitch lvl := int32(ent.%s()); {\n", getter)
		b.WriteString("\tcase lvl == 3: // RAW — explicit caller grant\n")
		fmt.Fprintf(b, "\tcase lvl == 2:\n\t\tout.%s = %s\n\t\tout.%s = true\n", gf, goZeroExpr(ef.Field), indicator)
		b.WriteString("\tdefault:\n")
		writeGoAnonymize(b, ef, gf, indicator, "\t\t", n)
		b.WriteString("\t}\n")
	}
}

// writeGoMsgRecurse emits field-by-field recursion into a message-typed field. A
// repeated field recurses ELEMENT-WISE (one sub-entitlement per element); a
// singular field recurses once. Recursion is UNCONDITIONAL on the sub-entitlement:
// a nil sub is fully protective inside Sanitize<Sub> (fail-closed), and nested
// hard floors must apply regardless of the caller's entitlement.
func writeGoMsgRecurse(b *strings.Builder, ef parser.EntitlementField, gf, getter, indent string) {
	sub := "ent.Get" + gf + "Sub()"
	if ef.Field.IsRepeated {
		fmt.Fprintf(b, "%sfor i := range out.%s {\n", indent, gf)
		fmt.Fprintf(b, "%s\tout.%s[i] = Sanitize%s(out.%s[i], %s, hmacKey)\n", indent, gf, ef.Field.Type, gf, sub)
		fmt.Fprintf(b, "%s}\n", indent)
		return
	}
	fmt.Fprintf(b, "%sif out.%s() != nil {\n", indent, getter)
	fmt.Fprintf(b, "%s\tout.%s = Sanitize%s(out.%s, %s, hmacKey)\n", indent, gf, ef.Field.Type, gf, sub)
	fmt.Fprintf(b, "%s}\n", indent)
}

// writeGoAnonymize emits the ANONYMIZE application for a leaf. For a string field
// it picks a FORMAT-PRESERVING primitive when the field carries a format
// constraint (assert_email/ipaddr/fqdn) so the surrogate re-passes that validator;
// otherwise the plain pseud: token. bytes→bytes surrogate; any other type→clear.
func writeGoAnonymize(b *strings.Builder, ef parser.EntitlementField, gf, indicator, indent string, n *sanitizeNeeds) {
	switch ef.Field.Type {
	case "string":
		prim := goAnonymizePrimitive(ef.Field, n)
		fmt.Fprintf(b, "%[1]sout.%[2]s = %[4]s(out.%[2]s, hmacKey)\n%[1]sout.%[3]s = true\n", indent, gf, indicator, prim)
	case "bytes":
		n.bytes = true
		fmt.Fprintf(b, "%[1]sout.%[2]s = tacplusPseudonymizeBytes(out.%[2]s, hmacKey)\n%[1]sout.%[3]s = true\n", indent, gf, indicator)
	default:
		fmt.Fprintf(b, "%[1]sout.%[2]s = %[3]s\n%[1]sout.%[4]s = true\n", indent, gf, goZeroExpr(ef.Field), indicator)
	}
}

// goAnonymizePrimitive picks the string anonymize primitive by the field's format
// annotation and records the need. Format-preserving primitives share the keyed
// HMAC core (n.mac); the plain fallback is tacplusPseudonymize.
func goAnonymizePrimitive(f model.Field, n *sanitizeNeeds) string {
	switch {
	case has(f, "assert_email"):
		n.email, n.mac = true, true
		return "tacplusPseudonymizeEmail"
	case has(f, "assert_fqdn"):
		n.fqdn, n.mac = true, true
		return "tacplusPseudonymizeFqdn"
	case isIPv4(f):
		n.ipv4, n.mac = true, true
		return "tacplusPseudonymizeIpv4"
	case isIPv6(f):
		n.ipv6, n.mac = true, true
		return "tacplusPseudonymizeIpv6"
	default:
		n.str = true
		return "tacplusPseudonymize"
	}
}

func has(f model.Field, ann string) bool { _, ok := f.Annotations[ann]; return ok }

// isIPv4/isIPv6 read (spec.assert_ipaddr_type). Only the pure-address V4/V6 types
// format-preserve; ANY / MASK / NET / CIDR variants fall through to the plain token.
func isIPv4(f model.Field) bool {
	a, ok := f.Annotations["assert_ipaddr_type"]
	return ok && a.EnumValue == "IP_ADDR_TYPE_IP_V4"
}
func isIPv6(f model.Field) bool {
	a, ok := f.Annotations["assert_ipaddr_type"]
	return ok && a.EnumValue == "IP_ADDR_TYPE_IP_V6"
}

// generateSanitizeHmacHelpers emits exactly the HMAC primitives the generated
// Sanitize code referenced.
func generateSanitizeHmacHelpers(b *strings.Builder, n *sanitizeNeeds) {
	if n.str {
		generatePseudonymizeHelper(b)
	}
	if n.bytes {
		generatePseudonymizeBytesHelper(b)
	}
	if n.mac {
		b.WriteString(`// tacplusMac returns HMAC-SHA256(key, s) — the shared keyed core for the
// format-preserving anonymize primitives.
func tacplusMac(s string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return mac.Sum(nil)
}

`)
	}
	if n.email {
		b.WriteString(`// tacplusPseudonymizeEmail returns a deterministic, fully-synthetic yet RFC-valid
// email surrogate for s under key (same (s, key) -> same surrogate). The .example
// TLD is reserved and never routable, and no fragment of s survives.
func tacplusPseudonymizeEmail(s string, key []byte) string {
	if len(key) == 0 {
		panic("tacplusPseudonymizeEmail: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if s == "" {
		return ""
	}
	m := tacplusMac(s, key)
	return "u" + hex.EncodeToString(m[0:6]) + "@h" + hex.EncodeToString(m[6:10]) + ".example"
}

`)
	}
	if n.fqdn {
		b.WriteString(`// tacplusPseudonymizeFqdn returns a deterministic, synthetic valid hostname for s
// under key. The .example TLD is reserved.
func tacplusPseudonymizeFqdn(s string, key []byte) string {
	if len(key) == 0 {
		panic("tacplusPseudonymizeFqdn: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if s == "" {
		return ""
	}
	m := tacplusMac(s, key)
	return "h" + hex.EncodeToString(m[0:8]) + ".example"
}

`)
	}
	if n.ipv4 {
		b.WriteString(`// tacplusPseudonymizeIpv4 returns a deterministic surrogate in the RFC-1918
// private range 10.0.0.0/8 for s under key — always a valid IPv4, never public.
func tacplusPseudonymizeIpv4(s string, key []byte) string {
	if len(key) == 0 {
		panic("tacplusPseudonymizeIpv4: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if s == "" {
		return ""
	}
	m := tacplusMac(s, key)
	return fmt.Sprintf("10.%d.%d.%d", m[0], m[1], m[2])
}

`)
	}
	if n.ipv6 {
		b.WriteString(`// tacplusPseudonymizeIpv6 returns a deterministic surrogate in the RFC-3849
// documentation range 2001:db8::/32 for s under key — always a valid IPv6.
func tacplusPseudonymizeIpv6(s string, key []byte) string {
	if len(key) == 0 {
		panic("tacplusPseudonymizeIpv6: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if s == "" {
		return ""
	}
	m := tacplusMac(s, key)
	return fmt.Sprintf("2001:db8:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x:%02x%02x",
		m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8], m[9], m[10], m[11])
}

`)
	}
}
