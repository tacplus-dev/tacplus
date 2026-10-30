// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// THE RECORD WRITE SURFACE GUARDS (#163), Java. Twins: Go recordwrite_gen.go,
// Rust recordwrite_gen.go. All three render the same checks, in the same
// order, with the same codes and messages; the Go file's header states the
// contract, the route pin of a plane routed by tenant (#207) included.
//
// Java's protobuf messages are immutable, so where Go stamps a field in place
// the wrapper here rebuilds the message and reassigns it: `request` for the
// single-type RPCs, the op's case message (then the op, then the ops list) on
// Apply. The handler receives the rebuilt request.

// javaRecordDurabilityName is the injected RecordDurability enum value name of
// a floor, as the refusal messages spell it in every language.
func javaRecordDurabilityName(d spec.Durability) string {
	return "RECORD_" + d.String()
}

// javaRecordGetter renders a field path off base as Java getters.
func javaRecordGetter(base string, path []string) string {
	for _, seg := range path {
		base += ".get" + javaPascalField(seg) + "()"
	}
	return base
}

// javaRecordSetter renders base rebuilt with the field at path set to value.
// Inner levels pass a Builder, which every generated setX accepts.
func javaRecordSetter(base string, path []string, value string) string {
	var set func(get string, path []string) string
	set = func(get string, path []string) string {
		p := javaPascalField(path[0])
		inner := value
		if len(path) > 1 {
			inner = set(get+".get"+p+"()", path[1:])
		}
		return get + ".toBuilder().set" + p + "(" + inner + ")"
	}
	return set(base, path) + ".build()"
}

// javaRecordCallerNs renders the caller's verified tenant and linked tenants,
// read once for every tenant pin of the method.
func javaRecordCallerNs(indent string) string {
	return fmt.Sprintf(`
%[1]s// The record write surface's tenant pins: the caller's VERIFIED namespace
%[1]s// and the tenants it is linked to, never a request field.
%[1]svar __recordTok = Auth.verifiedTokenFromContext();
%[1]sfinal String __recordCallerNs = __recordTok == null || __recordTok.getNamespace() == null ? "" : __recordTok.getNamespace();
%[1]sfinal java.util.List<String> __recordCallerLinked = Auth.callerLinkedFromContext();
`, indent)
}

// javaRecordTenantPin renders the tenant half of the guard over one entry's
// tenant field: a caller held to a tenant is pinned to its own (or a linked)
// tenant — unset is stamped, anything else refused — and a /system peer keeps
// the fleet-wide reach the plane's S2S half gives it. base is the (reassignable)
// message variable the path starts from; label names it in the refusal.
func javaRecordTenantPin(base string, path []string, label, indent string) string {
	if len(path) == 0 {
		return ""
	}
	get := javaRecordGetter(base, path)
	presence := ""
	if len(path) > 1 {
		parent := javaRecordGetter(base, path[:len(path)-2])
		presence = fmt.Sprintf(`%[1]s    if (!%[2]s.has%[3]s()) {
%[1]s        throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[4]s carries no %[5]s to hold to a tenant");
%[1]s    }
`, indent, parent, javaPascalField(path[len(path)-2]), label, strings.Join(path[:len(path)-1], "."))
	}
	return fmt.Sprintf(`%[1]sif (!__recordCallerNs.startsWith("/system")) {
%[1]s    if (__recordCallerNs.isEmpty()) {
%[1]s        throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[2]s addresses entries held to a tenant and the caller presented no verified namespace claim");
%[1]s    }
%[3]s%[1]s    if (%[4]s.isEmpty()) {
%[1]s        %[5]s = %[6]s;
%[1]s    } else if (!__recordCallerNs.equals(%[4]s) && !__recordCallerLinked.contains(%[4]s)) {
%[1]s        throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[2]s: %[7]s names a tenant the caller neither owns nor is linked to; the tenant boundary is not the caller's to choose");
%[1]s    }
%[1]s}
`, indent, label, presence, get, base, javaRecordSetter(base, path, "__recordCallerNs"), strings.Join(path, "."))
}

// javaRecordRoutePin renders the routed half of the tenant guard (#207): on a
// plane routed by tenant, the entry's tenant field must be in the tenant the
// call is routed to (/tenants/<tid> or below it), whoever the caller is, a
// /system peer included. route is the routed request's route field getter
// expression; the route check has already refused one that is not a tid. An
// unset tenant field is refused too: an entry on a tenant's instance belongs
// to that tenant, and the caller says so.
func javaRecordRoutePin(base string, path []string, route, label, indent string) string {
	if len(path) == 0 || route == "" {
		return ""
	}
	return fmt.Sprintf(`%[1]s{
%[1]s    final String __rt = %[2]s;
%[1]s    final String __v = %[3]s;
%[1]s    if (!__v.equals("/tenants/" + __rt) && !__v.startsWith("/tenants/" + __rt + "/")) {
%[1]s        throw new ProtoException(ProtoException.PERMISSION_DENIED, "%[4]s: %[5]s is not in tenant " + __rt + ", the tenant this call is routed to; a tenant's instance holds that tenant's entries only");
%[1]s    }
%[1]s}
`, indent, route, javaRecordGetter(base, path), label, strings.Join(path, "."))
}

