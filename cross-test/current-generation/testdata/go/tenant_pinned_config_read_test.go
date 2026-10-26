// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-slice fixture's grpc_config owner (issue #135).
// NOT generated: run.sh copies this file into the generated
// TenancyConfigStateService wrapper package of the tenancy build before
// `go test ./...`.
//
// tenancy's Config holds each tenant's generated TenantConfig in its `tenants`
// map, and ledger owns a slice of it, so ledger holds a TENANT-PINNED read of
// the Config. Before #135 ledger was not an allowed caller of the service at
// all (its Watch was refused outright) and nothing filtered the map. This
// drives the real generated wrapper — its authorize seam stubbed to hand back a
// chosen verified token — over a handler holding tenants x and y, each carrying
// ledger's slice AND registrar's secret slice, and pins what the serving side
// must do on GetConfig and WatchConfig:
//   - ledger in /tenants/x gets x's entry only, projected to ledger's slice
//     (registrar's client_secret never leaves), and none of the Config's
//     other fields;
//   - ledger in /system gets every tenant, each projected;
//   - ledger in any other namespace is PermissionDenied;
//   - the owner (tenancy) is not pinned and reads the Config unchanged.
// With the filter removed from the generator, the projection and the
// one-tenant assertions fail.

package tenancy

import (
	"context"
	"sort"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitlab.com/example/dummy/tenancy/tacplus/model/chassis/servicemanager"
	tokenlib "gitlab.com/example/dummy/tenancy/tacplus/model/chassis/token"
	tenantconfigpb "gitlab.com/example/dummy/tenancy/tacplus/model/types/generated/tenant_config"
	registrarslicepb "gitlab.com/example/dummy/tenancy/tacplus/model/types/platform/registrarslice"
	ledgerpb "gitlab.com/example/dummy/tenancy/tacplus/model/types/services/ledger"
	types "gitlab.com/example/dummy/tenancy/tacplus/model/types/services/tenancy"
	trace_pb "gitlab.com/example/dummy/tenancy/tacplus/model/types/sys/trace"
)

func pinTenant(tenant string) *tenantconfigpb.TenantConfig {
	return &tenantconfigpb.TenantConfig{
		Ledger:    &ledgerpb.LedgerTenantConfig{Currency: "cur-" + tenant},
		Registrar: &registrarslicepb.RegistrarTenantConfig{ClientSecret: "secret-" + tenant},
	}
}

func pinConfig() *types.Config {
	return &types.Config{
		Tenants: map[string]*tenantconfigpb.TenantConfig{"x": pinTenant("x"), "y": pinTenant("y")},
		Region:  "owner-region",
		TraceId: "owner-only",
	}
}

// pinHandler serves tenants x and y. Methods it does not override panic.
type pinHandler struct {
	TenancyConfigStateServiceHandler
}

func (pinHandler) GetConfig(context.Context, *servicemanager.Caller) (*types.Config, error) {
	return pinConfig(), nil
}

func (pinHandler) WatchConfig(_ *servicemanager.Caller, stream grpc.ServerStreamingServer[types.Config]) error {
	return stream.Send(pinConfig())
}

func (pinHandler) SetConfig(context.Context, *servicemanager.Caller, *types.Config) (*types.Config, error) {
	return pinConfig(), nil
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
func serviceAs(svc, ns string) *TenancyConfigStateService {
	w := NewTenancyConfigStateService(pinHandler{})
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

func sortedTenants(c *types.Config) []string {
	keys := make([]string, 0, len(c.GetTenants()))
	for k := range c.GetTenants() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// wantProjected asserts c carries exactly the tenants in want, each holding
// ledger's slice and nothing else, and none of the Config's other fields.
func wantProjected(t *testing.T, what string, c *types.Config, want ...string) {
	t.Helper()
	got := sortedTenants(c)
	if len(got) != len(want) {
		t.Fatalf("%s: tenants %v, want %v", what, got, want)
	}
	for i, tenant := range want {
		if got[i] != tenant {
			t.Fatalf("%s: tenants %v, want %v", what, got, want)
		}
		e := c.GetTenants()[tenant]
		if cur := e.GetLedger().GetCurrency(); cur != "cur-"+tenant {
			t.Fatalf("%s: tenant %s ledger slice = %q, want the caller's own slice", what, tenant, cur)
		}
		if e.GetRegistrar() != nil {
			t.Fatalf("%s: another service's slice (registrar client_secret %q) reached a pinned caller",
				what, e.GetRegistrar().GetClientSecret())
		}
	}
	if c.GetTraceId() != "" || c.GetRegion() != "" {
		t.Fatalf("%s: the Config's own fields (region %q, trace_id %q) reached a pinned caller", what, c.GetRegion(), c.GetTraceId())
	}
}

func TestTenantPinnedConfigRead_TenantCallerSeesOnlyItsOwnProjectedEntry(t *testing.T) {
	w := serviceAs("ledger", "/tenants/x")

	got, err := w.GetConfig(context.Background(), &trace_pb.TraceRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	wantProjected(t, "GetConfig /tenants/x", got, "x")

	ws := &pinStream[types.Config]{}
	if err := w.WatchConfig(&trace_pb.TraceRequest{}, ws); err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}
	if len(ws.sent) != 1 {
		t.Fatalf("WatchConfig: %d frames, want 1", len(ws.sent))
	}
	wantProjected(t, "WatchConfig /tenants/x", ws.sent[0], "x")
}

func TestTenantPinnedConfigRead_SystemCallerSeesEveryTenantProjected(t *testing.T) {
	w := serviceAs("ledger", "/system")
	got, err := w.GetConfig(context.Background(), &trace_pb.TraceRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	wantProjected(t, "GetConfig /system", got, "x", "y")

	ws := &pinStream[types.Config]{}
	if err := w.WatchConfig(&trace_pb.TraceRequest{}, ws); err != nil {
		t.Fatalf("WatchConfig: %v", err)
	}
	wantProjected(t, "WatchConfig /system", ws.sent[0], "x", "y")
}

func TestTenantPinnedConfigRead_OtherNamespacesGetNothing(t *testing.T) {
	for _, ns := range []string{"/tenants/x/workspaces/w", "/people", "/", "/tenants/"} {
		w := serviceAs("ledger", ns)
		_, err := w.GetConfig(context.Background(), &trace_pb.TraceRequest{})
		wantDenied(t, "GetConfig from "+ns, err)
		wantDenied(t, "WatchConfig from "+ns, w.WatchConfig(&trace_pb.TraceRequest{}, &pinStream[types.Config]{}))
	}
}

func TestTenantPinnedConfigRead_OwnerIsNotPinned(t *testing.T) {
	got, err := serviceAs("tenancy", "/system").GetConfig(context.Background(), &trace_pb.TraceRequest{})
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if len(got.GetTenants()) != 2 || got.GetTenants()["y"].GetRegistrar().GetClientSecret() != "secret-y" || got.GetRegion() != "owner-region" {
		t.Fatal("the owner's read must be unchanged: every tenant, unprojected, with the Config's other fields")
	}
}
