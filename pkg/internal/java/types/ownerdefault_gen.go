// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strconv"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The owner_default seam. (spec.owner_default) on a row type says what the
// type's owner may do with its own rows; the generated service WRAPPER refuses
// the RPCs that contradict it, at the same seam that already owns the principal
// stamp (requestscope_gen.go). A handler never sees the refused call.
//
// Java twin of go/types/ownerdefault_gen.go and rust/types/ownerdefault_gen.go:
// what is refused is decided by parser.PlanOwnerDefault, and the refusal is
// scoped the same way. Before this file the Java wrapper ignored the annotation
// entirely — not a refusal, an absence.

// javaOwnerDefaultSeam names what one seam renders the guard against: how it
// reaches the caller's principal_scope and subject, the request in scope, and
// its own refusal statement (one %s for the quoted message).
type javaOwnerDefaultSeam struct {
	callerLocals string // statements binding __principalScope and __odCaller
	request      string // the request expression, already stamped by (spec.principal)
	deny         string
}

// javaOwnerDefaultGrpcSeam reads the caller off the verified token, as the
// (spec.principal) stamp and the egress data grants on the same seam do.
func javaOwnerDefaultGrpcSeam(indent string) javaOwnerDefaultSeam {
	return javaOwnerDefaultSeam{
		callerLocals: fmt.Sprintf(`%[1]svar __odTok = Auth.verifiedTokenFromContext();
%[1]svar __odEnt = __odTok == null ? null : __odTok.getServiceEntitlements();
%[1]sfinal String __principalScope = __odEnt == null ? "" : __odEnt.getPrincipalScope();
%[1]sfinal String __odCaller = __odTok == null ? "" : __odTok.getSubject();
`, indent),
		request: "request",
		deny:    indent + "    throw new ProtoException(ProtoException.PERMISSION_DENIED, %s);\n",
	}
}

// javaOwnerDefaultMcpSeam reads the caller off the session's Caller value: a
// tool call runs on the MCP SDK's own thread, where the gRPC Context is not in
// scope.
func javaOwnerDefaultMcpSeam(indent string) javaOwnerDefaultSeam {
	return javaOwnerDefaultSeam{
		callerLocals: fmt.Sprintf(`%[1]sfinal String __principalScope = caller.getPrincipalScope();
%[1]sfinal String __odCaller = caller.getSubject();
`, indent),
		request: "__req",
		deny:    indent + "    return toolError(%s);\n",
	}
}

// javaOwnerDefaultPlan resolves the shared decision against this generator's
// message index.
func javaOwnerDefaultPlan(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string) (parser.OwnerDefaultPlan, error) {
	if san == nil {
		return parser.OwnerDefaultPlan{}, nil
	}
	return parser.PlanOwnerDefault(method, servicePackage, func(pkg, name string) (*model.Message, bool) {
		m, ok := san.msgs[pkg+"\x00"+name]
		return m, ok
	})
}

// javaOwnerDefaultGuard renders the refusal the wrapper emits when the method's
// access contradicts the owner rights of a type it touches, or "" when nothing
// is declared or nothing is contradicted. The error names the type and the
// right, so a denial here is never mistaken for a missing grant.
//
// The refusal is the OWNER's, not everyone's. Two facts scope it:
//
//   - __scopeIsSelf: a caller whose grant carries a (spec.principal_scope)
//     wider than self is not acting as the row's own principal, so an owner
//     default — a DEFAULT — does not bind it. Absent, empty, UNSPECIFIED or
//     SELF_ONLY is fail-closed to self.
//   - the request's (spec.principal) field: the principal this write applies
//     to, already bound to the authenticated subject by the stamp-or-refuse
//     block that runs just above. Equal means the owner is writing its own row.
//
// A write refusal on a request that carries no (spec.principal) field is a
// generation error: without one the wrapper cannot tell whose row is being
// written and must not guess.
func javaOwnerDefaultGuard(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string, seam javaOwnerDefaultSeam, indent string) (string, error) {
	plan, err := javaOwnerDefaultPlan(method, san, servicePackage)
	if err != nil || plan.Refused == "" {
		return "", err
	}
	deny := fmt.Sprintf(seam.deny, strconv.Quote(fmt.Sprintf("%s: (spec.owner_default) on %s denies the owner this operation", method.Name, plan.Refused)))
	scope := fmt.Sprintf(`%[1]s// (spec.principal_scope): the width of the caller's authority over principals.
%[2]s%[1]sfinal boolean __scopeIsSelf = __principalScope.isEmpty()
%[1]s        || "PRINCIPAL_SCOPE_SELF_ONLY".equals(__principalScope)
%[1]s        || "PRINCIPAL_SCOPE_UNSPECIFIED".equals(__principalScope);
`, indent, seam.callerLocals)

	if plan.Read {
		// A read the owner may not make. There is no row principal to compare:
		// the (spec.store_principal) read filter has already narrowed the rows
		// to the caller, so a self-scoped caller reading them IS the owner.
		return fmt.Sprintf(`%[1]s// (spec.owner_default): %[2]s forbids this access for the row owner.
%[3]s%[1]sif (__scopeIsSelf) {
%[4]s%[1]s}
`, indent, plan.Refused, scope, deny), nil
	}

	pf := san.annotatedStringField(method.FullInputType, servicePackage, "principal")
	if pf == "" {
		return "", fmt.Errorf("%s writes a type declaring (spec.owner_default) with write:false, but %s carries no (spec.principal) field naming the principal being written, so the wrapper cannot tell the owner's write from anyone else's", method.Name, method.FullInputType)
	}
	return fmt.Sprintf(`%[1]s// (spec.owner_default): %[2]s forbids this access for the row owner.
%[1]s// %[3]s.get%[4]s() is the row's principal — (spec.principal) bound it to the
%[1]s// authenticated subject just above — so an equal subject is the owner
%[1]s// writing its own row. A wider (spec.principal_scope) is not.
%[5]s%[1]sif (__scopeIsSelf && %[3]s.get%[4]s().equals(__odCaller)) {
%[6]s%[1]s}
`, indent, plan.Refused, seam.request, javaPascalField(pf), scope, deny), nil
}

// javaOwnerDefaultUnhostable reports why a method whose owner_default guard is
// non-empty cannot carry it on this wrapper, or "" when it can (or needs none).
// The guard rides the unary request-validation seam, immediately after the
// (spec.principal) stamp, because that is the only point where both halves of
// the comparison exist. A streaming, PAGE or empty-request shape has no such
// seam, so it is refused at generation rather than served under-enforced — the
// same set the Go and Rust wrappers refuse.
func javaOwnerDefaultUnhostable(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string) (string, error) {
	guard, err := javaOwnerDefaultGuard(method, san, servicePackage, javaOwnerDefaultGrpcSeam(""), "")
	if err != nil || guard == "" {
		return "", err
	}
	if method.FullInputType == "google.protobuf.Empty" || method.FullInputType == "trace.TraceRequest" ||
		method.ClientStreaming || method.ServerStreaming || method.IsPageCollection() {
		return "its request shape has no validation seam at which the caller can be compared to the row's principal", nil
	}
	return "", nil
}