// javaRecordRoute returns the getter expression of a routed method's route
// field on request, "" for a method not routed by tenant.
func javaRecordRoute(method *parser.MethodInfo) string {
	if method.RouteField == nil {
		return ""
	}
	return "request.get" + javaPascalField(method.RouteField.Field) + "()"
}

// javaRecordEntryPre renders the pre-handler half of the guard for a write
// surface RPC addressing one record type: its tenant pin, then — on a write —
// refuse below the floor and stamp the floor when unset.
func javaRecordEntryPre(method *parser.MethodInfo, indent string) string {
	e := method.RecordEntry
	if e == nil {
		return ""
	}
	var b strings.Builder
	if len(e.NamespacePath) > 0 {
		b.WriteString(javaRecordCallerNs(indent))
		b.WriteString(javaRecordTenantPin("request", e.NamespacePath, method.Name, indent))
		b.WriteString(javaRecordRoutePin("request", e.NamespacePath, javaRecordRoute(method), method.Name, indent))
	}
	if e.Durability != spec.Durability_DURABILITY_UNSPECIFIED {
		fmt.Fprintf(&b, `
%[1]s// (spec.record_entry): the record type's durability floor. A write may
%[1]s// raise it, never lower it; a write naming none is acked at the floor.
%[1]sif (request.getDurabilityValue() != 0 && request.getDurabilityValue() < %[2]d) {
%[1]s    throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[3]s asks for a durability below the record type's declared floor %[4]s; a write may raise its type's durability, never lower it");
%[1]s} else if (request.getDurabilityValue() == 0) {
%[1]s    request = request.toBuilder().setDurabilityValue(%[2]d).build();
%[1]s}
`, indent, int32(e.Durability), method.Name, javaRecordDurabilityName(e.Durability))
	}
	return b.String()
}

// javaRecordWritePost renders the post-handler half: never answer a write
// acked below the level the request required.
func javaRecordWritePost(method *parser.MethodInfo, indent string) string {
	if method.RecordEntry == nil || method.RecordEntry.Durability == spec.Durability_DURABILITY_UNSPECIFIED {
		return ""
	}
	return fmt.Sprintf(`%[1]sif (response.getDurabilityValue() < request.getDurabilityValue()) {
%[1]s    throw new ProtoException(ProtoException.INTERNAL, "%[2]s: the record store acked the write below the durability the request required; a write reported durable must be");
%[1]s}
`, indent, method.Name)
}

