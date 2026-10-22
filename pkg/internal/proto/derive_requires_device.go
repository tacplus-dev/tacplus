// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"sort"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// DeriveApprovalBrokerClient returns the S2S client entry a package needs
// because it declares a (spec.rpc).requires_device RPC: a client to the
// authoritative (spec.token_minter) service, which hosts the approval broker
// the generated check calls.
//
// Auto-injected rather than declared. The developer writes only the boolean —
// same spirit as a `private` record needing no manual wiring — so the
// dependency cannot disagree with the annotation that creates it. It is derived
// (not injected into the proto TEXT) for the same reason
// DeriveVirtualServiceClients is: client inference runs before the generated
// service surfaces are materialized.
//
// Returns nil when selfPackage declares no requires_device RPC, and nil when
// selfPackage IS the minter's own package — the minter does not call itself.
func DeriveApprovalBrokerClient(inputDir, selfPackage string) ([]tactype.GrpcEntry, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for approval-broker client derivation: %w", err)
	}

	// Sorted path order for the same reason DeriveVirtualServiceClients uses it:
	// a raw map walk over a package that spans several files could derive a
	// different client set on each build.
	var paths []string
	for rp := range dp.GetAllFiles() {
		paths = append(paths, rp)
	}
	sort.Strings(paths)

	var guarded []string
	for _, rp := range paths {
		fd := dp.GetAllFiles()[rp]
		if fd == nil || fd.Package != selfPackage {
			continue
		}
		for _, svc := range fd.Services {
			for _, m := range svc.Methods {
				if m.HasDeviceGate() {
					guarded = append(guarded, svc.Name+"."+m.Name)
				}
			}
		}
	}
	if len(guarded) == 0 {
		return nil, nil
	}

	broker, err := parser.DiscoverApprovalBroker(inputDir)
	if err != nil {
		return nil, err
	}
	if broker == nil {
		return nil, fmt.Errorf("%v declares (spec.rpc).requires_device, but no authoritative (spec.token_minter) service in this proto set hosts an approval broker (an rpc taking BrokerPendingApprovalRequest and returning BrokerPendingApprovalResponse); the generated device-approval check has nothing to call", guarded)
	}
	if broker.Package == selfPackage {
		return nil, nil
	}

	zap.L().Info("derived approval-broker client from requires_device",
		zap.String("service", selfPackage),
		zap.Strings("rpcs", guarded),
		zap.String("broker", broker.Package+"."+broker.ServiceName+"/"+broker.MethodName))

	return []tactype.GrpcEntry{{
		ProtoFile:    broker.Dir + ".proto",
		ServiceName:  broker.ServiceName,
		ProtoPackage: broker.Package,
	}}, nil
}
