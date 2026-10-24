// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-slice fixture's service_state owner (issue
// #130). NOT generated: run.sh copies this file into the generated
// RegistrarStateService wrapper package of the registrar build before
// `go test ./...`.
//
// registrar.RegistrarState.tenant_configs holds each tenant's generated
// TenantConfig, and ledger owns a slice of it, so ledger holds a TENANT-PINNED
// read of that one collection. This drives the real generated wrapper — its
// authorize seam stubbed to hand back a chosen verified token — over a handler
// holding tenants x and y, each carrying ledger's slice AND registrar's secret
// slice, and pins what the serving side must do:
//   - ledger in /tenants/x gets x's entry only, on every read RPC, projected to
//     the key and ledger's slice (registrar's client_secret never leaves);
//   - a Get/ContainsKey of y from /tenants/x is PermissionDenied;
//   - ledger in /system gets every tenant, each projected;
//   - ledger in any other namespace is PermissionDenied;
//   - the owner (registrar) is not pinned and reads the entries unchanged.
// With the filter removed from the generator, the projection and the
// one-tenant assertions fail.

package registrar

import (
	"context"
	"sort"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitlab.com/example/dummy/registrar/tacplus/model/chassis/servicemanager"
	tokenlib "gitlab.com/example/dummy/registrar/tacplus/model/chassis/token"
	tenantconfigpb "gitlab.com/example/dummy/registrar/tacplus/model/types/generated/tenant_config"
	registrarslicepb "gitlab.com/example/dummy/registrar/tacplus/model/types/platform/registrarslice"
	ledgerpb "gitlab.com/example/dummy/registrar/tacplus/model/types/services/ledger"
	types "gitlab.com/example/dummy/registrar/tacplus/model/types/services/registrar"
	trace_pb "gitlab.com/example/dummy/registrar/tacplus/model/types/sys/trace"
)

func pinEntry(tenant string) *types.TenantConfigEntry {
	return &types.TenantConfigEntry{
		TenantName: tenant,
		Config: &tenantconfigpb.TenantConfig{
			Ledger:    &ledgerpb.LedgerTenantConfig{Currency: "cur-" + tenant},
			Registrar: &registrarslicepb.RegistrarTenantConfig{ClientSecret: "secret-" + tenant},
		},
	}
}

// pinHandler serves tenants x and y. Methods it does not override panic.
type pinHandler struct {
	RegistrarStateServiceHandler
}

func (pinHandler) all() map[string]*types.TenantConfigEntry {
	return map[string]*types.TenantConfigEntry{"x": pinEntry("x"), "y": pinEntry("y")}
}

func (h pinHandler) GetTenantConfigs(_ context.Context, _ *servicemanager.Caller, req *types.TenantConfigsGetRequest) (*types.TenantConfigsValueResponse, error) {
	v, ok := h.all()[req.GetKey()]
	return &types.TenantConfigsValueResponse{Value: v, Found: ok}, nil
}

func (h pinHandler) ContainsKeyTenantConfigs(ctx context.Context, c *servicemanager.Caller, req *types.TenantConfigsGetRequest) (*types.TenantConfigsValueResponse, error) {
	return h.GetTenantConfigs(ctx, c, req)
}

func (h pinHandler) GetAllTenantConfigs(context.Context, *servicemanager.Caller, *types.TenantConfigsGetAllRequest) (*types.TenantConfigsEntriesResponse, error) {
	return &types.TenantConfigsEntriesResponse{Entries: h.all()}, nil
}

func (h pinHandler) KeySetTenantConfigs(context.Context, *servicemanager.Caller, *types.TenantConfigsKeysRequest) (*types.TenantConfigsKeysResponse, error) {
	return &types.TenantConfigsKeysResponse{Keys: []string{"x", "y"}}, nil
}

