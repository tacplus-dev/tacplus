// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateServiceSurfaces enforces that a service declares exactly one caller
// surface. Generation FAILS when one service carries more than one of
// (spec.service_c2s), (spec.service_s2s) and (spec.service_c2d).
//
// The combination used to be accepted and then silently reinterpreted, because
// every downstream consumer assumes a single classification and picks one:
// REST/proxy dispatch generation short-circuits on S2S and emits no route for
// the C2S half, the registry records a single auth_model, and
// collection.TrustOf takes all three flags and returns ONE trust tier — so a
// service that also claims to be browser-facing is classified at the most
// trusting tier of whatever it declared. C2D (unix socket, no auth, no gateway
// routing) collapses the same way when paired with either of the others.
// Refusing the shape is the only way those consumers stay honest.
func ValidateServiceSurfaces(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			var found []string
			if svc.IsC2S {
				found = append(found, "(spec.service_c2s)")
			}
			if svc.IsS2S {
				found = append(found, "(spec.service_s2s)")
			}
			if svc.IsC2D {
				found = append(found, "(spec.service_c2d)")
			}
			if len(found) > 1 {
				errs = append(errs, fmt.Sprintf("%s.%s: declares %s; a service has exactly one caller surface — split it into two services, one per surface (see testapp echo.proto's Echo/Probe split)",
					fd.Package, svc.Name, strings.Join(found, " and ")))
			}
		}
	}
	if len(errs) > 0 {
		// GetAllFiles is a map: sort so the message is identical run-to-run.
		sort.Strings(errs)
		return fmt.Errorf("service annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
