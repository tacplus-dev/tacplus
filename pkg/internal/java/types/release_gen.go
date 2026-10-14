// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// writeRelease emits the recursive data-entitlement Sanitize<T> transforms for
// every entitlement-relevant message in pf, plus the once-per-file keyed-HMAC
// primitive(s) when any ANONYMIZE field is present. Java twin of the Go/Rust
// sanitize generator: records are stored raw and the store applies Sanitize at
// Sample/Pull/Watch with a per-caller entitlement.
// javaSanitizeNeeds records which once-per-file HMAC primitives the sanitize code
// references.
type javaSanitizeNeeds struct {
	str, bytes, mac, email, ipv4, ipv6, fqdn bool
}

func writeRelease(b *strings.Builder, pf model.ProtoFile, outerClassName string) error {
	// Same-file resolver: covers a record and its sub-messages declared in the same
	// proto (the common case). Cross-file same-package recursion falls back to
	// whole-message redaction.
	resolver := parser.NewMessageResolver([]model.ProtoFile{pf})
	n := &javaSanitizeNeeds{}
	for mi := range pf.Messages {
		if !resolver.HasEntitlement(pf.Messages[mi].Name) {
			continue // no generated <T>Entitlement (e.g. an infra wrapper) → no sanitize
		}
		tree := parser.BuildEntitlementTree(&pf.Messages[mi], pf.Package, resolver)
		if !tree.IsRelevant() {
			continue
		}
		generateJavaSanitize(b, tree, msgOuterClass(pf.Messages[mi], outerClassName), n)
		generateJavaEntitlementBuilder(b, tree, msgOuterClass(pf.Messages[mi], outerClassName))
	}
	generateJavaHmacHelpers(b, n)
	return nil
}

// generateJavaEntitlementBuilder emits the caller → <T>Entitlement builder: a
// mechanical map from the caller's token data grants (TokenEntitlements.data_grants,
// OPA-stamped at mint) onto a <T>Entitlement. Fail-closed: a MAY_* field with a
// matching grant is set to RAW (3); absent a grant it stays UNSPECIFIED (0) and
// the sanitize transform applies its most-protective default. Nested
// sub-entitlements are reached INLINE via get<F>SubBuilder() paths following the
// cycle-guarded tree (never by a cross-call, which would loop forever on
// mutually recursive record types); an untouched sub stays absent, which
// sanitize treats as fully protective.
func generateJavaEntitlementBuilder(b *strings.Builder, node *parser.EntitlementNode, outerClassName string) {
	t := node.Message.Name
	entType := outerClassName + "." + t + "Entitlement"
	fmt.Fprintf(b, "// build%[1]sEntitlement maps the caller's token data grants onto a %[1]sEntitlement.\n", t)
	b.WriteString("// Fail-closed: a MAY_* field with a matching grant is set to RAW; absent a grant it\n")
	b.WriteString("// stays UNSPECIFIED and the sanitize transform applies its protective default.\n")
	fmt.Fprintf(b, "public static %[2]s build%[1]sEntitlement(java.util.Set<String> dataGrants) {\n", t, entType)
	fmt.Fprintf(b, "%s.Builder ent = %s.newBuilder();\n", entType, entType)
	writeJavaEntitlementFields(b, node, "ent")
	b.WriteString("return ent.build();\n}\n")
}

// writeJavaEntitlementFields emits the grant checks for one tree node, assigning
// through the builder accessor path.
func writeJavaEntitlementFields(b *strings.Builder, node *parser.EntitlementNode, path string) {
	for _, ef := range node.Fields {
		p := javaPascalField(ef.Field.Name)
		if ef.Mode == "may_redact" || ef.Mode == "may_anonymize" {
			token := parser.DataGrantToken(node.Package, node.Message.Name, ef.Field.Name)
			fmt.Fprintf(b, "if (dataGrants.contains(%q)) { %s.set%sValue(3); /* ENTITLEMENT_LEVEL_RAW */ }\n", token, path, p)
		}
		// Field-by-field recursion mirrors the <T>Entitlement _sub shape: only a
		// same-package relevant subtree gets a sub-entitlement.
		if ef.Field.IsMessage && ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == node.Package) {
			subName := javaPascalField(ef.Field.Name + "_sub")
			writeJavaEntitlementFields(b, ef.Nested, path+".get"+subName+"Builder()")
		}
	}
}

