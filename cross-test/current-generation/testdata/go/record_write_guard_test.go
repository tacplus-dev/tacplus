// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress HOST fixture (#163). NOT generated:
// run.sh copies this file into the generated servers/vservices/egressquery
// package before `go test ./...`, so it drives the REAL generated
// EgressRecordMap wrapper in-process: a fake handler behind it, a fake
// authorizer in front of it, and the record write surface's guards between.
//
// What it pins, through the wrapper's own methods:
//   - durability: a write below VaultRow's REMOTE floor is refused, a write
//     naming none reaches the handler at REMOTE, and a write the store acks
//     below that is not answered;
//   - tenant: a /tenants caller is pinned to its own tenant (stamped when
//     unset, refused when foreign), a /system peer is not;
//   - Apply: ops out of canonical lock order or naming one entry twice are
//     refused, each op is authorized as its single-type rpc, the handler sees
//     the highest floor, and a short answer is not returned.

package egressquery

import (
	"context"
	"strings"
	"testing"

	wrapperToken "gitlab.com/example/dummy/egressstratum/tacplus/model/chassis/token"
	"gitlab.com/example/dummy/egressstratum/tacplus/model/chassis/servicemanager"
	types "gitlab.com/example/dummy/egressstratum/tacplus/model/types/vservices/egressquery"
	egressvault_pb "gitlab.com/example/dummy/egressstratum/tacplus/model/types/services/egressvault"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// writeGuardHandler implements only the write rpcs the test drives; the
// embedded interface panics on anything else.
type writeGuardHandler struct {
	EgressRecordMapHandler
	putSeen   *types.PutVaultRowRequest
	delSeen   *types.DeleteVaultRowRequest
	applySeen *types.RecordApplyRequest
	ack       types.RecordDurability
	results   int
}

func (h *writeGuardHandler) PutVaultRow(_ context.Context, _ *servicemanager.Caller, req *types.PutVaultRowRequest) (*types.RecordWriteResult, error) {
	h.putSeen = req
	return &types.RecordWriteResult{Revision: 1, Durability: h.ack}, nil
}

func (h *writeGuardHandler) DeleteVaultRow(_ context.Context, _ *servicemanager.Caller, req *types.DeleteVaultRowRequest) (*types.RecordWriteResult, error) {
	h.delSeen = req
	return &types.RecordWriteResult{Revision: 2, Durability: h.ack}, nil
}

func (h *writeGuardHandler) Apply(_ context.Context, _ *servicemanager.Caller, req *types.RecordApplyRequest) (*types.RecordApplyResponse, error) {
	h.applySeen = req
	n := h.results
	if n < 0 {
		n = len(req.GetOps())
	}
	resp := &types.RecordApplyResponse{}
	for i := 0; i < n; i++ {
		resp.Results = append(resp.Results, &types.RecordWriteResult{Revision: int64(i + 1), Durability: h.ack})
	}
	return resp, nil
}

// writeGuardServer builds the wrapper with an authorizer that verifies a
// caller in namespace ns and refuses every full method in deny.
func writeGuardServer(h *writeGuardHandler, ns string, deny ...string) *EgressRecordMap {
	p := NewEgressRecordMap(h)
	p.SetAuthorizeFunc(func(_ context.Context, fullMethod string, _ any) (*wrapperToken.TokenWrapper, error) {
		for _, d := range deny {
			if strings.HasSuffix(fullMethod, "/"+d) {
				return nil, status.Error(codes.PermissionDenied, "denied: "+d)
			}
		}
		tok := wrapperToken.NewImmutableToken(nil).SetSubject("spiffe://example.com" + ns + "/services/egressvault").SetNamespace(ns)
		return tok, nil
	})
	return p
}

func vaultRow(id, ns string) *egressvault_pb.VaultRow {
	return &egressvault_pb.VaultRow{RowId: id, Namespace: ns, Owner: "o"}
}

func wantCode(t *testing.T, err error, code codes.Code, substr string) {
	t.Helper()
	if status.Code(err) != code || !strings.Contains(status.Convert(err).Message(), substr) {
		t.Fatalf("want %v containing %q, got %v", code, substr, err)
	}
}

func TestRecordWriteGuardDurabilityFloor(t *testing.T) {
	ctx := context.Background()
	h := &writeGuardHandler{ack: types.RecordDurability_RECORD_DURABILITY_REMOTE}
	p := writeGuardServer(h, "/system")

	_, err := p.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/acme"), Durability: types.RecordDurability_RECORD_DURABILITY_MEMORY})
	wantCode(t, err, codes.InvalidArgument, "below the record type's declared floor RECORD_DURABILITY_REMOTE")
	if h.putSeen != nil {
		t.Fatal("a write below the floor must not reach the handler")
	}

	if _, err := p.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/acme")}); err != nil {
		t.Fatalf("a write naming no durability must be accepted: %v", err)
	}
	if got := h.putSeen.GetDurability(); got != types.RecordDurability_RECORD_DURABILITY_REMOTE {
		t.Fatalf("the handler must receive the floor, got %v", got)
	}

	h.ack = types.RecordDurability_RECORD_DURABILITY_LOCAL
	_, err = p.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/acme")})
	wantCode(t, err, codes.Internal, "acked the write below the durability the request required")
}

