// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// pinned_tenant narrows a tenant-owned service to one tenant. On a shared
// service there is no tenant loop to narrow: the operator only branches on the
// pin inside its tenant_owned arm, so the declaration would sit in the proto
// doing nothing. Refuse it at the source instead.
func TestValidatePinnedTenant_RejectsPinWithoutTenantOwned(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package pindemo;

import "sys/spec.proto";

option (spec.service) = {name: "pindemo", pinned_tenant: "people"};

message PingRequest {}
message PingResponse {}

service Pin {
  option (spec.service_s2s) = {auth_services: ["tasks"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := ValidatePinnedTenant(dp)
	if err == nil {
		t.Fatal("ValidatePinnedTenant accepted pinned_tenant without tenant_owned")
	}
	for _, want := range []string{"pindemo", "pinned_tenant", "people", "tenant_owned"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
}

// The declared shape: pinned_tenant alongside tenant_owned = true, which is
// what a fleet-singleton service resident in a reserved tenant carries.
func TestValidatePinnedTenant_AcceptsPinWithTenantOwned(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package pindemo;

import "sys/spec.proto";

option (spec.service) = {name: "pindemo", tenant_owned: true, pinned_tenant: "people"};

message PingRequest {}
message PingResponse {}

service Pin {
  option (spec.service_s2s) = {auth_services: ["tasks"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	if err := ValidatePinnedTenant(dp); err != nil {
		t.Fatalf("ValidatePinnedTenant rejected tenant_owned + pinned_tenant: %v", err)
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != "pindemo" {
			continue
		}
		if fd.ServicePinnedTenant != "people" {
			t.Fatalf("ServicePinnedTenant = %q, want \"people\"", fd.ServicePinnedTenant)
		}
	}
}
