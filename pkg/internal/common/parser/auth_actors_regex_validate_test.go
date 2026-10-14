// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// ValidateAuthActorPatterns is the generation-time gate on auth_actors regex
// syntax, the sibling of ValidateAuthServicePatterns: without it a malformed
// pattern generates cleanly and panics the generated service at startup, inside
// SetMethodActors' MustCompile, with no way left to name the .proto that
// produced it.

// authActorsParser builds a one-file DescriptorParser with per-RPC auth_actors,
// the exact IR shape the validator consumes. auth_actors is declared on
// spec.rpc only, so there is no service-level list to build.
func authActorsParser(rpcPatterns []string) *DescriptorParser {
	return &DescriptorParser{
		descriptors: map[string]*FileDescriptor{
			"billing.proto": {
				Path:    "billing.proto",
				Package: "billing",
				Services: []ServiceDescriptor{
					{
						Name: "BillingService",
						Methods: []MethodDescriptor{
							{Name: "Charge", AuthActors: rpcPatterns},
						},
					},
				},
			},
		},
	}
}

func TestValidateAuthActorPatternsNilParser(t *testing.T) {
	if err := ValidateAuthActorPatterns(nil); err != nil {
		t.Fatalf("ValidateAuthActorPatterns(nil) = %v, want nil", err)
	}
}

func TestValidateAuthActorPatternsAcceptsValidPatterns(t *testing.T) {
	cases := map[string][]string{
		"wildcard shorthand":  {"*"},
		"literal actor":       {"mcp"},
		"workload child path": {"mcp/workloads/agent-42"},
		"alternation":         {"mcp|scotty"},
		"character class":     {"agent-[0-9]+"},
		"anchorable subgroup": {"(?:mcp|scotty)/workloads/.*"},
		"none declared":       nil,
	}
	for name, patterns := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAuthActorPatterns(authActorsParser(patterns)); err != nil {
				t.Fatalf("rpc-level %v rejected: %v", patterns, err)
			}
		})
	}
}

func TestValidateAuthActorPatternsRejectsMalformed(t *testing.T) {
	err := ValidateAuthActorPatterns(authActorsParser([]string{"agent[a-"}))
	if err == nil {
		t.Fatal("ValidateAuthActorPatterns accepted a pattern that is not valid regex")
	}
	for _, want := range []string{"billing.proto", "BillingService.Charge", "auth_actors", `"agent[a-"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
