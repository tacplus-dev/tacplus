// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
)

// The request half of the principal seam on the Java gRPC wrapper.
//
// (spec.principal) / (spec.namespace) on a REQUEST field say that the field
// names the caller: the generated wrapper resolves the authenticated caller's
// subject / namespace claim, stamps it into an empty field, accepts a field
// that already equals it, and refuses one that differs. A forged value is never
// silently overwritten. The contract in spec.proto is unqualified — it says
// "the generated wrapper", with no language or RPC-shape carve-out — but the
// Java wrapper rendered it on no shape at all, so a Java service declaring the
// marker generated with the field left exactly as the client wrote it.
//
// The mechanism is not new here: javaPredicatePrincipalPin /
// javaPredicateNamespacePin (recordprincipal_gen.go, recordnamespace_gen.go)
// have done this same stamp-or-refuse against Auth.verifiedTokenFromContext()
// on the record plane's query predicate since that plane landed. What was
// missing is a lookup that answers for a REQUEST: principalField/namespaceField
// both gate on recordMessage(), i.e. they only answer for (spec.record) types
// and only for the store_ halves, and a request message is not a record.

// annotatedStringField returns the name of fullType's field carrying the named
// annotation, or "" when it declares none. At most one such field per message
// is enforced by parser.ValidatePrincipalAnnotations. The Go and Rust
// generators carry the identical lookup (go/types/mcp_dispatch_gen.go,
// rust/types/principal_gen.go).
func (es *javaEgressSanitizer) annotatedStringField(fullType, servicePackage, annotation string) string {
	if es == nil || fullType == "" || strings.HasPrefix(fullType, "google.protobuf.") {
		return ""
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return ""
	}
	for i := range msg.Fields {
		if msg.Fields[i].Annotations[annotation].BoolValue {
			return msg.Fields[i].Name
		}
	}
	return ""
}

// javaRequestScopePin renders the stamp-or-refuse over one request field.
// valueExpr names the request in scope; it is REBOUND by the stamp, exactly as
// javaStoreEpochStamp rebinds it on the same seam.
func javaRequestScopePin(field, valueExpr, method, indent string, isNamespace bool) string {
	if field == "" {
		return ""
	}
	marker, local, tok, accessor := "principal", "__reqPrincipal", "__reqPrincipalTok", "getSubject"
	needs := "an authenticated caller"
	mismatch := "the authenticated caller"
	if isNamespace {
		marker, local, tok, accessor = "namespace", "__reqNamespace", "__reqNamespaceTok", "getNamespace"
		needs = "an authenticated caller carrying a namespace claim"
		mismatch = "the authenticated caller's namespace"
	}
	f := javaPascalField(field)
	return fmt.Sprintf(`%[1]s// (spec.%[2]s) %[3]s: bind the field to %[4]s.
%[1]s// Unset -> stamped; equal -> accepted; different -> refused.
%[1]svar %[5]s = Auth.verifiedTokenFromContext();
%[1]sfinal String %[6]s = %[5]s == null ? null : %[5]s.%[7]s();
%[1]sif (%[6]s == null || %[6]s.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.UNAUTHENTICATED, "%[8]s requires %[9]s");
%[1]s}
%[1]sif (%[10]s.get%[11]s().isEmpty()) {
%[1]s    %[10]s = %[10]s.toBuilder().set%[11]s(%[6]s).build();
%[1]s} else if (!%[6]s.equals(%[10]s.get%[11]s())) {
%[1]s    throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[3]s does not match %[4]s");
%[1]s}
`, indent, marker, field, mismatch, tok, local, accessor, method, needs, valueExpr, f)
}

// javaCallerTidPin renders the binding of a (spec.caller_tid) request field to
// the caller's token's tid claim (#475, products-layer-2026-11-08): a product
// keeps a tenant's data only keyed by the tid of the tenant calling, never by
// one the request names. Empty is stamped and equal passes; another tenant, or
// a caller with no tid claim (the platform, a product), is PERMISSION_DENIED.
// Go's goCallerTidBlock and Rust's twin read the same.
func javaCallerTidPin(field, valueExpr, method, indent string) string {
	if field == "" {
		return ""
	}
	f := javaPascalField(field)
	return fmt.Sprintf(`%[1]s// (spec.caller_tid) %[2]s: bind the field to the caller's tenant.
%[1]s// Unset -> stamped; equal -> accepted; another tenant, or none -> refused.
%[1]svar __reqCallerTidTok = Auth.verifiedTokenFromContext();
%[1]sfinal String __reqCallerTid = __reqCallerTidTok == null ? null : __reqCallerTidTok.getTid();
%[1]sif (__reqCallerTid == null || __reqCallerTid.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[3]s requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)");
%[1]s}
%[1]sif (%[4]s.get%[5]s().isEmpty()) {
%[1]s    %[4]s = %[4]s.toBuilder().set%[5]s(__reqCallerTid).build();
%[1]s} else if (!__reqCallerTid.equals(%[4]s.get%[5]s())) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data");
%[1]s}
`, indent, field, method, valueExpr, f)
}

// javaRequestScopePins renders every marker for one request ((spec.principal),
// (spec.namespace), (spec.caller_tid)), or "" when it declares none.
func javaRequestScopePins(san *javaEgressSanitizer, fullInputType, servicePackage, valueExpr, method, indent string) string {
	out := javaRequestScopePin(san.annotatedStringField(fullInputType, servicePackage, "principal"), valueExpr, method, indent, false)
	out += javaRequestScopePin(san.annotatedStringField(fullInputType, servicePackage, "namespace"), valueExpr, method, indent, true)
	out += javaCallerTidPin(san.annotatedStringField(fullInputType, servicePackage, "caller_tid"), valueExpr, method, indent)
	return out
}
