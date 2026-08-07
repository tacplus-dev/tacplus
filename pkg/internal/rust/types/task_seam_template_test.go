// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// CC-1 regression: ServiceManager must expose a public spawn_task method
// so application code can attach background work to SM shutdown.
func TestRustSMTemplatePublicTaskSeam(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	if !strings.Contains(tmpl, "pub fn spawn_task<F>(&self, task: F)") {
		t.Error("Rust SM template missing public spawn_task method (CC-1)")
	}
	if !strings.Contains(tmpl, "self.tasks.lock().unwrap().push(handle)") {
		t.Error("spawn_task must push to the tasks list for shutdown coordination")
	}
}
