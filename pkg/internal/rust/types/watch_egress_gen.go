// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The (spec.store_principal) / (spec.store_namespace) egress filters on the
// WATCH seam — the Rust twin of javaPrincipalWatchGuard / javaNamespaceWatchGuard
// (java/types/recordprincipal_gen.go, recordnamespace_gen.go).
//
// Until this file existed the Rust wrapper enforced both filters on the unary
// and PAGE seams only and REFUSED to generate a WATCH over pinned rows: there was
// no per-item hook on the stream path, and a stream that returned rows with no
// boundary would have been an under-enforcement nobody could see in a green
// build. That refusal was correct, and it is what kept scottypersonal's
// WatchChatEvents and work's WatchJobs/WatchTasks off the air (the client
// polled instead). The guard here is the missing hook: it runs inside the
// generated producer task, on every snapshot_item and every delta, before the
// envelope is built and sent, so a handler never sees or writes any of it.
//
// TWO ENVELOPE SHAPES ARE WALKED, because both exist in the fleet:
//
//	delta IS the row       — `ChatEvent delta = 3;` (an append-only record has
//	                         no update/delete frame to distinguish, so the
//	                         change is the new row itself).
//	delta WRAPS the row    — `ObservationWatchChange { Observation observation;
//	                         int64 revision; }` / `<T>WatchChange { item }`: a
//	                         change envelope with ONE level of singular message
//	                         fields, any of which may be a pinned row.
//
// Anything else — a pinned row inside a repeated or map field of the change,
// or nested deeper than one level — is refused at generation, exactly as the
// pre-hook wrapper refused everything: the stream path has no shape for it and
// a partial guard would be the silent under-enforcement this hook exists to end.
//
// SEMANTICS MATCH THE UNARY/PAGE SEAMS, in the same order (principal, then
// namespace over what survived):
//
//	(spec.store_principal)  mismatch -> the event is skipped, never sent. The
//	                         snapshot_done marker carries no row and always goes
//	                         out, so a fully filtered snapshot still terminates.
//	(spec.store_namespace)  mismatch -> the process aborts (rustNamespaceCrash),
//	                         for the reason the unary backstop aborts: a foreign
//	                         tenant's row reaching this comparison means the
//	                         handler's own query already leaked it.
//
// The caller's principal and namespace are resolved ONCE, at function level
// before the producer task is spawned (rustPrincipalResolve /
// rustNamespaceResolve, both fail-closed with `?`), and moved into the task.

// rustStreamEgressRefusal is the service-level generation check for pinned rows
// on a streaming path. (spec.store_principal)/(spec.store_namespace) are
// enforced on the unary, PAGE and WATCH egress seams. WATCH has its own
// per-frame hook (below) and is refused only for a delta shape that hook
// cannot walk; every OTHER streaming shape (Tier-2/Tier-3 SNAPSHOT, a plain
// server stream) has no per-item hook, and a stream over pinned rows would
// return them unfiltered — so refuse to generate rather than under-enforce
// silently. Returns nil for a unary method or an unpinned stream.
func rustStreamEgressRefusal(serviceName string, method *parser.MethodInfo, san *rustEgressSanitizer, pkg string) error {
	if method.IsWatchCollection() {
		_, err := san.watchEgress(serviceName, method.Name, method.Resolved.WatchItemType, method.Resolved.WatchChangeType, pkg)
		return err
	}
	if method.StreamingType() == "unary" {
		return nil
	}
	// Resolved is the computed COLLECTION view and is nil for a method that
	// declares no collection at all — a plain server-streaming RPC such as
	// oauth's Session.*. PAGE is unary and never reaches here; its branch in
	// generateRustGRPCWrapper generates the same egress filter the unary
	// branch does.
	candidates := []string{method.FullOutputType}
	if method.Resolved != nil {
		candidates = append(candidates, method.Resolved.WatchItemType)
	}
	for _, t := range candidates {
		if t == "" {
			continue
		}
		if san.storePrincipalField(t, pkg) != "" || len(san.principalListFields(t, pkg)) > 0 {
			return fmt.Errorf("%s.%s returns %s, which carries (spec.store_principal), on a %s path; the principal read filter is generated for unary, page and WATCH responses only",
				serviceName, method.Name, t, method.StreamingType())
		}
		// Same seam, same limit: (spec.store_namespace) is mandatory on every
		// record, so a Rust stream over record rows would return them with no
		// tenant boundary at all.
		if san.storeNamespaceField(t, pkg) != "" || len(san.namespaceListFields(t, pkg)) > 0 {
			return fmt.Errorf("%s.%s returns %s, which carries (spec.store_namespace), on a %s path; the namespace read filter is generated for unary, page and WATCH responses only",
				serviceName, method.Name, t, method.StreamingType())
		}
	}
	return nil
}