func (h pinHandler) SizeTenantConfigs(context.Context, *servicemanager.Caller, *types.TenantConfigsSizeRequest) (*types.TenantConfigsSizeResponse, error) {
	return &types.TenantConfigsSizeResponse{Size: 2}, nil
}

func (h pinHandler) QueryTenantConfigs(context.Context, *servicemanager.Caller, *types.TenantConfigsQueryRequest) (*types.TenantConfigsQueryResponse, error) {
	return &types.TenantConfigsQueryResponse{Results: []*types.TenantConfigEntry{pinEntry("x"), pinEntry("y")}, TotalCount: 2}, nil
}

func (h pinHandler) WatchTenantConfigs(_ *servicemanager.Caller, stream grpc.ServerStreamingServer[types.TenantConfigsEntriesResponse]) error {
	return stream.Send(&types.TenantConfigsEntriesResponse{Entries: h.all()})
}

func (h pinHandler) WatchQueryTenantConfigs(_ *servicemanager.Caller, _ *types.TenantConfigsQueryRequest, stream grpc.ServerStreamingServer[types.TenantConfigsQueryResponse]) error {
	return stream.Send(&types.TenantConfigsQueryResponse{Results: []*types.TenantConfigEntry{pinEntry("x"), pinEntry("y")}, TotalCount: 2})
}

// pinStream records what the wrapper sends.
type pinStream[T any] struct {
	grpc.ServerStream
	sent []*T
}

func (s *pinStream[T]) Context() context.Context { return context.Background() }
func (s *pinStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }

// serviceAs is the wrapper with its authorize seam handing back a verified
// service token for svc in namespace ns.
func serviceAs(svc, ns string) *RegistrarStateService {
	w := NewRegistrarStateService(pinHandler{})
	w.SetAuthorizeFunc(func(context.Context, string, any) (*tokenlib.TokenWrapper, error) {
		return tokenlib.NewImmutableToken(nil).
			SetSubType("service").
			SetSubject("spiffe://example.com" + ns + "/services/" + svc).
			SetNamespace(ns), nil
	})
	return w
}

func wantDenied(t *testing.T, what string, err error) {
	t.Helper()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("%s: want PermissionDenied, got %v", what, err)
	}
}

// wantProjected asserts e is tenant's entry carrying ledger's slice and nothing else.
func wantProjected(t *testing.T, what, tenant string, e *types.TenantConfigEntry) {
	t.Helper()
	if e.GetTenantName() != tenant {
		t.Fatalf("%s: tenant = %q, want %q", what, e.GetTenantName(), tenant)
	}
	if got := e.GetConfig().GetLedger().GetCurrency(); got != "cur-"+tenant {
		t.Fatalf("%s: ledger slice = %q, want the caller's own slice", what, got)
	}
	if e.GetConfig().GetRegistrar() != nil {
		t.Fatalf("%s: another service's slice (registrar client_secret %q) reached a pinned caller",
			what, e.GetConfig().GetRegistrar().GetClientSecret())
	}
}

