// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// writeRelease emits the recursive data-entitlement Sanitize<T> transforms for
// every entitlement-relevant message in pf, plus the once-per-file keyed-HMAC
// primitive(s) when any ANONYMIZE field is present. Rust twin of the Go sanitize
// generator: records are stored raw and the store applies Sanitize at
// Sample/Pull/Watch with a per-caller entitlement.
// rustSanitizeNeeds records which once-per-file HMAC primitives the generated
// sanitize code references.
type rustSanitizeNeeds struct {
	str, bytes, mac, email, ipv4, ipv6, fqdn bool
}

func writeRelease(b *strings.Builder, pf model.ProtoFile, allFiles []model.ProtoFile) error {
	resolver := parser.NewMessageResolver(allFiles)
	n := &rustSanitizeNeeds{}
	for mi := range pf.Messages {
		if !resolver.HasEntitlement(pf.Messages[mi].Name) {
			continue // no generated <T>Entitlement (e.g. an infra wrapper) → no sanitize
		}
		tree := parser.BuildEntitlementTree(&pf.Messages[mi], pf.Package, resolver)
		if !tree.IsRelevant() {
			continue
		}
		generateRustSanitize(b, tree, n)
		generateRustEntitlementBuilder(b, tree)
	}
	generateRustHmacHelpers(b, n)
	return nil
}

// generateRustEntitlementBuilder emits the caller → <T>Entitlement builder: a
// mechanical map from the caller's token data grants (TokenEntitlements.data_grants,
// OPA-stamped at mint) onto a <T>Entitlement. Fail-closed: a MAY_* field with a
// matching grant is set to RAW (3); absent a grant it stays UNSPECIFIED (0) and
// the sanitize transform applies its most-protective default. Nested
// sub-entitlements are constructed INLINE following the cycle-guarded tree
// (never by a cross-call, which would loop forever on mutually recursive record
// types); a tree edge cut by the cycle guard yields None, which sanitize treats
// as fully protective.
func generateRustEntitlementBuilder(b *strings.Builder, node *parser.EntitlementNode) {
	t := node.Message.Name
	fn := "build_" + strutil.ToSnakeCase(t) + "_entitlement"
	entType := t + "Entitlement"
	fmt.Fprintf(b, "/// %[1]s maps the caller's token data grants onto a %[2]s.\n", fn, entType)
	b.WriteString("/// Fail-closed: a MAY_* field with a matching grant is set to RAW; absent a grant\n")
	b.WriteString("/// it stays UNSPECIFIED and the sanitize transform applies its protective default.\n")
	fmt.Fprintf(b, "pub fn %[1]s(data_grants: &std::collections::HashSet<String>) -> %[2]s {\n", fn, entType)
	fmt.Fprintf(b, "    let mut ent = %s::default();\n", entType)
	writeRustEntitlementFields(b, node, "ent", "    ")
	b.WriteString("    ent\n}\n\n")
}

// writeRustEntitlementFields emits the grant checks and inline sub-entitlement
// construction for one tree node, assigning into the variable v.
func writeRustEntitlementFields(b *strings.Builder, node *parser.EntitlementNode, v, indent string) {
	for _, ef := range node.Fields {
		rf := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name))
		if ef.Mode == "may_redact" || ef.Mode == "may_anonymize" {
			token := parser.DataGrantToken(node.Package, node.Message.Name, ef.Field.Name)
			fmt.Fprintf(b, "%sif data_grants.contains(%q) {\n%s    %s.%s = 3; // ENTITLEMENT_LEVEL_RAW\n%s}\n", indent, token, indent, v, rf, indent)
		}
		// Field-by-field recursion mirrors the <T>Entitlement _sub shape: only a
		// same-package relevant subtree gets a sub-entitlement.
		if ef.Field.IsMessage && ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == node.Package) {
			subField := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name + "_sub"))
			fmt.Fprintf(b, "%s%s.%s = Some({\n", indent, v, subField)
			fmt.Fprintf(b, "%s    let mut sub = %sEntitlement::default();\n", indent, ef.Field.Type)
			writeRustEntitlementFields(b, ef.Nested, "sub", indent+"    ")
			fmt.Fprintf(b, "%s    sub\n%s});\n", indent, indent)
		}
	}
}

