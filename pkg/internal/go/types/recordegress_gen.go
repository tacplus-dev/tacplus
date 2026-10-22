// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The Go (spec.store_namespace) / (spec.store_principal) / (spec.record).private
// EGRESS ROW FILTERS — the third implementation of the seam Java
// (java/types/recordnamespace_gen.go, recordprincipal_gen.go) and Rust
// (rust/types/namespace_gen.go, principal_gen.go, watch_egress_gen.go) already
// carry, ported from them rather than re-derived, so the record plane behaves
// the same whichever language hosts it.
//
// Until this file existed the Go wrapper rendered NO row filter on any seam and
// goPinnedEgressRows / goPrivateEgressRows refused GENERATION wherever one was
// owed. That was a stopgap against returning every tenant's rows, never a
// design: the refusals it fed are gone and the filter below is what stands in
// their place.
//
// WHAT IS ENFORCED, and where each rule comes from:
//
//	(spec.store_namespace)  EXACT equality against the caller's namespace claim,
//	                        widened by the caller's `linked` claims. A mismatch
//	                        ABORTS the process: the filter is the backstop BEHIND
//	                        the store's own query, so it firing means that query
//	                        is leaking across tenants.
//	(spec.store_principal)  the same shape against the caller's subject, and a
//	                        mismatch DROPS the row and proceeds — one principal's
//	                        rows reaching another is a narrowing the plane is
//	                        expected to perform, not evidence of a broken query.
//	(spec.record).private   the principal pin, but ABORTING like the namespace
//	                        one, and resolved against the DELEGATE (the `act`
//	                        claim) when the caller is a service/workload token.
//
// THE EMISSION GATE is Java's, copied: the principal seams are c2s-only (a peer
// service is not a user and there is no user to narrow to), and the namespace
// seams additionally reach the synthesized record plane's s2s half, where a
// RUNTIME gate — not the generator — decides which read the caller gets: a
// /system peer keeps the fleet-wide read the plane has always served, a
// /tenants/<t> peer is pinned to its own tenant.
//
// SEAM COVERAGE matches Rust's, because the Go emit shapes match Rust's: the
// unary response, the PAGE window (filtered before the page is assembled, so
// next_page_token and total_count describe the rows actually shown), the WATCH
// stream's snapshot_item and delta frames, and the query predicate on the
// request. Every OTHER streaming shape has no per-item hook and is refused at
// generation (goStreamEgressRefusal), exactly as rustStreamEgressRefusal
// refuses it.

// goRowListField is one repeated record-bearing field of a response, paired
// with the row field naming the namespace or owner of each of its rows.
type goRowListField struct {
	Field     string // response field name, e.g. "items"
	ItemField string // row field naming the namespace/owner
	Private   bool   // the row type also declares (spec.record).private
}

// recordMessage resolves fullType to its parsed message when — and only when —
// it declares (spec.record). A non-record message carries no rows, so no filter
// is owed for it. Twin of the Java sanitizer's recordMessage.
func (es *egressSanitizer) recordMessage(fullType, servicePackage string) *model.Message {
	if es == nil || fullType == "" || strings.HasPrefix(fullType, "google.protobuf.") {
		return nil
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
		return nil
	}
	return msg
}

// recordPrivate reports whether fullType declares (spec.record).private = true
// — a MESSAGE-level marker, not a field one.
func (es *egressSanitizer) recordPrivate(fullType, servicePackage string) bool {
	msg := es.recordMessage(fullType, servicePackage)
	return msg != nil && msg.Annotations["record_private"].BoolValue
}

// namespaceField returns the name of fullType's (spec.store_namespace) field, or
// "" when the type is not a record.
func (es *egressSanitizer) namespaceField(fullType, servicePackage string) string {
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

// principalField returns the name of fullType's row-principal field, or "" when
// the type is not a record or declares no principal. Two annotations are read,
// most specific first, exactly as Java's principalField reads them:
// (spec.store_principal) on a field, then (spec.record).owner naming a plain
// string field.
func (es *egressSanitizer) principalField(fullType, servicePackage string) string {
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

// recordListFields returns the repeated record-bearing fields of fullType paired
// with each row type's namespace or owner field, as resolved by pin. A record
// row whose pin resolves to "" is reported through the zero ItemField so the
// caller can refuse generation rather than emit nothing.
func (es *egressSanitizer) recordListFields(fullType, servicePackage string, pin func(string, string) string) []goRowListField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []goRowListField
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsRepeated || !f.IsMessage || f.IsMap {
			continue
		}
		itemPkg := f.TypePackage
		if itemPkg == "" {
			itemPkg = pkg
		}
		ft := itemPkg + "." + f.Type
		if es.recordMessage(ft, servicePackage) == nil {
			continue
		}
		out = append(out, goRowListField{
			Field:     f.Name,
			ItemField: pin(ft, servicePackage),
			Private:   es.recordPrivate(ft, servicePackage),
		})
	}
	return out
}

