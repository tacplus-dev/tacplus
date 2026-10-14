// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The Java twin of the Rust (spec.store_principal) egress row filter
// (rust/types/principal_gen.go). §4b's guarantee is `principal ∈ entitled_set`:
// a read plane must never hand one principal another's rows. The Rust service
// wrapper has enforced that since the annotation landed; the Java wrapper never
// did, so every record-store host written in Java — stratum, and the generated
// record query plane it hosts — returned every principal's rows to any caller
// holding the plane's grant.
//
// WHERE THE ROW'S PRINCIPAL IS DECLARED. Two annotations name it, and both are
// read here, most specific first:
//
//	(spec.store_principal) on a record field — the purpose-built declaration,
//	    the one the Rust filter reads, and the override for a record whose
//	    (spec.record).owner is NOT the human principal (accounting rows are
//	    owned by the EMITTING SERVICE; their human principal is a different
//	    field entirely).
//	(spec.record).owner — the record's own owner declaration. Every record
//	    already carries one, so reading it as the fallback is what makes this
//	    filter apply to every current and future record type without a second
//	    annotation being remembered.
//
// A c2s method that returns record rows the wrapper cannot resolve a principal
// for FAILS GENERATION (javaRecordPrincipalUnenforceable) rather than shipping
// an unfiltered read plane — the same fail-closed stance (spec.min_group_size)
// and (spec.store_epoch) already take for the shapes they cannot reach.
//
// C2S ONLY. The filter compares the row's owning principal against the
// AUTHENTICATED CALLER's subject, which is a statement about a user. On the s2s
// half of the same plane the caller is a peer service authorized by identity
// through auth_services; narrowing its reads to rows it published itself would
// not be a privacy boundary, it would just break peer aggregation. Which rows a
// PEER may see is the auth_services decision, made before the handler runs.

// javaPrincipalListField is one repeated record-bearing field of a response,
// paired with the row field naming the owner of each of its rows.
type javaPrincipalListField struct {
	Field     string // response field name, e.g. "items"
	ItemField string // row field naming the owner, e.g. "spiffe_id"
	Private   bool   // the row type also declares (spec.record).private
}

// isRecordPrivate reports whether fullType itself declares
// (spec.record).private = true — the message-level marker a private record's
// stronger owner-pin guarantee is built on, mirroring Rust's isRecordPrivate
// (rust/types/principal_gen.go) and Go's goPrivateEgressRows.
func (es *javaEgressSanitizer) isRecordPrivate(fullType, servicePackage string) bool {
	msg := es.recordMessage(fullType, servicePackage)
	if msg == nil {
		return false
	}
	return msg.Annotations["record_private"].BoolValue
}

// principalField returns the name of fullType's row-principal field, or "" when
// the type is not a record or declares no principal. See the doctrine above for
// why two annotations are consulted.
func (es *javaEgressSanitizer) principalField(fullType, servicePackage string) string {
	msg := es.recordMessage(fullType, servicePackage)
	if msg == nil {
		return ""
	}
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_principal"].BoolValue {
			return msg.Fields[i].Name
		}
	}
	owner := msg.Annotations["record_owner"].StringValue
	if owner == "" {
		return ""
	}
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != owner {
			continue
		}
		// The owner has to be a plain string to be compared against a subject.
		if f.IsRepeated || f.IsMap || f.IsMessage || f.IsEnum || f.Type != "string" {
			return ""
		}
		return f.Name
	}
	return ""
}

// recordMessage resolves fullType to its parsed message when — and only when —
// it declares (spec.record). A non-record message carries no rows, so no filter
// is owed for it and none is refused over it.
func (es *javaEgressSanitizer) recordMessage(fullType, servicePackage string) *model.Message {
	if es == nil || fullType == "" || strings.HasPrefix(fullType, "google.protobuf.") {
		return nil
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
		return nil
	}
	return msg
}

// fieldTypeFQN is the fully qualified name of a message field's type.
func fieldTypeFQN(f *model.Field, ownerPkg string) string {
	pkg := f.TypePackage
	if pkg == "" {
		pkg = ownerPkg
	}
	return pkg + "." + f.Type
}

