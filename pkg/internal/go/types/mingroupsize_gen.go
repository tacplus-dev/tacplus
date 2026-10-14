// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The Go (spec.record).min_group_size guard — the second implementation of the
// seam Java (java/types/servicewrapper_gen.go) already carried, ported from it
// rather than re-derived, so a size-only query behaves the same whichever
// language hosts the record query plane.
//
// Until this file existed the Go wrapper refused GENERATION for any method
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
// covers: the unary guard runs before any transformation of the response, and
// the stream guard sits in front of the validating delegate so a below-threshold
// frame TERMINATES the stream rather than being withheld — a stream that went
// quiet at exactly N-1 would disclose the crossing by its silence. A client
// stream has no single response to compare and is refused at generation
// (goMinGroupSizeUnenforceable), exactly as Java refuses it. The MCP dispatch
// seam renders no size-only guard in any language and refuses the tool
// (goMcpToolGeneratable), which is Java's own stance there.

// minGroupSizeField resolves WHICH field of a size-only response carries the
// size the guard compares, via the shared parser.MinGroupSizeField so Go, Java
// and Rust cannot come to guard three different fields of one response.
//
// "" when the type is unknown or carries no single integer field.
// generateServiceWrapper turns that into a generation error rather than
// emitting an unguarded size-only RPC.
func (es *egressSanitizer) minGroupSizeField(fullType, servicePackage string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
	return parser.MinGroupSizeField(es.msgs[pkg+"\x00"+name])
}

// goMinGroupSizeRefusal renders the refusal body shared by both seams: the
// comment that says why, and the statement that denies. failStmt is how the
// enclosing shape gives up (a unary wrapper returns, a stream guard hands the
// error back to Send).
//
// The refusal is PermissionDenied and carries no count: naming the actual
// number, or any bound tighter than the declared threshold, would disclose
// exactly what the guard exists to withhold. The threshold itself is in the
// proto and is safe to name.
func goMinGroupSizeRefusal(n int32, valueExpr, sizeField, indent, failStmt string) string {
	return fmt.Sprintf(`%[1]s// (spec.record).min_group_size: a response that carries only a size, over
%[1]s// fewer than %[2]d records, is a membership disclosure by arithmetic. The
%[1]s// wrapper, never the handler, refuses.
%[1]sif %[3]s.Get%[4]s() < %[2]d {
%[1]s	%[5]s status.Error(codes.PermissionDenied, "min_group_size: a count over fewer than %[2]d records is a membership disclosure; widen the query's filter")
%[1]s}
`, indent, n, valueExpr, toGoFieldName(sizeField), failStmt)
}

// goMinGroupSizeGuard renders the guard over a UNARY size-only response.
// valueExpr names the response in scope; indent is the leading whitespace of the
// block it is spliced into. Empty when the method carries no threshold — the
// annotation is additive, so an undeclared record's RPCs are emitted exactly as
// before.
func goMinGroupSizeGuard(method *parser.MethodInfo, sizeField, valueExpr, indent string) string {
	if method.MinGroupSize <= 0 || sizeField == "" || method.ServerStreaming {
		return ""
	}
	return goMinGroupSizeRefusal(method.MinGroupSize, valueExpr, sizeField, indent, "return nil,")
}

// goMinGroupSizeStreamGuard renders the per-frame guard func for a STREAMED
// size (Subscribe<T>Count): the same arithmetic as the unary count, delivered
// repeatedly. It is handed to the generated guarded ServerStreamingServer, which
// applies it on Send ahead of response validation, so a below-threshold frame
// fails the stream carrying its own PermissionDenied rather than a validation
// Internal. Empty when the method carries no threshold.
func goMinGroupSizeStreamGuard(method *parser.MethodInfo, sizeField, goOutput string) string {
	if method.MinGroupSize <= 0 || sizeField == "" || !method.ServerStreaming || method.ClientStreaming {
		return ""
	}
	return fmt.Sprintf("func(msg *%s) error {\n%s\t\treturn nil\n\t}",
		goOutput, goMinGroupSizeRefusal(method.MinGroupSize, "msg", sizeField, "\t\t", "return"))
}

// goMinGroupSizeUnenforceable reports why the guard cannot be generated for a
// method that declares a threshold, or "" when it can. A size-only RPC that the
// wrapper cannot guard must fail generation rather than ship unguarded — the
// same fail-closed stance (spec.store_principal) already takes for the shapes
// its egress filter cannot reach. Twin of javaMinGroupSizeUnenforceable,
// refusing the same set for the same reasons.
func goMinGroupSizeUnenforceable(method *parser.MethodInfo, san *egressSanitizer, servicePackage string) string {
	if method.MinGroupSize <= 0 {
		return ""
	}
	if method.ClientStreaming {
		return "the guard is generated on unary responses and server streams only"
	}
	if san.minGroupSizeField(method.FullOutputType, servicePackage) == "" {
		return fmt.Sprintf("response %s carries no single integer size field for the guard to compare", method.FullOutputType)
	}
	return ""
}
