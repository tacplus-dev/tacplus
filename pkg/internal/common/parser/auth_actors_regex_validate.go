// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateAuthActorPatterns enforces at generation time that every auth_actors
// entry is a valid RE2 regex, for exactly the reason
// ValidateAuthServicePatterns does the same for auth_services: the values are
// carried verbatim into each backend's authorizer and compiled there with an
// unchecked constructor at service start, in code that cannot name the .proto
// line that produced them. Both fields are compiled the same way by the
// generated authorizers, so both are checked here with the same compile —
// including the "*" match-any shorthand, which is never compiled as a pattern.
//
// auth_actors is declared on spec.rpc only (there is no service-level default
// to inherit), so only the per-method lists are walked.
func ValidateAuthActorPatterns(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs []string
	for _, path := range paths {
		fd := all[path]
		for si := range fd.Services {
			svc := &fd.Services[si]
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				for _, p := range m.AuthActors {
					if err := compileAuthServicePattern(p); err != nil {
						errs = append(errs, fmt.Sprintf("%s: rpc %s.%s: auth_actors pattern %q is not a valid regex: %v",
							path, svc.Name, m.Name, p, err))
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("auth_actors pattern errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
