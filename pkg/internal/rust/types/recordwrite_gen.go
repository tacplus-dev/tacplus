// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// THE RECORD WRITE SURFACE GUARDS (#163), Rust. Twins: Go recordwrite_gen.go,
// Java recordwrite_gen.go. All three render the same checks, in the same
// order, with the same codes and messages.
//
// The record-map injector stamps every write of the record write surface with
// its record type's durability floor: (spec.record_durability) on the
// single-type RPCs, (spec.record_op) on each case of Apply's RecordOp oneof.
// The wrapper — never the handler — holds the contract around them:
//
//   - A request asking for a durability below its type's floor is refused
//     (INVALID_ARGUMENT); one asking for none is given the floor; the handler
//     always receives the level it must ack at in the request's durability.
//   - A write the handler reports acked below that level is not answered
//     (INTERNAL): a write the caller was told is durable must be.
//   - Apply: every op is authorized exactly as its single-type RPC is, so an
//     Apply can never write a type its caller could not write directly; the
//     ops must be in canonical lock order (record type full name, then key,
//     strictly ascending, each entry once) so every store takes the same locks
//     in the same order; and the Apply is acked at the highest level any of its
//     ops involves.
//
//   - On a tenant-owned record store the plane is routed by target_tenant
//     (#207), and every entry's tenant field must lie in that tenant
//     (/tenants/<tid> or below), whoever the caller is, a /system peer
//     included: PERMISSION_DENIED otherwise. check_route_tenant holds the route
//     field to the instance's own tenant, so an instance only ever stores its
//     own tenant's entries, and a call misrouted to it leaves nothing behind.
//
// Rust renders no (spec.store_namespace) predicate pin on the plane, so the
// tenant pin here reads the caller the way the (spec.store_namespace) egress
// backstop does (rustNamespaceResolve): the namespace and linked claims of the
// Caller re-parsed from the header the authorizer already verified.

// rustRecordDurabilityName is the injected RecordDurability enum value name of
// a floor, as the refusal messages spell it in every language.
func rustRecordDurabilityName(d spec.Durability) string {
	return "RECORD_" + d.String()
}

// rustRecordKeyIsUnsigned reports whether a key's proto kind orders as unsigned.
func rustRecordKeyIsUnsigned(kind string) bool {
	switch kind {
	case "uint32", "uint64", "fixed32", "fixed64":
		return true
	}
	return false
}

// rustRecordField is the prost field name of one path segment.
func rustRecordField(seg string) string {
	return escapeRustKeyword(strutil.ToSnakeCase(seg))
}

// rustRecordParentExpr renders the Option over the message holding a path's
// last segment: base.a.as_mut().and_then(|__p| __p.b.as_mut())... (as_ref for
// a read). Only called for a path of two or more segments.
func rustRecordParentExpr(base string, path []string, as string) string {
	expr := base + "." + rustRecordField(path[0]) + "." + as + "()"
	for _, seg := range path[1 : len(path)-1] {
		expr += fmt.Sprintf(".and_then(|__p| __p.%s.%s())", rustRecordField(seg), as)
	}
	return expr
}

// rustRecordTenantPin renders the tenant half of the guard over one entry's
// tenant field: a caller held to a tenant is pinned to its own (or a linked)
// tenant — unset is stamped, anything else refused — and a /system peer keeps
// the fleet-wide reach the plane's S2S half gives it. base is the (mutable)
// message the path starts from; label names it in the refusal.
func rustRecordTenantPin(base string, path []string, label, indent string) string {
	if len(path) == 0 {
		return ""
	}
	field := base + "." + rustRecordField(path[len(path)-1])
	parent := ""
	if len(path) > 1 {
		field = "__t." + rustRecordField(path[len(path)-1])
		parent = fmt.Sprintf(`%[1]s    let __t = match %[2]s {
%[1]s        Some(__p) => __p,
%[1]s        None => return Err(tonic::Status::invalid_argument("%[3]s carries no %[4]s to hold to a tenant")),
%[1]s    };
`, indent, rustRecordParentExpr(base, path, "as_mut"), label, strings.Join(path[:len(path)-1], "."))
	}
	return fmt.Sprintf(`%[1]sif !__record_caller_ns.starts_with("/system") {
%[1]s    if __record_caller_ns.is_empty() {
%[1]s        return Err(tonic::Status::permission_denied("%[2]s addresses entries held to a tenant and the caller presented no verified namespace claim"));
%[1]s    }
%[3]s%[1]s    if %[4]s.is_empty() {
%[1]s        %[4]s = __record_caller_ns.clone();
%[1]s    } else if %[4]s != __record_caller_ns && !__record_caller_linked.iter().any(|__l| __l == &%[4]s) {
%[1]s        return Err(tonic::Status::invalid_argument("%[2]s: %[5]s names a tenant the caller neither owns nor is linked to; the tenant boundary is not the caller's to choose"));
%[1]s    }
%[1]s}
`, indent, label, parent, field, strings.Join(path, "."))
}