// generateJavaSanitize emits the recursive sanitize<T> transform for one message.
// The <T>Entitlement / EntitlementLevel types come from proto; this emits behavior.
// FAIL-CLOSED: an unset level (UNSPECIFIED=0 — including a null ent) applies each
// MAY_* field's most-protective default; only an explicit RAW (3) egresses the
// value. The enum level is read via get<F>Value() (the int form) so no enum type
// import is needed: UNSPECIFIED=0, ANONYMIZE=1, REDACT=2, RAW=3.
func generateJavaSanitize(b *strings.Builder, node *parser.EntitlementNode, outerClassName string, n *javaSanitizeNeeds) {
	t := node.Message.Name
	fullType := outerClassName + "." + t
	entType := outerClassName + "." + t + "Entitlement"
	fmt.Fprintf(b, "// sanitize%[1]s returns a data-entitlement-sanitized copy of m. FAIL-CLOSED: a null\n", t)
	fmt.Fprintf(b, "// ent (or an unset level) applies each MAY_* field's most-protective default; only\n")
	fmt.Fprintf(b, "// an explicit RAW level egresses a field raw.\n")
	fmt.Fprintf(b, "public static %[1]s sanitize%[2]s(%[1]s m, %[3]s ent, byte[] hmacKey) {\n", fullType, t, entType)
	b.WriteString("if (m == null) { return null; }\n")
	fmt.Fprintf(b, "%s.Builder out = m.toBuilder();\n", fullType)
	for _, ef := range node.Fields {
		generateJavaSanitizeField(b, ef, node.Package, n)
	}
	b.WriteString("return out.build();\n}\n")
}

func generateJavaSanitizeField(b *strings.Builder, ef parser.EntitlementField, pkg string, n *javaSanitizeNeeds) {
	p := javaPascalField(ef.Field.Name)
	indicator := javaPascalField(ef.Field.Name + "_redacted")
	callerDecided := ef.Mode == "may_redact" || ef.Mode == "may_anonymize"

	if ef.Field.IsMessage {
		recurses := ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == pkg)
		subName := javaPascalField(ef.Field.Name + "_sub")
		switch {
		case ef.Mode == "drop":
			fmt.Fprintf(b, "out.clear%s();\nout.set%s(true);\n", p, indicator)
		case callerDecided:
			// Fail-closed: only an explicit RAW keeps the subtree; anything else
			// (UNSPECIFIED/ANONYMIZE/REDACT) clears it whole.
			fmt.Fprintf(b, "if ((ent == null ? 0 : ent.get%sValue()) != 3) {\nout.clear%s();\nout.set%s(true);\n", p, p, indicator)
			if recurses {
				b.WriteString("} else {\n")
				javaMsgRecurse(b, ef, p, subName)
			}
			b.WriteString("}\n")
		case recurses:
			javaMsgRecurse(b, ef, p, subName)
		}
		return
	}

	switch ef.Mode {
	case "drop":
		fmt.Fprintf(b, "out.clear%s();\nout.set%s(true);\n", p, indicator)
	case "pseudonymize":
		javaAnonymize(b, ef, p, indicator, n)
	case "may_redact":
		// Fail-closed: UNSPECIFIED (and REDACT) redact; ANONYMIZE loosens to a
		// surrogate; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "{\nint lvl = ent == null ? 0 : ent.get%sValue();\n", p)
		b.WriteString("if (lvl == 3) { /* RAW — explicit caller grant */ }\n")
		b.WriteString("else if (lvl == 1) {\n")
		javaAnonymize(b, ef, p, indicator, n)
		b.WriteString("}\n")
		fmt.Fprintf(b, "else { out.clear%s(); out.set%s(true); }\n}\n", p, indicator)
	case "may_anonymize":
		// Fail-closed: UNSPECIFIED (and ANONYMIZE) anonymize; REDACT tightens to a
		// clear; only an explicit RAW egresses the value.
		fmt.Fprintf(b, "{\nint lvl = ent == null ? 0 : ent.get%sValue();\n", p)
		b.WriteString("if (lvl == 3) { /* RAW — explicit caller grant */ }\n")
		fmt.Fprintf(b, "else if (lvl == 2) { out.clear%s(); out.set%s(true); }\n", p, indicator)
		b.WriteString("else {\n")
		javaAnonymize(b, ef, p, indicator, n)
		b.WriteString("}\n}\n")
	}
}

