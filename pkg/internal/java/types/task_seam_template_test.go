// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// CC-1 regression: ServiceManager must expose a public startTask method
// so application code can attach background work to SM shutdown.
func TestJavaSMTemplatePublicTaskSeam(t *testing.T) {
	tmpl, err := loadTemplate("ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	if !strings.Contains(tmpl, "public Thread startTask(String name, Runnable body)") {
		t.Error("Java SM template missing public startTask method (CC-1)")
	}
	if !strings.Contains(tmpl, "return startManagedThread(name, true, body)") {
		t.Error("startTask must delegate to startManagedThread")
	}
}