// generateRustSanitize emits the recursive sanitize_<t> transform for one message.
// The <T>Entitlement / EntitlementLevel types come from proto; this emits behavior.
// FAIL-CLOSED: an unset level (UNSPECIFIED=0 — including a None ent) applies each
// MAY_* field's most-protective default; only an explicit RAW (3) egresses the
// value. Prost stores an enum field as i32, so levels compare against
// 0(UNSPECIFIED)/1(ANONYMIZE)/2(REDACT)/3(RAW).
func generateRustSanitize(b *strings.Builder, node *parser.EntitlementNode, n *rustSanitizeNeeds) {
	t := node.Message.Name
	fn := "sanitize_" + strutil.ToSnakeCase(t)
	entType := t + "Entitlement"
	fmt.Fprintf(b, "/// %[1]s returns a data-entitlement-sanitized copy of m. FAIL-CLOSED: a None ent\n", fn)
	fmt.Fprintf(b, "/// (or an unset level) applies each MAY_* field's most-protective default; only an\n")
	fmt.Fprintf(b, "/// explicit RAW level egresses a field raw.\n")
	fmt.Fprintf(b, "pub fn %[1]s(m: &%[2]s, ent: Option<&%[3]s>, hmac_key: &[u8]) -> %[2]s {\n", fn, t, entType)
	b.WriteString("    let mut out = m.clone();\n")
	for _, ef := range node.Fields {
		generateRustSanitizeField(b, ef, t, node.Package, n)
	}
	b.WriteString("    out\n}\n\n")
}

