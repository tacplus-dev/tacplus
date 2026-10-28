// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// tenantRouteProto is a tenant-owned service whose rpcs carry the given
// (spec.rpc) bodies, Ping first and Pong second.
func tenantRouteProto(serviceOption, pingRpc, pongRpc string) string {
	return `syntax = "proto3";

package crew;

import "sys/spec.proto";

option (spec.service) = {name: "crew", ` + serviceOption + `};

message PingRequest {
  string tenant = 1;
  repeated string tenants = 2;
  int32 number = 3;
  optional string maybe = 4;
  Inner inner = 5;
}
message Inner { string tenant = 1; }
message PingResponse {}

service Crew {
  option (spec.service_c2s) = {auth_entitled_users: true};

  rpc Ping(PingRequest) returns (PingResponse) {
    option (spec.rpc) = {` + pingRpc + `};
  }
  rpc Pong(PingRequest) returns (PingResponse) {
    option (spec.rpc) = {` + pongRpc + `};
  }
}
`
}

const userRpc = `principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL`

// The declared shape: every rpc of a tenant-owned service names its route
// field, and the parser resolves its JSON name and wire number once.
func TestValidateTenantRouting_AcceptsEveryRpcRouted(t *testing.T) {
	dp := writeSurfaceProtoDir(t, tenantRouteProto("tenant_owned: true",
		userRpc+`, route_field: "tenant"`,
		userRpc+`, route_field: "tenant", flags: [RPC_FLAG_ANY_TENANT]`))
	if err := ValidateTenantRouting(dp); err != nil {
		t.Fatalf("ValidateTenantRouting rejected a fully routed tenant-owned service: %v", err)
	}
	m := findMethod(t, dp, "Crew", "Ping")
	if m.RouteField == nil || m.RouteField.Field != "tenant" || m.RouteField.JSONName != "tenant" || m.RouteField.Number != 1 {
		t.Fatalf("Ping RouteField = %+v, want tenant/tenant/1", m.RouteField)
	}
	if m.AnyTenant {
		t.Fatal("Ping without RPC_FLAG_ANY_TENANT parsed as AnyTenant")
	}
	if !findMethod(t, dp, "Crew", "Pong").AnyTenant {
		t.Fatal("Pong with RPC_FLAG_ANY_TENANT did not parse as AnyTenant")
	}
}

// Required: a tenant-owned rpc without a route field is refused at generation,
// never defaulted from the caller's token.
func TestValidateTenantRouting_RefusesMissingRouteField(t *testing.T) {
	dp := writeSurfaceProtoDir(t, tenantRouteProto("tenant_owned: true",
		userRpc+`, route_field: "tenant"`,
		userRpc))
	err := ValidateTenantRouting(dp)
	if err == nil {
		t.Fatal("ValidateTenantRouting accepted a tenant-owned rpc with no route_field")
	}
	for _, want := range []string{"Crew.Pong", "route_field", "tenant_owned"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "Crew.Ping") {
		t.Fatalf("error names the routed rpc Ping: %v", err)
	}
}

// A platform service is never addressed by tenant: a route field there, or the
// any-tenant door policy, is refused rather than silently ignored.
func TestValidateTenantRouting_RefusesRouteFieldOnPlatformService(t *testing.T) {
	dp := writeSurfaceProtoDir(t, tenantRouteProto("store: false",
		userRpc+`, route_field: "tenant"`,
		userRpc+`, flags: [RPC_FLAG_ANY_TENANT]`))
	err := ValidateTenantRouting(dp)
	if err == nil {
		t.Fatal("ValidateTenantRouting accepted route_field / RPC_FLAG_ANY_TENANT on a platform service")
	}
	for _, want := range []string{"Crew.Ping", "Crew.Pong", "route_field", "RPC_FLAG_ANY_TENANT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
}

// A platform service with no route field is unaffected.
func TestValidateTenantRouting_PlatformServiceUnaffected(t *testing.T) {
	dp := writeSurfaceProtoDir(t, tenantRouteProto("store: false", userRpc, userRpc))
	if err := ValidateTenantRouting(dp); err != nil {
		t.Fatalf("ValidateTenantRouting rejected a platform service: %v", err)
	}
	if m := findMethod(t, dp, "Crew", "Ping"); m.RouteField != nil {
		t.Fatalf("platform rpc has RouteField %+v", m.RouteField)
	}
}

// The field must be one plain string the router can read without guessing.
func TestRouteField_RefusesUnroutableShapes(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  string
	}{
		{"missing", "no top-level field"},
		{"tenants", "repeated"},
		{"number", "string"},
		{"maybe", "optional"},
		{"inner", "string"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			_, err := tryWriteSurfaceProtoDir(t, tenantRouteProto("tenant_owned: true",
				userRpc+`, route_field: "`+tc.field+`"`,
				userRpc+`, route_field: "tenant"`))
			if err == nil {
				t.Fatalf("route_field %q accepted", tc.field)
			}
			if !strings.Contains(err.Error(), "route_field") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("route_field %q error %q does not say %q", tc.field, err.Error(), tc.want)
			}
		})
	}
}

func findMethod(t *testing.T, dp *DescriptorParser, service, method string) *MethodDescriptor {
	t.Helper()
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			if fd.Services[si].Name != service {
				continue
			}
			for mi := range fd.Services[si].Methods {
				if fd.Services[si].Methods[mi].Name == method {
					return &fd.Services[si].Methods[mi]
				}
			}
		}
	}
	t.Fatalf("method %s.%s not found", service, method)
	return nil
}
