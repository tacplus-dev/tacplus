// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// THE RECORD WRITE SURFACE GUARDS (#163), Go. Twins: Java recordwrite_gen.go,
// Rust recordwrite_gen.go. All three render the same checks, in the same
// order, with the same codes and messages.
//
// The record-map injector stamps every write of the record write surface with
// its record type's durability floor: (spec.record_entry) on the
// single-type RPCs, (spec.record_op) on each case of Apply's RecordOp oneof.
// The wrapper — never the handler — holds the contract around them:
//
//   - A request asking for a durability below its type's floor is refused
//     (INVALID_ARGUMENT); one asking for none is given the floor; the handler
//     always receives the level it must ack at in the request's durability.
//   - A write the handler reports acked below that level is not answered
//     (INTERNAL): a write the caller was told is durable must be.
//   - A record held to a tenant ((spec.store_namespace)): a caller whose
//     verified namespace is a tenant is pinned to its own (or a linked) tenant
//     on the request's tenant field — stamped when unset, refused otherwise —
//     and a /system peer keeps the S2S half's fleet-wide reach.
//   - Apply: every op is authorized exactly as its single-type RPC is, so an
//     Apply can never write a type its caller could not write directly; the
//     ops must be in canonical lock order (record type full name, then key,
//     strictly ascending, each entry once) so every store takes the same locks
//     in the same order; and the Apply is acked at the highest level any of its
//     ops involves.

// recordDurabilityName is the injected RecordDurability enum value name of a
// floor, as the refusal messages spell it in every language.
func recordDurabilityName(d spec.Durability) string {
	return "RECORD_" + d.String()
}

// recordKeyIsUnsigned reports whether a key's proto kind orders as unsigned.
func recordKeyIsUnsigned(kind string) bool {
	switch kind {
	case "uint32", "uint64", "fixed32", "fixed64":
		return true
	}
	return false
}

// goEntryExpr renders a field path off base as Go getters, and the setter for
// its last segment.
func goEntryExpr(base string, path []string) (get, parent, set string) {
	get, parent = base, base
	for i, seg := range path {
		if i == len(path)-1 {
			parent = get
			set = parent + "." + toGoFieldName(seg)
		}
		get += ".Get" + toGoFieldName(seg) + "()"
	}
	return get, parent, set
}

// goRecordTenantPin renders the tenant half of the guard over one entry's
// tenant field: a caller held to a tenant is pinned to its own (or a linked)
// tenant — unset is stamped, anything else refused — and a /system peer keeps
// the fleet-wide reach the plane's S2S half gives it. base is the message the
// path starts from; label names it in the refusal.
func goRecordTenantPin(base string, path []string, method, label, indent, failStmt string) string {
	if len(path) == 0 {
		return ""
	}
	get, parent, set := goEntryExpr(base, path)
	nilCheck := ""
	if len(path) > 1 {
		nilCheck = fmt.Sprintf(`%[1]s	if %[2]s == nil {
%[1]s		%[3]s status.Error(codes.InvalidArgument, "%[4]s carries no %[5]s to hold to a tenant")
%[1]s	}
`, indent, parent, failStmt, label, strings.Join(path[:len(path)-1], "."))
	}
	return fmt.Sprintf(`%[1]sif __ns := __recordCallerNs; !strings.HasPrefix(__ns, "/system") {
%[1]s	if __ns == "" {
%[1]s		%[4]s status.Error(codes.PermissionDenied, "%[5]s addresses entries held to a tenant and the caller presented no verified namespace claim")
%[1]s	}
%[6]s%[1]s	if __v := %[2]s; __v == "" {
%[1]s		%[3]s = __ns
%[1]s	} else if __v != __ns && !__recordCallerLinked(__v) {
%[1]s		%[4]s status.Error(codes.InvalidArgument, "%[5]s: %[7]s names a tenant the caller neither owns nor is linked to; the tenant boundary is not the caller's to choose")
%[1]s	}
%[1]s}
`, indent, get, set, failStmt, label, nilCheck, strings.Join(path, "."))
}

// goRecordCallerNs renders the caller's verified tenant and linked tenants,
// read once for every tenant pin of the method.
func goRecordCallerNs() string {
	return `
	// The record write surface's tenant pins: the caller's VERIFIED namespace
	// and the tenants it is linked to, never a request field.
	__recordCallerNs := ""
	var __recordCallerLinkedNs []string
	if caller != nil {
		__recordCallerNs = caller.Token().GetNamespace()
		__recordCallerLinkedNs = caller.Token().GetLinked()
	}
	__recordCallerLinked := func(__ns string) bool {
		for _, __l := range __recordCallerLinkedNs {
			if __l == __ns {
				return true
			}
		}
		return false
	}
`
}

// goRecordEntryPre renders the pre-handler half of the guard for a write
// surface RPC addressing one record type: its tenant pin, then — on a write —
// refuse below the floor and stamp the floor when unset.
func goRecordEntryPre(method *parser.MethodInfo, failStmt string) string {
	e := method.RecordEntry
	if e == nil {
		return ""
	}
	var b strings.Builder
	if len(e.NamespacePath) > 0 {
		b.WriteString(goRecordCallerNs())
		b.WriteString(goRecordTenantPin("req", e.NamespacePath, method.Name, method.Name, "\t", failStmt))
	}
	if e.Durability != spec.Durability_DURABILITY_UNSPECIFIED {
		fmt.Fprintf(&b, `
	// (spec.record_entry): the record type's durability floor. A write may
	// raise it, never lower it; a write naming none is acked at the floor.
	if __d := int32(req.GetDurability()); __d != 0 && __d < %[1]d {
		%[4]s status.Error(codes.InvalidArgument, "%[2]s asks for a durability below the record type's declared floor %[3]s; a write may raise its type's durability, never lower it")
	} else if __d == 0 {
		req.Durability = %[1]d
	}
`, int32(e.Durability), method.Name, recordDurabilityName(e.Durability), failStmt)
	}
	return b.String()
}

