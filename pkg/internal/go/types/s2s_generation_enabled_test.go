// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// s2sGenerationEnabled decides whether ValidateTokenMinters fires, so what counts
// as "an S2S surface" here is the difference between a hard generation error and a
// clean build.
//
// The failing case in the field: sideband2, a c2d-only local daemon, declares
// spec.service_store records. Each record synthesizes an S2S <Msg>StoreService, so
// the build was reported as having an S2S surface and failed with "no
// authoritative (spec.token_minter) service is declared" — for a process that
// serves a Unix socket and uses only the s2sm/diskstore helpers.
//
// The state case is the reason this tests the KIND and not Synthesized: a service
// with a state root must reach its state host and does need an S2S identity, so
// excluding every synthesized service would have quietly dropped the minter
// requirement for that entire class.
func TestS2SGenerationEnabled(t *testing.T) {
	cfg := &tactype.Config{}

	for _, tc := range []struct {
		name     string
		services []ServiceInfo
		want     bool
	}{
		{
			name:     "synthesized store services alone do not require a minter",
			services: []ServiceInfo{{IsS2S: true, SynthesizedKind: parser.SynthesizedKindStore}},
			want:     false,
		},
		{
			name:     "a synthesized state service does",
			services: []ServiceInfo{{IsS2S: true, SynthesizedKind: parser.SynthesizedKindState}},
			want:     true,
		},
		{
			name:     "a hand-authored s2s service does",
			services: []ServiceInfo{{IsS2S: true}},
			want:     true,
		},
		{
			name: "a store service does not mask a real s2s surface next to it",
			services: []ServiceInfo{
				{IsS2S: true, SynthesizedKind: parser.SynthesizedKindStore},
				{IsS2S: true},
			},
			want: true,
		},
		{
			name:     "a c2d service is not an s2s surface",
			services: []ServiceInfo{{IsC2D: true}},
			want:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := s2sGenerationEnabled(cfg, tc.services); got != tc.want {
				t.Fatalf("s2sGenerationEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// A declared S2S client needs an identity to present, whatever the server side
// looks like — that arm must not be weakened by the store-service change.
func TestS2SGenerationEnabledStillTrueForDeclaredClients(t *testing.T) {
	cfg := &tactype.Config{GrpcS2SClients: []tactype.GrpcEntry{{ServiceName: "Mint", ProtoFile: "services/kernel.proto"}}}
	if !s2sGenerationEnabled(cfg, []ServiceInfo{{IsS2S: true, SynthesizedKind: parser.SynthesizedKindStore}}) {
		t.Fatal("a declared s2s client must still require a minter")
	}
}
