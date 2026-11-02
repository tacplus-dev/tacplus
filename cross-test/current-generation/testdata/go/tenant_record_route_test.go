// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-record fixture's HOST build (#207). NOT
// generated: run.sh copies this file into the generated
// servers/vservices/tquery package before `go test ./...`, so it drives the
// REAL generated TenantRecordMap wrapper in-process, with the REAL generated
// authorizer's route check for the instance of tenant bbbbbbbb in front of it
// and a fake handler behind it. The handler stands for the store and its redo:
// a call that never reaches it leaves nothing in either.
//
// What it pins: a call misrouted to tenant bbbbbbbb's instance (routed to
// aaaaaaaa, or routed to bbbbbbbb carrying tenant aaaaaaaa's entry) is refused
// by the instance, whoever the caller is, a /system peer included, and never
// reaches the handler.
//
// Identities are kernel#23's: a tenant-owned service's subject is
// spiffe://<td>/tenants/<tid>/services/<svc> while its namespace claim stays
// /tenants/<name>. Tenant beta is tid bbbbbbbb, tenant alpha is tid aaaaaaaa.
// The instance serves the tid of its own subject; a caller is in the tenant of
// its namespace claim, so its entries are /tenants/beta's, never
// /tenants/bbbbbbbb's.

package tquery

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/example/dummy/tstratum/tacplus/model/chassis/grpcserver"
	"gitlab.com/example/dummy/tstratum/tacplus/model/chassis/servicemanager"
	wrapperToken "gitlab.com/example/dummy/tstratum/tacplus/model/chassis/token"
	tmessaging_pb "gitlab.com/example/dummy/tstratum/tacplus/model/types/services/tmessaging"
	types "gitlab.com/example/dummy/tstratum/tacplus/model/types/vservices/tquery"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// routeHandler implements only the rpcs the test drives; the embedded
// interface panics on anything else. reached counts every call that got
// through the wrapper.
type routeHandler struct {
	TenantRecordMapHandler
	reached int
	lastNs  string
}

func (h *routeHandler) PutTMessage(_ context.Context, _ *servicemanager.Caller, req *types.PutTMessageRequest) (*types.RecordWriteResult, error) {
	h.reached++
	h.lastNs = req.GetItem().GetNamespace()
	return &types.RecordWriteResult{Revision: 1, Durability: req.GetDurability()}, nil
}

func (h *routeHandler) DeleteTMessage(_ context.Context, _ *servicemanager.Caller, req *types.DeleteTMessageRequest) (*types.RecordWriteResult, error) {
	h.reached++
	return &types.RecordWriteResult{Revision: 2, Durability: req.GetDurability()}, nil
}

func (h *routeHandler) GetTMessage(_ context.Context, _ *servicemanager.Caller, _ *types.GetTMessageRequest) (*types.GetTMessageResponse, error) {
	h.reached++
	return &types.GetTMessageResponse{}, nil
}

func (h *routeHandler) Apply(_ context.Context, _ *servicemanager.Caller, req *types.RecordApplyRequest) (*types.RecordApplyResponse, error) {
	h.reached++
	resp := &types.RecordApplyResponse{}
	for range req.GetOps() {
		resp.Results = append(resp.Results, &types.RecordWriteResult{Revision: 1, Durability: req.GetDurability()})
	}
	return resp, nil
}

// instanceOfB builds the wrapper as the ServiceManager of tenant beta's
// instance (tid bbbbbbbb) wires it: the authorizer knows its own identity and
// every routed rpc's route field, and the wrapper's route check and route
// namespace are the authorizer's. The caller in namespace ns, with subject sub,
// is admitted by authorize, so what refuses a call is the routing, never the
// caller's grant.
func instanceOfB(t *testing.T, h *routeHandler, ns string) *TenantRecordMap {
	t.Helper()
	sub := "spiffe://example.com/system/services/tmessaging"
	switch ns {
	case "/tenants/alpha":
		sub = "spiffe://example.com/tenants/aaaaaaaa/services/tmessaging"
	case "/tenants/beta":
		sub = "spiffe://example.com/tenants/bbbbbbbb/services/tmessaging"
	}
	return instanceOfBFor(t, h, ns, sub)
}

