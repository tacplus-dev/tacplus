// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
)

// The Java (spec.store_namespace) egress row filter — the tenant twin of the
// (spec.store_principal) filter next door in recordprincipal_gen.go, spliced
// into the same three seams of the same wrapper (unary rows, page items, watch
// events) and reading the same message index.
//
// WHAT IS DIFFERENT FROM THE PRINCIPAL FILTER, and why each difference is
// deliberate:
//
//	MANDATORY, NOT OPT-IN. The principal filter reads two annotations and
//	    tolerates a record declaring neither on an s2s plane. Every
//	    (spec.record) declares (spec.store_namespace) or generation fails
//	    (parser.ValidateRecordNamespaceAnnotations), so there is one place to
//	    look and no fallback to reason about.
//	EXACT MATCH. String equality against the caller's namespace claim, NOT
//	    (spec.method_c2s).target_namespace_field's level-or-below containment.
//	    That rule is for administrative reach down the tenant tree; borrowing
//	    it here would hand a caller at /acme every row of every tenant beneath
//	    it. Data lands in a leaf; root is a leaf like any other.
//	A MISMATCH ABORTS. The principal filter drops the row and returns the rest.
//	    This one drops it and then halts the JVM, because a row of another
//	    tenant reaching this comparison means the store's own query already
//	    returned it — the filter is the backstop BEHIND that query, and the only
//	    correct reading of it firing is that the query is broken. A thrown
//	    exception would degrade to one failed RPC and be swallowed; the point is
//	    that the fleet notices. See spec.proto's (spec.store_namespace) doctrine.
//
// C2S ALWAYS; S2S ON THE RECORD PLANE, AND THERE ONLY AT RUNTIME.
// The comparison is against the AUTHENTICATED CALLER's namespace, which on the
// c2s half is a statement about a user and their tenant. On the s2s half the
// caller is a peer service authorized by identity through auth_services, and
// narrowing EVERY peer's reads to its own namespace would not be a tenant
// boundary — it would break the fleet-wide peer aggregation the record query
// plane exists to serve.
//
// But a tenant-owned service (option (spec.service).tenant_owned) is deployed
// one instance per tenant and carries a /tenants/<t> service identity, so the
// s2s record plane has TWO kinds of caller and cannot answer them the same way.
// The runtime gate below is that distinction, and it is a RUNTIME one because
// the CALLER — not the generator — is what differs:
//
//	/system peer   -> fleet-wide, exactly the read this plane has always served.
//	/tenants/<t>   -> the same pin + filters the c2s half applies, with the
//	                  caller's OWN namespace claim as the pin value. Never a
//	                  request field and never service config: the tenant comes
//	                  only from the SPIFFE id in the token.
//
// The HOST's own tenant_owned is not consulted. Whether the caller is a tenant
// is a fact about the caller's token, so a /tenants/<t> peer reading through a
// SHARED host must be pinned exactly as it would be through a tenant-owned one;
// keying the gate on the host emitted no pin on that path and handed a tenant
// caller a fleet-wide read. So every s2s record plane carries the gate, and it
// is the runtime branch — not the presence of the code — that keeps /system
// peers fleet-wide. Surfaces that are not the record plane emit nothing and
// stay byte-identical to what they generated before this existed.

// javaNamespaceListField is one repeated record-bearing field of a response,
// paired with the row field naming the namespace each of its rows belongs to.
type javaNamespaceListField struct {
	Field     string // response field name, e.g. "items"
	ItemField string // row field naming the namespace, e.g. "namespace"
}

// namespaceField returns the name of fullType's (spec.store_namespace) field, or
// "" when the type is not a record. A record with none does not reach here:
// ValidateRecordNamespaceAnnotations fails generation first.
func (es *javaEgressSanitizer) namespaceField(fullType, servicePackage string) string {
	msg := es.recordMessage(fullType, servicePackage)
	if msg == nil {
		return ""
	}
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_namespace"].BoolValue {
			return msg.Fields[i].Name
		}
	}
	return ""
}

// namespaceListFields returns the repeated record-bearing fields of fullType
// paired with each row type's namespace field.
func (es *javaEgressSanitizer) namespaceListFields(fullType, servicePackage string) []javaNamespaceListField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []javaNamespaceListField
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsRepeated || !f.IsMessage || f.IsMap {
			continue
		}
		ft := fieldTypeFQN(f, pkg)
		if es.recordMessage(ft, servicePackage) == nil {
			continue
		}
		ns := es.namespaceField(ft, servicePackage)
		if ns == "" {
			continue
		}
		out = append(out, javaNamespaceListField{Field: f.Name, ItemField: ns})
	}
	return out
}