func generateRustSanitizeField(b *strings.Builder, ef parser.EntitlementField, msgName, pkg string, n *rustSanitizeNeeds) {
	rf := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name))
	indicator := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name + "_redacted"))
	callerDecided := ef.Mode == "may_redact" || ef.Mode == "may_anonymize"

	if ef.Field.IsMessage {
		recurses := ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == pkg)
		if ef.Field.IsOneOf {
			// A message-typed ONEOF member can never carry a redact mode
			// (ValidateDataEntitlementAnnotations rejects it) — only field-by-field
			// recursion applies. Prost stores the oneof as
			// Option<<snake_msg>::<PascalOneof>> with one variant per member; the
			// sanitized copy is re-wrapped into the member's variant. Any other
			// active variant is left untouched.
			if recurses {
				oneofField := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.OneOfName))
				enumPath := strutil.ToSnakeCase(msgName) + "::" + strutil.ToPascalCase(ef.Field.OneOfName)
				variant := strutil.ToPascalCase(ef.Field.Name)
				subField := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name + "_sub"))
				subFn := "sanitize_" + strutil.ToSnakeCase(ef.Field.Type)
				fmt.Fprintf(b, "    if let Some(%s::%s(v)) = out.%s.as_ref() {\n", enumPath, variant, oneofField)
				fmt.Fprintf(b, "        out.%s = Some(%s::%s(%s(v, ent.and_then(|e| e.%s.as_ref()), hmac_key)));\n", oneofField, enumPath, variant, subFn, subField)
				b.WriteString("    }\n")
			}
			return
		}
		subField := escapeRustKeyword(strutil.ToSnakeCase(ef.Field.Name + "_sub"))
		// Whole-clear: prost stores a singular message as Option<T> (None clears) and
		// a repeated message as Vec<T> (empty Vec clears).
		clearExpr := "None"
		if ef.Field.IsRepeated {
			clearExpr = "Vec::new()"
		}
		switch {
		case ef.Mode == "drop":
			fmt.Fprintf(b, "    out.%s = %s;\n    out.%s = true;\n", rf, clearExpr, indicator)
		case callerDecided:
			// Fail-closed: only an explicit RAW keeps the subtree; anything else
			// (UNSPECIFIED/ANONYMIZE/REDACT) clears it whole.
			fmt.Fprintf(b, "    if ent.map(|e| e.%s).unwrap_or(0) != 3 {\n        out.%s = %s;\n        out.%s = true;\n", rf, rf, clearExpr, indicator)
			if recurses {
				b.WriteString("    } else {\n")
				rustMsgRecurse(b, ef, rf, subField, "        ")
			}
			b.WriteString("    }\n")
		case recurses:
			rustMsgRecurse(b, ef, rf, subField, "    ")
		}
		return
	}

	switch ef.Mode {
	case "drop":
		fmt.Fprintf(b, "    out.%s = Default::default();\n    out.%s = true;\n", rf, indicator)
	case "pseudonymize":
		rustAnonymize(b, ef, rf, indicator, "    ", n)
	case "may_redact":
		// Fail-closed: UNSPECIFIED (and REDACT) redact; ANONYMIZE loosens to a
		// surrogate; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "    match ent.map(|e| e.%s).unwrap_or(0) {\n", rf)
		b.WriteString("        3 => {} // RAW — explicit caller grant\n")
		b.WriteString("        1 => {\n")
		rustAnonymize(b, ef, rf, indicator, "            ", n)
		b.WriteString("        }\n")
		fmt.Fprintf(b, "        _ => { out.%s = Default::default(); out.%s = true; }\n    }\n", rf, indicator)
	case "may_anonymize":
		// Fail-closed: UNSPECIFIED (and ANONYMIZE) anonymize; REDACT tightens to a
		// clear; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "    match ent.map(|e| e.%s).unwrap_or(0) {\n", rf)
		b.WriteString("        3 => {} // RAW — explicit caller grant\n")
		fmt.Fprintf(b, "        2 => { out.%s = Default::default(); out.%s = true; }\n", rf, indicator)
		b.WriteString("        _ => {\n")
		rustAnonymize(b, ef, rf, indicator, "            ", n)
		b.WriteString("        }\n    }\n")
	}
}

// rustMsgRecurse emits field-by-field recursion into a message-typed field. A
// repeated field (Vec<T>) recurses ELEMENT-WISE; a singular field (Option<T>)
// recurses once. Recursion is UNCONDITIONAL on the sub-entitlement: a None sub
// is fully protective inside sanitize_<sub> (fail-closed), and nested hard
// floors must apply regardless of the caller's entitlement.
func rustMsgRecurse(b *strings.Builder, ef parser.EntitlementField, rf, subField, indent string) {
	subFn := "sanitize_" + strutil.ToSnakeCase(ef.Field.Type)
	if ef.Field.IsRepeated {
		fmt.Fprintf(b, "%s{\n", indent)
		fmt.Fprintf(b, "%s    let sub = ent.and_then(|e| e.%s.as_ref());\n", indent, subField)
		fmt.Fprintf(b, "%s    for v in out.%s.iter_mut() {\n", indent, rf)
		fmt.Fprintf(b, "%s        *v = %s(v, sub, hmac_key);\n", indent, subFn)
		fmt.Fprintf(b, "%s    }\n%s}\n", indent, indent)
		return
	}
	fmt.Fprintf(b, "%sif let Some(v) = out.%s.as_ref() {\n", indent, rf)
	fmt.Fprintf(b, "%s    out.%s = Some(%s(v, ent.and_then(|e| e.%s.as_ref()), hmac_key));\n%s}\n", indent, rf, subFn, subField, indent)
}