func sortedKeys(m map[string]*types.TenantConfigEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestTenantPinnedRead_TenantCallerSeesOnlyItsOwnProjectedEntry(t *testing.T) {
	ctx := context.Background()
	w := serviceAs("ledger", "/tenants/x")

	all, err := w.GetAllTenantConfigs(ctx, &types.TenantConfigsGetAllRequest{})
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if keys := sortedKeys(all.GetEntries()); len(keys) != 1 || keys[0] != "x" {
		t.Fatalf("GetAll: tenant x was served keys %v, want only [x]", keys)
	}
	wantProjected(t, "GetAll", "x", all.GetEntries()["x"])

	got, err := w.GetTenantConfigs(ctx, &types.TenantConfigsGetRequest{Key: "x"})
	if err != nil {
		t.Fatalf("Get x: %v", err)
	}
	wantProjected(t, "Get", "x", got.GetValue())

	keys, err := w.KeySetTenantConfigs(ctx, &types.TenantConfigsKeysRequest{})
	if err != nil || len(keys.GetKeys()) != 1 || keys.GetKeys()[0] != "x" {
		t.Fatalf("KeySet: %v (%v), want [x]", keys.GetKeys(), err)
	}
	size, err := w.SizeTenantConfigs(ctx, &types.TenantConfigsSizeRequest{})
	if err != nil || size.GetSize() != 1 {
		t.Fatalf("Size: %d (%v), want 1", size.GetSize(), err)
	}
	q, err := w.QueryTenantConfigs(ctx, &types.TenantConfigsQueryRequest{})
	if err != nil || len(q.GetResults()) != 1 || q.GetTotalCount() != 1 {
		t.Fatalf("Query: %d results, total %d (%v), want 1 and 1", len(q.GetResults()), q.GetTotalCount(), err)
	}
	wantProjected(t, "Query", "x", q.GetResults()[0])

	ws := &pinStream[types.TenantConfigsEntriesResponse]{}
	if err := w.WatchTenantConfigs(&trace_pb.TraceRequest{}, ws); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if len(ws.sent) != 1 || len(ws.sent[0].GetEntries()) != 1 {
		t.Fatalf("Watch: frame carried %v, want only x", sortedKeys(ws.sent[0].GetEntries()))
	}
	wantProjected(t, "Watch", "x", ws.sent[0].GetEntries()["x"])

	wq := &pinStream[types.TenantConfigsQueryResponse]{}
	if err := w.WatchQueryTenantConfigs(&types.TenantConfigsQueryRequest{}, wq); err != nil {
		t.Fatalf("WatchQuery: %v", err)
	}
	if len(wq.sent) != 1 || len(wq.sent[0].GetResults()) != 1 {
		t.Fatalf("WatchQuery: frame carried %d results, want only x", len(wq.sent[0].GetResults()))
	}
	wantProjected(t, "WatchQuery", "x", wq.sent[0].GetResults()[0])
}

func TestTenantPinnedRead_CrossTenantKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	w := serviceAs("ledger", "/tenants/x")
	_, err := w.GetTenantConfigs(ctx, &types.TenantConfigsGetRequest{Key: "y"})
	wantDenied(t, "Get y from /tenants/x", err)
	_, err = w.ContainsKeyTenantConfigs(ctx, &types.TenantConfigsGetRequest{Key: "y"})
	wantDenied(t, "ContainsKey y from /tenants/x", err)
}

func TestTenantPinnedRead_SystemCallerSeesEveryTenantProjected(t *testing.T) {
	all, err := serviceAs("ledger", "/system").GetAllTenantConfigs(context.Background(), &types.TenantConfigsGetAllRequest{})
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if keys := sortedKeys(all.GetEntries()); len(keys) != 2 {
		t.Fatalf("GetAll from /system: keys %v, want [x y]", keys)
	}
	for _, tenant := range []string{"x", "y"} {
		wantProjected(t, "GetAll /system", tenant, all.GetEntries()[tenant])
	}
}

func TestTenantPinnedRead_OtherNamespacesGetNothing(t *testing.T) {
	for _, ns := range []string{"/tenants/x/workspaces/w", "/people", "/", "/tenants/"} {
		_, err := serviceAs("ledger", ns).GetAllTenantConfigs(context.Background(), &types.TenantConfigsGetAllRequest{})
		wantDenied(t, "GetAll from "+ns, err)
	}
}

func TestTenantPinnedRead_OwnerIsNotPinned(t *testing.T) {
	all, err := serviceAs("registrar", "/system").GetAllTenantConfigs(context.Background(), &types.TenantConfigsGetAllRequest{})
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all.GetEntries()) != 2 || all.GetEntries()["y"].GetConfig().GetRegistrar().GetClientSecret() != "secret-y" {
		t.Fatal("the owner's read must be unchanged: every entry, unprojected")
	}
}
