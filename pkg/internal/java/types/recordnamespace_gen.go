// Copyright (c) 2026 Second Star Cavalier LLC

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
// C2S ONLY, for the same reason the principal filter is. The comparison is
// against the AUTHENTICATED CALLER's namespace, which is a statement about a
// user and their tenant. On the s2s half of the same plane the caller is a peer
// service authorized by identity through auth_services, and narrowing its reads
// to its own service namespace would not be a tenant boundary — it would break
// every peer aggregation the record query plane exists to serve. Which rows a
// PEER may see is the auth_services decision, made before the handler runs.

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
func javaNamespaceResolve(method, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.store_namespace): the tenant boundary is the AUTHENTICATED caller's
%[1]s// namespace claim, compared for EXACT equality — not the level-or-below rule
%[1]s// (spec.method_c2s).target_namespace_field uses for administrative reach.
%[1]s// Fail closed: no verified namespace, no rows.
%[1]sfinal String __rowNamespace = Auth.callerNamespaceFromContext();
%[1]sif (__rowNamespace.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s returns rows owned by a namespace and the caller presented no verified namespace claim");
%[1]s}
`, indent, method)
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
func javaNamespaceUnaryFilter(fields []javaNamespaceListField, outputType, method, indent string) string {
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(javaNamespaceResolve(method, indent))
	fmt.Fprintf(&b, "%s%s.Builder __nfb = response.toBuilder();\n", indent, outputType)
	for _, f := range fields {
		p := javaPascalField(f.Field)
		fmt.Fprintf(&b, "%[1]sfor (int __ni = __nfb.get%[2]sCount() - 1; __ni >= 0; __ni--) {\n", indent, p)
		fmt.Fprintf(&b, "%[1]s    if (!__rowNamespace.equals(__nfb.get%[2]s(__ni).get%[3]s())) {\n", indent, p, javaPascalField(f.ItemField))
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
func javaNamespacePageFilter(namespace, itemType, srcExpr, method, indent string) string {
	if namespace == "" {
		return ""
	}
	return javaNamespaceResolve(method, indent) +
		fmt.Sprintf(`%[1]sjava.util.List<%[2]s> __tenantItems = new java.util.ArrayList<>(%[3]s.size());
%[1]sfor (%[2]s __nrow : %[3]s) {
%[1]s    if (__rowNamespace.equals(__nrow.get%[4]s())) { __tenantItems.add(__nrow); continue; }
%[5]s%[1]s}
`, indent, itemType, srcExpr, javaPascalField(namespace), javaNamespaceCrash(method, "page rows", indent+"    "))
}

// javaNamespaceWatchGuard renders the per-event check spliced into the watch
// stream's send path. Two levels are walked for the same reason the principal
// guard walks two: <T>WatchEvent.snapshot_item is a row, and <T>WatchEvent.delta
// is a <T>WatchChange whose `item` is one. snapshot_done carries no row and is
// untouched, so a guarded stream still ends its snapshot phase.
func (es *javaEgressSanitizer) javaNamespaceWatchGuard(eventType, servicePackage, method, indent string) string {
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
			fmt.Fprintf(&b, "%[1]sif (__event.has%[2]s() && !__rowNamespace.equals(__event.get%[2]s().get%[3]s())) {\n", indent, p, javaPascalField(ns))
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
			fmt.Fprintf(&b, "%[1]sif (__event.has%[2]s() && __event.get%[2]s().has%[3]s() && !__rowNamespace.equals(__event.get%[2]s().get%[3]s().get%[4]s())) {\n",
				indent, p, np, javaPascalField(ns))
			b.WriteString(javaNamespaceCrash(method, "a watch event", indent+"    "))
			fmt.Fprintf(&b, "%s}\n", indent)
		}
	}
	return b.String()
}
