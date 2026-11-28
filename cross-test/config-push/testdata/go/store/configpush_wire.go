// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// cross-test/config-push (tacplus#927) copies this file into the generated
// s2sm package of the fixture's stratum, the tenant's record store: it is the
// gate's, not generated. It hands the owner's test the store's generated push
// (its owners table, from the registry, and its feed from the record plane's
// configpush.TenantSliceRecord entries) on the test's own connection and
// token, in place of the fleet's service health and the kernel's.

package sm

import (
	"gitlab.com/example/dummy/stratum/tacplus/model/chassis/configpush"
	"gitlab.com/example/dummy/stratum/tacplus/model/chassis/service_im"
	query_fe "gitlab.com/example/dummy/stratum/tacplus/model/s2sm/grpc/servers/vservices/query"
	"go.uber.org/zap"
)

// WireOwners is the store's generated owners table: every registered
// tenant_slice_field, to its owner's routing label.
func WireOwners() map[int32]string {
	out := make(map[int32]string, len(configPushOwners))
	for f, l := range configPushOwners {
		out[f] = l
	}
	return out
}

// WirePublisher is the store's publisher over its generated owners table,
// each owner's stream opened by open, fed by the generated feed from plane.
// Its identity holds no token, so the feed watches with no target tenant, as
// a store whose token is not yet minted.
func WirePublisher(plane query_fe.RecordMapHandler, open configpush.Opener) *configpush.Publisher {
	sm := &ServiceManager{identity: &service_im.IdentityManager{}}
	sm.configPushPublisher = configpush.NewPublisher(configPushOwners, open, zap.L())
	sm.feedConfigPushPublisher(plane)
	return sm.configPushPublisher
}
