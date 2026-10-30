// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
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

// Issue #207: the record plane on a tenant-owned record store is routed by
// tenant, but the flat proto tree the ServiceManager scans is read before the
// plane is injected into its trait's package. Both halves must still be marked
// routed, so the plane's S2S target is pooled per tenant and the host tells
// the plane's wrappers which tenant their instance serves.
func TestRustTenantRoutedServicesIncludesTheRecordPlane(t *testing.T) {
	flat := filepath.Join(t.TempDir(), "flat")
	if err := os.CopyFS(flat, os.DirFS(filepath.Join("..", "..", "..", "..", "cross-test", "proto-tenantrecord"))); err != nil {
		t.Fatal(err)
	}
	if err := protogen.CopySysProtos(flat); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(flat, flat); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	routed, err := rustTenantRoutedServices(flat)
	if err != nil {
		t.Fatalf("rustTenantRoutedServices: %v", err)
	}
	for _, want := range []string{"tquery.TenantRecordMap", "tquery.TenantRecordQuery", "tstratum.TStratum"} {
		if !routed[want] {
			t.Fatalf("%s must be tenant-routed; got %v", want, routed)
		}
	}
	if routed["tmessaging.TMessaging"] {
		t.Fatalf("a platform service must not be tenant-routed")
	}

	cfg := &tactype.Config{}
	plane := ServiceInfo{PackageName: "tquery", ServiceName: "TenantRecordMap", HostLabel: "tstratum", Config: cfg}
	got, err := markTenantRoutedTargets(CollectTargetServices([]ServiceInfo{plane}), []ServiceInfo{plane}, routed)
	if err != nil {
		t.Fatalf("markTenantRoutedTargets: %v", err)
	}
	if len(got) != 1 || got[0].ServiceID != "tstratum" || !got[0].TenantRouted {
		t.Fatalf("the plane's target must be pooled per tenant, got %+v", got)
	}
}
