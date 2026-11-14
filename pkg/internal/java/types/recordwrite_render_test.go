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

// THE RECORD WRITE SURFACE GUARDS (#163), Java. These pin what the wrapper
// renders around the stamps the record-map injector puts on the write surface:
// the same checks as Go's recordwrite_gen.go, in the same order, with the same
// codes and texts. The behavior itself is driven against the generated
// fixture by cross-test/current-generation/testdata/java/RecordWriteGuardTest.java.

func renderRecordWrite(m *parser.MethodInfo, authDisabled bool) string {
	return generateJavaGRPCWrapper(
		m,
		"EgressRecordMap",
		"egressquery",
		"Egressquery."+m.InputType,
		"Egressquery."+m.OutputType,
		"Helpers",
		"Helpers",
		false,
		authDisabled,
		map[string]service.AccountingInfo{},
		"egressquery",
		"vservices",
		map[string]string{},
		proto.SkippedTraceTypes{},
		newJavaEgressSanitizer(nil),
		nil,
		nil,
	)
}

func putVaultRowMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "PutVaultRow", InputType: "PutVaultRowRequest", OutputType: "RecordWriteResult",
		FullInputType: "egressquery.PutVaultRowRequest", FullOutputType: "egressquery.RecordWriteResult",
		RecordEntry: &parser.RecordEntry{
			Record: "egressvault.VaultRow", Durability: spec.Durability_DURABILITY_REMOTE,
			KeyPath: []string{"item", "row_id"}, KeyKind: "string",
			NamespacePath: []string{"item", "namespace"},
		},
	}
}

func applyMethod() *parser.MethodInfo {
	op := func(caseField, caseType, record, rpc string, d spec.Durability, key []string, kind string, ns []string) parser.RecordEntry {
		return parser.RecordEntry{
			OpsField: "ops", OpType: "egressquery.RecordOp", OneofName: "op",
			CaseField: caseField, CaseType: caseType, Record: record, RPC: rpc,
			Durability: d, KeyPath: key, KeyKind: kind, NamespacePath: ns,
		}
	}
	return &parser.MethodInfo{
		Name: "Apply", InputType: "RecordApplyRequest", OutputType: "RecordApplyResponse",
		FullInputType: "egressquery.RecordApplyRequest", FullOutputType: "egressquery.RecordApplyResponse",
		RecordApplyOps: []parser.RecordEntry{
			op("insert_if_absent_vault_audit_row", "egressquery.InsertIfAbsentVaultAuditRowRequest", "egressvault.VaultAuditRow",
				"InsertIfAbsentVaultAuditRow", spec.Durability_DURABILITY_LOCAL, []string{"item", "seq"}, "int64", []string{"item", "namespace"}),
			op("put_vault_row", "egressquery.PutVaultRowRequest", "egressvault.VaultRow",
				"PutVaultRow", spec.Durability_DURABILITY_REMOTE, []string{"item", "row_id"}, "string", []string{"item", "namespace"}),
			op("delete_vault_row", "egressquery.DeleteVaultRowRequest", "egressvault.VaultRow",
				"DeleteVaultRow", spec.Durability_DURABILITY_REMOTE, []string{"row_id"}, "string", []string{"namespace"}),
			op("put_counter", "egressquery.PutCounterRequest", "egressvault.Counter",
				"PutCounter", spec.Durability_DURABILITY_MEMORY, []string{"id"}, "uint32", nil),
		},
	}
}

// mustInOrder fails unless every needle is in got, each after the one before.
func mustInOrder(t *testing.T, got string, needles ...string) {
	t.Helper()
	at := 0
	for _, n := range needles {
		i := strings.Index(got[at:], n)
		if i < 0 {
			t.Fatalf("missing (or out of order) %q after offset %d\n--- wrapper ---\n%s", n, at, got)
		}
		at += i + len(n)
	}
}