// javaMsgRecurse emits field-by-field recursion into a message-typed field. A
// repeated field iterates element-wise by index (no hasX for repeated); a singular
// field recurses once. Recursion is UNCONDITIONAL on the sub-entitlement: a null
// sub is fully protective inside sanitize<Sub> (fail-closed), and nested hard
// floors must apply regardless of the caller's entitlement.
func javaMsgRecurse(b *strings.Builder, ef parser.EntitlementField, p, subName string) {
	sub := fmt.Sprintf("(ent == null ? null : ent.get%s())", subName)
	if ef.Field.IsRepeated {
		fmt.Fprintf(b, "for (int i = 0; i < out.get%sCount(); i++) {\n", p)
		fmt.Fprintf(b, "out.set%s(i, sanitize%s(out.get%s(i), %s, hmacKey));\n}\n", p, ef.Field.Type, p, sub)
		return
	}
	fmt.Fprintf(b, "if (out.has%s()) {\n", p)
	fmt.Fprintf(b, "out.set%s(sanitize%s(out.get%s(), %s, hmacKey));\n}\n", p, ef.Field.Type, p, sub)
}

// javaAnonymize emits ANONYMIZE for a leaf: string/bytes get the keyed surrogate;
// any other type clears (cannot HMAC a non-string value).
func javaAnonymize(b *strings.Builder, ef parser.EntitlementField, p, indicator string, n *javaSanitizeNeeds) {
	switch ef.Field.Type {
	case "string":
		prim := javaAnonymizePrimitive(ef.Field, n)
		fmt.Fprintf(b, "out.set%[1]s(%[3]s(out.get%[1]s(), hmacKey));\nout.set%[2]s(true);\n", p, indicator, prim)
	case "bytes":
		n.bytes = true
		fmt.Fprintf(b, "out.set%[1]s(tacplusPseudonymizeBytes(out.get%[1]s(), hmacKey));\nout.set%[2]s(true);\n", p, indicator)
	default:
		fmt.Fprintf(b, "out.clear%s();\nout.set%s(true);\n", p, indicator)
	}
}

// javaAnonymizePrimitive picks the string anonymize primitive by the field's format
// annotation (matching the Go/Rust dispatch) and records the need.
func javaAnonymizePrimitive(f model.Field, n *javaSanitizeNeeds) string {
	ipType := ""
	if a, ok := f.Annotations["assert_ipaddr_type"]; ok {
		ipType = a.EnumValue
	}
	switch {
	case hasJavaAnn(f, "assert_email"):
		n.email, n.mac = true, true
		return "tacplusPseudonymizeEmail"
	case hasJavaAnn(f, "assert_fqdn"):
		n.fqdn, n.mac = true, true
		return "tacplusPseudonymizeFqdn"
	case ipType == "IP_ADDR_TYPE_IP_V4":
		n.ipv4, n.mac = true, true
		return "tacplusPseudonymizeIpv4"
	case ipType == "IP_ADDR_TYPE_IP_V6":
		n.ipv6, n.mac = true, true
		return "tacplusPseudonymizeIpv6"
	default:
		n.str = true
		return "tacplusPseudonymize"
	}
}

func hasJavaAnn(f model.Field, ann string) bool { _, ok := f.Annotations[ann]; return ok }

// javaPascalField returns the PascalCase form of a proto field name used in
// getX/setX/clearX/revealX accessors (matches the getter convention in validation.go).
func javaPascalField(name string) string {
	return javaFieldNameUpper(name)
}

