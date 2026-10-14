// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/verbs"
)

// methodShape adapts a MethodInfo into the primitive shape the verb classifier
// consumes. The collection mode (when annotated) is what disambiguates LIST vs
// WATCH for streaming reads and confirms PAGE is unary.
func methodShape(m *MethodInfo) verbs.MethodShape {
	mode := ""
	if m.Collection != nil {
		mode = m.Collection.Mode
	}
	return verbs.MethodShape{
		Name:            m.Name,
		ClientStreaming: m.ClientStreaming,
		ServerStreaming: m.ServerStreaming,
		CollectionMode:  mode,
	}
}

// ClassifyMethods runs the exhaustive verb/pattern classifier over every method
// and attaches the resolved Classification to MethodInfo.Class. It is the IR
// step entitlements (§5) key on.
//
// report is called once per method that fails classification (unregistered /
// synonym / noun-first verb, mis-case, or verb↔pattern↔cardinality mismatch).
// Generation hard-fails when a generated-auth surface leaves Class nil; this
// helper keeps the attach step separate so callers can aggregate every bad RPC
// in one diagnostic. Unannotated support services may remain unclassified until
// they opt into a generated auth surface.
func ClassifyMethods(services []*ServiceInfo, report func(svc, method string, err error)) {
	for _, svc := range services {
		for _, m := range svc.Methods {
			c, err := verbs.Classify(methodShape(m))
			if err != nil {
				if report != nil && serviceEnforcesVerbs(svc) {
					report(svc.Name, m.Name, err)
				}
				m.Class = nil
				continue
			}
			cc := c
			m.Class = &cc
		}
	}
}

func serviceEnforcesVerbs(svc *ServiceInfo) bool {
	return svc != nil && !svc.Synthesized && (svc.IsS2S || svc.IsC2S || svc.IsC2D)
}
