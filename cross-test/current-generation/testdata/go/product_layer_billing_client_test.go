// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the product-layer fixture's billing build (#475). NOT
// generated: run.sh copies this file into the generated
// s2sm/grpc/clients/services/meter package before `go test ./...`, so it
// drives the REAL generated typed clients of meter.MeterService (product
// ledger's) and hub.HubService (the platform's) over a connection that
// records what reaches it.
//
// What it pins: the edge table of roadmap/PRODUCTS_LAYER.md at the client,
// before anything is sent (products-layer-2026-11-08). Into product ledger:
// its own services and a tenant's pass; another product's and the platform's
// are refused PERMISSION_DENIED and never sent. Into the platform: every layer
// passes.

package meter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	hubclient "gitlab.com/example/dummy/billing/tacplus/model/s2sm/grpc/clients/services/hub"
	hubtypes "gitlab.com/example/dummy/billing/tacplus/model/types/services/hub"
	types "gitlab.com/example/dummy/billing/tacplus/model/types/services/meter"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// countingConn stands for the connection: it counts the calls that reach it.
type countingConn struct {
	calls int
}

func (c *countingConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	c.calls++
	return nil
}

func (c *countingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
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

type layerCaller struct {
	name                         string
	subType, sub, namespace, tid string
}

var (
	ledgerService  = layerCaller{"product ledger's service", "service", "spiffe://example.com/products/ledger/services/billing", "/products/ledger", ""}
	ledgerWorkload = layerCaller{"product ledger's workload", "workload", "spiffe://example.com/products/ledger/services/billing/workloads/w-1", "/products/ledger", ""}
	booksService   = layerCaller{"product books' service", "service", "spiffe://example.com/products/books/services/audit", "/products/books", ""}
	systemService  = layerCaller{"the platform's service", "service", "spiffe://example.com/system/services/hub", "/system", ""}
	tenantService  = layerCaller{"tenant alpha's service", "service", "spiffe://example.com/tenants/aaaaaaaa/services/crew", "/tenants/alpha", "aaaaaaaa"}
)

func TestProductLayerClientIntoOwnProduct(t *testing.T) {
	for _, tc := range []struct {
		caller  layerCaller
		refused bool
	}{
		{ledgerService, false}, {ledgerWorkload, false}, {tenantService, false},
		{booksService, true}, {systemService, true},
	} {
		conn := &countingConn{}
		client := NewMeterServiceClient(conn, layerToken(t, tc.caller.subType, tc.caller.sub, tc.caller.namespace, tc.caller.tid))
		_, err := client.GetMeterReading(context.Background(), &types.GetMeterReadingRequest{})
		if tc.refused {
			if status.Code(err) != codes.PermissionDenied || conn.calls != 0 {
				t.Fatalf("%s calling product ledger's meter: want PERMISSION_DENIED and nothing sent, got %v and %d call(s)", tc.caller.name, err, conn.calls)
			}
			continue
		}
		if err != nil || conn.calls != 1 {
			t.Fatalf("%s calling product ledger's meter: want it sent, got %v and %d call(s)", tc.caller.name, err, conn.calls)
		}
	}
}

func TestProductLayerClientIntoThePlatform(t *testing.T) {
	for _, caller := range []layerCaller{ledgerService, booksService, systemService, tenantService} {
		conn := &countingConn{}
		client := hubclient.NewHubServiceClient(conn, layerToken(t, caller.subType, caller.sub, caller.namespace, caller.tid))
		if _, err := client.GetHubStatus(context.Background(), &hubtypes.GetHubStatusRequest{}); err != nil || conn.calls != 1 {
			t.Fatalf("%s calling the platform's hub: want it sent, got %v and %d call(s)", caller.name, err, conn.calls)
		}
	}
}