// generateJavaHmacHelpers emits exactly the HMAC primitives the sanitize code used.
func generateJavaHmacHelpers(b *strings.Builder, n *javaSanitizeNeeds) {
	if n.str {
		generateJavaPseudonymizeHelper(b)
	}
	if n.bytes {
		generateJavaPseudonymizeBytesHelper(b)
	}
	if n.mac {
		b.WriteString(`// tacplusMac returns HMAC-SHA256(key, s); tacplusHex hex-encodes a byte range.
// Shared core for the format-preserving anonymize primitives.
static byte[] tacplusMac(String s, byte[] key) {
try {
javax.crypto.Mac mac = javax.crypto.Mac.getInstance("HmacSHA256");
mac.init(new javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"));
return mac.doFinal(s.getBytes(java.nio.charset.StandardCharsets.UTF_8));
} catch (java.security.GeneralSecurityException e) { throw new RuntimeException("tacplusMac failed", e); }
}
static String tacplusHex(byte[] b, int from, int to) {
StringBuilder sb = new StringBuilder();
for (int i = from; i < to; i++) { sb.append(String.format("%02x", b[i] & 0xff)); }
return sb.toString();
}
`)
	}
	if n.email {
		b.WriteString(`// tacplusPseudonymizeEmail returns a deterministic, fully-synthetic yet valid email
// surrogate. The .example TLD is reserved and never routable.
public static String tacplusPseudonymizeEmail(String s, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymizeEmail: empty pseudonym key (store must supply a per-deployment secret)"); }
if (s.isEmpty()) { return ""; }
byte[] m = tacplusMac(s, key);
return "u" + tacplusHex(m, 0, 6) + "@h" + tacplusHex(m, 6, 10) + ".example";
}
`)
	}
	if n.fqdn {
		b.WriteString(`// tacplusPseudonymizeFqdn returns a deterministic synthetic valid hostname.
public static String tacplusPseudonymizeFqdn(String s, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymizeFqdn: empty pseudonym key (store must supply a per-deployment secret)"); }
if (s.isEmpty()) { return ""; }
byte[] m = tacplusMac(s, key);
return "h" + tacplusHex(m, 0, 8) + ".example";
}
`)
	}
	if n.ipv4 {
		b.WriteString(`// tacplusPseudonymizeIpv4 returns a deterministic surrogate in the RFC-1918 private
// range 10.0.0.0/8 — always a valid IPv4, never public.
public static String tacplusPseudonymizeIpv4(String s, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymizeIpv4: empty pseudonym key (store must supply a per-deployment secret)"); }
if (s.isEmpty()) { return ""; }
byte[] m = tacplusMac(s, key);
return "10." + (m[0] & 0xff) + "." + (m[1] & 0xff) + "." + (m[2] & 0xff);
}
`)
	}
	if n.ipv6 {
		b.WriteString(`// tacplusPseudonymizeIpv6 returns a deterministic surrogate in the RFC-3849
// documentation range 2001:db8::/32 — always a valid IPv6.
public static String tacplusPseudonymizeIpv6(String s, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymizeIpv6: empty pseudonym key (store must supply a per-deployment secret)"); }
if (s.isEmpty()) { return ""; }
byte[] m = tacplusMac(s, key);
return "2001:db8:" + tacplusHex(m, 0, 2) + ":" + tacplusHex(m, 2, 4) + ":" + tacplusHex(m, 4, 6)
     + ":" + tacplusHex(m, 6, 8) + ":" + tacplusHex(m, 8, 10) + ":" + tacplusHex(m, 10, 12);
}
`)
	}
}

// generateJavaPseudonymizeHelper emits the once-per-file pseudonymize primitive:
// a keyed HMAC-SHA256 (JDK javax.crypto) over s, hex of the first 16 bytes with a
// "pseud:" prefix — byte-identical to the Go/Rust tokens. An empty key panics
// rather than emitting a dictionary-reversible token.
func generateJavaPseudonymizeHelper(b *strings.Builder) {
	b.WriteString(`// tacplusPseudonymize returns a keyed, non-reversible pseudonym for s — an
// HMAC-SHA256 over s under key, hex-encoded (first 16 bytes) with a "pseud:" prefix.
// key must be non-empty; an empty key is a misconfiguration and throws.
public static String tacplusPseudonymize(String s, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymize: empty pseudonym key (store must supply a per-deployment secret)"); }
if (s.isEmpty()) { return ""; }
try {
javax.crypto.Mac mac = javax.crypto.Mac.getInstance("HmacSHA256");
mac.init(new javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"));
byte[] full = mac.doFinal(s.getBytes(java.nio.charset.StandardCharsets.UTF_8));
StringBuilder sb = new StringBuilder("pseud:");
for (int i = 0; i < 16; i++) { sb.append(String.format("%02x", full[i] & 0xff)); }
return sb.toString();
} catch (java.security.GeneralSecurityException e) {
throw new RuntimeException("tacplusPseudonymize failed", e);
}
}
`)
}

// generateJavaPseudonymizeBytesHelper emits the once-per-file bytes pseudonymize
// primitive: the raw first 16 bytes of HMAC-SHA256 over the ByteString (no prefix,
// no hex — the field is already bytes). Twin of Go's tacplusPseudonymizeBytes.
func generateJavaPseudonymizeBytesHelper(b *strings.Builder) {
	b.WriteString(`// tacplusPseudonymizeBytes returns a keyed, non-reversible pseudonym for b — the
// first 16 bytes of HMAC-SHA256 over b under key. key must be non-empty; an empty
// key is a misconfiguration and throws.
public static com.google.protobuf.ByteString tacplusPseudonymizeBytes(com.google.protobuf.ByteString b, byte[] key) {
if (key.length == 0) { throw new IllegalStateException("tacplusPseudonymizeBytes: empty pseudonym key (store must supply a per-deployment secret)"); }
if (b.isEmpty()) { return b; }
try {
javax.crypto.Mac mac = javax.crypto.Mac.getInstance("HmacSHA256");
mac.init(new javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"));
byte[] full = mac.doFinal(b.toByteArray());
return com.google.protobuf.ByteString.copyFrom(full, 0, 16);
} catch (java.security.GeneralSecurityException e) {
throw new RuntimeException("tacplusPseudonymizeBytes failed", e);
}
}
`)
}
