// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// The Rust (spec.record).min_group_size guard — the third implementation of the
// seam Java (java/types/servicewrapper_gen.go) and Go (go/types/mingroupsize_gen.go)
// carry, ported from them rather than re-derived, so a size-only query behaves
// the same whichever language hosts the record query plane.
//
// Until this file existed the Rust wrapper refused GENERATION for any method
// carrying the annotation ("the guard is generated for Java service wrappers
// only"). That was a stopgap against serving the disclosure unguarded, never a
// design: the refusal it fed is gone and the guard below stands in its place.
//
// WHAT IT GUARDS. min_group_size is the smallest result size a record's
// size-only surfaces will disclose. A response that carries only a number, over
// fewer than N records, is a membership disclosure by arithmetic — asking for
// the count of a filter narrow enough to name one row answers whether that row
// exists. The record-map injector stamps the threshold onto exactly those RPCs:
// Get<T>Count and Subscribe<T>Count (RecordCount.count) and Update<T>Query
// (QueryStats.size, the live match count of the newly installed predicate).
//
// WHICH SEAMS. The unary response and the server stream, which is the set Java
// and Go cover: the unary guard runs before any transformation of the response,
// and the stream guard wraps the response stream ahead of the sanitize and the
// per-item validation, so a below-threshold frame TERMINATES the stream rather
// than being withheld — a stream that went quiet at exactly N-1 would disclose
// the crossing by its silence. A client stream has no single response to compare
// and is refused at generation (rustMinGroupSizeUnenforceable), exactly as Java
// and Go refuse it. The MCP dispatch seam renders no size-only guard in any
// language and refuses the tool (rustMcpToolGeneratable), which is Java's own
// stance there.

// minGroupSizeField resolves WHICH field of a size-only response carries the
// size the guard compares, via the shared parser.MinGroupSizeField so Go, Java
// and Rust cannot come to guard three different fields of one response.
//
// "" when the type is unknown or carries no single integer field.
// generateServiceWrapper turns that into a generation error rather than
// emitting an unguarded size-only RPC.
func (es *rustEgressSanitizer) minGroupSizeField(fullType, servicePkg string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	return prostSnakeName(parser.MinGroupSizeField(es.msgs[pkg+"\x00"+name]))
}

// rustMinGroupSizeRefusal renders the refusal body shared by both seams: the
// comment that says why, and the statement that denies.
//
// The refusal is PERMISSION_DENIED and carries no count: naming the actual
// number, or any bound tighter than the declared threshold, would disclose
// exactly what the guard exists to withhold. The threshold itself is in the
// proto and is safe to name.
func rustMinGroupSizeRefusal(n int32, valueExpr, sizeField, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.record).min_group_size: a response that carries only a size, over
%[1]s// fewer than %[2]d records, is a membership disclosure by arithmetic. The
%[1]s// wrapper, never the handler, refuses.
%[1]sif %[3]s.%[4]s < %[2]d {
%[1]s    return Err(tonic::Status::permission_denied("min_group_size: a count over fewer than %[2]d records is a membership disclosure; widen the query's filter"));
%[1]s}
`, indent, n, valueExpr, sizeField)
}

// rustMinGroupSizeGuard renders the guard over a UNARY size-only response.
// valueExpr names the response in scope; indent is the leading whitespace of the
// block it is spliced into. Empty when the method carries no threshold — the
// annotation is additive, so an undeclared record's RPCs are emitted exactly as
// before.
func rustMinGroupSizeGuard(method *parser.MethodInfo, sizeField, valueExpr, indent string) string {
	if method.MinGroupSize <= 0 || sizeField == "" || method.ServerStreaming {
		return ""
	}
	return rustMinGroupSizeRefusal(method.MinGroupSize, valueExpr, sizeField, indent)
}

// rustMinGroupSizeStreamGuard renders the per-frame guard over a STREAMED size
// (Subscribe<T>Count): the same arithmetic as the unary count, delivered
// repeatedly. It rebinds the handler's stream the same way the lazy egress
// sanitize does, and is spliced AHEAD of it so a below-threshold frame fails the
// stream with its own PERMISSION_DENIED before anything transforms it. Empty
// when the method carries no threshold.
func rustMinGroupSizeStreamGuard(method *parser.MethodInfo, sizeField string) string {
	if method.MinGroupSize <= 0 || sizeField == "" || !method.ServerStreaming || method.ClientStreaming {
		return ""
	}
	return fmt.Sprintf(`        let response = response.map(|stream| {
            let stream = stream.map(|item| {
                item.and_then(|msg| {
%s                    Ok(msg)
                })
            });
            Box::pin(stream) as Self::%sStream
        });
`, rustMinGroupSizeRefusal(method.MinGroupSize, "msg", sizeField, "                    "), method.Name)
}

// rustMinGroupSizeUnenforceable reports why the guard cannot be generated for a
// method that declares a threshold, or "" when it can. A size-only RPC that the
// wrapper cannot guard must fail generation rather than ship unguarded — the
// same fail-closed stance (spec.store_principal) already takes for the shapes
// its egress filter cannot reach. Twin of javaMinGroupSizeUnenforceable,
// refusing the same set for the same reasons.
func rustMinGroupSizeUnenforceable(method *parser.MethodInfo, san *rustEgressSanitizer, servicePkg string) string {
	if method.MinGroupSize <= 0 {
		return ""
	}
	if method.ClientStreaming {
		return "the guard is generated on unary responses and server streams only"
	}
	if san.minGroupSizeField(method.FullOutputType, servicePkg) == "" {
		return fmt.Sprintf("response %s carries no single integer size field for the guard to compare", method.FullOutputType)
	}
	return ""
}
