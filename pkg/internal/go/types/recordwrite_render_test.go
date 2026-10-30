// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// The record write surface's guards (#163) on the generated Go store wrapper.
// The behavior is driven end to end by the record-egress host fixture's
// self-test (cross-test/current-generation/testdata/go/record_write_guard_test.go);
// these pin the rendering, so a guard dropped from a seam fails here first.

func recordWriteService(methods ...*parser.MethodInfo) *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "RecordMap", IsS2S: true, Methods: methods}
}

func renderRecordWrite(m *parser.MethodInfo) string {
	return generateGRPCWrapper(recordWriteService(m), m, "query", "vservices", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil)
}

func putXidRowMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "PutXidRow", InputType: "PutXidRowRequest", OutputType: "RecordWriteResult",
		FullInputType: "query.PutXidRowRequest", FullOutputType: "query.RecordWriteResult",
		RecordEntry: &parser.RecordEntry{
			Record: "ident.XidRow", Durability: spec.Durability_DURABILITY_REMOTE,
			KeyPath: []string{"item", "xid"}, KeyKind: "string", NamespacePath: []string{"item", "tenant"},
		},
	}
}

func TestRecordWriteGuardFloorAndTenant(t *testing.T) {
	got := renderRecordWrite(putXidRowMethod())
	for _, want := range []string{
		`if __d := int32(req.GetDurability()); __d != 0 && __d < 3 {`,
		`"PutXidRow asks for a durability below the record type's declared floor RECORD_DURABILITY_REMOTE; a write may raise its type's durability, never lower it"`,
		`req.Durability = 3`,
		`if resp.GetDurability() < req.GetDurability() {`,
		`status.Error(codes.Internal, "PutXidRow: the record store acked the write below the durability the request required; a write reported durable must be")`,
		`__recordCallerNs = caller.Token().GetNamespace()`,
		`if __ns := __recordCallerNs; !strings.HasPrefix(__ns, "/system") {`,
		`if req.GetItem() == nil {`,
		`if __v := req.GetItem().GetTenant(); __v == "" {`,
		`req.GetItem().Tenant = __ns`,
		`"PutXidRow: item.tenant names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PutXidRow wrapper missing %q\n%s", want, got)
		}
	}
	// Order: the tenant pin and the floor run before the handler; the ack
	// check after it.
	pin, floor, call, ack := strings.Index(got, "__recordCallerNs :="), strings.Index(got, "__d < 3"),
		strings.Index(got, "p.handler.PutXidRow("), strings.Index(got, "resp.GetDurability() < req.GetDurability()")
	if !(pin < floor && floor < call && call < ack) {
		t.Fatalf("guard order: pin %d, floor %d, handler %d, ack %d", pin, floor, call, ack)
	}
}

// A read carries a tenant pin and no durability guard; a method with no stamp
// carries neither.
func TestRecordWriteGuardReadAndUnstamped(t *testing.T) {
	get := &parser.MethodInfo{
		Name: "GetXidRow", InputType: "GetXidRowRequest", OutputType: "GetXidRowResponse",
		FullInputType: "query.GetXidRowRequest", FullOutputType: "query.GetXidRowResponse",
		RecordEntry: &parser.RecordEntry{Record: "ident.XidRow", KeyPath: []string{"xid"}, KeyKind: "string", NamespacePath: []string{"tenant"}},
	}
	got := renderRecordWrite(get)
	if !strings.Contains(got, `if __v := req.GetTenant(); __v == "" {`) || !strings.Contains(got, `req.Tenant = __ns`) {
		t.Errorf("Get<T> must pin its tenant field:\n%s", got)
	}
	if strings.Contains(got, "GetDurability()") {
		t.Errorf("a read carries no durability guard:\n%s", got)
	}
	plain := renderRecordWrite(&parser.MethodInfo{
		Name: "GetPing", InputType: "Ping", OutputType: "Ping", FullInputType: "query.Ping", FullOutputType: "query.Ping",
	})
	if strings.Contains(plain, "__recordCallerNs") || strings.Contains(plain, "GetDurability()") {
		t.Errorf("an unstamped rpc carries no record write guard:\n%s", plain)
	}
}

func TestRecordApplyGuard(t *testing.T) {
	apply := &parser.MethodInfo{
		Name: "Apply", InputType: "RecordApplyRequest", OutputType: "RecordApplyResponse",
		FullInputType: "query.RecordApplyRequest", FullOutputType: "query.RecordApplyResponse",
		RecordApplyOps: []parser.RecordEntry{
			{OpsField: "ops", OpType: "query.RecordOp", OneofName: "op", CaseField: "put_xid_row",
				Record: "ident.XidRow", RPC: "PutXidRow", Durability: spec.Durability_DURABILITY_REMOTE,
				KeyPath: []string{"item", "xid"}, KeyKind: "string", NamespacePath: []string{"item", "tenant"}},
			{OpsField: "ops", OpType: "query.RecordOp", OneofName: "op", CaseField: "insert_if_absent_link_row",
				Record: "ident.LinkRow", RPC: "InsertIfAbsentLinkRow", Durability: spec.Durability_DURABILITY_LOCAL,
				KeyPath: []string{"item", "seq"}, KeyKind: "uint64"},
		},
	}
	// Unformatted render: gofmt later joins `"/vservices" + types.X` as the
	// generated file spells it.
	got := strings.ReplaceAll(renderRecordWrite(apply), `" + types.`, `"+types.`)
	for _, want := range []string{
		`for __i, __op := range req.GetOps() {`,
		`switch __c := __op.GetOp().(type) {`,
		`case *types.RecordOp_PutXidRow:`,
		`if _, err := p.authorize(ctx, "/vservices"+types.RecordMap_PutXidRow_FullMethodName, req); err != nil {`,
		`if _, err := p.authorize(ctx, "/vservices"+types.RecordMap_InsertIfAbsentLinkRow_FullMethodName, req); err != nil {`,
		`__k.rec, __floor, __d = "ident.XidRow", 3, int32(__c.PutXidRow.GetDurability())`,
		`__k.s = __c.PutXidRow.GetItem().GetXid()`,
		`__k.u = uint64(__c.InsertIfAbsentLinkRow.GetItem().GetSeq())`,
		`if __v := __c.PutXidRow.GetItem().GetTenant(); __v == "" {`,
		`"Apply (put_xid_row): item.tenant names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`"Apply: ops[%d] names no operation"`,
		`"Apply: ops[%d] asks for a durability below %s's declared floor; a write may raise its type's durability, never lower it"`,
		`"Apply: the request asks for a durability below %s's declared floor; an Apply is acked at the highest level its ops involve, never lower"`,
		`"Apply: ops[%d] is out of canonical lock order (record type full name, then key, each entry once)"`,
		`req.Durability = types.RecordDurability(__eff)`,
		`"Apply: the record store answered %d results for %d ops"`,
		`"Apply: the record store acked ops[%d] below the durability the request required; a write reported durable must be"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Apply wrapper missing %q", want)
		}
	}
	if !(strings.Index(got, "range req.GetOps()") < strings.Index(got, "p.handler.Apply(") &&
		strings.Index(got, "p.handler.Apply(") < strings.Index(got, "answered %d results")) {
		t.Fatal("Apply's op guards must run before the handler and the result check after it")
	}
}

// On a tenant-owned record store (#207) the plane is routed by target_tenant:
// after the route check and the tenant pin, the entry's tenant field must be in
// the routed tenant, for every caller; an unrouted plane renders no such pin.
func TestRecordWriteGuardRoutePin(t *testing.T) {
	m := putXidRowMethod()
	if got := renderRecordWrite(m); strings.Contains(got, "the tenant this call is routed to") {
		t.Fatalf("an unrouted plane must render no route pin:\n%s", got)
	}
	m.RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 15}
	got := renderRecordWrite(m)
	for _, want := range []string{
		`if err := p.checkRoute(`,
		`if __rt, __v := req.GetTargetTenant(), req.GetItem().GetTenant(); __v != "/tenants/"+__rt && !strings.HasPrefix(__v, "/tenants/"+__rt+"/") {`,
		`status.Error(codes.PermissionDenied, "PutXidRow: item.tenant is not in tenant "+__rt+", the tenant this call is routed to; a tenant's instance holds that tenant's entries only")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("routed PutXidRow wrapper missing %q\n%s", want, got)
		}
	}
	// Order: the route check, then the caller's tenant pin, then the route
	// pin, then the handler.
	route, pin, rpin, call := strings.Index(got, "p.checkRoute("), strings.Index(got, "__recordCallerNs :="),
		strings.Index(got, "__rt, __v :="), strings.Index(got, "p.handler.PutXidRow(")
	if !(route < pin && pin < rpin && rpin < call) {
		t.Fatalf("guard order: route %d, pin %d, route pin %d, handler %d", route, pin, rpin, call)
	}
}