func instanceOfBFor(t *testing.T, h *routeHandler, ns, sub string) *TenantRecordMap {
	t.Helper()
	auth, err := grpcserver.NewAuthorizer(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	auth.SetHostIdentity("/tenants/beta", "spiffe://example.com/tenants/bbbbbbbb/services/tquery")
	for _, m := range []string{"PutTMessage", "DeleteTMessage", "GetTMessage", "Apply"} {
		auth.SetMethodRouteField("tquery.TenantRecordMap/"+m, "target_tenant")
	}
	p := NewTenantRecordMap(h)
	p.SetRouteCheckFunc(auth.CheckRoute)
	p.SetRouteNamespaceFunc(auth.RouteNamespace)
	p.SetAuthorizeFunc(func(_ context.Context, _ string, _ any) (*wrapperToken.TokenWrapper, error) {
		return wrapperToken.NewImmutableToken(nil).SetSubType("service").SetSubject(sub).SetNamespace(ns), nil
	})
	return p
}

func msg(id, ns string) *tmessaging_pb.TMessage {
	return &tmessaging_pb.TMessage{MessageId: id, Namespace: ns, Owner: "o"}
}

func refused(t *testing.T, h *routeHandler, err error, code codes.Code, substr string) {
	t.Helper()
	if status.Code(err) != code || !strings.Contains(status.Convert(err).Message(), substr) {
		t.Fatalf("want %v containing %q, got %v", code, substr, err)
	}
	if h.reached != 0 {
		t.Fatalf("a refused call reached the handler (the store and its redo) %d time(s)", h.reached)
	}
}

func TestTenantRecordMisroutedCallRefused(t *testing.T) {
	ctx := context.Background()
	h := &routeHandler{}
	b := instanceOfB(t, h, "/system")

	// The client forced to B's instance with A's call: the route names A.
	_, err := b.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "aaaaaaaa", Item: msg("m1", "/tenants/alpha")})
	refused(t, h, err, codes.PermissionDenied, "routed to tenant aaaaaaaa, this instance serves tenant bbbbbbbb")

	// Routed to B, carrying A's row.
	_, err = b.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/alpha")})
	refused(t, h, err, codes.PermissionDenied, "PutTMessage: item.namespace is not in tenant bbbbbbbb")

	// No tenant named: refused before anything else is decided.
	_, err = b.PutTMessage(ctx, &types.PutTMessageRequest{Item: msg("m1", "/tenants/beta")})
	refused(t, h, err, codes.InvalidArgument, "route field target_tenant is empty")

	// Key-addressed requests carry the entry's tenant beside the key.
	_, err = b.DeleteTMessage(ctx, &types.DeleteTMessageRequest{TargetTenant: "bbbbbbbb", MessageId: "m1", Namespace: "/tenants/alpha"})
	refused(t, h, err, codes.PermissionDenied, "DeleteTMessage: namespace is not in tenant bbbbbbbb")
	_, err = b.GetTMessage(ctx, &types.GetTMessageRequest{TargetTenant: "bbbbbbbb", MessageId: "m1", Namespace: "/tenants/alpha"})
	refused(t, h, err, codes.PermissionDenied, "GetTMessage: namespace is not in tenant bbbbbbbb")
	_, err = b.GetTMessage(ctx, &types.GetTMessageRequest{TargetTenant: "aaaaaaaa", MessageId: "m1", Namespace: "/tenants/alpha"})
	refused(t, h, err, codes.PermissionDenied, "routed to tenant aaaaaaaa")

	// Apply: routed by its own route field, every op's row held to it.
	foreign := &types.RecordOp{Op: &types.RecordOp_PutTMessage{PutTMessage: &types.PutTMessageRequest{Item: msg("m1", "/tenants/alpha")}}}
	_, err = b.Apply(ctx, &types.RecordApplyRequest{TargetTenant: "bbbbbbbb", Ops: []*types.RecordOp{foreign}})
	refused(t, h, err, codes.PermissionDenied, "Apply (put_t_message): item.namespace is not in tenant bbbbbbbb")
	_, err = b.Apply(ctx, &types.RecordApplyRequest{TargetTenant: "aaaaaaaa", Ops: []*types.RecordOp{foreign}})
	refused(t, h, err, codes.PermissionDenied, "routed to tenant aaaaaaaa")

	// A caller of tenant aaaaaaaa writing its own row to B's instance.
	a := instanceOfB(t, h, "/tenants/alpha")
	_, err = a.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/alpha")})
	refused(t, h, err, codes.PermissionDenied, "not in tenant bbbbbbbb")
	_, err = a.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "aaaaaaaa", Item: msg("m1", "/tenants/alpha")})
	refused(t, h, err, codes.PermissionDenied, "routed to tenant aaaaaaaa")
}