// javaRecordApplyPre renders Apply's pre-handler guard: per op, the case, its
// authorization, its tenant pin, its floor, and its place in the canonical lock
// order; then the Apply's effective durability. Each op is authorized through
// the wrapper's own checkAuth under its single-type RPC's key, so the
// authorizer's refusal propagates unchanged.
func javaRecordApplyPre(method *parser.MethodInfo, serviceName, servicePackage, parentDir string, skipAuth bool, indent string) string {
	ops := method.RecordApplyOps
	if len(ops) == 0 {
		return ""
	}
	first := ops[0]
	in := indent + "            " // inside for { switch { case: {
	var cases strings.Builder
	hasTenant := false
	for _, op := range ops {
		p := javaPascalField(op.CaseField)
		keyExpr := javaRecordGetter("__c", op.KeyPath)
		var keySet string
		switch op.KeyKind {
		case "string":
			keySet = "__kS = " + keyExpr
		case "uint32", "fixed32":
			keySet = "__kU = Integer.toUnsignedLong(" + keyExpr + ")"
		case "uint64", "fixed64":
			keySet = "__kU = " + keyExpr
		default:
			keySet = "__kN = (long) " + keyExpr
		}
		auth := ""
		if !skipAuth {
			auth = fmt.Sprintf("%scheckAuth(%q, request);\n", in, javaMethodAuthKey(servicePackage, serviceName, op.RPC, parentDir))
		}
		pin := ""
		if len(op.NamespacePath) > 0 {
			hasTenant = true
			pin = javaRecordTenantPin("__c", op.NamespacePath, "Apply ("+op.CaseField+")", in) +
				javaRecordRoutePin("__c", op.NamespacePath, javaRecordRoute(method), "Apply ("+op.CaseField+")", in) +
				fmt.Sprintf("%s__op = __op.toBuilder().set%s(__c).build();\n", in, p)
		}
		fmt.Fprintf(&cases, `%[1]s        case %[2]s: {
%[3]s%[4]svar __c = __op.get%[5]s();
%[6]s%[4]s__kRec = %[7]q;
%[4]s__floor = %[8]d;
%[4]s__d = __c.getDurabilityValue();
%[4]s%[9]s;
%[4]sbreak;
%[1]s        }
`, indent, strings.ToUpper(op.CaseField), auth, in, p, pin, op.Record, int32(op.Durability), keySet)
	}
	callerNs := ""
	if hasTenant {
		callerNs = javaRecordCallerNs(indent)
	}
	opsP := javaPascalField(first.OpsField)
	return callerNs + fmt.Sprintf(`
%[1]s// Apply (#163): authorize, tenant-pin, floor and lock-order every op before the store is
%[1]s// asked. The canonical lock order is (record type full name, then key),
%[1]s// strictly ascending: every store takes the same entry locks in the same
%[1]s// order, and no entry is named twice. Strings compare by their UTF-8 bytes,
%[1]s// as every language orders them.
%[1]sfinal java.util.Comparator<com.google.protobuf.ByteString> __recordBytes = com.google.protobuf.ByteString.unsignedLexicographicalComparator();
%[1]sfinal int __top = request.getDurabilityValue();
%[1]sint __eff = __top;
%[1]sString __prevRec = null;
%[1]sString __prevS = "";
%[1]slong __prevN = 0L;
%[1]slong __prevU = 0L;
%[1]svar __applyB = request.toBuilder();
%[1]sfor (int __i = 0; __i < __applyB.get%[2]sCount(); __i++) {
%[1]s    var __op = __applyB.get%[2]s(__i);
%[1]s    String __kRec;
%[1]s    String __kS = "";
%[1]s    long __kN = 0L;
%[1]s    long __kU = 0L;
%[1]s    int __floor;
%[1]s    int __d;
%[1]s    switch (__op.get%[3]sCase()) {
%[4]s%[1]s        default:
%[1]s            throw new ProtoException(ProtoException.INVALID_ARGUMENT, "Apply: ops[" + __i + "] names no operation");
%[1]s    }
%[1]s    if (__d != 0 && __d < __floor) {
%[1]s        throw new ProtoException(ProtoException.INVALID_ARGUMENT, "Apply: ops[" + __i + "] asks for a durability below " + __kRec + "'s declared floor; a write may raise its type's durability, never lower it");
%[1]s    }
%[1]s    if (__top != 0 && __top < __floor) {
%[1]s        throw new ProtoException(ProtoException.INVALID_ARGUMENT, "Apply: the request asks for a durability below " + __kRec + "'s declared floor; an Apply is acked at the highest level its ops involve, never lower");
%[1]s    }
%[1]s    if (__prevRec != null) {
%[1]s        int __cmp = __recordBytes.compare(com.google.protobuf.ByteString.copyFromUtf8(__prevRec), com.google.protobuf.ByteString.copyFromUtf8(__kRec));
%[1]s        if (__cmp == 0) {
%[1]s            __cmp = __recordBytes.compare(com.google.protobuf.ByteString.copyFromUtf8(__prevS), com.google.protobuf.ByteString.copyFromUtf8(__kS));
%[1]s        }
%[1]s        if (__cmp == 0) {
%[1]s            __cmp = Long.compare(__prevN, __kN);
%[1]s        }
%[1]s        if (__cmp == 0) {
%[1]s            __cmp = Long.compareUnsigned(__prevU, __kU);
%[1]s        }
%[1]s        if (__cmp >= 0) {
%[1]s            throw new ProtoException(ProtoException.INVALID_ARGUMENT, "Apply: ops[" + __i + "] is out of canonical lock order (record type full name, then key, each entry once)");
%[1]s        }
%[1]s    }
%[1]s    __prevRec = __kRec;
%[1]s    __prevS = __kS;
%[1]s    __prevN = __kN;
%[1]s    __prevU = __kU;
%[1]s    __applyB.set%[2]s(__i, __op);
%[1]s    if (__floor > __eff) {
%[1]s        __eff = __floor;
%[1]s    }
%[1]s    if (__d > __eff) {
%[1]s        __eff = __d;
%[1]s    }
%[1]s}
%[1]srequest = __applyB.setDurabilityValue(__eff).build();
`, indent, opsP, javaPascalField(first.OneofName), cases.String())
}

// javaRecordApplyPost renders Apply's post-handler guard: one result per op,
// none acked below the Apply's effective durability.
func javaRecordApplyPost(method *parser.MethodInfo, indent string) string {
	if len(method.RecordApplyOps) == 0 {
		return ""
	}
	return fmt.Sprintf(`%[1]sif (response.getResultsCount() != request.get%[2]sCount()) {
%[1]s    throw new ProtoException(ProtoException.INTERNAL, "Apply: the record store answered " + response.getResultsCount() + " results for " + request.get%[2]sCount() + " ops");
%[1]s}
%[1]sfor (int __i = 0; __i < response.getResultsCount(); __i++) {
%[1]s    if (response.getResults(__i).getDurabilityValue() < request.getDurabilityValue()) {
%[1]s        throw new ProtoException(ProtoException.INTERNAL, "Apply: the record store acked ops[" + __i + "] below the durability the request required; a write reported durable must be");
%[1]s    }
%[1]s}
`, indent, javaPascalField(method.RecordApplyOps[0].OpsField))
}