// rustWatchRowGuard names one pinned row reachable from a watch frame.
//
//	Path  is the field chain from the frame variable to the row, empty when
//	      the frame IS the row (snapshot_item, or a row-shaped delta), or one
//	      singular message field name when the delta wraps it.
//	Principal / Namespace are the row's (spec.store_principal) /
//	      (spec.store_namespace) field names; either may be "".
type rustWatchRowGuard struct {
	Path      string
	Principal string
	Namespace string
	Private   bool // the row type also declares (spec.record).private
}

// rustWatchEgress is the resolved guard set for one WATCH method.
type rustWatchEgress struct {
	Item   []rustWatchRowGuard // guards on the snapshot_item frame
	Change []rustWatchRowGuard // guards on the delta frame
}

// pinned reports whether any guard exists at all — the wrapper emits nothing
// (and resolves nothing) for a WATCH over unpinned rows such as weather's.
func (w rustWatchEgress) pinned() bool { return len(w.Item)+len(w.Change) > 0 }

func (w rustWatchEgress) needsPrincipal() bool {
	for _, g := range append(append([]rustWatchRowGuard{}, w.Item...), w.Change...) {
		if g.Principal != "" {
			return true
		}
	}
	return false
}

// needsPrivatePrincipal reports whether any principal-pinned guard is also a
// (spec.record).private row — the resolve for the whole preamble (they share
// one __read_principal binding) must be the private-aware one if so.
func (w rustWatchEgress) needsPrivatePrincipal() bool {
	for _, g := range append(append([]rustWatchRowGuard{}, w.Item...), w.Change...) {
		if g.Principal != "" && g.Private {
			return true
		}
	}
	return false
}

func (w rustWatchEgress) needsNamespace() bool {
	for _, g := range append(append([]rustWatchRowGuard{}, w.Item...), w.Change...) {
		if g.Namespace != "" {
			return true
		}
	}
	return false
}

// dropNamespace returns the guard set with every (spec.store_namespace) pin
// cleared, and any guard that carried nothing else removed. This is the WATCH
// seam's form of the `svc.IsC2S || recordPlaneS2S` existence gate the unary and
// PAGE seams apply before they splice rustResponseNamespaceFilterBlock, and the
// twin of the same gate around Java's javaNamespaceWatchGuard call site: the
// namespace comparison is against the AUTHENTICATED CALLER's namespace, which is
// a tenant boundary on the c2s half and on the s2s RECORD plane (where
// __ns_pinned decides per caller), and nothing at all on a plain s2s surface —
// there every peer is a fleet service whose reads were never tenant-scoped, so
// narrowing them to the peer's own namespace is not a boundary but a break. The
// WATCH seam is the one place that emitted the guard unconditionally, which made
// a mismatch on such a surface an abort where its unary sibling emitted no
// comparison at all.
//
// Only the namespace half is gated. (spec.store_principal) stays where it is:
// the Rust unary, PAGE and WATCH seams all emit it ungated, so the WATCH seam is
// already consistent with its siblings there.
func (w rustWatchEgress) dropNamespace() rustWatchEgress {
	var out rustWatchEgress
	for _, src := range []struct {
		in  []rustWatchRowGuard
		dst *[]rustWatchRowGuard
	}{{w.Item, &out.Item}, {w.Change, &out.Change}} {
		for _, g := range src.in {
			g.Namespace = ""
			if g.Principal == "" {
				continue
			}
			*src.dst = append(*src.dst, g)
		}
	}
	return out
}