// principalListFields returns the repeated record-bearing fields of fullType.
// An unresolvable record (repeated rows, no principal) is reported through the
// zero ItemField so the caller can refuse generation rather than emit nothing.
func (es *javaEgressSanitizer) principalListFields(fullType, servicePackage string) []javaPrincipalListField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []javaPrincipalListField
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsRepeated || !f.IsMessage || f.IsMap {
			continue
		}
		ft := fieldTypeFQN(f, pkg)
		if es.recordMessage(ft, servicePackage) == nil {
			continue
		}
		out = append(out, javaPrincipalListField{Field: f.Name, ItemField: es.principalField(ft, servicePackage), Private: es.isRecordPrivate(ft, servicePackage)})
	}
	return out
}

// javaPrincipalResolve renders the local binding the authenticated caller's
// principal. It is the token's `sub` claim verbatim, because that is exactly
// what EventBus stamps into a record's owner field at publish time
// (PublishHandler's IdentityStamper: "the sub claim, already a SPIFFE URI").
// Comparing anything else here would compare two different spellings of the
// same identity and filter every row away.
//
// Fail-closed: no verified token, or a token with no subject, is a refusal, not
// an empty filter.
func javaPrincipalResolve(method, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.store_principal): the row filter's principal is the AUTHENTICATED
%[1]s// caller's subject — the same value EventBus stamps into the row's owner
%[1]s// field at publish time. Fail closed: no verified subject, no rows.
%[1]svar __pTok = Auth.verifiedTokenFromContext();
%[1]sfinal String __rowPrincipal = __pTok == null ? null : __pTok.getSubject();
%[1]sif (__rowPrincipal == null || __rowPrincipal.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s returns rows owned by a principal and the caller presented no verified subject");
%[1]s}
`, indent, method)
}

// javaPrivatePrincipalResolve is javaPrincipalResolve's twin for a
// (spec.record).private row: the pin must bind against the DELEGATE, not the
// calling service's own subject, when the caller is a service/workload token
// acting on another's behalf — see rustPrivatePrincipalResolve
// (rust/types/principal_gen.go) for the full doctrine, identical here except
// for the chassis API shape (TokenWrapper.getSubType()/getAct() vs Rust's
// Token::sub_type()/actor()). The chassis Authorizer's existing auth_actors
// gate has already decided whether this act may call the method at all; this
// only decides which identity the pin compares rows against.
func javaPrivatePrincipalResolve(method, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.record).private: pin against the caller's subject, or — for a
%[1]s// delegated service/workload token — the DELEGATE (act), never the calling
%[1]s// service's own identity, which is not the row's owner.
%[1]svar __pTok = Auth.verifiedTokenFromContext();
%[1]sif (__pTok == null) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s reads a private record and requires an authenticated caller");
%[1]s}
%[1]sfinal String __pSubType = __pTok.getSubType();
%[1]sfinal String __rowPrincipal;
%[1]sif ("service".equals(__pSubType) || "workload".equals(__pSubType)) {
%[1]s    __rowPrincipal = __pTok.getAct();
%[1]s    if (__rowPrincipal == null || __rowPrincipal.isEmpty()) {
%[1]s        throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s reads a private record and requires a delegated caller identity (act claim)");
%[1]s    }
%[1]s} else {
%[1]s    __rowPrincipal = __pTok.getSubject();
%[1]s    if (__rowPrincipal == null || __rowPrincipal.isEmpty()) {
%[1]s        throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s returns rows owned by a principal and the caller presented no verified subject");
%[1]s    }
%[1]s}
`, indent, method)
}

// javaPrincipalCrash renders the halt taken when a (spec.record).private
// row's owner did not match the caller — see javaNamespaceCrash, the same
// doctrine applied to the principal seam.
func javaPrincipalCrash(method, what, indent string) string {
	return fmt.Sprintf(`%[1]sSystem.err.println("FATAL: (spec.record).private backstop fired: %[2]s returned %[3]s owned by a principal other than the caller's; the store's own query is leaking across owners");
%[1]sSystem.err.flush();
%[1]sRuntime.getRuntime().halt(1);
`, indent, method, what)
}

