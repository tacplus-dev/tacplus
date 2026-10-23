// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"regexp"
	"strings"
)

// A service declares the services it calls in tacplus.yaml by registry name:
//
//	depends:
//	  - llmproxy        # latest
//	  - weather@1.2     # the latest patch of 1.2 (a floor-and-minor, never a patch pin)
//
// Generation resolves each name through the global registry the project's
// registry lock names (`global:` in proto/registry.yaml) to the repo that
// declares it, fetches that repo's proto tree at the version, and merges it
// into this generation, so the (spec.grpc_s2s_client) declared in proto has
// a proto to generate its client from. Only direct dependencies are fetched:
// following them transitively is the fleet build's job.

var dependRe = regexp.MustCompile(`^([a-z][a-z0-9_-]*)(?:@(\d+\.\d+))?$`)

// CheckDepends rejects malformed dependency entries: a name must be a registry
// name, and a version, when given, is MAJOR.MINOR only.
func CheckDepends(depends []string) error {
	seen := map[string]bool{}
	for _, d := range depends {
		d = strings.TrimSpace(d)
		m := dependRe.FindStringSubmatch(d)
		if m == nil {
			return fmt.Errorf("%s: depends entry %q must be a registry name, optionally @MAJOR.MINOR (never a patch)", Filename, d)
		}
		if seen[m[1]] {
			return fmt.Errorf("%s: depends names %q twice", Filename, m[1])
		}
		seen[m[1]] = true
	}
	return nil
}

// SplitDepend returns the registry name and the MAJOR.MINOR version ("" when
// unversioned) of one depends entry. It assumes CheckDepends has passed.
func SplitDepend(d string) (name, version string) {
	m := dependRe.FindStringSubmatch(strings.TrimSpace(d))
	if m == nil {
		return strings.TrimSpace(d), ""
	}
	return m[1], m[2]
}