// watchEgress resolves the guard set for a WATCH method's item and change
// types. The error is the generation refusal for a shape the guard cannot
// reach; a nil error with an empty set means the rows carry no pins.
func (es *rustEgressSanitizer) watchEgress(serviceName, methodName, itemType, changeType, servicePkg string) (rustWatchEgress, error) {
	var out rustWatchEgress
	if es == nil {
		return out, nil
	}
	// snapshot_item is always the row itself.
	if g, ok := es.watchRowPins(itemType, servicePkg); ok {
		out.Item = append(out.Item, g)
	}
	// delta: the row itself, or a one-level envelope around it.
	if g, ok := es.watchRowPins(changeType, servicePkg); ok {
		out.Change = append(out.Change, g)
	} else if changeType != "" {
		pkg, name := splitRustEgressFQN(changeType, servicePkg)
		if msg, found := es.msgs[pkg+"\x00"+name]; found {
			for _, f := range msg.Fields {
				if !f.IsMessage {
					continue
				}
				fpkg := f.TypePackage
				if fpkg == "" {
					fpkg = pkg
				}
				g, pinned := es.watchRowPins(fpkg+"."+f.Type, servicePkg)
				if !pinned {
					continue
				}
				if f.IsRepeated || f.IsMap || f.IsOneOf {
					return out, fmt.Errorf("%s.%s: delta %s carries pinned %s rows in %s, a %s field; the WATCH egress filter walks one singular message field of the delta and cannot hold a %s to the caller — reshape the delta to carry the row directly or as one singular field",
						serviceName, methodName, changeType, fpkg+"."+f.Type, f.Name, rustWatchFieldKind(f.IsRepeated, f.IsMap, f.IsOneOf), rustWatchFieldKind(f.IsRepeated, f.IsMap, f.IsOneOf))
				}
				g.Path = prostSnakeName(f.Name)
				out.Change = append(out.Change, g)
			}
		}
	}
	return out, nil
}

func rustWatchFieldKind(repeated, isMap, oneof bool) string {
	switch {
	case isMap:
		return "map"
	case repeated:
		return "repeated"
	case oneof:
		return "oneof"
	}
	return "singular"
}

// watchRowPins returns the pin fields of fullType when it is a pinned row.
func (es *rustEgressSanitizer) watchRowPins(fullType, servicePkg string) (rustWatchRowGuard, bool) {
	g := rustWatchRowGuard{
		Principal: es.storePrincipalField(fullType, servicePkg),
		Namespace: es.storeNamespaceField(fullType, servicePkg),
		Private:   es.isRecordPrivate(fullType, servicePkg),
	}
	return g, g.Principal != "" || g.Namespace != ""
}

// rustWatchEgressPreamble renders the function-level resolution of the
// caller's principal and namespace, to be spliced before the producer task is
// spawned. Fail-closed like the unary seam: an unauthenticated caller, or one
// with no namespace claim, cannot open a watch over pinned rows.
// runtimeGate is the s2s record plane's gate, threaded through exactly as the
// unary and PAGE seams thread it: it adds __ns_pinned to the namespace binding,
// which the per-frame guards below branch on so a /system peer keeps its
// fleet-wide read.
func rustWatchEgressPreamble(w rustWatchEgress, method string, runtimeGate bool) string {
	if !w.pinned() {
		return ""
	}
	var b strings.Builder
	// Name only the pins actually rendered: a surface whose namespace half was
	// gated off (dropNamespace) must not carry a comment claiming a
	// (spec.store_namespace) backstop it does not enforce.
	pins := "(spec.store_principal)/(spec.store_namespace)"
	switch {
	case !w.needsNamespace():
		pins = "(spec.store_principal)"
	case !w.needsPrincipal():
		pins = "(spec.store_namespace)"
	}
	fmt.Fprintf(&b, "\n        // %s on the WATCH seam: the same\n", pins)
	b.WriteString("        // egress backstop the unary and PAGE seams apply, resolved once here and\n")
	b.WriteString("        // applied to every snapshot_item and delta inside the producer task.\n")
	if w.needsPrincipal() {
		if w.needsPrivatePrincipal() {
			b.WriteString(rustPrivatePrincipalResolve("__read_principal", method, "        "))
		} else {
			b.WriteString(rustPrincipalResolve("__read_principal", method, "        "))
		}
	}
	if w.needsNamespace() {
		b.WriteString(rustNamespaceResolve("__read_namespace", method, "        ", runtimeGate))
	}
	return b.String()
}