// javaPrincipalUnaryFilter renders the egress row filter over a unary response
// that carries repeated record rows. Rows are dropped through the builder's
// remove<Field>(int) so no item type has to be named here.
func javaPrincipalUnaryFilter(fields []javaPrincipalListField, outputType, method, indent string) string {
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	if anyPrincipalFieldPrivate(fields) {
		b.WriteString(javaPrivatePrincipalResolve(method, indent))
	} else {
		b.WriteString(javaPrincipalResolve(method, indent))
	}
	fmt.Fprintf(&b, "%s%s.Builder __pfb = response.toBuilder();\n", indent, outputType)
	for _, f := range fields {
		p := javaPascalField(f.Field)
		fmt.Fprintf(&b, "%[1]sfor (int __pi = __pfb.get%[2]sCount() - 1; __pi >= 0; __pi--) {\n", indent, p)
		if !f.Private {
			fmt.Fprintf(&b, "%[1]s    if (!__rowPrincipal.equals(__pfb.get%[2]s(__pi).get%[3]s())) { __pfb.remove%[2]s(__pi); }\n",
				indent, p, javaPascalField(f.ItemField))
		} else {
			fmt.Fprintf(&b, "%[1]s    if (!__rowPrincipal.equals(__pfb.get%[2]s(__pi).get%[3]s())) {\n",
				indent, p, javaPascalField(f.ItemField))
			fmt.Fprintf(&b, "%[1]s        __pfb.remove%[2]s(__pi);\n", indent, p)
			b.WriteString(javaPrincipalCrash(method, "rows in "+f.Field, indent+"        "))
			fmt.Fprintf(&b, "%s    }\n", indent)
		}
		fmt.Fprintf(&b, "%s}\n", indent)
	}
	fmt.Fprintf(&b, "%sresponse = __pfb.build();\n", indent)
	return b.String()
}

// anyPrincipalFieldPrivate reports whether any field in the list is a
// (spec.record).private row — the resolve for the whole block (they share one
// __rowPrincipal binding) must be the delegate-aware one if so.
func anyPrincipalFieldPrivate(fields []javaPrincipalListField) bool {
	for _, f := range fields {
		if f.Private {
			return true
		}
	}
	return false
}

// javaPrincipalPageFilter renders the egress row filter over a page of record
// rows, before the page is sanitized and assembled. Filtering BEFORE the page is
// built (rather than after) is what keeps next_page_token and total_count
// describing the rows the caller is actually shown.
func javaPrincipalPageFilter(principal, itemType, method, indent string, private bool) string {
	if principal == "" {
		return ""
	}
	if !private {
		return javaPrincipalResolve(method, indent) +
			fmt.Sprintf(`%[1]sjava.util.List<%[2]s> __ownedItems = new java.util.ArrayList<>(__page.items.size());
%[1]sfor (%[2]s __prow : __page.items) { if (__rowPrincipal.equals(__prow.get%[3]s())) { __ownedItems.add(__prow); } }
`, indent, itemType, javaPascalField(principal))
	}
	return javaPrivatePrincipalResolve(method, indent) +
		fmt.Sprintf(`%[1]sjava.util.List<%[2]s> __ownedItems = new java.util.ArrayList<>(__page.items.size());
%[1]sfor (%[2]s __prow : __page.items) {
%[1]s    if (__rowPrincipal.equals(__prow.get%[3]s())) { __ownedItems.add(__prow); continue; }
%[4]s%[1]s}
`, indent, itemType, javaPascalField(principal), javaPrincipalCrash(method, "page rows", indent+"    "))
}