func TestRecordWriteGuardTenantPin(t *testing.T) {
	ctx := context.Background()
	h := &writeGuardHandler{ack: types.RecordDurability_RECORD_DURABILITY_REMOTE}
	tenant := writeGuardServer(h, "/tenants/acme")

	// A key-addressed write names its tenant beside the key; unset is stamped.
	// (A row's own tenant field is required by VaultRow, so validation refuses
	// an unset one before any pin runs.)
	if _, err := tenant.DeleteVaultRow(ctx, &types.DeleteVaultRowRequest{RowId: "r1"}); err != nil {
		t.Fatalf("a tenant caller's unset tenant must be stamped, not refused: %v", err)
	}
	if got := h.delSeen.GetNamespace(); got != "/tenants/acme" {
		t.Fatalf("stamped tenant = %q, want /tenants/acme", got)
	}
	_, err := tenant.DeleteVaultRow(ctx, &types.DeleteVaultRowRequest{RowId: "r1", Namespace: "/tenants/beta"})
	wantCode(t, err, codes.InvalidArgument, "DeleteVaultRow: namespace names a tenant the caller neither owns nor is linked to")
	if _, err := tenant.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/acme")}); err != nil {
		t.Fatalf("a tenant caller writing its own tenant's row must pass: %v", err)
	}
	_, err = tenant.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/beta")})
	wantCode(t, err, codes.InvalidArgument, "names a tenant the caller neither owns nor is linked to")

	system := writeGuardServer(h, "/system")
	if _, err := system.PutVaultRow(ctx, &types.PutVaultRowRequest{Item: vaultRow("r1", "/tenants/beta")}); err != nil {
		t.Fatalf("a /system peer keeps the fleet-wide reach: %v", err)
	}
	if got := h.putSeen.GetItem().GetNamespace(); got != "/tenants/beta" {
		t.Fatalf("a /system peer's tenant must pass untouched, got %q", got)
	}
}

func putOp(id string) *types.RecordOp {
	return &types.RecordOp{Op: &types.RecordOp_PutVaultRow{PutVaultRow: &types.PutVaultRowRequest{Item: vaultRow(id, "/tenants/acme")}}}
}

func auditOp(seq int64) *types.RecordOp {
	return &types.RecordOp{Op: &types.RecordOp_InsertIfAbsentVaultAuditRow{InsertIfAbsentVaultAuditRow: &types.InsertIfAbsentVaultAuditRowRequest{
		Item: &egressvault_pb.VaultAuditRow{Seq: seq, Namespace: "/tenants/acme", Owner: "o"}}}}
}

func TestRecordApplyGuard(t *testing.T) {
	ctx := context.Background()
	h := &writeGuardHandler{ack: types.RecordDurability_RECORD_DURABILITY_REMOTE, results: -1}
	p := writeGuardServer(h, "/system")

	// egressvault.VaultAuditRow sorts before egressvault.VaultRow.
	_, err := p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{putOp("a"), auditOp(1)}})
	wantCode(t, err, codes.InvalidArgument, "out of canonical lock order")
	_, err = p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{putOp("a"), putOp("a")}})
	wantCode(t, err, codes.InvalidArgument, "ops[1] is out of canonical lock order")
	// Integer keys order numerically: 9 before 10.
	if _, err := p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{auditOp(9), auditOp(10)}}); err != nil {
		t.Fatalf("integer keys order numerically: %v", err)
	}
	_, err = p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{{}}})
	wantCode(t, err, codes.InvalidArgument, "ops[0] names no operation")

	if _, err := p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{auditOp(1), putOp("a"), putOp("b")}}); err != nil {
		t.Fatalf("ops in canonical order must be accepted: %v", err)
	}
	if got := h.applySeen.GetDurability(); got != types.RecordDurability_RECORD_DURABILITY_REMOTE {
		t.Fatalf("an Apply is acked at the highest floor its ops involve (REMOTE), handler saw %v", got)
	}
	_, err = p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{auditOp(1)}, Durability: types.RecordDurability_RECORD_DURABILITY_MEMORY})
	wantCode(t, err, codes.InvalidArgument, "the request asks for a durability below egressvault.VaultAuditRow's declared floor")

	h.results = 1
	_, err = p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{putOp("a"), putOp("b")}})
	wantCode(t, err, codes.Internal, "answered 1 results for 2 ops")
	h.results = -1
	h.ack = types.RecordDurability_RECORD_DURABILITY_LOCAL
	_, err = p.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{putOp("a")}})
	wantCode(t, err, codes.Internal, "acked ops[0] below the durability the request required")

	// Each op is authorized as its single-type rpc: Apply itself is allowed,
	// PutVaultRow is not, so an Apply carrying a Put is refused with the
	// authorizer's own answer and never reaches the handler.
	h.ack = types.RecordDurability_RECORD_DURABILITY_REMOTE
	h.applySeen = nil
	denied := writeGuardServer(h, "/system", "PutVaultRow")
	_, err = denied.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{putOp("a")}})
	wantCode(t, err, codes.PermissionDenied, "denied: PutVaultRow")
	if h.applySeen != nil {
		t.Fatal("an Apply carrying an op its caller may not make must not reach the handler")
	}
	if _, err := denied.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{auditOp(1)}}); err != nil {
		t.Fatalf("an Apply whose ops the caller may all make must pass: %v", err)
	}

	// The tenant pin runs per op.
	tenant := writeGuardServer(h, "/tenants/acme")
	foreign := &types.RecordOp{Op: &types.RecordOp_PutVaultRow{PutVaultRow: &types.PutVaultRowRequest{Item: vaultRow("a", "/tenants/beta")}}}
	_, err = tenant.Apply(ctx, &types.RecordApplyRequest{Ops: []*types.RecordOp{foreign}})
	wantCode(t, err, codes.InvalidArgument, "Apply (put_vault_row): item.namespace names a tenant the caller neither owns nor is linked to")
}
