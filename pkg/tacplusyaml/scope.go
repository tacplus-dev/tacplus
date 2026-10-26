// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import "fmt"

// A project generates from its own contract, the contracts it names in
// `depends`, and the defaults every project gets (the compiler's sys protos).
// Two kinds of project need every service in the fleet instead, and say so
// per surface (Jody, 2026-10-25: "in the yaml something like gRPC-servers:
// all"):
//
//	grpc-servers: all   # the gateway: it dispatches to every service's surface
//	grpc-clients: all   # a CLI: it talks to every service
//	states: all         # the kernel: it holds every service's config and state
//	records: all        # the record store: it subscribes to every record in the fleet
//
// Any of them widens the project's scope to the whole fleet tree, and so its
// inputs digest: such a project is rebuilt whenever any type in the fleet
// changes, where a service that names its dependencies is rebuilt only when
// one of them changes.
const ScopeAll = "all"

// CheckScope rejects a scope value other than "all" or unset.
func CheckScope(servers, clients, states, records string) error {
	for key, v := range map[string]string{"grpc-servers": servers, "grpc-clients": clients, "states": states, "records": records} {
		if v != "" && v != ScopeAll {
			return fmt.Errorf("%s: %s must be %q or unset (own surface / declared depends), got %q", Filename, key, ScopeAll, v)
		}
	}
	return nil
}

// WholeFleet reports whether the project generates from every service in
// the tree it is built against rather than from its own contract and its
// declared depends.
func (c *Config) WholeFleet() bool {
	return c != nil && (c.GrpcServers == ScopeAll || c.GrpcClients == ScopeAll || c.States == ScopeAll || c.Records == ScopeAll)
}
