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

// goRequestScopeBlocks renders every marker for one method's request
// ((spec.principal), (spec.namespace), (spec.caller_tid)), or "" when it
// declares none. The wording of each refusal is the MCP dispatch's,
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
	if f := san.annotatedStringField(method.FullInputType, servicePackage, "caller_tid"); f != "" {
		out += goCallerTidBlock(method.Name, f, reqExpr, indent, failRet)
	}
	return out
}

// goCallerTidBlock renders the binding of a (spec.caller_tid) request field to
// the caller's token's tid claim (#475, products-layer-2026-11-08): a product
// keeps a tenant's data only keyed by the tid of the tenant calling, never by
// one the request names. An empty field is stamped and an equal one passes;
// another tenant, or a caller with no tid claim (the platform, a product), is
// PERMISSION_DENIED before the handler runs: an authenticated caller that may
// not reach any tenant's rows, not a missing credential.
func goCallerTidBlock(method, field, reqExpr, indent, failRet string) string {
	return fmt.Sprintf(`
%[1]s// (spec.caller_tid) %[2]s: bind the field to the caller's tenant.
%[1]s__caller_tid := ""
%[1]sif caller != nil {
%[1]s	__caller_tid = caller.Token().GetTid()
%[1]s}
%[1]sif __caller_tid == "" {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[4]s requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)")
%[1]s}
%[1]sif %[5]s.%[6]s == "" {
%[1]s	%[5]s.%[6]s = __caller_tid
%[1]s} else if %[5]s.%[6]s != __caller_tid {
%[1]s	%[3]s status.Error(codes.PermissionDenied, "%[2]s does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data")
%[1]s}
`, indent, field, failRet, method, reqExpr, toGoFieldName(field))
}