// rustRecordRoutePin renders the routed half of the tenant guard (#207): on a
// plane routed by tenant, the entry's tenant field must be in the tenant the
// call is routed to. route is an expression naming the routed request's route
// field (a String); the route check has already refused one that is not a
// tid. An unset tenant field is refused too: an entry on a tenant's instance
// belongs to that tenant, and the caller says so. Runs after the tenant pin,
// so a tenant caller's stamped tenant is the one held to the route.
func rustRecordRoutePin(base string, path []string, route, label, indent string) string {
	if len(path) == 0 || route == "" {
		return ""
	}
	last := rustRecordField(path[len(path)-1])
	get := base + "." + last + ".as_str()"
	if len(path) > 1 {
		get = fmt.Sprintf("%s.map(|__p| __p.%s.as_str()).unwrap_or(\"\")", rustRecordParentExpr(base, path, "as_ref"), last)
	}
	return fmt.Sprintf(`%[1]s{
%[1]s    let (__rt, __v): (&str, &str) = (%[2]s.as_str(), %[3]s);
%[1]s    let __home = format!("/tenants/{}", __rt);
%[1]s    if __v != __home && !__v.starts_with(&format!("{}/", __home)) {
%[1]s        return Err(tonic::Status::permission_denied(format!("%[4]s: %[5]s is not in tenant {}, the tenant this call is routed to; a tenant's instance holds that tenant's entries only", __rt)));
%[1]s    }
%[1]s}
`, indent, route, get, label, strings.Join(path, "."))
}

// rustRecordRoute returns the expression of a routed method's route field on
// the request binding req, "" for a method not routed by tenant.
func rustRecordRoute(method *parser.MethodInfo, req string) string {
	if method.RouteField == nil {
		return ""
	}
	return req + "." + rustRequestFieldIdent(method.RouteField.Field)
}

// rustRecordCallerNs renders the caller's verified tenant and linked tenants,
// read once for every tenant pin of the method.
func rustRecordCallerNs() string {
	return `
        // The record write surface's tenant pins: the caller's VERIFIED namespace
        // and the tenants it is linked to, never a request field.
        let __record_caller_ns: String = caller.as_ref().and_then(|c| c.namespace()).unwrap_or_default().to_string();
        let __record_caller_linked: Vec<String> = caller.as_ref().map(|c| c.linked()).unwrap_or_default();
`
}

// rustRecordEntryPre renders the pre-handler half of the guard for a write
// surface RPC addressing one record type: its tenant pin, then — on a write —
// refuse below the floor and stamp the floor when unset. The request binding
// is `req`; a write also binds __record_required, the level the post half
// holds the handler's ack to (the handler consumes `req`).
func rustRecordEntryPre(method *parser.MethodInfo) string {
	e := method.RecordEntry
	if e == nil || (len(e.NamespacePath) == 0 && e.Durability == spec.Durability_DURABILITY_UNSPECIFIED) {
		return ""
	}
	var b strings.Builder
	b.WriteString("        let mut req = req;\n")
	if len(e.NamespacePath) > 0 {
		b.WriteString(rustRecordCallerNs())
		b.WriteString(rustRecordTenantPin("req", e.NamespacePath, method.Name, "        "))
		b.WriteString(rustRecordRoutePin("req", e.NamespacePath, rustRecordRoute(method, "req"), method.Name, "        "))
	}
	if e.Durability != spec.Durability_DURABILITY_UNSPECIFIED {
		fmt.Fprintf(&b, `
        // (spec.record_entry): the record type's durability floor. A write may
        // raise it, never lower it; a write naming none is acked at the floor.
        {
            let __d = req.durability;
            if __d != 0 && __d < %[1]d {
                return Err(tonic::Status::invalid_argument("%[2]s asks for a durability below the record type's declared floor %[3]s; a write may raise its type's durability, never lower it"));
            } else if __d == 0 {
                req.durability = %[1]d;
            }
        }
        let __record_required = req.durability;
`, int32(e.Durability), method.Name, rustRecordDurabilityName(e.Durability))
	}
	b.WriteString("        let req = req;\n")
	return b.String()
}

