// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// The Go (spec.store_epoch) stamp — the second implementation of the seam Java
// (java/types/servicewrapper_gen.go, javaStoreEpochStamp) already carried,
// ported from it rather than re-derived, so a write lands the same clock
// whichever language hosts it.
//
// Until this file existed the Go wrapper refused GENERATION for any method whose
// request carried the annotation ("the stamp is generated for Java service
// wrappers only"). That was a stopgap against reaching a handler with an
// unstamped epoch, never a design: the refusal it fed is gone and the stamp
// below stands in its place.
//
// WHAT IT STAMPS. store_epoch is the row's last-WRITE clock, in Unix
// milliseconds: what deleted_max_age_seconds measures the delete write from and
// what the disk leg ranges on. The store owns it outright. Unlike
// (spec.principal) there is no "equal passes" case — the value is never
// caller-supplied, so any supplied value, even a correct-looking one, is a
// refusal. A row that reached the store unstamped is a row the purge measures as
// infinitely old.
//
// WHICH SEAM. The unary request only, which is the set Java covers: a streamed
// write would need a per-item stamp no wrapper renders, and is refused at
// generation (goStoreEpochUnenforceable), exactly as Java refuses it. The MCP
// dispatch seam renders no stamp in any language and refuses the tool
// (goMcpToolGeneratable), which is Java's own stance there.
//
// SPLICE POINT: the stamp is prepended to callerExtract, ahead of the
// (spec.store_principal)/(spec.store_namespace) predicate pins and the
// (spec.principal)/(spec.namespace) request pins, which is Java's relative
// ordering on its own unary seam. callerExtract, not inputValidation, for the
// reason requestscope_gen.go gives: on the trace-v2 unary emit inputValidation
// is hoisted into the auth preamble, so it is the one statement that appears in
// the same relative position in every Go emit shape. The fields are disjoint —
// an int64 clock against string principal/namespace fields — so the ordering is
// about keeping one readable order across three languages, not about a conflict.

// storeEpochField resolves WHICH field of a message carries the row's store
// epoch, via the shared parser.StoreEpochField so Go, Java and Rust cannot come
// to stamp three different fields of one request.
//
// "" when the type is unknown or declares no epoch field, which is the common
// case: the annotation is additive and a request that declares none is emitted
// exactly as before.
func (es *egressSanitizer) storeEpochField(fullType, servicePackage string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	return parser.StoreEpochField(es.msgs[pkg+"\x00"+name])
}

// goStoreEpochStamp renders the stamp-or-refuse over a request in scope.
// reqExpr names the request; indent is the leading whitespace of the block it is
// spliced into and failRet the shape's error return. Empty when the request
// declares no epoch field.
func goStoreEpochStamp(field, reqExpr, indent, failRet string) string {
	if field == "" {
		return ""
	}
	f := toGoFieldName(field)
	return fmt.Sprintf(`
%[1]s// (spec.store_epoch) %[2]s: the row's last-write clock is the STORE's, never
%[1]s// the caller's. Unset -> stamped here; set -> refused. The wrapper owns the
%[1]s// stamp so no handler hand-writes it (same doctrine as (spec.principal)).
%[1]sif %[3]s.%[4]s != 0 {
%[1]s	%[5]s status.Error(codes.InvalidArgument, "%[2]s is stamped by the store and must not be supplied by the caller")
%[1]s}
%[1]s%[3]s.%[4]s = time.Now().UnixMilli()
`, indent, field, reqExpr, f, failRet)
}

// goStoreEpochUnenforceable reports why the stamp cannot be generated for a
// method whose request carries an epoch field, or "" when it can. A write the
// wrapper cannot stamp must fail generation rather than reach a handler with an
// unstamped epoch — the same fail-closed stance (spec.store_principal) already
// takes for the shapes its egress filter cannot reach. Twin of
// javaStoreEpochUnenforceable, refusing the same set for the same reasons.
func goStoreEpochUnenforceable(method *parser.MethodInfo, san *egressSanitizer, servicePackage string) string {
	if san.storeEpochField(method.FullInputType, servicePackage) == "" {
		return ""
	}
	if method.ClientStreaming || method.ServerStreaming {
		return "the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render"
	}
	return ""
}