// javaPrincipalWatchGuard renders the per-event refusal spliced into the watch
// stream's send path: an event carrying a row the caller does not own is never
// written to the stream. The snapshot_done marker carries no row and is always
// sent, so a filtered stream still terminates its snapshot phase.
//
// Two levels are walked because that is exactly what the record query plane
// emits: <T>WatchEvent.snapshot_item is a row, and <T>WatchEvent.delta is a
// <T>WatchChange whose `item` is one.
// The second return reports whether any guarded row is (spec.record).private —
// the caller needs it to choose which resolve function feeds __rowPrincipal.
func (es *javaEgressSanitizer) javaPrincipalWatchGuard(eventType, servicePackage, method, indent string) (string, bool) {
	if es == nil || eventType == "" {
		return "", false
	}
	pkg, name := splitJavaEgressFQN(eventType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return "", false
	}
	var b strings.Builder
	anyPrivate := false
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsMessage || f.IsRepeated || f.IsMap {
			continue
		}
		ft := fieldTypeFQN(f, pkg)
		p := javaPascalField(f.Name)
		if principal := es.principalField(ft, servicePackage); principal != "" {
			private := es.isRecordPrivate(ft, servicePackage)
			cond := fmt.Sprintf("__event.has%[1]s() && !__rowPrincipal.equals(__event.get%[1]s().get%[2]s())", p, javaPascalField(principal))
			if !private {
				fmt.Fprintf(&b, "%[1]sif (%[2]s) { return; }\n", indent, cond)
			} else {
				anyPrivate = true
				fmt.Fprintf(&b, "%[1]sif (%[2]s) {\n", indent, cond)
				b.WriteString(javaPrincipalCrash(method, "a watch event", indent+"    "))
				fmt.Fprintf(&b, "%s}\n", indent)
			}
			continue
		}
		// One level deeper: the change envelope around a row.
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
			nft := fieldTypeFQN(nf, npkg)
			principal := es.principalField(nft, servicePackage)
			if principal == "" {
				continue
			}
			np := javaPascalField(nf.Name)
			cond := fmt.Sprintf("__event.has%[1]s() && __event.get%[1]s().has%[2]s() && !__rowPrincipal.equals(__event.get%[1]s().get%[2]s().get%[3]s())",
				p, np, javaPascalField(principal))
			if !es.isRecordPrivate(nft, servicePackage) {
				fmt.Fprintf(&b, "%[1]sif (%[2]s) { return; }\n", indent, cond)
			} else {
				anyPrivate = true
				fmt.Fprintf(&b, "%[1]sif (%[2]s) {\n", indent, cond)
				b.WriteString(javaPrincipalCrash(method, "a watch event", indent+"    "))
				fmt.Fprintf(&b, "%s}\n", indent)
			}
		}
	}
	return b.String(), anyPrivate
}

