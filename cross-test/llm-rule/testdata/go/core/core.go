// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package core is the core of a service with an advisor (#909): it builds the
// generated adapter over its ServiceManager and passes the advisor only the
// port. Built, not run.
package core

import (
	"context"

	"gitlab.com/example/dummy/advised/advisor"
	port "gitlab.com/example/dummy/advised/model/advisor"
	sm "gitlab.com/example/dummy/advised/model/s2sm"
)

// ModelForTheAdvisor is the only model the advisor gets.
func ModelForTheAdvisor(m *sm.ServiceManager) port.ModelPort {
	return sm.NewModelPortAdapter(m)
}

// Answer asks the advisor on the inbound request's trace.
func Answer(ctx context.Context, m *sm.ServiceManager, traceID, question string) (string, error) {
	return advisor.Ask(ctx, ModelForTheAdvisor(m), traceID, question)
}
