// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestMarkTenantRoutedTargets(t *testing.T) {
	cfg := &tactype.Config{}
	crew := ServiceInfo{PackageName: "crew", ServiceName: "CrewService", Config: cfg}
	crewAdmin := ServiceInfo{PackageName: "crew", ServiceName: "CrewAdmin", Config: cfg}
	dock := ServiceInfo{PackageName: "dock", ServiceName: "DockService", Config: cfg}
	targets := CollectTargetServices([]ServiceInfo{crew, dock})

	got, err := markTenantRoutedTargets(targets, []ServiceInfo{crew, dock}, map[string]bool{"crew.CrewService": true})
	if err != nil {
		t.Fatalf("markTenantRoutedTargets: %v", err)
	}
	want := map[string]bool{"crew": true, "dock": false}
	for _, target := range got {
		if target.TenantRouted != want[target.ServiceID] {
			t.Fatalf("target %s TenantRouted = %v, want %v", target.ServiceID, target.TenantRouted, want[target.ServiceID])
		}
	}

	_, err = markTenantRoutedTargets(CollectTargetServices([]ServiceInfo{crew, crewAdmin}), []ServiceInfo{crew, crewAdmin}, map[string]bool{"crew.CrewService": true})
	if err == nil || !strings.Contains(err.Error(), `s2s target "crew" is reached by tenant-routed and platform services alike`) {
		t.Fatalf("a target mixing routed and platform services must fail generation, got %v", err)
	}
}