// rustRecordWritePost renders the post-handler half: never answer a write
// acked below the level the request required. Runs inside the handler's async
// block, so the `return` fails the call.
func rustRecordWritePost(method *parser.MethodInfo) string {
	if method.RecordEntry == nil || method.RecordEntry.Durability == spec.Durability_DURABILITY_UNSPECIFIED {
		return ""
	}
	return fmt.Sprintf(`            if resp.durability < __record_required {
                return Err(tonic::Status::internal("%s: the record store acked the write below the durability the request required; a write reported durable must be"));
            }
`, method.Name)
}

// rustRecordApplyMetadata renders the capture of the request's metadata Apply
// authorizes each op against: `request` is consumed before the guard runs.
// Empty when the method authorizes nothing.
func rustRecordApplyMetadata(method *parser.MethodInfo, skipAuth bool) string {
	if len(method.RecordApplyOps) == 0 || skipAuth {
		return ""
	}
	return "\n        // Apply (#163): each op is authorized as its single-type rpc, against the\n" +
		"        // same authorization header Apply itself was.\n" +
		"        let __record_apply_metadata = request.metadata().clone();"
}

// rustRecordKeyExpr renders an op's lock key field initializers off the case
// message __c: a string key by its UTF-8 bytes (Rust's String order), a signed
// one numerically, an unsigned one as unsigned.
func rustRecordKeyExpr(op parser.RecordEntry) string {
	var get string
	last := rustRecordField(op.KeyPath[len(op.KeyPath)-1])
	clone := ""
	if op.KeyKind == "string" {
		clone = ".clone()"
	}
	if len(op.KeyPath) == 1 {
		get = "__c." + last + clone
	} else {
		get = fmt.Sprintf("%s.map(|__p| __p.%s%s).unwrap_or_default()", rustRecordParentExpr("__c", op.KeyPath, "as_ref"), last, clone)
	}
	switch {
	case op.KeyKind == "string":
		return "s: " + get + ", n: 0, u: 0"
	case rustRecordKeyIsUnsigned(op.KeyKind):
		return "s: String::new(), n: 0, u: " + get + " as u64"
	default:
		return "s: String::new(), n: " + get + " as i64, u: 0"
	}
}