// goRecordWritePost renders the post-handler half: never answer a write acked
// below the level the request required.
func goRecordWritePost(method *parser.MethodInfo) string {
	if method.RecordEntry == nil || method.RecordEntry.Durability == spec.Durability_DURABILITY_UNSPECIFIED {
		return ""
	}
	return fmt.Sprintf(`	if resp.GetDurability() < req.GetDurability() {
		return nil, status.Error(codes.Internal, "%s: the record store acked the write below the durability the request required; a write reported durable must be")
	}
`, method.Name)
}

// goRecordApplyPre renders Apply's pre-handler guard: per op, the case, its
// authorization, its floor, and its place in the canonical lock order; then the
// Apply's effective durability.
func goRecordApplyPre(method *parser.MethodInfo, svc *parser.ServiceInfo, servicePackage, parentDir string, skipAuth bool) string {
	ops := method.RecordApplyOps
	if len(ops) == 0 {
		return ""
	}
	first := ops[0]
	opType := goTypeRef(first.OpType, simpleName(first.OpType), servicePackage)
	var cases strings.Builder
	hasTenant := false
	for _, op := range ops {
		caseType := opType + "_" + goCamelCase(op.CaseField)
		inner := "__c." + goCamelCase(op.CaseField)
		keyExpr := inner
		for _, seg := range op.KeyPath {
			keyExpr += ".Get" + toGoFieldName(seg) + "()"
		}
		var keySet string
		switch {
		case op.KeyKind == "string":
			keySet = "__k.s = " + keyExpr
		case recordKeyIsUnsigned(op.KeyKind):
			keySet = "__k.u = uint64(" + keyExpr + ")"
		default:
			keySet = "__k.n = int64(" + keyExpr + ")"
		}
		auth := ""
		if !skipAuth {
			auth = fmt.Sprintf(`
			if _, err := p.authorize(ctx, "/%s" + types.%s_%s_FullMethodName, req); err != nil {
				return nil, err
			}`, parentDir, svc.Name, op.RPC)
		}
		fmt.Fprintf(&cases, `		case *%[1]s:%[2]s
%[7]s			__k.rec, __floor, __d = %[3]q, %[4]d, int32(%[5]s.GetDurability())
			%[6]s
`, caseType, auth, op.Record, int32(op.Durability), inner, keySet,
			goRecordTenantPin(inner, op.NamespacePath, method.Name, "Apply ("+op.CaseField+")", "\t\t\t", "return nil,"))
		if len(op.NamespacePath) > 0 {
			hasTenant = true
		}
	}
	callerNs := ""
	if hasTenant {
		callerNs = goRecordCallerNs()
	}
	return callerNs + fmt.Sprintf(`
	// Apply (#163): authorize, tenant-pin, floor and lock-order every op before the store is
	// asked. The canonical lock order is (record type full name, then key),
	// strictly ascending: every store takes the same entry locks in the same
	// order, and no entry is named twice.
	type __recordLockKey struct {
		rec string
		s   string
		n   int64
		u   uint64
	}
	__less := func(a, b __recordLockKey) bool {
		if a.rec != b.rec {
			return a.rec < b.rec
		}
		if a.s != b.s {
			return a.s < b.s
		}
		if a.n != b.n {
			return a.n < b.n
		}
		return a.u < b.u
	}
	__top := int32(req.GetDurability())
	__eff := __top
	var __prev *__recordLockKey
	for __i, __op := range req.Get%[1]s() {
		var __k __recordLockKey
		var __floor, __d int32
		switch __c := __op.Get%[2]s().(type) {
%[3]s		default:
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Apply: ops[%%d] names no operation", __i))
		}
		if __d != 0 && __d < __floor {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Apply: ops[%%d] asks for a durability below %%s's declared floor; a write may raise its type's durability, never lower it", __i, __k.rec))
		}
		if __top != 0 && __top < __floor {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Apply: the request asks for a durability below %%s's declared floor; an Apply is acked at the highest level its ops involve, never lower", __k.rec))
		}
		if __prev != nil && !__less(*__prev, __k) {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Apply: ops[%%d] is out of canonical lock order (record type full name, then key, each entry once)", __i))
		}
		__prev = &__k
		if __floor > __eff {
			__eff = __floor
		}
		if __d > __eff {
			__eff = __d
		}
	}
	req.Durability = %[4]s(__eff)
`, toGoFieldName(first.OpsField), toGoFieldName(first.OneofName), cases.String(),
		goTypeRef(packageOf(first.OpType)+".RecordDurability", "RecordDurability", servicePackage))
}

// goRecordApplyPost renders Apply's post-handler guard: one result per op, none
// acked below the Apply's effective durability.
func goRecordApplyPost(method *parser.MethodInfo) string {
	if len(method.RecordApplyOps) == 0 {
		return ""
	}
	return fmt.Sprintf(`	if len(resp.GetResults()) != len(req.Get%[1]s()) {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Apply: the record store answered %%d results for %%d ops", len(resp.GetResults()), len(req.Get%[1]s())))
	}
	for __i, __r := range resp.GetResults() {
		if __r.GetDurability() < req.GetDurability() {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Apply: the record store acked ops[%%d] below the durability the request required; a write reported durable must be", __i))
		}
	}
`, toGoFieldName(method.RecordApplyOps[0].OpsField))
}

// packageOf is everything before the last segment of a fully qualified name.
func packageOf(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[:i]
	}
	return ""
}

// simpleName is the last segment of a fully qualified proto name.
func simpleName(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}
