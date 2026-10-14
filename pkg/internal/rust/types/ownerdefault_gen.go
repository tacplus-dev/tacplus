// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
)

// The owner_default seam (§4b). (spec.owner_default) on a row type says what the
// type's owner may do with its own rows; the generated service WRAPPER refuses
// the RPCs that contradict it, at the same seam that already owns the principal
// stamp and the read filter. A handler never sees the refused call.
//
// It sits on the AUTH line rather than in the response path, so the refusal
// applies to every method shape — unary, PAGE, streaming alike — instead of the
// unary-only egress seam (spec.store_principal) has to use.

// ownerDefaultTypes returns the owner_default-bearing message types a method
// touches: the request or response type itself, and any message-typed field of
// either at depth 1. Depth 1 is the whole of it — a record type is named
// directly or wrapped in a request/response envelope, and a deeper scan would
// start refusing methods that merely mention the type in passing.
func (es *rustEgressSanitizer) ownerDefaultTypes(method *parser.MethodInfo, servicePkg string) []parser.OwnerDefaultRights {
	if es == nil || method == nil {
		return nil
	}
	found := map[string]parser.OwnerDefaultRights{}
	for _, root := range []string{method.FullInputType, method.FullOutputType} {
		if root == "" || root == "google.protobuf.Empty" {
			continue
		}
		pkg, name := splitRustEgressFQN(root, servicePkg)
		msg, ok := es.msgs[pkg+"\x00"+name]
		if !ok {
			continue
		}
		if r, ok := parser.OwnerDefaultOf(msg.Annotations, name); ok {
			found[name] = r
		}
		for _, f := range msg.Fields {
			if !f.IsMessage || f.IsMap {
				continue
			}
			fpkg := f.TypePackage
			if fpkg == "" {
				fpkg = pkg
			}
			fmsg, ok := es.msgs[fpkg+"\x00"+f.Type]
			if !ok {
				continue
			}
			if r, ok := parser.OwnerDefaultOf(fmsg.Annotations, f.Type); ok {
				found[f.Type] = r
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]parser.OwnerDefaultRights, 0, len(names))
	for _, n := range names {
		out = append(out, found[n])
	}
	return out
}

// rustPrincipalScopeLocal renders the local binding the caller's grant-carried
// (spec.principal_scope): how wide the caller's authority over PRINCIPALS is.
// It rides the same OPA-stamped service_entitlements claim bag the egress data
// grants already ride, so this is one more claim lookup rather than a second
// auth path. Absent, empty, UNSPECIFIED or SELF_ONLY is fail-closed to self —
// the caller may act only as its own principal — which is what scopes an
// owner_default refusal to the owner instead of to everybody.
func rustPrincipalScopeLocal(indent string) string {
	return fmt.Sprintf(`%s// (spec.principal_scope): the width of the caller's authority over principals.
%slet mut __principal_scope = String::new();
%sif let Some(c) = caller.as_ref() {
%s    if let Some(se) = c.token().service_entitlements() {
%s        if let Some(s) = se.get("principal_scope").and_then(|v| v.as_str()) {
%s            __principal_scope = s.to_string();
%s        }
%s    }
%s}
%slet __scope_is_self = __principal_scope.is_empty()
%s    || __principal_scope == "PRINCIPAL_SCOPE_SELF_ONLY"
%s    || __principal_scope == "PRINCIPAL_SCOPE_UNSPECIFIED";
`, indent, indent, indent, indent, indent, indent, indent, indent, indent, indent, indent, indent)
}

// rustOwnerDefaultGuard renders the refusal the wrapper emits when the method's
// access contradicts the owner rights of a type it touches, or "" when nothing
// is declared or nothing is contradicted. The error names the type and the
// right, so a denial here is never mistaken for a missing grant.
//
// The refusal is the OWNER's, not everyone's. Two facts scope it:
//
//   - __scope_is_self: a caller whose grant carries a (spec.principal_scope)
//     wider than self is not acting as the row's own principal, so an owner
//     default — a DEFAULT — does not bind it.
//   - the request's (spec.principal) field: the principal this write applies
//     to, already bound to the authenticated subject by the stamp-or-refuse
//     block that runs just above. Equal means the owner is writing its own row.
//
// CREATE is exempt: it brings a row into existence, so there is no prior owner
// for the type's owner rights to speak about. UPDATE/DELETE and the other write
// verbs are what "the owner may not write its own rows" governs.
//
// A method that touches such a type but carries no resolved verb classification
// cannot be judged either way; that is an error rather than a silent pass,
// matching how the principal read filter refuses to under-enforce. So is a write
// refusal on a request that carries no (spec.principal) field: without one the
// wrapper cannot tell whose row is being written and must not guess.
func rustOwnerDefaultGuard(method *parser.MethodInfo, san *rustEgressSanitizer, servicePkg string) (string, error) {
	rights := san.ownerDefaultTypes(method, servicePkg)
	if len(rights) == 0 {
		return "", nil
	}
	if method.Class == nil {
		return "", fmt.Errorf("%s touches a type declaring (spec.owner_default) but its verb did not classify, so its read/write access is unknown and the owner default cannot be enforced", method.Name)
	}
	if method.Class.Pattern == verbs.PatternCreate {
		return "", nil
	}
	var refused []string
	for _, r := range rights {
		switch {
		case method.Class.Access == verbs.AccessWrite && !r.Write:
			refused = append(refused, fmt.Sprintf("%s (write)", r.Type))
		case method.Class.Access == verbs.AccessRead && !r.Read:
			refused = append(refused, fmt.Sprintf("%s (read)", r.Type))
		}
	}
	if len(refused) == 0 {
		return "", nil
	}
	named := strings.Join(refused, ", ")
	deny := fmt.Sprintf(`            return Err(tonic::Status::permission_denied(
                "%s: (spec.owner_default) on %s denies the owner this operation"));
`, method.Name, named)

	if method.Class.Access == verbs.AccessRead {
		// A read the owner may not make. There is no row principal to compare:
		// the (spec.store_principal) read filter has already narrowed the rows
		// to the caller, so a self-scoped caller reading them IS the owner.
		return fmt.Sprintf(`
        // (spec.owner_default): %s forbids this access for the row owner.
%s        if __scope_is_self {
%s        }
`, named, rustPrincipalScopeLocal("        "), deny), nil
	}

	pf := san.requestPrincipalField(method.FullInputType, servicePkg)
	if pf == "" {
		return "", fmt.Errorf("%s writes a type declaring (spec.owner_default) with write:false, but %s carries no (spec.principal) field naming the principal being written, so the wrapper cannot tell the owner's write from anyone else's", method.Name, method.FullInputType)
	}
	return fmt.Sprintf(`
        // (spec.owner_default): %s forbids this access for the row owner.
        // req.%s is the row's principal — (spec.principal) bound it to the
        // authenticated subject just above — so an equal subject is the owner
        // writing its own row. A wider (spec.principal_scope) is not.
%s%s        if __scope_is_self && req.%s == __od_caller {
%s        }
`, named, pf, rustPrincipalScopeLocal("        "), rustPrincipalResolve("__od_caller", method.Name, "        "), pf, deny), nil
}
