// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// physicalServingLabels returns the labels of services that PHYSICALLY exist in
// the loaded proto set: a file-level (spec.service) identity, and the gRPC
// identity of any service that is not itself host-labelled.
//
// This is deliberately narrower than CollectServingLabels, which also treats a
// declared (spec.service_s2s).label as served. That set is self-satisfying — a
// host label makes itself served — so validating a host label against it can
// never fail, which is exactly the check that must be able to fail here.
func physicalServingLabels(fds []*FileDescriptor) map[string]bool {
	labels := make(map[string]bool)
	for _, fd := range fds {
		if fd.ServiceName != "" {
			labels[fd.ServiceName] = true
		}
		if fd.ServiceStore && fd.Package != "" {
			labels[fd.Package] = true
		}
		for _, svc := range fd.Services {
			if svc.HostLabel != "" {
				continue
			}
			if svc.GrpcName != "" {
				labels[svc.GrpcName] = true
			}
		}
	}
	return labels
}

// PhysicalServingLabels is the DescriptorParser form of physicalServingLabels:
// the set of labels the loaded proto set actually serves. Callers that resolve
// a declared host label against reality (the C2S validator here, the
// trait-driven record plane injector) share this one definition so "served"
// never means two different things.
func PhysicalServingLabels(dp *DescriptorParser) map[string]bool {
	all := dp.GetAllFiles()
	fds := make([]*FileDescriptor, 0, len(all))
	for _, fd := range all {
		fds = append(fds, fd)
	}
	return physicalServingLabels(fds)
}

// ValidateC2SHostLabels enforces the two halves of a trait-hosted C2S surface.
//
// A C2S service declaring (spec.service_c2s).label says "these client-facing
// RPCs are defined here but served by that physical service". Two things must
// hold, or the generated gateway route is nonsense:
//
//  1. The label must name a service the loaded proto set actually contains.
//     A label nothing serves routes nowhere, silently.
//  2. The declaring file must carry a trait identity ((spec.trait).name). The
//     route's ServiceID is the host, but its entitlement audience is the TRAIT
//     — the audience is the contract, not the concrete service, so a second
//     implementor needs no client change. With no trait name there is no
//     audience to check the caller's token against.
//
// This is the same paired control (spec.service_event).label and
// (spec.service_store).label already carry: declare a label no service serves
// and generation fails loud.
func ValidateC2SHostLabels(dp *DescriptorParser) error {
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateC2SHostLabels(fds)
}

// validateC2SHostLabels is the file-list form of ValidateC2SHostLabels; fds must
// already be in a deterministic order so the error text is stable.
func validateC2SHostLabels(fds []*FileDescriptor) error {
	served := physicalServingLabels(fds)

	var errs []string
	for _, fd := range fds {
		for _, svc := range fd.Services {
			if !svc.IsC2S || svc.HostLabel == "" {
				continue
			}
			if !served[svc.HostLabel] {
				errs = append(errs, fmt.Sprintf(
					"%s.%s declares (spec.service_c2s).label %q but no loaded service serves that label",
					fd.Package, svc.Name, svc.HostLabel))
			}
			if fd.VServiceName == "" {
				errs = append(errs, fmt.Sprintf(
					"%s.%s declares (spec.service_c2s).label %q but %s declares no (spec.trait).name to entitle callers against",
					fd.Package, svc.Name, svc.HostLabel, fd.Path))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("c2s host label errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