// javaNamespaceResolve renders the local binding the caller's namespace claim.
// Fail-closed: no verified token, or a token with no namespace claim, is a
// refusal, not an unbounded read.
//
// runtimeGate renders the s2s record-plane form as well: the same binding, plus
// __nsPinned, which is what every filter below is guarded on so a /system peer
// keeps its fleet-wide read. On the c2s half there is nothing to decide at
// runtime and no __nsPinned is emitted.
func javaNamespaceResolve(method, indent string, runtimeGate bool) string {
	out := fmt.Sprintf(`%[1]s// (spec.store_namespace): the tenant boundary is the AUTHENTICATED caller's
%[1]s// namespace claim, compared for EXACT equality — not the level-or-below rule
%[1]s// (spec.method_c2s).target_namespace_field uses for administrative reach.
%[1]s// Fail closed: no verified namespace, no rows.
%[1]sfinal String __rowNamespace = Auth.callerNamespaceFromContext();
%[1]sif (__rowNamespace.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s returns rows owned by a namespace and the caller presented no verified namespace claim");
%[1]s}
`, indent, method)
	if runtimeGate {
		out += fmt.Sprintf(`%[1]s// This is the s2s record read plane, so WHICH of the two reads the caller gets
%[1]s// is decided here, per request, by the caller's own identity — never by which
%[1]s// service hosts the plane: a /system peer keeps the fleet-wide read this plane
%[1]s// has always served; a /tenants/<t> peer is pinned to its own tenant and to
%[1]s// nothing else.
%[1]sfinal boolean __nsPinned = !__rowNamespace.startsWith("/system");
`, indent)
	}
	return out
}

// javaNamespaceAcceptsExpr is the row-acceptance test shared by all four egress
// filters below (unary, page, and the watch guard's two shapes): a row is
// accepted if its namespace exactly matches the caller's own namespace claim
// and from no other (the retired `linked` claim widens nothing, #171) — one
// change point for all four call sites. candidate names the
// Java expression for the row's namespace value in scope at the call site
// (already dereferenced, e.g. "__nfb.getItems(__ni).getNamespace()").
func javaNamespaceAcceptsExpr(candidate string) string {
	return fmt.Sprintf("__rowNamespace.equals(%[1]s)", candidate)
}

// javaNamespaceGuardExpr is the row comparison the filters branch on: the plain
// mismatch on the c2s half, and the mismatch AND-ed with the runtime pin on the
// s2s record plane's half.
func javaNamespaceGuardExpr(mismatch string, runtimeGate bool) string {
	if runtimeGate {
		return "__nsPinned && " + mismatch
	}
	return mismatch
}

// javaNamespaceCrash renders the halt taken when a foreign row is found. halt,
// not exit: shutdown hooks draining in-flight work would let the very rows this
// fired over finish leaving the process.
func javaNamespaceCrash(method, what, indent string) string {
	return fmt.Sprintf(`%[1]sSystem.err.println("FATAL: (spec.store_namespace) backstop fired: %[2]s returned %[3]s stamped with a namespace other than the caller's (" + __rowNamespace + "); the store's own query is leaking across tenants");
%[1]sSystem.err.flush();
%[1]sRuntime.getRuntime().halt(1);
`, indent, method, what)
}

// javaNamespaceUnaryFilter renders the egress filter over a unary response
// carrying repeated record rows.
func javaNamespaceUnaryFilter(fields []javaNamespaceListField, outputType, method, indent string, runtimeGate bool) string {
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(javaNamespaceResolve(method, indent, runtimeGate))
	fmt.Fprintf(&b, "%s%s.Builder __nfb = response.toBuilder();\n", indent, outputType)
	for _, f := range fields {
		p := javaPascalField(f.Field)
		fmt.Fprintf(&b, "%[1]sfor (int __ni = __nfb.get%[2]sCount() - 1; __ni >= 0; __ni--) {\n", indent, p)
		fmt.Fprintf(&b, "%[1]s    if (%[2]s) {\n", indent,
			javaNamespaceGuardExpr("!"+javaNamespaceAcceptsExpr(fmt.Sprintf("__nfb.get%s(__ni).get%s()", p, javaPascalField(f.ItemField))), runtimeGate))
		fmt.Fprintf(&b, "%[1]s        __nfb.remove%[2]s(__ni);\n", indent, p)
		b.WriteString(javaNamespaceCrash(method, "rows in "+f.Field, indent+"        "))
		fmt.Fprintf(&b, "%s    }\n%s}\n", indent, indent)
	}
	fmt.Fprintf(&b, "%sresponse = __nfb.build();\n", indent)
	return b.String()
}

