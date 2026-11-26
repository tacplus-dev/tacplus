// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// all_grpc_s2s_clients generates no client for an edge the product layer
// refuses (products-layer-2026-11-08).
func TestResolveClientServicesDropsRefusedEdges(t *testing.T) {
	config := &tactype.Config{GrpcS2SClientAll: true,
		GrpcS2SClientAllRefused: []tactype.GrpcEntry{{ProtoFile: "services/crew.proto", ServiceName: "Crew"}}}
	got := resolveClientServices(config, []ServiceInfo{
		{ProtoFile: "services/core.proto", ServiceName: "Core", IsGrpc: true},
		{ProtoFile: "services/crew.proto", ServiceName: "Crew", IsGrpc: true},
	})
	if len(got) != 1 || got[0].ServiceName != "Core" {
		t.Fatalf("resolveClientServices = %+v, want Core only", got)
	}
}