// rustAnonymize emits ANONYMIZE for a leaf: string/bytes get the keyed surrogate;
// any other type clears (cannot HMAC a non-string value).
func rustAnonymize(b *strings.Builder, ef parser.EntitlementField, rf, indicator, indent string, n *rustSanitizeNeeds) {
	switch ef.Field.Type {
	case "string":
		prim := rustAnonymizePrimitive(ef.Field, n)
		fmt.Fprintf(b, "%[1]sout.%[2]s = %[4]s(&out.%[2]s, hmac_key);\n%[1]sout.%[3]s = true;\n", indent, rf, indicator, prim)
	case "bytes":
		n.bytes = true
		fmt.Fprintf(b, "%[1]sout.%[2]s = tacplus_pseudonymize_bytes(&out.%[2]s, hmac_key);\n%[1]sout.%[3]s = true;\n", indent, rf, indicator)
	default:
		fmt.Fprintf(b, "%[1]sout.%[2]s = Default::default();\n%[1]sout.%[3]s = true;\n", indent, rf, indicator)
	}
}

// rustAnonymizePrimitive picks the string anonymize primitive by the field's format
// annotation (matching the Go dispatch) and records the need.
func rustAnonymizePrimitive(f model.Field, n *rustSanitizeNeeds) string {
	ipType := func() string {
		if a, ok := f.Annotations["assert_ipaddr_type"]; ok {
			return a.EnumValue
		}
		return ""
	}()
	switch {
	case hasRustAnn(f, "assert_email"):
		n.email, n.mac = true, true
		return "tacplus_pseudonymize_email"
	case hasRustAnn(f, "assert_fqdn"):
		n.fqdn, n.mac = true, true
		return "tacplus_pseudonymize_fqdn"
	case ipType == "IP_ADDR_TYPE_IP_V4":
		n.ipv4, n.mac = true, true
		return "tacplus_pseudonymize_ipv4"
	case ipType == "IP_ADDR_TYPE_IP_V6":
		n.ipv6, n.mac = true, true
		return "tacplus_pseudonymize_ipv6"
	default:
		n.str = true
		return "tacplus_pseudonymize"
	}
}

func hasRustAnn(f model.Field, ann string) bool { _, ok := f.Annotations[ann]; return ok }

// generateRustHmacHelpers emits exactly the HMAC primitives the sanitize code used.
func generateRustHmacHelpers(b *strings.Builder, n *rustSanitizeNeeds) {
	if n.str {
		generateRustPseudonymizeHelper(b)
	}
	if n.bytes {
		generateRustPseudonymizeBytesHelper(b)
	}
	if n.mac {
		b.WriteString(`/// tacplus_mac returns HMAC-SHA256(key, s) — the shared keyed core for the
/// format-preserving anonymize primitives.
fn tacplus_mac(s: &str, key: &[u8]) -> Vec<u8> {
    use hmac::{Hmac, Mac};
    use sha2::Sha256;
    let mut mac = <Hmac<Sha256>>::new_from_slice(key).expect("HMAC accepts a key of any length");
    mac.update(s.as_bytes());
    mac.finalize().into_bytes().to_vec()
}

`)
	}
	if n.email {
		b.WriteString(`/// tacplus_pseudonymize_email returns a deterministic, fully-synthetic yet valid
/// email surrogate for s under key. The .example TLD is reserved and never routable.
pub fn tacplus_pseudonymize_email(s: &str, key: &[u8]) -> String {
    if key.is_empty() {
        panic!("tacplus_pseudonymize_email: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if s.is_empty() {
        return String::new();
    }
    let m = tacplus_mac(s, key);
    format!("u{}@h{}.example", hex::encode(&m[0..6]), hex::encode(&m[6..10]))
}

`)
	}
	if n.fqdn {
		b.WriteString(`/// tacplus_pseudonymize_fqdn returns a deterministic synthetic valid hostname.
pub fn tacplus_pseudonymize_fqdn(s: &str, key: &[u8]) -> String {
    if key.is_empty() {
        panic!("tacplus_pseudonymize_fqdn: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if s.is_empty() {
        return String::new();
    }
    let m = tacplus_mac(s, key);
    format!("h{}.example", hex::encode(&m[0..8]))
}

`)
	}
	if n.ipv4 {
		b.WriteString(`/// tacplus_pseudonymize_ipv4 returns a deterministic surrogate in the RFC-1918
/// private range 10.0.0.0/8 — always a valid IPv4, never public.
pub fn tacplus_pseudonymize_ipv4(s: &str, key: &[u8]) -> String {
    if key.is_empty() {
        panic!("tacplus_pseudonymize_ipv4: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if s.is_empty() {
        return String::new();
    }
    let m = tacplus_mac(s, key);
    format!("10.{}.{}.{}", m[0], m[1], m[2])
}

`)
	}
	if n.ipv6 {
		b.WriteString(`/// tacplus_pseudonymize_ipv6 returns a deterministic surrogate in the RFC-3849
/// documentation range 2001:db8::/32 — always a valid IPv6.
pub fn tacplus_pseudonymize_ipv6(s: &str, key: &[u8]) -> String {
    if key.is_empty() {
        panic!("tacplus_pseudonymize_ipv6: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if s.is_empty() {
        return String::new();
    }
    let m = tacplus_mac(s, key);
    format!("2001:db8:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}:{:02x}{:02x}",
        m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8], m[9], m[10], m[11])
}

`)
	}
}

