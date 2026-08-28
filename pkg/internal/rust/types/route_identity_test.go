// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// A trait-hosted C2S surface routes to the physical host but entitles against
// the TRAIT. The audience is the contract, not the concrete service: pinning it
// to "stratum" would force every client to re-issue its token the day a second
// service implements the same trait.
func TestRouteIdentityForTraitHostedC2S(t *testing.T) {
	svc := ServiceInfo{
		PackageName: "taskquery",
		ServiceName: "QueryApi",
		IsC2S:       true,
		HostLabel:   "stratum",
	}
	serviceID, entitlementService, err := routeIdentityForService(svc, "query")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}
	if serviceID != "stratum" {
		t.Errorf("ServiceID = %q, want stratum (the physical host the gateway dials)", serviceID)
	}
	if entitlementService != `Some("query")` {
		t.Errorf(`EntitlementService = %q, want Some("query") (the trait name, not the host)`, entitlementService)
	}
	if strings.Contains(entitlementService, "stratum") {
		t.Errorf("EntitlementService must never be the host service: %q", entitlementService)
	}
	if strings.Contains(entitlementService, "taskquery") {
		t.Errorf("EntitlementService must be the trait name, not the proto package: %q", entitlementService)
	}
}

// An ordinary C2S service is its own host and its own audience: ServiceID is
// the proto package and there is no separate entitlement service.
func TestRouteIdentityForOrdinaryC2S(t *testing.T) {
	svc := ServiceInfo{PackageName: "identity", ServiceName: "UserApi", IsC2S: true}
	serviceID, entitlementService, err := routeIdentityForService(svc, "")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}
	if serviceID != "identity" {
		t.Errorf("ServiceID = %q, want identity", serviceID)
	}
	if entitlementService != "None" {
		t.Errorf("EntitlementService = %q, want None", entitlementService)
	}
}

// An event-labelled C2S surface is declared in the record owner's package but
// physically served by EventBus. The gateway must dial the event label while
// checking the caller's entitlement against the declared client contract.
func TestRouteIdentityForEventLabelledC2S(t *testing.T) {
	svc := ServiceInfo{
		PackageName: "tasks",
		ServiceName: "DelegationProbeBusStateService",
		IsC2S:       true,
		EventLabel:  "eventbus",
	}
	serviceID, entitlementService, err := routeIdentityForService(svc, "")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}
	if serviceID != "eventbus" {
		t.Errorf("ServiceID = %q, want eventbus (the physical broker the gateway dials)", serviceID)
	}
	if entitlementService != `Some("tasks")` {
		t.Errorf(`EntitlementService = %q, want Some("tasks") (the declared client contract)`, entitlementService)
	}
}

func TestRouteIdentityEventLabelTakesPrecedenceOverHostLabel(t *testing.T) {
	svc := ServiceInfo{
		PackageName: "tasks",
		ServiceName: "DelegationProbeBusStateService",
		IsC2S:       true,
		EventLabel:  "eventbus",
		HostLabel:   "stratum",
	}
	serviceID, entitlementService, err := routeIdentityForService(svc, "query")
	if err != nil {
		t.Fatalf("routeIdentityForService: %v", err)
	}
	if serviceID != "eventbus" || entitlementService != `Some("tasks")` {
		t.Fatalf("route identity = (%q, %q), want event label and package contract", serviceID, entitlementService)
	}
}

// A host label with no trait name has no audience: emitting Some("") would give
// the gateway an entitlement check against the empty string.
func TestRouteIdentityRejectsHostLabelWithoutTrait(t *testing.T) {
	svc := ServiceInfo{PackageName: "taskquery", ServiceName: "QueryApi", IsC2S: true, HostLabel: "stratum"}
	if _, _, err := routeIdentityForService(svc, ""); err == nil {
		t.Fatal("a host label with no trait name must fail generation")
	}
}