// A single-type write: the tenant pin, then the floor (refused below, stamped
// when unset), then the handler, then the ack check FIRST on the response.
func TestJavaRecordWrite_UnaryWriteGuards(t *testing.T) {
	got := renderRecordWrite(putVaultRowMethod(), false)
	mustInOrder(t, got,
		`checkAuth("egressquery.EgressRecordMap/PutVaultRow", request)`,
		`Helpers.validatePutVaultRowRequest(request);`,
		`final String __recordCallerNs = `,
		`if (!__recordCallerNs.startsWith("/system")) {`,
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "PutVaultRow addresses entries held to a tenant and the caller presented no verified namespace claim");`,
		`if (!request.hasItem()) {`,
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "PutVaultRow carries no item to hold to a tenant");`,
		`if (request.getItem().getNamespace().isEmpty()) {`,
		`request = request.toBuilder().setItem(request.getItem().toBuilder().setNamespace(__recordCallerNs)).build();`,
		`} else if (!__recordCallerNs.equals(request.getItem().getNamespace())) {`,
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "PutVaultRow: item.namespace names a tenant other than the caller's; the tenant boundary is not the caller's to choose");`,
		`if (request.getDurabilityValue() != 0 && request.getDurabilityValue() < 3) {`,
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "PutVaultRow asks for a durability below the record type's declared floor RECORD_DURABILITY_REMOTE; a write may raise its type's durability, never lower it");`,
		`} else if (request.getDurabilityValue() == 0) {`,
		`request = request.toBuilder().setDurabilityValue(3).build();`,
		`response = handler.putVaultRow(caller, request);`,
		`if (response.getDurabilityValue() < request.getDurabilityValue()) {`,
		`throw new ProtoException(ProtoException.INTERNAL, "PutVaultRow: the record store acked the write below the durability the request required; a write reported durable must be");`,
		`Helpers.validateRecordWriteResult(response);`,
	)
}

// A top-level tenant field needs no presence check and is set directly.
func TestJavaRecordWrite_TopLevelTenantField(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "DeleteVaultRow", InputType: "DeleteVaultRowRequest", OutputType: "RecordWriteResult",
		FullInputType: "egressquery.DeleteVaultRowRequest", FullOutputType: "egressquery.RecordWriteResult",
		RecordEntry: &parser.RecordEntry{
			Record: "egressvault.VaultRow", Durability: spec.Durability_DURABILITY_REMOTE,
			KeyPath: []string{"row_id"}, KeyKind: "string", NamespacePath: []string{"namespace"},
		},
	}
	got := renderRecordWrite(m, false)
	if strings.Contains(got, "carries no") {
		t.Fatalf("a top-level tenant field has no parent to check\n%s", got)
	}
	mustInOrder(t, got,
		`if (request.getNamespace().isEmpty()) {`,
		`request = request.toBuilder().setNamespace(__recordCallerNs).build();`,
		`"DeleteVaultRow: namespace names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
	)
}

// A read (Get<T>) carries no floor: the tenant pin only, no durability checks.
func TestJavaRecordWrite_ReadHasNoFloor(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "GetVaultRow", InputType: "GetVaultRowRequest", OutputType: "GetVaultRowResponse",
		FullInputType: "egressquery.GetVaultRowRequest", FullOutputType: "egressquery.GetVaultRowResponse",
		RecordEntry: &parser.RecordEntry{
			Record: "egressvault.VaultRow", KeyPath: []string{"row_id"}, KeyKind: "string", NamespacePath: []string{"namespace"},
		},
	}
	got := renderRecordWrite(m, false)
	if strings.Contains(got, "getDurabilityValue") {
		t.Fatalf("a read must carry no durability guard\n%s", got)
	}
	mustInOrder(t, got, `"GetVaultRow addresses entries held to a tenant`, `handler.getVaultRow(caller, request)`)
}