// javaNamespacePageFilter renders the egress filter over a page of record rows,
// before the page is sanitized and assembled. srcExpr names the list in scope —
// the raw page items, or the principal filter's output when that ran first.
func javaNamespacePageFilter(namespace, itemType, srcExpr, method, indent string, runtimeGate bool) string {
	if namespace == "" {
		return ""
	}
	// The keep test, not the drop test, is what carries the runtime gate here:
	// an unpinned (/system) caller keeps every row, so __tenantItems is still
	// the list the page is assembled from and nothing downstream changes shape.
	keep := javaNamespaceAcceptsExpr(fmt.Sprintf("__nrow.get%s()", javaPascalField(namespace)))
	if runtimeGate {
		keep = "!__nsPinned || " + keep
	}
	return javaNamespaceResolve(method, indent, runtimeGate) +
		fmt.Sprintf(`%[1]sjava.util.List<%[2]s> __tenantItems = new java.util.ArrayList<>(%[3]s.size());
%[1]sfor (%[2]s __nrow : %[3]s) {
%[1]s    if (%[4]s) { __tenantItems.add(__nrow); continue; }
%[5]s%[1]s}
`, indent, itemType, srcExpr, keep, javaNamespaceCrash(method, "page rows", indent+"    "))
}

// javaNamespaceWatchGuard renders the per-event check spliced into the watch
// stream's send path. Two levels are walked for the same reason the principal
// guard walks two: <T>WatchEvent.snapshot_item is a row, and <T>WatchEvent.delta
// is a <T>WatchChange whose `item` is one. snapshot_done carries no row and is
// untouched, so a guarded stream still ends its snapshot phase.
func (es *javaEgressSanitizer) javaNamespaceWatchGuard(eventType, servicePackage, method, indent string, runtimeGate bool) string {
	if es == nil || eventType == "" {
		return ""
	}
	pkg, name := splitJavaEgressFQN(eventType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return ""
	}
	var b strings.Builder
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsMessage || f.IsRepeated || f.IsMap {
			continue
		}
		ft := fieldTypeFQN(f, pkg)
		p := javaPascalField(f.Name)
		if ns := es.namespaceField(ft, servicePackage); ns != "" {
			fmt.Fprintf(&b, "%[1]sif (%[2]s) {\n", indent,
				javaNamespaceGuardExpr(fmt.Sprintf("__event.has%[1]s() && !%[2]s", p, javaNamespaceAcceptsExpr(fmt.Sprintf("__event.get%[1]s().get%[2]s()", p, javaPascalField(ns)))), runtimeGate))
			b.WriteString(javaNamespaceCrash(method, "a watch event", indent+"    "))
			fmt.Fprintf(&b, "%s}\n", indent)
			continue
		}
		npkg, nname := splitJavaEgressFQN(ft, servicePackage)
		nested, ok := es.msgs[npkg+"\x00"+nname]
		if !ok {
			continue
		}
		for j := range nested.Fields {
			nf := &nested.Fields[j]
			if !nf.IsMessage || nf.IsRepeated || nf.IsMap {
				continue
			}
			ns := es.namespaceField(fieldTypeFQN(nf, npkg), servicePackage)
			if ns == "" {
				continue
			}
			np := javaPascalField(nf.Name)
			fmt.Fprintf(&b, "%[1]sif (%[2]s) {\n", indent,
				javaNamespaceGuardExpr(fmt.Sprintf("__event.has%[1]s() && __event.get%[1]s().has%[2]s() && !%[3]s",
					p, np, javaNamespaceAcceptsExpr(fmt.Sprintf("__event.get%[1]s().get%[2]s().get%[3]s()", p, np, javaPascalField(ns)))), runtimeGate))
			b.WriteString(javaNamespaceCrash(method, "a watch event", indent+"    "))
			fmt.Fprintf(&b, "%s}\n", indent)
		}
	}
	return b.String()
}

// THE TENANT PIN ON THE QUERY PREDICATE.
//
// The twin of javaPredicatePrincipalPin next door, and it became necessary the
// moment a record's (spec.store_namespace) field acquired a (spec.query_field) —
// which (spec.record).c2s_read now requires, because a tenant pin the store does
// not evaluate is not a pin at all.
//
// Without this, that criterion is a NEW hazard on the record plane's existing
// c2s half: a caller could name another tenant in the predicate, the store would
// return that tenant's rows, and the egress filter above would drop them and
// ABORT. A client-triggerable process halt is strictly worse than the state
// before, so the criterion and this pin land together.
//
// Same doctrine as the principal pin, with one difference that follows from the
// backstop's: a mismatch is refused rather than overwritten, so a caller naming
// a foreign tenant gets INVALID_ARGUMENT and the abort behind it stays
// unreachable from the outside.

