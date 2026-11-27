// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's hub build (#475). NOT
// generated: run.sh copies this file into the generated
// s2sm/grpc/clients/services/crew package before `go test ./...`, so it drives
// the REAL generated typed client of crew.CrewPeerService (tenant-owned,
// routed by its "tenant" field) over a connection that records what reaches
// it.
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08): a product's service or
// workload never calls a tenant (PERMISSION_DENIED, nothing sent), whatever
// tenant it names; the platform names the tenant it calls; a tenant's service
// reaches its own tenant and no other (#258).

package crew

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"gitlab.com/example/dummy/hub/tacplus/model/chassis/serviceconnection"
	types "gitlab.com/example/dummy/hub/tacplus/model/types/services/crew"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// routedConn stands for the tenant-routed ChannelProvider: it records the
// tenant each call that reaches it is routed to.
type routedConn struct {
	tenants []string
}

func (r *routedConn) Invoke(ctx context.Context, _ string, _, _ any, _ ...grpc.CallOption) error {
	tid, _ := serviceconnection.RouteTenantFromContext(ctx)
	r.tenants = append(r.tenants, tid)
	return nil
}

func (r *routedConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams in this test")
}

// layerToken is an unsigned JWT with the claims of a caller in a layer: the
// client reads its own token and never verifies it; the target does.
func layerToken(t *testing.T, subType, subject, namespace, tid string) func() (string, error) {
	t.Helper()
	claims := map[string]string{"iss": "https://kernel.example", "sub_type": subType, "sub": subject, "namespace": namespace}
	if tid != "" {
		claims["tid"] = tid
	}
	part := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	token := part(map[string]string{"alg": "EdDSA", "typ": "JWT"}) + "." + part(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return func() (string, error) { return token, nil }
}

func TestProductLayerClientIntoATenant(t *testing.T) {
	for _, tc := range []struct {
		name, subType, sub, namespace, tid, named string
		want                                      string // the tenant it reaches; "" refused PERMISSION_DENIED
	}{
		{"product ledger's service", "service", "spiffe://example.com/products/ledger/services/billing", "/products/ledger", "", "bbbbbbbb", ""},
		{"product ledger's workload", "workload", "spiffe://example.com/products/ledger/services/billing/workloads/w-1", "/products/ledger", "", "bbbbbbbb", ""},
		{"the platform's service", "service", "spiffe://example.com/system/services/hub", "/system", "", "bbbbbbbb", "bbbbbbbb"},
		{"tenant alpha's service, its own tenant", "service", "spiffe://example.com/tenants/aaaaaaaa/services/crew", "/tenants/alpha", "aaaaaaaa", "", "aaaaaaaa"},
		{"tenant alpha's service, tenant bbbbbbbb", "service", "spiffe://example.com/tenants/aaaaaaaa/services/crew", "/tenants/alpha", "aaaaaaaa", "bbbbbbbb", ""},
	} {
		conn := &routedConn{}
		client := NewCrewPeerServiceClient(conn, layerToken(t, tc.subType, tc.sub, tc.namespace, tc.tid))
		_, err := client.GetCrewStatus(context.Background(), &types.GetCrewStatusRequest{Tenant: tc.named})
		if tc.want == "" {
			if status.Code(err) != codes.PermissionDenied || len(conn.tenants) != 0 {
				t.Fatalf("%s calling tenant-owned crew: want PERMISSION_DENIED and nothing sent, got %v and %v", tc.name, err, conn.tenants)
			}
			continue
		}
		if err != nil || len(conn.tenants) != 1 || conn.tenants[0] != tc.want {
			t.Fatalf("%s calling tenant-owned crew: want it sent to %s, got %v and %v", tc.name, tc.want, err, conn.tenants)
		}
	}
}
