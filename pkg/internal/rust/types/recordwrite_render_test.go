// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// The record write surface's guards (#163) on the generated Rust store wrapper.
// The behavior is driven end to end by the record-egress host fixture's
// self-test (cross-test/current-generation/testdata/rust/record_write_guard_test.rs);
// these pin the rendering, so a guard dropped from a seam fails here first.

func rustRecordWriteService(methods ...*parser.MethodInfo) *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "RecordMap", IsS2S: true, Methods: methods}
}

func renderRustRecordWrite(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(t, rustRecordWriteService(m), m, "query", "vservices", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
}

func rustPutXidRowMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "PutXidRow", InputType: "PutXidRowRequest", OutputType: "RecordWriteResult",
		FullInputType: "query.PutXidRowRequest", FullOutputType: "query.RecordWriteResult",
		RecordEntry: &parser.RecordEntry{
			Record: "ident.XidRow", Durability: spec.Durability_DURABILITY_REMOTE,
			KeyPath: []string{"item", "xid"}, KeyKind: "string", NamespacePath: []string{"item", "tenant"},
		},
	}
}

func TestRustRecordWriteGuardFloorAndTenant(t *testing.T) {
	got := renderRustRecordWrite(t, rustPutXidRowMethod())
	for _, want := range []string{
		`let __record_caller_ns: String = caller.as_ref().and_then(|c| c.namespace()).unwrap_or_default().to_string();`,
		`if !__record_caller_ns.starts_with("/system") {`,
		`tonic::Status::permission_denied("PutXidRow addresses entries held to a tenant and the caller presented no verified namespace claim")`,
		`let __t = match req.item.as_mut() {`,
		`tonic::Status::invalid_argument("PutXidRow carries no item to hold to a tenant")`,
		`if __t.tenant.is_empty() {`,
		`__t.tenant = __record_caller_ns.clone();`,
		`} else if __t.tenant != __record_caller_ns {`,
		`"PutXidRow: item.tenant names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`if __d != 0 && __d < 3 {`,
		`"PutXidRow asks for a durability below the record type's declared floor RECORD_DURABILITY_REMOTE; a write may raise its type's durability, never lower it"`,
		`req.durability = 3;`,
		`let __record_required = req.durability;`,
		`if resp.durability < __record_required {`,
		`tonic::Status::internal("PutXidRow: the record store acked the write below the durability the request required; a write reported durable must be")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PutXidRow wrapper missing %q\n%s", want, got)
		}
	}
	// Order: request validation, then the tenant pin and the floor, before the
	// handler; the ack check after it, ahead of response validation.
	validate, pin, floor := strings.Index(got, "req.validate()"), strings.Index(got, "let __record_caller_ns"), strings.Index(got, "__d < 3")
	call, ack, respValidate := strings.Index(got, "self.handler.put_xid_row("), strings.Index(got, "resp.durability < __record_required"), strings.Index(got, "resp.validate()")
	if !(validate < pin && pin < floor && floor < call && call < ack && (respValidate < 0 || ack < respValidate)) {
		t.Fatalf("guard order: validate %d, pin %d, floor %d, handler %d, ack %d, response validation %d", validate, pin, floor, call, ack, respValidate)
	}
}

// A read carries a tenant pin and no durability guard; a method with no stamp
// carries neither.
func TestRustRecordWriteGuardReadAndUnstamped(t *testing.T) {
	get := &parser.MethodInfo{
		Name: "GetXidRow", InputType: "GetXidRowRequest", OutputType: "GetXidRowResponse",
		FullInputType: "query.GetXidRowRequest", FullOutputType: "query.GetXidRowResponse",
		RecordEntry: &parser.RecordEntry{Record: "ident.XidRow", KeyPath: []string{"xid"}, KeyKind: "string", NamespacePath: []string{"tenant"}},
	}
	got := renderRustRecordWrite(t, get)
	if !strings.Contains(got, `if req.tenant.is_empty() {`) || !strings.Contains(got, `req.tenant = __record_caller_ns.clone();`) {
		t.Errorf("Get<T> must pin its tenant field:\n%s", got)
	}
	if strings.Contains(got, "carries no") {
		t.Errorf("a top-level tenant field has no parent to be absent:\n%s", got)
	}
	if strings.Contains(got, "durability") {
		t.Errorf("a read carries no durability guard:\n%s", got)
	}
	plain := renderRustRecordWrite(t, &parser.MethodInfo{
		Name: "GetPing", InputType: "Ping", OutputType: "Ping", FullInputType: "query.Ping", FullOutputType: "query.Ping",
	})
	if strings.Contains(plain, "__record_caller_ns") || strings.Contains(plain, "durability") {
		t.Errorf("an unstamped rpc carries no record write guard:\n%s", plain)
	}
}

// Watch<T>Changes carries the tenant pin on the WATCH seam, before the
// handler is asked for its source, and hands the handler the pinned request.
func TestRustRecordWatchChangesTenantPin(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "WatchXidRowChanges", InputType: "WatchXidRowChangesRequest", OutputType: "WatchXidRowChangesEvent",
		FullInputType: "query.WatchXidRowChangesRequest", FullOutputType: "query.WatchXidRowChangesEvent",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"revision"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1000, OrderBy: []string{"revision"},
			WatchOneOf: "event", WatchItemType: "query.XidRow", WatchChangeType: "query.XidRowWatchChange",
			WatchRevisionField: "revision", WatchHasFromRevision: true,
		},
		RecordEntry: &parser.RecordEntry{Record: "ident.XidRow", NamespacePath: []string{"tenant"}},
	}
	got := renderRustRecordWrite(t, m)
	for _, want := range []string{
		`let req = request.into_inner();`,
		`"WatchXidRowChanges addresses entries held to a tenant and the caller presented no verified namespace claim"`,
		`req.tenant = __record_caller_ns.clone();`,
		`"WatchXidRowChanges: tenant names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`let req_msg = req;`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("WatchXidRowChanges wrapper missing %q\n%s", want, got)
		}
	}
	if strings.Index(got, "let __record_caller_ns") > strings.Index(got, "self.handler.watch_xid_row_changes(") {
		t.Fatal("the tenant pin must run before the handler is asked for its source")
	}
	if strings.Contains(got, "durability") {
		t.Errorf("a watch carries no durability guard:\n%s", got)
	}
}

func rustRecordApplyMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "Apply", InputType: "RecordApplyRequest", OutputType: "RecordApplyResponse",
		FullInputType: "query.RecordApplyRequest", FullOutputType: "query.RecordApplyResponse",
		RecordApplyOps: []parser.RecordEntry{
			{OpsField: "ops", OpType: "query.RecordOp", OneofName: "op", CaseField: "put_xid_row",
				Record: "ident.XidRow", RPC: "PutXidRow", Durability: spec.Durability_DURABILITY_REMOTE,
				KeyPath: []string{"item", "xid"}, KeyKind: "string", NamespacePath: []string{"item", "tenant"}},
			{OpsField: "ops", OpType: "query.RecordOp", OneofName: "op", CaseField: "insert_if_absent_link_row",
				Record: "ident.LinkRow", RPC: "InsertIfAbsentLinkRow", Durability: spec.Durability_DURABILITY_LOCAL,
				KeyPath: []string{"item", "seq"}, KeyKind: "uint64"},
			{OpsField: "ops", OpType: "query.RecordOp", OneofName: "op", CaseField: "delete_audit_row",
				Record: "ident.AuditRow", RPC: "DeleteAuditRow", Durability: spec.Durability_DURABILITY_MEMORY,
				KeyPath: []string{"seq"}, KeyKind: "int64"},
		},
	}
}

func TestRustRecordApplyGuard(t *testing.T) {
	got := renderRustRecordWrite(t, rustRecordApplyMethod())
	for _, want := range []string{
		`let __record_apply_metadata = request.metadata().clone();`,
		`struct __RecordLockKey {`,
		`for (__i, __op) in req.ops.iter_mut().enumerate() {`,
		`match __op.op.as_mut() {`,
		`Some(record_op::Op::PutXidRow(__c)) => {`,
		`Some(record_op::Op::InsertIfAbsentLinkRow(__c)) => {`,
		`*__op_request.metadata_mut() = __record_apply_metadata.clone();`,
		`self.authorize_method("/vservices/query.RecordMap/PutXidRow", &__op_request).await?;`,
		`self.authorize_method("/vservices/query.RecordMap/InsertIfAbsentLinkRow", &__op_request).await?;`,
		`self.authorize_method("/vservices/query.RecordMap/DeleteAuditRow", &__op_request).await?;`,
		`let __t = match __c.item.as_mut() {`,
		`"Apply (put_xid_row): item.tenant names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`rec: "ident.XidRow", s: __c.item.as_ref().map(|__p| __p.xid.clone()).unwrap_or_default(), n: 0, u: 0 }, 3, __c.durability)`,
		`rec: "ident.LinkRow", s: String::new(), n: 0, u: __c.item.as_ref().map(|__p| __p.seq).unwrap_or_default() as u64 }, 2, __c.durability)`,
		`rec: "ident.AuditRow", s: String::new(), n: __c.seq as i64, u: 0 }, 1, __c.durability)`,
		`None => return Err(tonic::Status::invalid_argument(format!("Apply: ops[{}] names no operation", __i))),`,
		`"Apply: ops[{}] asks for a durability below {}'s declared floor; a write may raise its type's durability, never lower it"`,
		`"Apply: the request asks for a durability below {}'s declared floor; an Apply is acked at the highest level its ops involve, never lower"`,
		`"Apply: ops[{}] is out of canonical lock order (record type full name, then key, each entry once)"`,
		`req.durability = __eff;`,
		`"Apply: the record store answered {} results for {} ops"`,
		`"Apply: the record store acked ops[{}] below the durability the request required; a write reported durable must be"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Apply wrapper missing %q", want)
		}
	}
	// Only the op whose case carries a tenant field is pinned.
	if n := strings.Count(got, "names a tenant other than the caller's"); n != 1 {
		t.Errorf("want one tenant pin (put_xid_row), got %d", n)
	}
	loop, call, answered := strings.Index(got, "req.ops.iter_mut()"), strings.Index(got, "self.handler.apply("), strings.Index(got, "answered {} results")
	if !(strings.Index(got, "req.validate()") < loop && loop < call && call < answered) {
		t.Fatal("Apply's op guards must run after request validation and before the handler, and the result check after it")
	}
}

// An auth-disabled service verifies nobody, so Apply has no per-op authorizer
// call to make (Go: skipAuth) and captures no metadata for one.
func TestRustRecordApplyGuardAuthDisabled(t *testing.T) {
	m := rustRecordApplyMethod()
	svc := rustRecordWriteService(m)
	svc.AuthDisabled = true
	got := mustGenerateRustGRPCWrapper(t, svc, m, "query", "vservices", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	if strings.Contains(got, "__record_apply_metadata") || strings.Contains(got, "authorize_method") {
		t.Errorf("an auth-disabled Apply must not authorize its ops:\n%s", got)
	}
	if !strings.Contains(got, "out of canonical lock order") {
		t.Errorf("an auth-disabled Apply still orders its ops:\n%s", got)
	}
}

// Issue #207: on a plane routed by tenant, the entry's tenant field must lie in
// the tenant the call is routed to, whoever the caller is: after the route
// check and the tenant pin, before the durability floor and the handler.
func TestRustRecordWriteRoutePin(t *testing.T) {
	m := rustPutXidRowMethod()
	m.RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 15}
	got := renderRustRecordWrite(t, m)
	for _, want := range []string{
		`self.check_route_tenant("target_tenant", &req.target_tenant)?;`,
		`let (__rt, __v): (&str, &str) = (req.target_tenant.as_str(), req.item.as_ref().map(|__p| __p.tenant.as_str()).unwrap_or(""));`,
		`let __home = format!("/tenants/{}", __rt);`,
		`if __v != __home && !__v.starts_with(&format!("{}/", __home)) {`,
		`tonic::Status::permission_denied(format!("PutXidRow: item.tenant is not in tenant {}, the tenant this call is routed to; a tenant's instance holds that tenant's entries only", __rt))`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("routed PutXidRow wrapper missing %q\n%s", want, got)
		}
	}
	route, pin, routePin, floor, call := strings.Index(got, "self.check_route_tenant("), strings.Index(got, "let __record_caller_ns"),
		strings.Index(got, "let __home"), strings.Index(got, "__d < 3"), strings.Index(got, "self.handler.put_xid_row(")
	if !(route < pin && pin < routePin && routePin < floor && floor < call) {
		t.Fatalf("guard order: route check %d, tenant pin %d, route pin %d, floor %d, handler %d", route, pin, routePin, floor, call)
	}
	if plain := renderRustRecordWrite(t, rustPutXidRowMethod()); strings.Contains(plain, "the tenant this call is routed to") {
		t.Errorf("an unrouted plane carries no route pin:\n%s", plain)
	}
}

// Apply's route pin runs per op against the Apply's own route field, read once
// before the ops are borrowed.
func TestRustRecordApplyRoutePin(t *testing.T) {
	m := rustRecordApplyMethod()
	m.RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 15}
	got := renderRustRecordWrite(t, m)
	for _, want := range []string{
		`let __record_route: String = req.target_tenant.clone();`,
		`let (__rt, __v): (&str, &str) = (__record_route.as_str(), __c.item.as_ref().map(|__p| __p.tenant.as_str()).unwrap_or(""));`,
		`"Apply (put_xid_row): item.tenant is not in tenant {}, the tenant this call is routed to; a tenant's instance holds that tenant's entries only"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("routed Apply wrapper missing %q\n%s", want, got)
		}
	}
	if n := strings.Count(got, "the tenant this call is routed to"); n != 1 {
		t.Errorf("want one route pin (put_xid_row), got %d", n)
	}
	if !(strings.Index(got, "let __record_route") < strings.Index(got, "req.ops.iter_mut()") &&
		strings.Index(got, "names a tenant other than the caller's") < strings.Index(got, "the tenant this call is routed to")) {
		t.Fatal("the route is bound before the op loop, and each op's route pin follows its tenant pin")
	}
}