func TestTenantRecordOwnTenantServed(t *testing.T) {
	ctx := context.Background()
	h := &routeHandler{}
	b := instanceOfB(t, h, "/system")
	if _, err := b.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/beta")}); err != nil {
		t.Fatalf("a row of the instance's own tenant, routed to it, must be served: %v", err)
	}
	if _, err := b.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m2", "/tenants/beta/users/u1")}); err != nil {
		t.Fatalf("a row below the instance's tenant must be served: %v", err)
	}
	own := &types.RecordOp{Op: &types.RecordOp_PutTMessage{PutTMessage: &types.PutTMessageRequest{Item: msg("m1", "/tenants/beta")}}}
	if _, err := b.Apply(ctx, &types.RecordApplyRequest{TargetTenant: "bbbbbbbb", Ops: []*types.RecordOp{own}}); err != nil {
		t.Fatalf("an Apply of the instance's own tenant must be served: %v", err)
	}
	if h.reached != 3 {
		t.Fatalf("want 3 calls served, the handler saw %d", h.reached)
	}

	// The tid is not the namespace: an entry spelled /tenants/bbbbbbbb is not
	// tenant beta's, though the call is routed to bbbbbbbb.
	_, err := b.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m3", "/tenants/bbbbbbbb")})
	refusedAfter(t, h, 3, err, codes.PermissionDenied, "item.namespace is not in tenant bbbbbbbb (/tenants/beta)")
}

// A tenant-owned caller (kernel#23: subject /tenants/<tid>/services/<svc>,
// namespace /tenants/<name>) is in the tenant of its namespace claim: its entry
// in /tenants/beta is served by beta's instance, and one it spells with its
// subject's tid, /tenants/bbbbbbbb, is not its tenant's. A caller whose subject
// names beta's tid under another tenant's namespace is held to that namespace,
// and refused.
func TestTenantRecordCallerTenantIsTheNamespaceClaim(t *testing.T) {
	ctx := context.Background()
	h := &routeHandler{}
	beta := instanceOfBFor(t, h, "/tenants/beta", "spiffe://example.com/tenants/bbbbbbbb/services/tmessaging")
	if _, err := beta.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/beta")}); err != nil {
		t.Fatalf("beta's own caller must be served by beta's instance: %v", err)
	}
	if h.lastNs != "/tenants/beta" {
		t.Fatalf("beta's caller's entry reached the handler in %q; want /tenants/beta", h.lastNs)
	}
	_, err := beta.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m2", "/tenants/bbbbbbbb")})
	refusedAfter(t, h, 1, err, codes.InvalidArgument, "names a tenant other than the caller's")

	h2 := &routeHandler{}
	alpha := instanceOfBFor(t, h2, "/tenants/alpha", "spiffe://example.com/tenants/bbbbbbbb/services/tmessaging")
	_, err = alpha.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/alpha")})
	refused(t, h2, err, codes.PermissionDenied, "item.namespace is not in tenant bbbbbbbb (/tenants/beta)")
	_, err = alpha.PutTMessage(ctx, &types.PutTMessageRequest{TargetTenant: "bbbbbbbb", Item: msg("m1", "/tenants/beta")})
	refused(t, h2, err, codes.InvalidArgument, "names a tenant other than the caller's")
}

func refusedAfter(t *testing.T, h *routeHandler, served int, err error, code codes.Code, substr string) {
	t.Helper()
	if status.Code(err) != code || !strings.Contains(status.Convert(err).Message(), substr) {
		t.Fatalf("want %v containing %q, got %v", code, substr, err)
	}
	if h.reached != served {
		t.Fatalf("a refused call reached the handler (the store and its redo): %d calls, want %d", h.reached, served)
	}
}
