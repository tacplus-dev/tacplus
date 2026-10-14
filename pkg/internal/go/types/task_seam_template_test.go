// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"strings"
	"testing"
)

// CC-1 regression: ServiceManager must expose a public GoTask method that
// delegates to the internal serviceTaskScope, so application code can attach
// background work to SM shutdown.
func TestGoSMTemplatePublicTaskSeam(t *testing.T) {
	data, err := os.ReadFile("../../../templates/go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "func (sm *ServiceManager) GoTask(fn func(context.Context))") {
		t.Error("SM template missing public GoTask method (CC-1)")
	}
	if !strings.Contains(s, "sm.tasks.Go(fn)") {
		t.Error("GoTask must delegate to sm.tasks.Go")
	}
}