// rustWatchEgressGuardStmts renders the per-frame guards for one frame
// variable (`item` or `change`) inside the producer loop. A principal
// mismatch `continue`s the enclosing `while let` (the frame is never sent); a
// namespace mismatch aborts.
func rustWatchEgressGuardStmts(guards []rustWatchRowGuard, frameVar, method, indent string, runtimeGate bool) string {
	if len(guards) == 0 {
		return ""
	}
	var b strings.Builder
	// Principal first, then namespace over what survived — the order the PAGE
	// seam pins (TestRustPageWrapperAppliesPrincipalAndNamespaceEgressFilter).
	for _, g := range guards {
		if g.Principal == "" {
			continue
		}
		field := frameVar
		if g.Path != "" {
			field = frameVar + "." + g.Path
		}
		if !g.Private {
			fmt.Fprintf(&b, "\n%s// (spec.store_principal): a frame carrying a row the caller does not own is never sent.\n", indent)
			if g.Path == "" {
				fmt.Fprintf(&b, "%sif %s.%s != __read_principal { continue; }\n", indent, frameVar, g.Principal)
			} else {
				fmt.Fprintf(&b, "%sif let Some(__row) = %s.%s.as_ref() { if __row.%s != __read_principal { continue; } }\n", indent, frameVar, g.Path, g.Principal)
			}
			continue
		}
		// (spec.record).private: a mismatch here means the store's own query
		// already leaked another owner's row, same posture as the namespace
		// guard below — abort, don't silently drop the frame.
		fmt.Fprintf(&b, "\n%s// (spec.record).private: a frame carrying a row the caller does not own aborts.\n", indent)
		if g.Path == "" {
			fmt.Fprintf(&b, "%sif %s.%s != __read_principal {\n%s%s}\n", indent, frameVar, g.Principal,
				rustPrincipalCrash(method, field, indent+"    "), indent)
		} else {
			fmt.Fprintf(&b, "%sif let Some(__row) = %s.%s.as_ref() { if __row.%s != __read_principal {\n%s%s} }\n", indent, frameVar, g.Path, g.Principal,
				rustPrincipalCrash(method, field, indent+"    "), indent)
		}
	}
	for _, g := range guards {
		if g.Namespace == "" {
			continue
		}
		field := frameVar
		if g.Path != "" {
			field = frameVar + "." + g.Path
		}
		fmt.Fprintf(&b, "\n%s// (spec.store_namespace): hold the frame to the caller's tenant; a foreign row aborts.\n", indent)
		if g.Path == "" {
			fmt.Fprintf(&b, "%sif %s {\n%s%s}\n", indent,
				rustNamespaceDropExpr(fmt.Sprintf("%s.%s != __read_namespace", frameVar, g.Namespace), runtimeGate),
				rustNamespaceCrash(method, field, indent+"    "), indent)
		} else {
			fmt.Fprintf(&b, "%sif let Some(__row) = %s.%s.as_ref() { if %s {\n%s%s} }\n", indent, frameVar, g.Path,
				rustNamespaceDropExpr(fmt.Sprintf("__row.%s != __read_namespace", g.Namespace), runtimeGate),
				rustNamespaceCrash(method, field, indent+"    "), indent)
		}
	}
	return b.String()
}