// rustRecordApplyPre renders Apply's pre-handler guard: per op, the case, its
// authorization, its tenant, its floor, and its place in the canonical lock
// order; then the Apply's effective durability. Binds __record_apply_ops and
// __record_required for the post half.
func rustRecordApplyPre(method *parser.MethodInfo, svc *parser.ServiceInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, skipAuth bool) string {
	ops := method.RecordApplyOps
	if len(ops) == 0 {
		return ""
	}
	first := ops[0]
	opType := resolveOneRustTypeQualified(simpleTypeName(first.OpType), first.OpType, servicePackage, crossPkgTypes)
	enumPath := rustOneofEnumPath(opType, simpleTypeName(first.OpType), first.OneofName)
	// The route field is read once, before the ops are borrowed mutably.
	route := ""
	routeBind := ""
	if r := rustRecordRoute(method, "req"); r != "" {
		route = "__record_route"
		routeBind = "        let __record_route: String = " + r + ".clone();\n"
	}
	var cases strings.Builder
	hasTenant := false
	for _, op := range ops {
		auth := ""
		if !skipAuth {
			auth = fmt.Sprintf(`                    {
                        let mut __op_request = tonic::Request::new(());
                        *__op_request.metadata_mut() = __record_apply_metadata.clone();
                        self.authorize_method("/%s/%s.%s/%s", &__op_request).await?;
                    }
`, parentDir, servicePackage, svc.Name, op.RPC)
		}
		if len(op.NamespacePath) > 0 {
			hasTenant = true
		}
		fmt.Fprintf(&cases, `                Some(%[1]s::%[2]s(__c)) => {
%[3]s%[4]s%[8]s                    (__RecordLockKey { rec: %[5]q, %[6]s }, %[7]d, __c.durability)
                }
`, enumPath, toProstTypeName(strutil.ToCamelCase(op.CaseField)), auth,
			rustRecordTenantPin("__c", op.NamespacePath, "Apply ("+op.CaseField+")", "                    "),
			op.Record, rustRecordKeyExpr(op), int32(op.Durability),
			rustRecordRoutePin("__c", op.NamespacePath, route, "Apply ("+op.CaseField+")", "                    "))
	}
	callerNs := ""
	if hasTenant {
		callerNs = rustRecordCallerNs()
	}
	return callerNs + routeBind + fmt.Sprintf(`
        // Apply (#163): authorize, tenant-pin, floor and lock-order every op before the store is
        // asked. The canonical lock order is (record type full name, then key),
        // strictly ascending: every store takes the same entry locks in the same
        // order, and no entry is named twice. The derived order compares the
        // fields in declaration order, and a String compares by its UTF-8 bytes.
        #[derive(PartialEq, Eq, PartialOrd, Ord)]
        struct __RecordLockKey {
            rec: &'static str,
            s: String,
            n: i64,
            u: u64,
        }
        let mut req = req;
        let __top: i32 = req.durability;
        let mut __eff = __top;
        let mut __prev: Option<__RecordLockKey> = None;
        for (__i, __op) in req.%[1]s.iter_mut().enumerate() {
            let (__k, __floor, __d): (__RecordLockKey, i32, i32) = match __op.%[2]s.as_mut() {
%[3]s                None => return Err(tonic::Status::invalid_argument(format!("Apply: ops[{}] names no operation", __i))),
            };
            if __d != 0 && __d < __floor {
                return Err(tonic::Status::invalid_argument(format!("Apply: ops[{}] asks for a durability below {}'s declared floor; a write may raise its type's durability, never lower it", __i, __k.rec)));
            }
            if __top != 0 && __top < __floor {
                return Err(tonic::Status::invalid_argument(format!("Apply: the request asks for a durability below {}'s declared floor; an Apply is acked at the highest level its ops involve, never lower", __k.rec)));
            }
            if let Some(ref __p) = __prev {
                if !(*__p < __k) {
                    return Err(tonic::Status::invalid_argument(format!("Apply: ops[{}] is out of canonical lock order (record type full name, then key, each entry once)", __i)));
                }
            }
            __prev = Some(__k);
            if __floor > __eff {
                __eff = __floor;
            }
            if __d > __eff {
                __eff = __d;
            }
        }
        req.durability = __eff;
        let __record_required = req.durability;
        let __record_apply_ops = req.%[1]s.len();
        let req = req;
`, rustRecordField(first.OpsField), rustRecordField(first.OneofName), cases.String())
}

// rustRecordApplyPost renders Apply's post-handler guard: one result per op,
// none acked below the Apply's effective durability.
func rustRecordApplyPost(method *parser.MethodInfo) string {
	if len(method.RecordApplyOps) == 0 {
		return ""
	}
	return `            if resp.results.len() != __record_apply_ops {
                return Err(tonic::Status::internal(format!("Apply: the record store answered {} results for {} ops", resp.results.len(), __record_apply_ops)));
            }
            for (__i, __r) in resp.results.iter().enumerate() {
                if __r.durability < __record_required {
                    return Err(tonic::Status::internal(format!("Apply: the record store acked ops[{}] below the durability the request required; a write reported durable must be", __i)));
                }
            }
`
}
