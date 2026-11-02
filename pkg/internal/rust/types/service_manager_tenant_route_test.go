// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func renderTenantRouteSM(t *testing.T) string {
	t.Helper()
	return renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:          "skipper",
		StateStoreServiceID:   "statestore",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasS2SGrpc:            true,
		HasServiceHealthState: true,
		HasS2SClients:         true,
		TargetServices: []TargetServiceInfo{
			{ServiceID: "crew", AccessorName: "crew", TenantRouted: true},
			{ServiceID: "identity", AccessorName: "identity"},
			{ServiceID: "statestore", AccessorName: "statestore"},
		},
		GrpcServices: []grpcServiceEntry{
			{WrapperModPath: "crate::s2sm::crew", WrapperVar: "wrapper_crew", TraitName: "CrewService", IsS2S: true, TenantRouted: true},
			{WrapperModPath: "crate::s2sm::dock", WrapperVar: "wrapper_dock", TraitName: "DockService", IsS2S: true},
		},
	})
}

// Issue #170: an S2S target that is a tenant-owned service is one pool per
// tenant, fed by the same health poller; a platform target keeps its single
// dynamic pool.
func TestRustServiceManagerPoolsTenantRoutedTargetsPerTenant(t *testing.T) {
	got := renderTenantRouteSM(t)
	for _, want := range []string{
		"let crew_sc = Arc::new(ServiceConnection::new_tenant_routed(\n            \"crew\",",
		"let identity_sc = Arc::new(ServiceConnection::new_dynamic(\n            \"identity\",",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("service manager missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ServiceConnection::new_tenant_routed(\n            \"identity\"") {
		t.Fatalf("a platform target must not be tenant-routed")
	}
}

// A routed service's wrapper is told its own instance's identity (the
// authorizer's host identity: the tid of its subject and its namespace claim),
// so its backstop refuses a request routed to another tenant and its record
// plane holds entries to the instance's namespace; a platform service's
// wrapper has no such seam.
func TestRustServiceManagerTellsRoutedWrappersTheirTenant(t *testing.T) {
	got := renderTenantRouteSM(t)
	if !strings.Contains(got, "wrapper_crew.set_host_identity(self.authorizer.host_identity());") {
		t.Fatalf("routed wrapper must be told its instance's identity:\n%s", got)
	}
	if strings.Contains(got, "wrapper_dock.set_host_identity(") {
		t.Fatalf("platform wrapper has no host identity seam")
	}
}

// The wrapper's authorizer checks name their rpc, and the adapter passes it on
// so the tenant door follows RPC_FLAG_ANY_TENANT on every path.
func TestRustServiceManagerAdapterPassesTheRpcToTheDoor(t *testing.T) {
	got := renderTenantRouteSM(t)
	for _, want := range []string{
		"async fn check_at_or_below(&self, full_method: &str, authorization: Option<&str>, target_namespace: &str)",
		".check_caller_at_or_below_method(full_method, authorization, target_namespace)",
		"async fn authenticate(&self, full_method: &str, authorization: Option<&str>)",
		".authenticate_caller_method(full_method, authorization)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("authorizer adapter missing %q:\n%s", want, got)
		}
	}
}
