// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// The Rust (spec.store_epoch) stamp — the third implementation of the seam Java
// (java/types/servicewrapper_gen.go, javaStoreEpochStamp) and Go
// (go/types/storeepoch_gen.go) carry, ported from them rather than re-derived,
// so a write lands the same clock whichever language hosts it.
//
// Until this file existed the Rust wrapper refused GENERATION for any method
// whose request carried the annotation ("the stamp is generated for Java service
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
// WHICH SEAM. The unary request only, which is the set Java and Go cover: a
// streamed write would need a per-item stamp no wrapper renders, and is refused
// at generation (rustStoreEpochUnenforceable), exactly as Java and Go refuse it.
// The MCP dispatch seam renders no stamp in any language and refuses the tool
// (rustMcpToolGeneratable), which is Java's own stance there.
//
// SPLICE POINT: the stamp opens the unary validation block, immediately after
// req.validate() and AHEAD of the (spec.principal)/(spec.namespace) request
// pins — Java's relative ordering on its own unary seam, verbatim. The fields
// are disjoint (an int64 clock against string principal/namespace fields), so
// the ordering is about keeping one readable order across three languages, not
// about a conflict.

// storeEpochField resolves WHICH field of a message carries the row's store
// epoch, via the shared parser.StoreEpochField so Go, Java and Rust cannot come
// to stamp three different fields of one request. The PROTO name is returned —
// it is what the refusal names — and the renderer below casts it to prost's
// snake_case for the accessor.
//
// "" when the type is unknown or declares no epoch field, which is the common
// case: the annotation is additive and a request that declares none is emitted
// exactly as before.
func (es *rustEgressSanitizer) storeEpochField(fullType, servicePkg string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	return parser.StoreEpochField(es.msgs[pkg+"\x00"+name])
}

// rustStoreEpochStamp renders the stamp-or-refuse over a request in scope.
// reqVar names the owned request binding; indent is the column of the block it
// is spliced into. The binding is rebound mut for the stamp and frozen again
// after it, the same shape rustRequestPrincipalBlock uses on this seam. Empty
// when the request declares no epoch field.
func rustStoreEpochStamp(field, reqVar, indent string) string {
	if field == "" {
		return ""
	}
	return fmt.Sprintf(`
%[1]s// (spec.store_epoch) %[2]s: the row's last-write clock is the STORE's, never
%[1]s// the caller's. Unset -> stamped here; set -> refused. The wrapper owns the
%[1]s// stamp so no handler hand-writes it (same doctrine as (spec.principal)).
%[1]slet mut %[3]s = %[3]s;
%[1]sif %[3]s.%[4]s != 0 {
%[1]s    return Err(tonic::Status::invalid_argument(
%[1]s        "%[2]s is stamped by the store and must not be supplied by the caller"));
%[1]s}
%[1]s%[3]s.%[4]s = std::time::SystemTime::now()
%[1]s    .duration_since(std::time::UNIX_EPOCH)
%[1]s    .unwrap_or_default()
%[1]s    .as_millis() as i64;
%[1]slet %[3]s = %[3]s;
`, indent, field, reqVar, prostSnakeName(field))
}

// rustStoreEpochUnenforceable reports why the stamp cannot be generated for a
// method whose request carries an epoch field, or "" when it can. A write the
// wrapper cannot stamp must fail generation rather than reach a handler with an
// unstamped epoch — the same fail-closed stance (spec.store_principal) already
// takes for the shapes its egress filter cannot reach. Twin of
// javaStoreEpochUnenforceable, refusing the same set for the same reasons.
func rustStoreEpochUnenforceable(method *parser.MethodInfo, san *rustEgressSanitizer, servicePkg string) string {
	if san.storeEpochField(method.FullInputType, servicePkg) == "" {
		return ""
	}
	if method.ClientStreaming || method.ServerStreaming {
		return "the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render"
	}
	return ""
}
