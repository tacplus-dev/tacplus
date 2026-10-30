// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// rustTenantRoutedServices is the set of services ("pkg.Service") whose rpcs
// declare (spec.rpc).route_field. The parser requires the field on every rpc
// of a tenant-routed service and refuses it everywhere else, so one routed rpc
// marks the whole service.
//
// The flat proto dir is the tree from BEFORE the record plane is injected into
// its trait's package, so the plane's services are not there to ask. On a
// tenant-owned record store the injector gives every rpc of both halves its
// route field (#207); the trait names both halves, so they are added from it.
// Without this the ServiceManager pooled the plane's S2S target as one pool
// over every tenant's instance, and the host never told the plane's wrappers
// which tenant their instance serves.
func rustTenantRoutedServices(flatProtoDir string) (map[string]bool, error) {
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("tenant routing service scan: parse %q: %w", flatProtoDir, err)
	}
	routed := make(map[string]bool)
	for _, fd := range dp.GetAllFiles() {
		if fd == nil {
			continue
		}
		for _, svc := range fd.Services {
			for _, m := range svc.Methods {
				if m.RouteField != nil {
					routed[fd.Package+"."+svc.Name] = true
				}
			}
		}
	}
	addTenantRoutedRecordPlanes(dp, routed)
	return routed, nil
}

// addTenantRoutedRecordPlanes marks both halves of every record plane hosted
// on a tenant-owned record store as tenant-routed (#207).
func addTenantRoutedRecordPlanes(dp *parser.DescriptorParser, routed map[string]bool) {
	planes := parser.TenantRoutedRecordPlanePackages(dp)
	for _, fd := range dp.GetAllFiles() {
		if fd == nil || fd.Trait == nil || !planes[fd.Package] {
			continue
		}
		for _, svc := range []string{fd.Trait.S2SService, fd.Trait.C2SService} {
			if svc != "" {
				routed[fd.Package+"."+svc] = true
			}
		}
	}
}

// markTenantRoutedTargets sets TenantRouted on every S2S target whose client
// services are tenant-routed, so the ServiceManager pools that target per
// tenant. A target reached by both routed and platform services has no single
// pool shape that serves both without letting one of them pick any tenant's
// instance, so generation fails instead.
func markTenantRoutedTargets(targets []TargetServiceInfo, clientServices []ServiceInfo, routed map[string]bool) ([]TargetServiceInfo, error) {
	byTarget := make(map[string][]string)
	routedByTarget := make(map[string]int)
	for _, svc := range clientServices {
		target := resolveMeshTarget(svc)
		key := svc.PackageName + "." + svc.ServiceName
		byTarget[target] = append(byTarget[target], key)
		if routed[key] {
			routedByTarget[target]++
		}
	}
	out := make([]TargetServiceInfo, len(targets))
	copy(out, targets)
	for i := range out {
		svcs := byTarget[out[i].ServiceID]
		n := routedByTarget[out[i].ServiceID]
		switch {
		case n == 0:
		case n == len(svcs):
			out[i].TenantRouted = true
		default:
			sort.Strings(svcs)
			return nil, fmt.Errorf("s2s target %q is reached by tenant-routed and platform services alike (%s); a tenant-owned service's instances are reached per tenant by (spec.rpc).route_field, so every service a client reaches on it must declare one",
				out[i].ServiceID, strings.Join(svcs, ", "))
		}
	}
	return out, nil
}
