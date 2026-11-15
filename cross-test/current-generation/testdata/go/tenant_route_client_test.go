// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-route fixture's harbor build (#258). NOT
// generated: run.sh copies this file into the generated
// s2sm/grpc/clients/services/crew package before `go test ./...`, so it drives
// the REAL generated typed CrewPeerService client over a connection that
// records what reaches it.
//
// What it pins: S2S never crosses tenants (roadmap
// decisions/s2s-never-crosses-tenants-2026-10-31.md). A service of tenant
// alpha (tid aaaaaaaa) calling with its own service token is refused
// PERMISSION_DENIED, before anything is sent, when the call names tenant
// bbbbbbbb, and a call naming no tenant goes to aaaaaaaa with the route field
// stamped. A /system service names bbbbbbbb and reaches it.

package crew

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"gitlab.com/example/dummy/harbor/tacplus/model/chassis/serviceconnection"
	types "gitlab.com/example/dummy/harbor/tacplus/model/types/services/crew"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sent is one call that reached the connection.
type sent struct {
	routeTenant string
	field       string
}

// recordingConn stands for the tenant-routed ChannelProvider: it records the
// tenant each call is routed to and the route field the request carries.
type recordingConn struct {
	calls []sent
}

func (r *recordingConn) Invoke(ctx context.Context, _ string, args, _ any, _ ...grpc.CallOption) error {
	tid, _ := serviceconnection.RouteTenantFromContext(ctx)
	r.calls = append(r.calls, sent{routeTenant: tid, field: args.(*types.GetCrewStatusRequest).GetTenant()})
	return nil
}

func (r *recordingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "no streams in this test")
}

// serviceToken is an unsigned JWT with claims: the client reads its own token
// and never verifies it; the target does.
func serviceToken(t *testing.T, claims map[string]string) func() (string, error) {
	t.Helper()
	claims["iss"] = "https://kernel.example"
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

func TestTenantServiceClientNeverCrossesTenants(t *testing.T) {
	alpha := serviceToken(t, map[string]string{"sub_type": "service", "namespace": "/tenants/alpha", "tid": "aaaaaaaa", "aud": "crew"})

	conn := &recordingConn{}
	client := NewCrewPeerServiceClient(conn, alpha)
	_, err := client.GetCrewStatus(context.Background(), &types.GetCrewStatusRequest{Tenant: "bbbbbbbb"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("tenant alpha's service calling tenant bbbbbbbb: want PERMISSION_DENIED, got %v", err)
	}
	if len(conn.calls) != 0 {
		t.Fatalf("a call refused for crossing tenants must never be sent, got %+v", conn.calls)
	}

	req := &types.GetCrewStatusRequest{}
	if _, err := client.GetCrewStatus(context.Background(), req); err != nil {
		t.Fatalf("tenant alpha's service calling its own tenant: %v", err)
	}
	if len(conn.calls) != 1 || conn.calls[0] != (sent{routeTenant: "aaaaaaaa", field: "aaaaaaaa"}) {
		t.Fatalf("a call naming no tenant must go to the caller's own tid with the route field stamped, got %+v", conn.calls)
	}
	if req.GetTenant() != "aaaaaaaa" {
		t.Fatalf("the request's route field must carry the caller's own tid, got %q", req.GetTenant())
	}
}

func TestSystemServiceClientNamesTheTargetTenant(t *testing.T) {
	system := serviceToken(t, map[string]string{"sub_type": "service", "namespace": "/system", "aud": "crew"})

	conn := &recordingConn{}
	client := NewCrewPeerServiceClient(conn, system)
	if _, err := client.GetCrewStatus(context.Background(), &types.GetCrewStatusRequest{Tenant: "bbbbbbbb"}); err != nil {
		t.Fatalf("a /system service naming tenant bbbbbbbb: %v", err)
	}
	if len(conn.calls) != 1 || conn.calls[0] != (sent{routeTenant: "bbbbbbbb", field: "bbbbbbbb"}) {
		t.Fatalf("a /system service's call must go to the tenant it names, got %+v", conn.calls)
	}
	_, err := client.GetCrewStatus(context.Background(), &types.GetCrewStatusRequest{})
	if status.Code(err) != codes.InvalidArgument || len(conn.calls) != 1 {
		t.Fatalf("a /system service naming no tenant: want INVALID_ARGUMENT and nothing sent, got %v, %+v", err, conn.calls)
	}
}
