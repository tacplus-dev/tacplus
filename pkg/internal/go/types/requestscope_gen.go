// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The request half of the principal seam on the PLAIN gRPC wrapper.
//
// (spec.principal) and (spec.namespace) on a request field are a statement
// about the CALL, not about the handler: the generated wrapper resolves the
// authenticated caller's subject / namespace claim and stamps it into an empty
// field, accepts a field that already equals it, and refuses one that differs.
// A forged value is never silently overwritten, and a caller with no verified
// claim never reaches the handler.
//
// The MCP tool dispatch next door has rendered exactly this since it was
// written (mcp_dispatch_gen.go, goMcpRequestScopeBlock) over the same
// annotatedStringField lookup. The plain wrapper did not, on any RPC shape,
// which is what this file fixes — the contract in spec.proto says "the
// generated wrapper" and carves out neither shape nor surface.
//
// SPLICE POINT: these blocks are appended to callerExtract, not to
// inputValidation. callerExtract is the one statement that appears, in the same
// relative position, in every Go emit shape — unary, PAGE, Tier-2, Tier-3,
// WATCH and server-streaming — and it is where `caller` becomes bound. On the
// trace-v2 unary emit inputValidation is hoisted INTO the auth preamble, ahead
// of callerExtract, so a guard spliced there would run before the caller it
// compares against exists. The consequence is that the stamp runs before
// request validation: a field the stamp is about to fill is not first rejected
// for being empty.

// goRequestScopeBlock renders the stamp-or-refuse for one caller-scoped request
// field. reqExpr names the request in scope and failRet is the shape's error
// return ("return nil," on a unary/PAGE wrapper, "return" on a streaming one).
func goRequestScopeBlock(method, field, marker, accessor, needs, mismatch, reqExpr, indent, failRet string) string {
	local := "__" + marker
	return fmt.Sprintf(`
%[1]s// (spec.%[2]s) %[3]s: bind the field to %[4]s.
%[1]s%[5]s := ""
%[1]sif caller != nil {
%[1]s	%[5]s = caller.Token().%[6]s()
%[1]s}
%[1]sif %[5]s == "" {
%[1]s	%[7]s status.Error(codes.Unauthenticated, "%[8]s requires %[9]s")
%[1]s}
%[1]sif %[10]s.%[11]s == "" {
%[1]s	%[10]s.%[11]s = %[5]s
%[1]s} else if %[10]s.%[11]s != %[5]s {
%[1]s	%[7]s status.Error(codes.InvalidArgument, "%[3]s does not match %[4]s")
%[1]s}
`, indent, marker, field, mismatch, local, accessor, failRet, method, needs, reqExpr, toGoFieldName(field))
}

// goRequestScopeBlocks renders both markers for one method's request, or "" when
// it declares neither. The wording of each refusal is the MCP dispatch's,
// verbatim: the same annotation must not read differently depending on which
// surface served the call.
func goRequestScopeBlocks(san *egressSanitizer, method *parser.MethodInfo, servicePackage, reqExpr, indent, failRet string) string {
	out := ""
	if f := san.annotatedStringField(method.FullInputType, servicePackage, "principal"); f != "" {
		out += goRequestScopeBlock(method.Name, f, "principal", "GetSubject",
			"an authenticated caller", "the authenticated caller", reqExpr, indent, failRet)
	}
	if f := san.annotatedStringField(method.FullInputType, servicePackage, "namespace"); f != "" {
		out += goRequestScopeBlock(method.Name, f, "namespace", "GetNamespace",
			"an authenticated caller carrying a namespace claim", "the authenticated caller's namespace", reqExpr, indent, failRet)
	}
	return out
}