// Watch<T>Changes: the tenant pin before the handler opens the source.
func TestJavaRecordWrite_WatchTenantPin(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "WatchVaultRowChanges", InputType: "WatchVaultRowChangesRequest", OutputType: "VaultRowWatchEvent",
		FullInputType: "egressquery.WatchVaultRowChangesRequest", FullOutputType: "egressquery.VaultRowWatchEvent",
		ServerStreaming: true,
		Collection:      &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"row_id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"row_id"}, WatchOneOf: "event",
			WatchItemType: "egressvault.VaultRow", WatchChangeType: "egressquery.VaultRowWatchChange",
		},
		RecordEntry: &parser.RecordEntry{Record: "egressvault.VaultRow", NamespacePath: []string{"namespace"}},
	}
	got := renderRecordWrite(m, false)
	mustInOrder(t, got,
		`checkAuth("egressquery.EgressRecordMap/WatchVaultRowChanges", request)`,
		`"WatchVaultRowChanges addresses entries held to a tenant and the caller presented no verified namespace claim"`,
		`request = request.toBuilder().setNamespace(__recordCallerNs).build();`,
		`"WatchVaultRowChanges: namespace names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`handler.watchVaultRowChanges(caller, request)`,
	)
}

// Apply: per op the case, its own authorization, its tenant pin, its floor and
// its place in the lock order; then the effective durability; after the
// handler, one result per op, none acked below it.
func TestJavaRecordWrite_ApplyGuards(t *testing.T) {
	got := renderRecordWrite(applyMethod(), false)
	mustInOrder(t, got,
		`checkAuth("egressquery.EgressRecordMap/Apply", request)`,
		`final String __recordCallerNs = `,
		`com.google.protobuf.ByteString.unsignedLexicographicalComparator()`,
		`final int __top = request.getDurabilityValue();`,
		`for (int __i = 0; __i < __applyB.getOpsCount(); __i++) {`,
		`switch (__op.getOpCase()) {`,
		`case INSERT_IF_ABSENT_VAULT_AUDIT_ROW: {`,
		`checkAuth("egressquery.EgressRecordMap/InsertIfAbsentVaultAuditRow", request);`,
		`var __c = __op.getInsertIfAbsentVaultAuditRow();`,
		`"Apply (insert_if_absent_vault_audit_row) addresses entries held to a tenant and the caller presented no verified namespace claim"`,
		`if (!__c.hasItem()) {`,
		`"Apply (insert_if_absent_vault_audit_row) carries no item to hold to a tenant"`,
		`__c = __c.toBuilder().setItem(__c.getItem().toBuilder().setNamespace(__recordCallerNs)).build();`,
		`"Apply (insert_if_absent_vault_audit_row): item.namespace names a tenant other than the caller's; the tenant boundary is not the caller's to choose"`,
		`__op = __op.toBuilder().setInsertIfAbsentVaultAuditRow(__c).build();`,
		`__kRec = "egressvault.VaultAuditRow";`,
		`__floor = 2;`,
		`__d = __c.getDurabilityValue();`,
		`__kN = (long) __c.getItem().getSeq();`,
		`case PUT_VAULT_ROW: {`,
		`checkAuth("egressquery.EgressRecordMap/PutVaultRow", request);`,
		`__kS = __c.getItem().getRowId();`,
		`case DELETE_VAULT_ROW: {`,
		`"Apply (delete_vault_row): namespace names a tenant`,
		`__kS = __c.getRowId();`,
		`case PUT_COUNTER: {`,
		`__kU = Integer.toUnsignedLong(__c.getId());`,
		`default:`,
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "Apply: ops[" + __i + "] names no operation");`,
		`if (__d != 0 && __d < __floor) {`,
		`"Apply: ops[" + __i + "] asks for a durability below " + __kRec + "'s declared floor; a write may raise its type's durability, never lower it"`,
		`if (__top != 0 && __top < __floor) {`,
		`"Apply: the request asks for a durability below " + __kRec + "'s declared floor; an Apply is acked at the highest level its ops involve, never lower"`,
		`__recordBytes.compare(com.google.protobuf.ByteString.copyFromUtf8(__prevRec), com.google.protobuf.ByteString.copyFromUtf8(__kRec))`,
		`__recordBytes.compare(com.google.protobuf.ByteString.copyFromUtf8(__prevS), com.google.protobuf.ByteString.copyFromUtf8(__kS))`,
		`Long.compare(__prevN, __kN)`,
		`Long.compareUnsigned(__prevU, __kU)`,
		`if (__cmp >= 0) {`,
		`"Apply: ops[" + __i + "] is out of canonical lock order (record type full name, then key, each entry once)"`,
		`__applyB.setOps(__i, __op);`,
		`request = __applyB.setDurabilityValue(__eff).build();`,
		`response = handler.apply(caller, request);`,
		`if (response.getResultsCount() != request.getOpsCount()) {`,
		`"Apply: the record store answered " + response.getResultsCount() + " results for " + request.getOpsCount() + " ops"`,
		`if (response.getResults(__i).getDurabilityValue() < request.getDurabilityValue()) {`,
		`"Apply: the record store acked ops[" + __i + "] below the durability the request required; a write reported durable must be"`,
		`Helpers.validateRecordApplyResponse(response);`,
	)
	// A case with no tenant field has no pin and nothing to put back.
	if strings.Contains(got, "Apply (put_counter)") || strings.Contains(got, "setPutCounter(") {
		t.Fatalf("a case with no tenant field must not be pinned\n%s", got)
	}
}

// An Apply whose auth is disabled skips only the per-op authorization.
func TestJavaRecordWrite_ApplyAuthDisabledSkipsOpAuth(t *testing.T) {
	got := renderRecordWrite(applyMethod(), true)
	if strings.Contains(got, `checkAuth("egressquery.EgressRecordMap/PutVaultRow"`) {
		t.Fatalf("an auth-disabled Apply must not authorize its ops\n%s", got)
	}
	mustInOrder(t, got, `case PUT_VAULT_ROW: {`, `is out of canonical lock order`)
}

// An unstamped method's wrapper carries none of it.
func TestJavaRecordWrite_UnstampedMethodUnchanged(t *testing.T) {
	m := putVaultRowMethod()
	m.RecordEntry = nil
	got := renderRecordWrite(m, false)
	for _, s := range []string{"__recordCallerNs", "getDurabilityValue", "__applyB"} {
		if strings.Contains(got, s) {
			t.Fatalf("an unstamped method rendered %q\n%s", s, got)
		}
	}
}

// routed is m declared route_field "target_tenant", as the record-map injector
// declares every rpc of a plane on a tenant-owned record store (#207).
func routed(m *parser.MethodInfo) *parser.MethodInfo {
	m.RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 15}
	return m
}

const javaRoutePinTail = ` (" + __rn + "), the tenant this call is routed to; a tenant's instance holds that tenant's entries only");`

// On a plane routed by tenant (#207): the route check, then the tenant pin,
// then the route pin (the entry must lie in the tenant the call is routed to,
// whoever the caller is: the pin is outside the /system branch), then the floor.
// The routed tenant is a tid and the entry's tenant a namespace, which names it
// by name (kernel#23): the pin holds the entry to the namespace the instance's
// authorizer gives for that tid, never to /tenants/<tid>.
func TestJavaRecordWrite_RoutePin(t *testing.T) {
	got := renderRecordWrite(routed(putVaultRowMethod()), false)
	mustInOrder(t, got,
		`checkRouteTenant("target_tenant", request.getTargetTenant());`,
		`if (!__recordCallerNs.startsWith("/system")) {`,
		`"PutVaultRow: item.namespace names a tenant other than the caller's; the tenant boundary is not the caller's to choose");`,
		`final String __rt = request.getTargetTenant();`,
		`final String __v = request.getItem().getNamespace();`,
		`final String __rn = routeNamespace(__rt);`,
		`if (!__v.equals(__rn) && !__v.startsWith(__rn + "/")) {`,
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "PutVaultRow: item.namespace is not in tenant " + __rt + "`+javaRoutePinTail,
		`if (request.getDurabilityValue() != 0 && request.getDurabilityValue() < 3) {`,
		`response = handler.putVaultRow(caller, request);`,
	)
	// The route pin is not inside the /system exemption: its block opens after
	// the tenant pin's closes.
	pin := strings.Index(got, `final String __rt = `)
	sys := strings.Index(got, `if (!__recordCallerNs.startsWith("/system")) {`)
	if strings.Count(got[sys:pin], "{") != strings.Count(got[sys:pin], "}")+1 {
		t.Fatalf("the route pin must hold every caller, a /system peer included\n%s", got)
	}
	if strings.Contains(got, `"/tenants/" + __rt`) {
		t.Fatalf("the route pin must not spell the routed tid as a namespace\n%s", got)
	}

	if g := renderRecordWrite(putVaultRowMethod(), false); strings.Contains(g, "__rt") || strings.Contains(g, "the tenant this call is routed to") {
		t.Fatalf("a plane not routed by tenant renders no route pin\n%s", g)
	}
}

// Apply: each op's tenant field against the Apply's OWN route field, after the
// op's tenant pin and before the op is put back; an op with no tenant field
// has none.
func TestJavaRecordWrite_ApplyRoutePin(t *testing.T) {
	got := renderRecordWrite(routed(applyMethod()), false)
	mustInOrder(t, got,
		`checkRouteTenant("target_tenant", request.getTargetTenant());`,
		`case INSERT_IF_ABSENT_VAULT_AUDIT_ROW: {`,
		`"Apply (insert_if_absent_vault_audit_row): item.namespace names a tenant other than the caller's`,
		`final String __rt = request.getTargetTenant();`,
		`final String __v = __c.getItem().getNamespace();`,
		`final String __rn = routeNamespace(__rt);`,
		`"Apply (insert_if_absent_vault_audit_row): item.namespace is not in tenant " + __rt + "`+javaRoutePinTail,
		`__op = __op.toBuilder().setInsertIfAbsentVaultAuditRow(__c).build();`,
		`case PUT_VAULT_ROW: {`,
		`"Apply (put_vault_row): item.namespace is not in tenant " + __rt + "`,
		`case DELETE_VAULT_ROW: {`,
		`final String __v = __c.getNamespace();`,
		`"Apply (delete_vault_row): namespace is not in tenant " + __rt + "`,
		`case PUT_COUNTER: {`,
		`response = handler.apply(caller, request);`,
	)
	if strings.Contains(got, "Apply (put_counter): ") {
		t.Fatalf("a case with no tenant field has no route pin\n%s", got)
	}
	if strings.Contains(got, `"/tenants/" + __rt`) {
		t.Fatalf("the route pin must not spell the routed tid as a namespace\n%s", got)
	}
}

// A stored row never keeps a trace (#313): the wrapper rebuilds the request
// with the injected trace_id cleared off every row it carries — item, and
// ReplaceIfUnchanged's expected_item, whose compare value would otherwise
// mismatch the stored (traceless) row — before the handler, and leaves the
// request's own trace_id, the call's, alone.
func TestJavaRecordWrite_ClearsRowTrace(t *testing.T) {
	put := putVaultRowMethod()
	put.RecordEntry.RowFields = []string{"item"}
	got := renderRecordWrite(put, false)
	mustInOrder(t, got,
		`request = request.toBuilder().setDurabilityValue(3).build();`,
		`// A stored row never keeps a trace (#313).`,
		`if (request.hasItem()) {`,
		`request = request.toBuilder().setItem(request.getItem().toBuilder().clearTraceId()).build();`,
		`handler.putVaultRow(caller, request)`)
	if strings.Contains(got, "request.toBuilder().clearTraceId()") {
		t.Errorf("the request's own trace_id is the call's and stays:\n%s", got)
	}

	replace := &parser.MethodInfo{
		Name: "ReplaceIfUnchangedVaultRow", InputType: "ReplaceIfUnchangedVaultRowRequest", OutputType: "RecordWriteResult",
		FullInputType: "egressquery.ReplaceIfUnchangedVaultRowRequest", FullOutputType: "egressquery.RecordWriteResult",
		RecordEntry: &parser.RecordEntry{Record: "egressvault.VaultRow", Durability: spec.Durability_DURABILITY_REMOTE,
			KeyPath: []string{"item", "row_id"}, KeyKind: "string", RowFields: []string{"item", "expected_item"}},
	}
	mustInOrder(t, renderRecordWrite(replace, false),
		`request = request.toBuilder().setItem(request.getItem().toBuilder().clearTraceId()).build();`,
		`if (request.hasExpectedItem()) {`,
		`request = request.toBuilder().setExpectedItem(request.getExpectedItem().toBuilder().clearTraceId()).build();`,
		`handler.replaceIfUnchangedVaultRow(caller, request)`)

	// A record type without the injected trace_id generates exactly as before.
	skipped := generateJavaGRPCWrapper(replace, "EgressRecordMap", "egressquery", "Egressquery."+replace.InputType,
		"Egressquery."+replace.OutputType, "Helpers", "Helpers", false, false, map[string]service.AccountingInfo{},
		"egressquery", "vservices", map[string]string{}, proto.SkippedTraceTypes{"egressvault.VaultRow": true},
		newJavaEgressSanitizer(nil), nil, nil)
	if strings.Contains(skipped, "clearTraceId") || strings.Contains(skipped, "#313") {
		t.Errorf("a record type without the injected trace_id clears nothing:\n%s", skipped)
	}

	apply := applyMethod()
	for i := range apply.RecordApplyOps {
		if apply.RecordApplyOps[i].CaseField != "delete_vault_row" {
			apply.RecordApplyOps[i].RowFields = []string{"item"}
		}
	}
	apply.RecordApplyOps[3].KeyPath = []string{"item", "id"}
	apply.RecordApplyOps = append(apply.RecordApplyOps, parser.RecordEntry{
		OpsField: "ops", OpType: "egressquery.RecordOp", OneofName: "op", CaseField: "replace_if_unchanged_vault_row",
		CaseType: "egressquery.ReplaceIfUnchangedVaultRowRequest", Record: "egressvault.VaultRow", RPC: "ReplaceIfUnchangedVaultRow",
		Durability: spec.Durability_DURABILITY_REMOTE, KeyPath: []string{"item", "row_id"}, KeyKind: "string",
		RowFields: []string{"item", "expected_item"},
	})
	got = renderRecordWrite(apply, false)
	// put_counter carries no tenant: its case gains the clear and the op rebuild.
	mustInOrder(t, got,
		`case PUT_COUNTER: {`,
		`__c = __c.toBuilder().setItem(__c.getItem().toBuilder().clearTraceId()).build();`,
		`__op = __op.toBuilder().setPutCounter(__c).build();`,
		`case REPLACE_IF_UNCHANGED_VAULT_ROW: {`,
		`__c = __c.toBuilder().setItem(__c.getItem().toBuilder().clearTraceId()).build();`,
		`__c = __c.toBuilder().setExpectedItem(__c.getExpectedItem().toBuilder().clearTraceId()).build();`,
		`__op = __op.toBuilder().setReplaceIfUnchangedVaultRow(__c).build();`,
		`__applyB.setOps(__i, __op);`,
		`handler.apply(caller, request)`)
	if n := strings.Count(got, "clearTraceId()"); n != 5 {
		t.Errorf("Apply must clear exactly the five op rows, got %d:\n%s", n, got)
	}
}

// A (spec.service_store) <T>StoreService's Put stores its request as the row
// (#313): the wrapper clears the request's trace_id just before the handler —
// after the request trace event has read it — and the trace events after the
// handler carry the trace the call arrived with.
func TestJavaStoreServicePut_ClearsRowTrace(t *testing.T) {
	put := &parser.MethodInfo{
		Name: "Put", InputType: "Task", OutputType: "Empty",
		FullInputType: "tasks.Task", FullOutputType: "google.protobuf.Empty",
	}
	render := func(store bool) string {
		return generateJavaGRPCWrapperSurface(put, "TaskStoreService", "tasks", "Tasks.Task", "com.google.protobuf.Empty",
			"Helpers", "", false, false, map[string]service.AccountingInfo{}, "tasks", "vservices", map[string]string{},
			proto.SkippedTraceTypes{}, newJavaEgressSanitizer(nil), nil, nil, false, false, store)
	}
	got := render(true)
	mustInOrder(t, got,
		`.setTraceId(request.getTraceId())`,
		`final String __traceId = request.getTraceId();`,
		`request = request.toBuilder().clearTraceId().build();`,
		`handler.put(caller, request);`,
		`.setTraceId(__traceId)`)
	call := strings.Index(got, "handler.put(caller, request);")
	if strings.Count(got, ".setTraceId(__traceId)") != 2 || strings.Contains(got[call:], "request.getTraceId()") {
		t.Errorf("both post-handler trace events must carry the saved trace:\n%s", got)
	}
	if plain := render(false); strings.Contains(plain, "__traceId") || strings.Contains(plain, "#313") {
		t.Errorf("a hand-written Put stores nothing the wrapper knows of:\n%s", plain)
	}
}
