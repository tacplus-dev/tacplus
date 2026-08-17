// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateServiceSurfaces enforces that a service declares exactly one caller
// surface. Generation FAILS when one service carries both (spec.service_c2s)
// and (spec.service_s2s).
//
// The combination used to be accepted and then silently reinterpreted, because
// every downstream consumer assumes a single classification and picks one:
// REST/proxy dispatch generation short-circuits on S2S and emits no route for
// the C2S half, the registry records a single auth_model, and collection trust
// classifies the whole service TrustS2S — the most trusting tier — for a
// service that also claims to be browser-facing. Refusing the shape is the only
// way those three stay honest.
func ValidateServiceSurfaces(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			if svc.IsC2S && svc.IsS2S {
				errs = append(errs, fmt.Sprintf("%s.%s: declares both (spec.service_c2s) and (spec.service_s2s); a service has exactly one caller surface — split it into two services, one per surface (see testapp echo.proto's Echo/Probe split)",
					fd.Package, svc.Name))
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