// namespaceListFields / principalListFields are the two pins recordListFields
// is walked with. A namespace-less record does not reach generation
// (parser.ValidateRecordNamespaceAnnotations), so the namespace walk drops the
// unresolved entries; the principal walk keeps them, because an unresolved
// owner on a private row is a generation error the caller must report.
func (es *egressSanitizer) namespaceListFields(fullType, servicePackage string) []goRowListField {
	var out []goRowListField
	for _, f := range es.recordListFields(fullType, servicePackage, es.namespaceField) {
		if f.ItemField != "" {
			out = append(out, f)
		}
	}
	return out
}

func (es *egressSanitizer) principalListFields(fullType, servicePackage string) []goRowListField {
	return es.recordListFields(fullType, servicePackage, es.principalField)
}

// anyRowPrivate reports whether any field in the list is a (spec.record).private
// row — the resolve for the whole block (they share one __readPrincipal
// binding) must be the delegate-aware one if so.
func anyRowPrivate(fields []goRowListField) bool {
	for _, f := range fields {
		if f.Private {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Resolves: the locals every filter below compares rows against.

// goNamespaceResolve renders the binding of the caller's namespace claim, the
// `linked` widening, and the acceptance closure all four filters share — one
// change point rather than four copies of the OR.
//
// Fail-closed: no verified token, or a token with no namespace claim, is a
// refusal, not an unbounded read.
//
// runtimeGate renders the s2s record-plane form as well: __nsPinned, which
// every filter is guarded on so a /system peer keeps its fleet-wide read. On
// the c2s half there is nothing to decide at runtime and none is emitted.
func goNamespaceResolve(method, indent, failRet string, runtimeGate bool) string {
	out := fmt.Sprintf(`
%[1]s// (spec.store_namespace): the tenant boundary is the AUTHENTICATED caller's
%[1]s// namespace claim, compared for EXACT equality — not the level-or-below rule
%[1]s// (spec.method_c2s).target_namespace_field uses for administrative reach.
%[1]s// Fail closed: no verified namespace, no rows.
%[1]s__readNamespace := ""
%[1]sif caller != nil {
%[1]s	__readNamespace = caller.Token().GetNamespace()
%[1]s}
%[1]sif __readNamespace == "" {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[2]s returns rows owned by a namespace and the caller presented no verified namespace claim")
%[1]s}
%[1]s// A linked claim widens which namespaces a row may be accepted from, on top
%[1]s// of the caller's own. This is a READ seam, so the widening always applies.
%[1]svar __readNamespaceLinked []string
%[1]sif caller != nil {
%[1]s	__readNamespaceLinked = caller.Token().GetLinked()
%[1]s}
%[1]s__nsAccepted := func(__ns string) bool {
%[1]s	if __ns == __readNamespace {
%[1]s		return true
%[1]s	}
%[1]s	for _, __linked := range __readNamespaceLinked {
%[1]s		if __linked == __ns {
%[1]s			return true
%[1]s		}
%[1]s	}
%[1]s	return false
%[1]s}
`, indent, method, failRet)
	if runtimeGate {
		out += fmt.Sprintf(`%[1]s// This is the s2s record read plane, so WHICH of the two reads the caller gets
%[1]s// is decided here, per request, by the caller's own identity — never by which
%[1]s// service hosts the plane: a /system peer keeps the fleet-wide read this plane
%[1]s// has always served; a /tenants/<t> peer is pinned to its own tenant and to
%[1]s// nothing else.
%[1]s__nsPinned := !strings.HasPrefix(__readNamespace, "/system")
`, indent)
	}
	return out
}

// goNamespaceKeepExpr is the row KEEP test the filters branch on: the plain
// acceptance on the c2s half, and the acceptance OR-ed with "not pinned" on the
// s2s record plane's half. The keep test, not the drop test, carries the gate:
// an unpinned (/system) caller keeps every row, so nothing downstream changes
// shape.
func goNamespaceKeepExpr(candidate string, runtimeGate bool) string {
	keep := "__nsAccepted(" + candidate + ")"
	if runtimeGate {
		return "!__nsPinned || " + keep
	}
	return keep
}

// goNamespaceDropExpr is the same gate over a DROP test — the watch guards fire
// on a mismatch rather than keeping a match. Twin of rustNamespaceDropExpr.
func goNamespaceDropExpr(candidate string, runtimeGate bool) string {
	drop := "!__nsAccepted(" + candidate + ")"
	if runtimeGate {
		return "__nsPinned && " + drop
	}
	return drop
}

// goNamespaceCrash renders the halt taken when a foreign row is found. os.Exit,
// not a returned error: deferred work draining in-flight requests would let the
// very rows this fired over finish leaving the process, and a returned error
// degrades to one failed RPC and is swallowed. The point is that the fleet
// notices.
func goNamespaceCrash(method, what, indent string) string {
	return fmt.Sprintf(`%[1]sfmt.Fprintf(os.Stderr, "FATAL: (spec.store_namespace) backstop fired: %[2]s returned %[3]s stamped with a namespace other than the caller's (%%s); the store's own query is leaking across tenants\n", __readNamespace)
%[1]sos.Exit(1)
`, indent, method, what)
}

// goPrincipalResolve renders the binding of the authenticated caller's subject.
// It is the token's `sub` claim verbatim, because that is exactly what EventBus
// stamps into a record's owner field at publish time; comparing anything else
// would compare two spellings of the same identity and filter every row away.
func goPrincipalResolve(method, indent, failRet string) string {
	return fmt.Sprintf(`
%[1]s// (spec.store_principal): the row filter's principal is the AUTHENTICATED
%[1]s// caller's subject — the same value EventBus stamps into the row's owner
%[1]s// field at publish time. Fail closed: no verified subject, no rows.
%[1]s__readPrincipal := ""
%[1]sif caller != nil {
%[1]s	__readPrincipal = caller.Token().GetSubject()
%[1]s}
%[1]sif __readPrincipal == "" {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[2]s returns rows owned by a principal and the caller presented no verified subject")
%[1]s}
`, indent, method, failRet)
}

// goPrivatePrincipalResolve is goPrincipalResolve's twin for a
// (spec.record).private row: the pin binds against the DELEGATE, not the
// calling service's own subject, when the caller is a service/workload token
// acting on another's behalf. A service/workload token carrying no act claim
// has no delegate to pin to at all and is refused outright — PERMISSION_DENIED,
// not a default/empty owner match. The chassis Authorizer's auth_actors gate has
// already decided whether this act may call the method; this only decides which
// identity the pin compares rows against.
func goPrivatePrincipalResolve(method, indent, failRet string) string {
	return fmt.Sprintf(`
%[1]s// (spec.record).private: pin against the caller's subject, or — for a
%[1]s// delegated service/workload token — the DELEGATE (act), never the calling
%[1]s// service's own identity, which is not the row's owner.
%[1]sif caller == nil {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[2]s reads a private record and requires an authenticated caller")
%[1]s}
%[1]s__readPrincipal := ""
%[1]sswitch caller.Token().GetSubType() {
%[1]scase "service", "workload":
%[1]s	__readPrincipal = caller.Token().GetAct()
%[1]s	if __readPrincipal == "" {
%[1]s		%[3]s status.Error(codes.PermissionDenied, "%[2]s reads a private record and requires a delegated caller identity (act claim)")
%[1]s	}
%[1]sdefault:
%[1]s	__readPrincipal = caller.Token().GetSubject()
%[1]s	if __readPrincipal == "" {
%[1]s		%[3]s status.Error(codes.PermissionDenied, "%[2]s returns rows owned by a principal and the caller presented no verified subject")
%[1]s	}
%[1]s}
`, indent, method, failRet)
}

// goPrincipalCrash renders the halt taken when a (spec.record).private row's
// owner did not match the caller — see goNamespaceCrash, the same doctrine
// applied to the principal seam.
func goPrincipalCrash(method, what, indent string) string {
	return fmt.Sprintf(`%[1]sfmt.Fprintf(os.Stderr, "FATAL: (spec.record).private backstop fired: %[2]s returned %[3]s owned by a principal other than the caller's; the store's own query is leaking across owners\n")
%[1]sos.Exit(1)
`, indent, method, what)
}

// goEgressResolvePreamble renders the resolves a method's egress filters need,
// once, at the seam `caller` is bound. principal and namespace are the two
// halves; either may be absent.
func goEgressResolvePreamble(method, indent, failRet string, needPrincipal, needPrivatePrincipal, needNamespace, runtimeGate bool) string {
	out := ""
	if needPrincipal {
		if needPrivatePrincipal {
			out += goPrivatePrincipalResolve(method, indent, failRet)
		} else {
			out += goPrincipalResolve(method, indent, failRet)
		}
	}
	if needNamespace {
		out += goNamespaceResolve(method, indent, failRet, runtimeGate)
	}
	return out
}

// ---------------------------------------------------------------------------
// The UNARY seam.

// goRowsFilterStmts renders the row filter over one repeated field of a message
// held in msgVar. The kept slice is built with `[:0:0]` so the element type is
// never named and the first append allocates: the handler's own backing array is
// never written through.
func goRowsFilterStmts(msgVar, field, kept, keepExpr, crash, indent string) string {
	gf := toGoFieldName(field)
	out := fmt.Sprintf(`%[1]s%[2]s := %[3]s.%[4]s[:0:0]
%[1]sfor _, __row := range %[3]s.%[4]s {
%[1]s	if %[5]s {
%[1]s		%[2]s = append(%[2]s, __row)
%[1]s	}
%[1]s}
`, indent, kept, msgVar, gf, keepExpr)
	if crash != "" {
		out += fmt.Sprintf(`%[1]sif len(%[2]s) != len(%[3]s.%[4]s) {
%[5]s%[1]s}
`, indent, kept, msgVar, gf, crash)
	}
	return out + fmt.Sprintf("%s%s.%s = %s\n", indent, msgVar, gf, kept)
}

// goPrincipalUnaryFilter renders the egress row filter over a unary response
// carrying repeated record rows. A plain (spec.store_principal) row is dropped;
// a (spec.record).private row aborts.
func goPrincipalUnaryFilter(fields []goRowListField, msgVar, method, indent string) string {
	var b strings.Builder
	for i, f := range fields {
		if f.ItemField == "" {
			continue
		}
		crash := ""
		if f.Private {
			crash = goPrincipalCrash(method, "rows in "+f.Field, indent+"\t")
		}
		b.WriteString(goRowsFilterStmts(msgVar, f.Field, fmt.Sprintf("__principalRows%d", i),
			fmt.Sprintf("__row.Get%s() == __readPrincipal", toGoFieldName(f.ItemField)), crash, indent))
	}
	return b.String()
}

// goNamespaceUnaryFilter renders the egress row filter over a unary response
// carrying repeated record rows. A mismatch aborts.
func goNamespaceUnaryFilter(fields []goRowListField, msgVar, method, indent string, runtimeGate bool) string {
	var b strings.Builder
	for i, f := range fields {
		b.WriteString(goRowsFilterStmts(msgVar, f.Field, fmt.Sprintf("__namespaceRows%d", i),
			goNamespaceKeepExpr(fmt.Sprintf("__row.Get%s()", toGoFieldName(f.ItemField)), runtimeGate),
			goNamespaceCrash(method, "rows in "+f.Field, indent+"\t"), indent))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// The PAGE seam. Filtering BEFORE the page is assembled (rather than after) is
// what keeps next_page_token and total_count describing the rows actually shown.

// goPrincipalPageFilter renders the owner filter over the handler's page items.
func goPrincipalPageFilter(principal, method, indent string, private bool) string {
	if principal == "" {
		return ""
	}
	crash := ""
	if private {
		crash = goPrincipalCrash(method, "page rows", indent+"\t")
	}
	return goRowsFilterStmts("page", "items", "__ownedItems",
		fmt.Sprintf("__row.Get%s() == __readPrincipal", toGoFieldName(principal)), crash, indent)
}

// goNamespacePageFilter renders the tenant filter over the handler's page items,
// after the principal filter and over its output.
func goNamespacePageFilter(namespace, method, indent string, runtimeGate bool) string {
	if namespace == "" {
		return ""
	}
	return goRowsFilterStmts("page", "items", "__tenantItems",
		goNamespaceKeepExpr(fmt.Sprintf("__row.Get%s()", toGoFieldName(namespace)), runtimeGate),
		goNamespaceCrash(method, "page rows", indent+"\t"), indent)
}

// ---------------------------------------------------------------------------
// The WATCH seam. Go's emit shape is Rust's — a snapshot `item` frame and a
// delta `change` frame, each visited inside the producer loop — so the guards
// are Rust's (rust/types/watch_egress_gen.go), not Java's whole-envelope walk.

// goWatchRowGuard names one pinned row reachable from a watch frame.
//
//	Path  is the field chain from the frame variable to the row, empty when the
//	      frame IS the row (snapshot_item, or a row-shaped delta), or one
//	      singular message field name when the delta wraps it.
type goWatchRowGuard struct {
	Path      string
	Principal string
	Namespace string
	Private   bool
}

// goWatchEgress is the resolved guard set for one WATCH method.
type goWatchEgress struct {
	Item   []goWatchRowGuard // guards on the snapshot_item frame
	Change []goWatchRowGuard // guards on the delta frame
}

func (w goWatchEgress) all() []goWatchRowGuard {
	return append(append([]goWatchRowGuard{}, w.Item...), w.Change...)
}

// pinned reports whether any guard exists at all — the wrapper emits nothing
// (and resolves nothing) for a WATCH over unpinned rows.
func (w goWatchEgress) pinned() bool { return len(w.Item)+len(w.Change) > 0 }

func (w goWatchEgress) needsPrincipal() bool {
	for _, g := range w.all() {
		if g.Principal != "" {
			return true
		}
	}
	return false
}

func (w goWatchEgress) needsPrivatePrincipal() bool {
	for _, g := range w.all() {
		if g.Principal != "" && g.Private {
			return true
		}
	}
	return false
}

func (w goWatchEgress) needsNamespace() bool {
	for _, g := range w.all() {
		if g.Namespace != "" {
			return true
		}
	}
	return false
}

// watchRowPins returns the pin fields of fullType when it is a pinned row.
func (es *egressSanitizer) watchRowPins(fullType, servicePackage string) (goWatchRowGuard, bool) {
	g := goWatchRowGuard{
		Principal: es.principalField(fullType, servicePackage),
		Namespace: es.namespaceField(fullType, servicePackage),
		Private:   es.recordPrivate(fullType, servicePackage),
	}
	return g, g.Principal != "" || g.Namespace != ""
}

// watchEgress resolves the guard set for a WATCH method's item and change types.
// The error is the generation refusal for a shape the guards cannot reach; a nil
// error with an empty set means the rows carry no pins. Same walk, same refusal
// wording shape as rustEgressSanitizer.watchEgress.
func (es *egressSanitizer) watchEgress(serviceName, methodName, itemType, changeType, servicePackage string) (goWatchEgress, error) {
	var out goWatchEgress
	if es == nil {
		return out, nil
	}
	// snapshot_item is always the row itself.
	if g, ok := es.watchRowPins(itemType, servicePackage); ok {
		out.Item = append(out.Item, g)
	}
	// delta: the row itself, or a one-level envelope around it.
	if g, ok := es.watchRowPins(changeType, servicePackage); ok {
		out.Change = append(out.Change, g)
	} else if changeType != "" {
		pkg, name := splitEgressFQN(changeType, servicePackage)
		if msg, found := es.msgs[pkg+"\x00"+name]; found {
			for i := range msg.Fields {
				f := &msg.Fields[i]
				if !f.IsMessage {
					continue
				}
				fpkg := f.TypePackage
				if fpkg == "" {
					fpkg = pkg
				}
				g, isPinned := es.watchRowPins(fpkg+"."+f.Type, servicePackage)
				if !isPinned {
					continue
				}
				if f.IsRepeated || f.IsMap {
					kind := "repeated"
					if f.IsMap {
						kind = "map"
					}
					return out, fmt.Errorf("%s.%s: delta %s carries pinned %s rows in %s, a %s field; the WATCH egress filter walks one singular message field of the delta and cannot hold a %s to the caller — reshape the delta to carry the row directly or as one singular field",
						serviceName, methodName, changeType, fpkg+"."+f.Type, f.Name, kind, kind)
				}
				g.Path = f.Name
				out.Change = append(out.Change, g)
			}
		}
	}
	return out, nil
}

// goWatchEgressGuardStmts renders the per-frame guards for one frame variable
// (`item` or `change`) inside the producer loop. A principal mismatch skips the
// frame (`continue`); a private or namespace mismatch aborts.
func goWatchEgressGuardStmts(guards []goWatchRowGuard, frameVar, method, indent string, emitPrincipal, runtimeGate bool) string {
	if len(guards) == 0 {
		return ""
	}
	var b strings.Builder
	// Principal first, then namespace over what survived — the order the unary
	// and PAGE seams apply them in.
	if emitPrincipal {
		for _, g := range guards {
			if g.Principal == "" {
				continue
			}
			row, openStmt, closeStmt, inner := goWatchFrameRow(frameVar, g.Path, indent)
			body := inner + "\tcontinue\n"
			what := "(spec.store_principal): a frame carrying a row the caller does not own is never sent."
			if g.Private {
				body = goPrincipalCrash(method, "a watch event", inner+"\t")
				what = "(spec.record).private: a frame carrying a row the caller does not own aborts."
			}
			fmt.Fprintf(&b, "%s// %s\n%s%sif %s.Get%s() != __readPrincipal {\n%s%s}\n%s",
				indent, what, openStmt, inner, row, toGoFieldName(g.Principal), body, inner, closeStmt)
		}
	}
	for _, g := range guards {
		if g.Namespace == "" {
			continue
		}
		row, openStmt, closeStmt, inner := goWatchFrameRow(frameVar, g.Path, indent)
		fmt.Fprintf(&b, "%s// (spec.store_namespace): hold the frame to the caller's tenant; a foreign row aborts.\n%s%sif %s {\n%s%s}\n%s",
			indent, openStmt, inner,
			goNamespaceDropExpr(fmt.Sprintf("%s.Get%s()", row, toGoFieldName(g.Namespace)), runtimeGate),
			goNamespaceCrash(method, "a watch event", inner+"\t"), inner, closeStmt)
	}
	return b.String()
}

// goWatchFrameRow names the row inside a watch frame, plus the nil-guard that
// has to wrap the check when the delta WRAPS the row rather than being it: an
// unset envelope field is a nil message, and a guard that dereferenced it would
// turn a delete frame into a panic. inner is the column the guard itself sits
// at, one tab in when that wrapper is present.
func goWatchFrameRow(frameVar, path, indent string) (row, openStmt, closeStmt, inner string) {
	if path == "" {
		return frameVar, "", "", indent
	}
	openStmt = fmt.Sprintf("%sif __row := %s.Get%s(); __row != nil {\n", indent, frameVar, toGoFieldName(path))
	return "__row", openStmt, indent + "}\n", indent + "\t"
}

// ---------------------------------------------------------------------------
// The QUERY PREDICATE seam.
//
// The egress filters above narrow the ROWS a caller is handed. They cannot
// narrow Get<T>Count / Subscribe<T>Count / Update<T>Query's SIZE: those return a
// number computed over the handle's predicate and no rows at all. Create<T>Query
// is the lifecycle's single entry point, so a predicate pinned there is carried
// by every later call made against the handle.
//
// The namespace pin additionally keeps the ABORTING egress backstop unreachable
// from outside the process: without it a caller could name another tenant in the
// predicate, the store would return that tenant's rows, and the filter above
// would halt — a client-triggerable process halt is strictly worse than no pin.

// goPredicateField describes the query predicate a request carries: which field
// on the request holds it, its Go type reference (so an ABSENT predicate can be
// stamped into existence, the way Java's builder path does), which criterion
// field carries the pin, and whether that criterion is proto3-`optional` (a
// *string on the Go struct).
type goPredicateField struct {
	ReqField    string
	TypeRef     string
	PinField    string
	PinOptional bool
}

// predicateAnnotatedField describes the query predicate of a request whose
// criteria include the named annotation, or the zero value when it carries none.
func (es *egressSanitizer) predicateAnnotatedField(fullType, servicePackage, annotation string) goPredicateField {
	if es == nil || fullType == "" {
		return goPredicateField{}
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return goPredicateField{}
	}
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.IsMessage || f.IsRepeated || f.IsMap {
			continue
		}
		fpkg := f.TypePackage
		if fpkg == "" {
			fpkg = pkg
		}
		pred, ok := es.msgs[fpkg+"\x00"+f.Type]
		if !ok {
			continue
		}
		for j := range pred.Fields {
			pf := &pred.Fields[j]
			if pf.Annotations[annotation].BoolValue && !pf.IsRepeated && !pf.IsMessage {
				return goPredicateField{
					ReqField:    f.Name,
					TypeRef:     goTypeRef(fpkg+"."+f.Type, f.Type, servicePackage),
					PinField:    pf.Name,
					PinOptional: pf.IsOptional,
				}
			}
		}
	}
	return goPredicateField{}
}

// goPredicatePrincipalPin renders the stamp-or-refuse over the request's query
// predicate: unset -> stamped with the caller's subject; equal -> accepted;
// different -> INVALID_ARGUMENT, never silently overwritten.
func goPredicatePrincipalPin(pred goPredicateField, method, indent, failRet string) string {
	if pred.ReqField == "" || pred.PinField == "" {
		return ""
	}
	reqField, principal := pred.ReqField, pred.PinField
	rf, pf := toGoFieldName(reqField), toGoFieldName(principal)
	stamp := "__queryPrincipal"
	if pred.PinOptional {
		// The plane declares its filter fields `optional string`, so the Go
		// struct field is a *string. Java stamps the same pin through a builder
		// setter, which takes the value; Go has to take its address. The Get
		// accessor already reads nil and "" alike, which is exactly Java's
		// `!hasX() || getX().isEmpty()`.
		stamp = "&__queryPrincipal"
	}
	return fmt.Sprintf(`
%[1]s// (spec.store_principal) on the query predicate: this plane is per-principal.
%[1]s// The wrapper — never the handler — pins the predicate to the authenticated
%[1]s// caller at the lifecycle's single entry point, so every later call made
%[1]s// against the returned handle inherits it, the size-only ones included.
%[1]s// Unset -> stamped; equal -> accepted; different -> refused.
%[1]s__queryPrincipal := ""
%[1]sif caller != nil {
%[1]s	__queryPrincipal = caller.Token().GetSubject()
%[1]s}
%[1]sif __queryPrincipal == "" {
%[1]s	%[6]s status.Error(codes.PermissionDenied, "%[2]s queries rows owned by a principal and the caller presented no verified subject")
%[1]s}
%[1]s// An ABSENT predicate is stamped into existence rather than left unpinned:
%[1]s// Java pins through request.get%[3]s(), which is never null, so a request
%[1]s// that names no predicate at all still reaches the handler pinned.
%[1]sif req.%[3]s == nil {
%[1]s	req.%[3]s = &%[8]s{}
%[1]s}
%[1]sif __pred := req.Get%[3]s(); __pred != nil {
%[1]s	if __pred.Get%[4]s() == "" {
%[1]s		__pred.%[4]s = %[7]s
%[1]s	} else if __pred.Get%[4]s() != __queryPrincipal {
%[1]s		%[6]s status.Error(codes.InvalidArgument, "%[5]s names a principal other than the caller; the query plane is per-principal and the predicate is not the caller's to choose")
%[1]s	}
%[1]s}
`, indent, method, rf, pf, reqField+"."+principal, failRet, stamp, pred.TypeRef)
}

// goPredicateNamespacePin renders the same stamp-or-refuse for the tenant, with
// one difference that follows from the namespace backstop's: the caller's
// `linked` claims widen which tenant the predicate may NAME, exactly as they
// widen which tenant a row may be ACCEPTED from — the two must agree, or a
// linked caller's query would be answered and then aborted on the way out. The
// UNSET branch still stamps the caller's OWN namespace: a linked tenant is only
// ever reached by naming it, never by default.
func goPredicateNamespacePin(pred goPredicateField, method, indent, failRet string, runtimeGate bool) string {
	if pred.ReqField == "" || pred.PinField == "" {
		return ""
	}
	reqField, namespace := pred.ReqField, pred.PinField
	rf, nf := toGoFieldName(reqField), toGoFieldName(namespace)
	nsStamp := "__queryNamespace"
	if pred.PinOptional {
		nsStamp = "&__queryNamespace"
	}
	pinIndent, gateOpen, gateClose := indent, "", ""
	if runtimeGate {
		pinIndent = indent + "\t"
		gateOpen = fmt.Sprintf("%[1]s// A /system peer keeps the fleet-wide read: no pin, predicate untouched.\n%[1]sif !strings.HasPrefix(__queryNamespace, \"/system\") {\n", indent)
		gateClose = indent + "}\n"
	}
	body := fmt.Sprintf(`%[1]s// An ABSENT predicate is stamped into existence, as on the principal pin.
%[1]sif req.%[2]s == nil {
%[1]s	req.%[2]s = &%[7]s{}
%[1]s}
%[1]sif __pred := req.Get%[2]s(); __pred != nil {
%[1]s	if __pred.Get%[3]s() == "" {
%[1]s		__pred.%[3]s = %[6]s
%[1]s	} else if !__queryNsAccepted(__pred.Get%[3]s()) {
%[1]s		%[5]s status.Error(codes.InvalidArgument, "%[4]s names a tenant the caller neither owns nor is linked to; the tenant boundary is not the predicate's to choose")
%[1]s	}
%[1]s}
`, pinIndent, rf, nf, reqField+"."+namespace, failRet, nsStamp, pred.TypeRef)
	return fmt.Sprintf(`
%[1]s// (spec.store_namespace) on the query predicate: pin the tenant before the
%[1]s// store is asked. EXACT match against the caller's namespace claim, never the
%[1]s// level-or-below containment the administrative rule uses.
%[1]s// Unset -> stamped; the caller's own or a namespace it is LINKED to ->
%[1]s// accepted; anything else -> refused, which is what keeps the aborting egress
%[1]s// backstop unreachable from outside the process.
%[1]s__queryNamespace := ""
%[1]svar __queryNamespaceLinked []string
%[1]sif caller != nil {
%[1]s	__queryNamespace = caller.Token().GetNamespace()
%[1]s	__queryNamespaceLinked = caller.Token().GetLinked()
%[1]s}
%[1]sif __queryNamespace == "" {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[2]s queries rows held to a tenant and the caller presented no verified namespace claim")
%[1]s}
%[1]s__queryNsAccepted := func(__ns string) bool {
%[1]s	if __ns == __queryNamespace {
%[1]s		return true
%[1]s	}
%[1]s	for _, __linked := range __queryNamespaceLinked {
%[1]s		if __linked == __ns {
%[1]s			return true
%[1]s		}
%[1]s	}
%[1]s	return false
%[1]s}
`, indent, method, failRet) + gateOpen + body + gateClose
}

// goPredicatePins renders both predicate pins for one method, under the same
// gates Java applies: the principal pin is c2s-only (a peer service is not a
// user), the namespace pin also reaches the record plane's s2s half.
func goPredicatePins(san *egressSanitizer, method *parser.MethodInfo, servicePackage, indent, failRet string, isC2S, recordPlaneS2S bool) string {
	if !isC2S && !recordPlaneS2S {
		return ""
	}
	out := ""
	if isC2S {
		out += goPredicatePrincipalPin(san.predicateAnnotatedField(method.FullInputType, servicePackage, "store_principal"), method.Name, indent, failRet)
	}
	out += goPredicateNamespacePin(san.predicateAnnotatedField(method.FullInputType, servicePackage, "store_namespace"), method.Name, indent, failRet, recordPlaneS2S)
	return out
}

// ---------------------------------------------------------------------------
// The per-seam entry points the wrapper emitter calls. Each returns the resolve
// PREAMBLE (spliced where `caller` is bound) and the filter STATEMENTS (spliced
// after the handler returns), under Java's emission gate: the principal seams
// are c2s-only, the namespace seams also reach the record plane's s2s half,
// where the runtime gate keeps a /system peer fleet-wide.

// goRecordPlaneS2S is the ONE way the namespace seams reach an s2s surface: the
// synthesized record query plane's peer half, which a tenant-owned service
// calls with a /tenants/<t> identity. Same definition Java and Rust use.
func goRecordPlaneS2S(svc *parser.ServiceInfo) bool {
	return !svc.IsC2S && svc.SynthesizedKind == parser.SynthesizedKindRecordMap
}

// goPinnedPrincipalRows is the principal list with the unresolved rows dropped.
// An unresolved row on a private record is a generation error
// (goPrivatePinUnenforceable) and never reaches here; on a plain record it is
// simply a row with no owner to pin to.
func goPinnedPrincipalRows(san *egressSanitizer, fullType, servicePackage string) []goRowListField {
	var out []goRowListField
	for _, f := range san.principalListFields(fullType, servicePackage) {
		if f.ItemField != "" {
			out = append(out, f)
		}
	}
	return out
}

// goUnaryEgressFilter renders the egress row filters over a unary response.
func goUnaryEgressFilter(san *egressSanitizer, svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, indent, failRet string) (string, string) {
	recordPlaneS2S := goRecordPlaneS2S(svc)
	var principal, namespace []goRowListField
	if svc.IsC2S {
		principal = goPinnedPrincipalRows(san, method.FullOutputType, servicePackage)
	}
	if svc.IsC2S || recordPlaneS2S {
		namespace = san.namespaceListFields(method.FullOutputType, servicePackage)
	}
	if len(principal) == 0 && len(namespace) == 0 {
		return "", ""
	}
	return goEgressResolvePreamble(method.Name, indent, failRet, len(principal) > 0, anyRowPrivate(principal), len(namespace) > 0, recordPlaneS2S),
		goPrincipalUnaryFilter(principal, "resp", method.Name, indent) +
			goNamespaceUnaryFilter(namespace, "resp", method.Name, indent, recordPlaneS2S)
}

// goPageEgressFilter renders the egress row filters over a PAGE window's items,
// before the page is assembled into the response.
func goPageEgressFilter(san *egressSanitizer, svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, indent, failRet string) (string, string) {
	recordPlaneS2S := goRecordPlaneS2S(svc)
	itemType := method.Resolved.PageItemType
	principal, private, namespace := "", false, ""
	if svc.IsC2S {
		principal = san.principalField(itemType, servicePackage)
		private = san.recordPrivate(itemType, servicePackage)
	}
	if svc.IsC2S || recordPlaneS2S {
		namespace = san.namespaceField(itemType, servicePackage)
	}
	if principal == "" && namespace == "" {
		return "", ""
	}
	return goEgressResolvePreamble(method.Name, indent, failRet, principal != "", private, namespace != "", recordPlaneS2S),
		goPrincipalPageFilter(principal, method.Name, indent, private) +
			goNamespacePageFilter(namespace, method.Name, indent, recordPlaneS2S)
}

// goWatchEgressFilter renders the per-frame guards for a WATCH stream. The
// preamble resolves once, ahead of the producer loop; itemGuard and changeGuard
// are spliced into the snapshot and delta legs respectively, before the frame is
// sanitized and sent.
func goWatchEgressFilter(san *egressSanitizer, svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, indent, failRet string) (preamble, itemGuard, changeGuard string, err error) {
	recordPlaneS2S := goRecordPlaneS2S(svc)
	if !svc.IsC2S && !recordPlaneS2S {
		return "", "", "", nil
	}
	w, err := san.watchEgress(svc.Name, method.Name, method.Resolved.WatchItemType, method.Resolved.WatchChangeType, servicePackage)
	if err != nil || !w.pinned() {
		return "", "", "", err
	}
	// The principal guard is a statement about a user, so — as on the unary,
	// PAGE and predicate seams — it stays c2s-only.
	emitPrincipal := svc.IsC2S && w.needsPrincipal()
	preamble = goEgressResolvePreamble(method.Name, indent, failRet,
		emitPrincipal, w.needsPrivatePrincipal(), w.needsNamespace(), recordPlaneS2S)
	itemGuard = goWatchEgressGuardStmts(w.Item, "item", method.Name, "\t\t\t", emitPrincipal, recordPlaneS2S)
	changeGuard = goWatchEgressGuardStmts(w.Change, "change", method.Name, "\t\t", emitPrincipal, recordPlaneS2S)
	return preamble, itemGuard, changeGuard, nil
}

// ---------------------------------------------------------------------------
// What is still REFUSED at generation, and why neither refusal is a Go gap.

// goStreamEgressRefusal is the generation check for pinned rows on a streaming
// path. The egress filters are rendered on the unary, PAGE and WATCH seams.
// WATCH has its own per-frame hook and is refused only for a delta shape that
// hook cannot walk; every OTHER streaming shape (Tier-2/Tier-3 SNAPSHOT, a plain
// server stream) has no per-item hook at all, so a stream over pinned rows would
// return them unfiltered — refuse rather than under-enforce silently. Twin of
// rustStreamEgressRefusal, refusing the same set for the same reasons.
func goStreamEgressRefusal(serviceName string, method *parser.MethodInfo, san *egressSanitizer, servicePackage string) error {
	if method.IsWatchCollection() {
		_, err := san.watchEgress(serviceName, method.Name, method.Resolved.WatchItemType, method.Resolved.WatchChangeType, servicePackage)
		return err
	}
	if !method.ClientStreaming && !method.ServerStreaming {
		return nil
	}
	// Resolved is the computed COLLECTION view and is nil for a method that
	// declares no collection at all. PAGE is unary and never reaches here; its
	// branch renders the same filters the unary branch does.
	candidates := []string{method.FullOutputType}
	if method.Resolved != nil {
		candidates = append(candidates, method.Resolved.WatchItemType)
	}
	for _, t := range candidates {
		if t == "" {
			continue
		}
		for _, annotation := range []string{"store_principal", "store_namespace"} {
			pin := san.principalField
			if annotation == "store_namespace" {
				pin = san.namespaceField
			}
			if pin(t, servicePackage) != "" || len(san.recordListFields(t, servicePackage, pin)) > 0 {
				return fmt.Errorf("%s.%s returns %s, which carries (spec.%s), on a streaming path; the egress row filter is generated for unary, page and WATCH responses only",
					serviceName, method.Name, t, annotation)
			}
		}
	}
	return nil
}

// goPrivatePinUnenforceable reports why a (spec.record).private read cannot be
// pinned, or "" when it can. This is the one refusal that is LANGUAGE
// INDEPENDENT: a private record's rows belong to exactly one owner, and a
// surface with no caller principal to compare them against — no user-token path
// reaches it, or the row declares no owner any annotation names — has nothing
// for the pin to be. That is a generation-time error in Go, Java and Rust
// alike, not a Go gap.
func goPrivatePinUnenforceable(svc *parser.ServiceInfo, method *parser.MethodInfo, san *egressSanitizer, servicePackage string) string {
	t := goPrivateEgressRows(method, san, servicePackage)
	if t == "" {
		return ""
	}
	if !svc.IsC2S {
		return fmt.Sprintf("it returns %s, which carries (spec.record).private = true, on a surface no user token reaches; "+
			"the owner pin has no caller principal to compare rows against, so the read cannot be hosted here in any language", t)
	}
	if san.principalField(t, servicePackage) == "" {
		return fmt.Sprintf("it returns %s, which carries (spec.record).private = true but declares no owner the wrapper can pin to: "+
			"give the row's owning-principal field (spec.store_principal) = true, or declare (spec.record).owner naming a plain string field", t)
	}
	return ""
}
