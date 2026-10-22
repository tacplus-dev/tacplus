// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A record's (or service_event's) synthesized <Record>BusStateService is the
// EventBus broker's topic surface, not the owner's. The owner gets the publish
// client for it, never a server wrapper, handler method or registration (issue
// #112). The second service pins that an ordinary state root is still hosted by
// its owner.
func TestResolveServerServices_EventBusTopicIsNotHostedByOwner(t *testing.T) {
	cfg := &tactype.Config{SelfPackage: "egressvault"}
	got := resolveServerServices(cfg, []ServiceInfo{
		{ProtoFile: "services/egressvault.proto", PackageName: "egressvault", ServiceName: "EgressVaultApi", GrpcName: "egressvault", IsGrpc: true, IsS2S: true},
		{ProtoFile: "services/egressvault.proto", PackageName: "egressvault", ServiceName: "VaultRowBusStateService", GrpcName: "egressvault", IsGrpc: true, IsS2S: true, SynthesizedKind: parser.SynthesizedKindState, EventLabel: "eventbus"},
		{ProtoFile: "services/egressvault.proto", PackageName: "egressvault", ServiceName: "VaultStateService", GrpcName: "egressvault", IsGrpc: true, IsS2S: true, SynthesizedKind: parser.SynthesizedKindState},
	})
	var names []string
	for _, svc := range got {
		names = append(names, svc.ServiceName)
	}
	want := []string{"EgressVaultApi", "VaultStateService"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("server services = %v, want %v", names, want)
	}
}