// generateRustPseudonymizeHelper emits the once-per-file pseudonymize primitive:
// a keyed HMAC-SHA256 over s, hex-encoded with a "pseud:" prefix. The same (s, key)
// yields the same token so pseudonymized records still join on the field, but without
// the per-deployment key the token cannot be reversed by hashing the (possibly
// enumerable) input space. An empty key is a misconfiguration and panics rather than
// emitting a dictionary-reversible token.
func generateRustPseudonymizeHelper(b *strings.Builder) {
	b.WriteString(`/// tacplus_pseudonymize returns a keyed, non-reversible pseudonym for s — an
/// HMAC-SHA256 over s under key, hex-encoded with a "pseud:" prefix. key must be
/// non-empty; an empty key is a misconfiguration and panics rather than emitting a
/// dictionary-reversible token.
pub fn tacplus_pseudonymize(s: &str, key: &[u8]) -> String {
    if key.is_empty() {
        panic!("tacplus_pseudonymize: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if s.is_empty() {
        return String::new();
    }
    use hmac::{Hmac, Mac};
    use sha2::Sha256;
    let mut mac = <Hmac<Sha256>>::new_from_slice(key).expect("HMAC accepts a key of any length");
    mac.update(s.as_bytes());
    let bytes = mac.finalize().into_bytes();
    format!("pseud:{}", hex::encode(&bytes[..16]))
}

`)
}

// generateRustPseudonymizeBytesHelper emits the once-per-file bytes pseudonymize
// primitive: the raw first-128-bits of HMAC-SHA256 over b (no prefix, no hex — the
// field is already bytes). Same (b, key) yields the same surrogate so bytes fields
// still join. Twin of Go's tacplusPseudonymizeBytes.
func generateRustPseudonymizeBytesHelper(b *strings.Builder) {
	b.WriteString(`/// tacplus_pseudonymize_bytes returns a keyed, non-reversible pseudonym for b —
/// the first 128 bits of HMAC-SHA256 over b under key. key must be non-empty; an
/// empty key is a misconfiguration and panics.
pub fn tacplus_pseudonymize_bytes(b: &[u8], key: &[u8]) -> Vec<u8> {
    if key.is_empty() {
        panic!("tacplus_pseudonymize_bytes: empty pseudonym key (store must supply a per-deployment secret)");
    }
    if b.is_empty() {
        return Vec::new();
    }
    use hmac::{Hmac, Mac};
    use sha2::Sha256;
    let mut mac = <Hmac<Sha256>>::new_from_slice(key).expect("HMAC accepts a key of any length");
    mac.update(b);
    let bytes = mac.finalize().into_bytes();
    bytes[..16].to_vec()
}

`)
}
