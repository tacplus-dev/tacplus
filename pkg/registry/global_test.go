// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const globalSeed = `services:
    - {name: kernel, id: 8, repo: git@gitlab.com:x/platform.git, visibility: private, tenant_slice_field: 1}
    - {name: tally, id: 79, repo: https://gitlab.com/x/sonar.git, visibility: private}
vservices:
    - {name: query, field_id: 71, repo: https://gitlab.com/x/sonar.git, visibility: private}
`

func TestRegisterAllocatesFromGlobalMinNeverLegacy(t *testing.T) {
	g, err := Load(writeRegistry(t, globalSeed))
	if err != nil {
		t.Fatal(err)
	}
	id, err := g.Register("service", "doppelganger", "https://github.com/tacplus-dev/doppelganger.git", "public")
	if err != nil || id != GlobalIDMin {
		t.Fatalf("first registration = %d, %v; want %d (legacy ids below it are frozen)", id, err, GlobalIDMin)
	}
	id2, err := g.Register("vservice", "thing", "https://example/r.git", "private")
	if err != nil || id2 != GlobalIDMin+1 {
		t.Fatalf("second registration = %d, %v", id2, err)
	}
	if _, err := g.Register("service", "doppelganger", "https://example/r.git", "public"); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate registration err = %v", err)
	}
	if _, err := g.Register("service", "bad", "https://example/r.git", "secret"); err == nil {
		t.Fatal("bad visibility accepted")
	}
	if err := g.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load(g.inputDir)
	if err != nil {
		t.Fatal(err)
	}
	e := again.byName["doppelganger"]
	if e == nil || e.ID != GlobalIDMin || e.Repo != "https://github.com/tacplus-dev/doppelganger.git" || e.Visibility != "public" {
		t.Fatalf("saved entry = %+v", e)
	}
}

func TestRegisterReportsGlobalExhaustion(t *testing.T) {
	g, err := Load(writeRegistry(t, "services:\n    - {name: last, id: 2047, repo: r, visibility: public}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Register("service", "one-more", "r", "public"); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("err = %v", err)
	}
}

func TestSyncFromGlobalFillsLockAndReportsUnregistered(t *testing.T) {
	g, err := Load(writeRegistry(t, globalSeed))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Load(writeRegistry(t, "global: https://github.com/tacplus-dev/registry.git\nservices: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = lock.SyncFromGlobal(g, []string{"tally", "kernel", "nobody"}, []string{"query"})
	if err == nil || !strings.Contains(err.Error(), `service "nobody"`) || !strings.Contains(err.Error(), "tacplus registry register") {
		t.Fatalf("err = %v", err)
	}
	// The registered names were still synced, so one fix is one re-run.
	if lock.byName["tally"] == nil || lock.byName["tally"].ID != 79 || lock.vsByName["query"] == nil || lock.vsByName["query"].FieldID != 71 {
		t.Fatalf("lock not synced: %+v %+v", lock.Services, lock.VServices)
	}
	if k := lock.byName["kernel"]; k == nil || k.TenantSliceField != 1 {
		t.Fatalf("slice fields are wire numbers and must sync from the global registry: %+v", k)
	}
	if err := lock.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(lock.inputDir, "registry.yaml"))
	if !strings.Contains(string(data), "global: https://github.com/tacplus-dev/registry.git") {
		t.Fatalf("saved lock lost its authority:\n%s", data)
	}
}

func TestSyncFromGlobalNeverRenumbers(t *testing.T) {
	g, err := Load(writeRegistry(t, globalSeed))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Load(writeRegistry(t, "global: g\nservices:\n    - {name: tally, id: 12}\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = lock.SyncFromGlobal(g, []string{"tally"}, nil)
	if err == nil || !strings.Contains(err.Error(), "12 in this lock but 79") {
		t.Fatalf("err = %v", err)
	}
	if lock.byName["tally"].ID != 12 {
		t.Fatal("lock was renumbered")
	}
}

func TestLockRefusesToAllocate(t *testing.T) {
	lock, err := Load(writeRegistry(t, "global: g\nservices:\n    - {name: tally, id: 79}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if id, err := lock.ServiceID("tally"); err != nil || id != 79 {
		t.Fatalf("known service: %d %v", id, err)
	}
	if _, err := lock.ServiceID("newcomer"); err == nil || !strings.Contains(err.Error(), "not in the registry lock") {
		t.Fatalf("unknown service in a lock must not be auto-assigned: %v", err)
	}
	if _, err := lock.VServiceFieldID("newvs"); err == nil {
		t.Fatal("unknown vservice in a lock must not be auto-assigned")
	}
}

func TestLegalIDRanges(t *testing.T) {
	for id, want := range map[int32]bool{0: false, 1: true, 99: true, 100: false, 101: true, 2047: true, 2048: false} {
		if LegalID(id) != want {
			t.Errorf("LegalID(%d) = %v", id, !want)
		}
	}
}