// predicateNamespaceField returns (requestFieldName, namespaceFieldName,
// optional) for a request carrying a query predicate whose criteria include the
// row's tenant, or two empty strings when it carries none. optional reports
// whether the tenant field is proto3 `optional` (a plane filter criterion) and
// so has a has-accessor; a row itself (the record write surface's
// Put<T>Request.item) declares it plain.
func (es *javaEgressSanitizer) predicateNamespaceField(fullType, servicePackage string) (string, string, bool) {
	if es == nil || fullType == "" {
		return "", "", false
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return "", "", false
	}
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsMessage || f.IsRepeated || f.IsMap {
			continue
		}
		fpkg, fname := splitJavaEgressFQN(fieldTypeFQN(f, pkg), servicePackage)
		pred, ok := es.msgs[fpkg+"\x00"+fname]
		if !ok {
			continue
		}
		for j := range pred.Fields {
			pf := &pred.Fields[j]
			if pf.Annotations["store_namespace"].BoolValue && !pf.IsRepeated && !pf.IsMessage {
				return f.Name, pf.Name, pf.IsOptional
			}
		}
	}
	return "", "", false
}

// javaUnsetPrefix is the has-accessor half of an "unset" test on a pinned
// predicate field: `!req.getF().hasX() || ` for a proto3 optional field, empty
// for a plain one (whose empty string is its only unset).
func javaUnsetPrefix(valueExpr, rf, field string, optional bool) string {
	if !optional {
		return ""
	}
	return fmt.Sprintf("!%s.get%s().has%s() || ", valueExpr, rf, field)
}

// javaPredicateNamespacePin renders the stamp-or-refuse over the request's query
// predicate. valueExpr names the request in scope.
func javaPredicateNamespacePin(reqField, namespace string, optional bool, valueExpr, method, indent string, runtimeGate bool) string {
	if reqField == "" || namespace == "" {
		return ""
	}
	rf := javaPascalField(reqField)
	nf := javaPascalField(namespace)
	// The stamp-or-refuse itself is identical on both halves; on the s2s record
	// plane it is reached only by a /tenants/<t> peer, so a /system peer's
	// predicate is left exactly as it wrote it and its read stays fleet-wide.
	var body, gateOpen, gateClose string
	pinIndent := indent
	if runtimeGate {
		pinIndent = indent + "    "
		gateOpen = fmt.Sprintf(`%[1]s// A /system peer keeps the fleet-wide read: no pin, predicate untouched.
%[1]sif (!__queryNamespace.startsWith("/system")) {
`, indent)
		gateClose = indent + "}\n"
	}
	body = fmt.Sprintf(`%[1]sif (%[6]s%[2]s.get%[3]s().get%[4]s().isEmpty()) {
%[1]s    %[2]s = %[2]s.toBuilder().set%[3]s(%[2]s.get%[3]s().toBuilder().set%[4]s(__queryNamespace)).build();
%[1]s} else if (!__queryNamespace.equals(%[2]s.get%[3]s().get%[4]s())) {
%[1]s    throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[5]s names a tenant other than the caller's; the tenant boundary is not the predicate's to choose");
%[1]s}
`, pinIndent, valueExpr, rf, nf, reqField+"."+namespace, javaUnsetPrefix(valueExpr, rf, nf, optional))
	return fmt.Sprintf(`%[1]s// (spec.store_namespace) on the query predicate: pin the tenant before the
%[1]s// store is asked. EXACT match against the caller's namespace claim, never the
%[1]s// level-or-below containment the administrative rule uses.
%[1]s// Unset -> stamped; the caller's own -> accepted; anything else -> refused,
%[1]s// which is what keeps the aborting egress backstop unreachable from outside
%[1]s// the process. The predicate may name no tenant a row is not ACCEPTED from on
%[1]s// the egress side (see javaNamespaceAcceptsExpr) — the two must agree, or a
%[1]s// query would be answered and then aborted on the way out.
%[1]svar __nsTok = Auth.verifiedTokenFromContext();
%[1]sfinal String __queryNamespace = __nsTok == null ? null : __nsTok.getNamespace();
%[1]sif (__queryNamespace == null || __queryNamespace.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s queries rows held to a tenant and the caller presented no verified namespace claim");
%[1]s}
`, indent, method) + gateOpen + body + gateClose
}