// javaRecordPrincipalUnenforceable reports why the row filter cannot be
// generated for a c2s method that returns record rows, or "" when it can.
//
// A record read plane the wrapper cannot narrow must fail generation. The
// alternative — emitting the RPC unfiltered — is exactly the state this whole
// file exists to end, and it is invisible in a green build.
func javaRecordPrincipalUnenforceable(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string, isC2S bool) string {
	if !isC2S || san == nil {
		return ""
	}
	// Paged reads.
	if method.Resolved != nil && method.Resolved.PageItemType != "" {
		if san.recordMessage(method.Resolved.PageItemType, servicePackage) != nil &&
			san.principalField(method.Resolved.PageItemType, servicePackage) == "" {
			return fmt.Sprintf("it pages %s, a (spec.record) declaring no owner the wrapper can filter on: "+
				"give the row's owning-principal field (spec.store_principal) = true, or declare "+
				"(spec.record).owner naming a plain string field", method.Resolved.PageItemType)
		}
		return ""
	}
	// Watched reads.
	if method.Resolved != nil && method.Resolved.WatchItemType != "" {
		if san.recordMessage(method.Resolved.WatchItemType, servicePackage) != nil &&
			san.principalField(method.Resolved.WatchItemType, servicePackage) == "" {
			return fmt.Sprintf("it watches %s, a (spec.record) declaring no owner the wrapper can filter on: "+
				"give the row's owning-principal field (spec.store_principal) = true, or declare "+
				"(spec.record).owner naming a plain string field", method.Resolved.WatchItemType)
		}
		return ""
	}
	// Unary reads carrying repeated rows.
	for _, lf := range san.principalListFields(method.FullOutputType, servicePackage) {
		if lf.ItemField == "" {
			return fmt.Sprintf("its response field %q carries (spec.record) rows declaring no owner the wrapper "+
				"can filter on: give the row's owning-principal field (spec.store_principal) = true, or declare "+
				"(spec.record).owner naming a plain string field", lf.Field)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The SIZE-ONLY half.
//
// The egress filter above narrows the ROWS a c2s caller is handed. It cannot
// narrow Get<T>Count, Subscribe<T>Count or Update<T>Query's size: those return a
// number computed over the handle's predicate and no rows at all, so there is
// nothing at egress to drop. Left alone they are a per-victim cardinality
// oracle — "how many rows match spiffe_id == <someone else>" is answerable in
// one RPC by a caller who can see none of them.
//
// The plane was shaped for the fix: Create<T>Query is its single entry point, so
// a predicate pinned there is carried by every later call made against the
// handle. record_map.go marks the owning-principal CRITERION of the generated
// <T>Filter with (spec.store_principal); this is the wrapper end that pins it,
// with the same stamp-or-refuse doctrine (spec.principal) already uses:
//
//	unset    -> stamped with the caller's subject
//	equal    -> accepted unchanged
//	different-> INVALID_ARGUMENT, never silently overwritten
//
// A record whose principal is not EQ-queryable carries no such criterion and
// gets no pin. That is a narrowing gap in the size-only RPCs only — the rows
// themselves are still filtered at egress.

// javaPredicatePrincipalField returns (requestFieldName, principalFieldName) for
// a request that carries a query predicate naming the row's owning principal, or
// two empty strings when it carries none.
func (es *javaEgressSanitizer) predicatePrincipalField(fullType, servicePackage string) (string, string) {
	if es == nil || fullType == "" {
		return "", ""
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return "", ""
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
			if pf.Annotations["store_principal"].BoolValue && !pf.IsRepeated && !pf.IsMessage {
				return f.Name, pf.Name
			}
		}
	}
	return "", ""
}

// javaPredicatePrincipalPin renders the stamp-or-refuse over the request's query
// predicate. valueExpr names the request in scope.
func javaPredicatePrincipalPin(reqField, principal, valueExpr, method, indent string) string {
	if reqField == "" || principal == "" {
		return ""
	}
	rf := javaPascalField(reqField)
	pf := javaPascalField(principal)
	return fmt.Sprintf(`%[1]s// (spec.store_principal) on the query predicate: this plane is per-principal.
%[1]s// The wrapper — never the handler — pins the predicate to the authenticated
%[1]s// caller at the lifecycle's single entry point, so every later call made
%[1]s// against the returned handle inherits it, the size-only ones included.
%[1]s// Unset -> stamped; equal -> accepted; different -> refused.
%[1]svar __qTok = Auth.verifiedTokenFromContext();
%[1]sfinal String __queryPrincipal = __qTok == null ? null : __qTok.getSubject();
%[1]sif (__queryPrincipal == null || __queryPrincipal.isEmpty()) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s queries rows owned by a principal and the caller presented no verified subject");
%[1]s}
%[1]sif (!%[3]s.get%[4]s().has%[5]s() || %[3]s.get%[4]s().get%[5]s().isEmpty()) {
%[1]s    %[3]s = %[3]s.toBuilder().set%[4]s(%[3]s.get%[4]s().toBuilder().set%[5]s(__queryPrincipal)).build();
%[1]s} else if (!__queryPrincipal.equals(%[3]s.get%[4]s().get%[5]s())) {
%[1]s    throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[6]s names a principal other than the caller; the query plane is per-principal and the predicate is not the caller's to choose");
%[1]s}
`, indent, method, valueExpr, rf, pf, reqField+"."+principal)
}
