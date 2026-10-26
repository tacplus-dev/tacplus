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

const globalSeed = `scope: global
services:
    - {name: kernel, id: 8, contract: git@gitlab.com:x/platform.git, visibility: private, tenant_slice_field: 1}
    - {name: tally, id: 79, contract: https://gitlab.com/x/sonar.git, visibility: private}
vservices:
    - {name: query, field_id: 71, contract: https://gitlab.com/x/sonar.git, visibility: private}
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
	if e == nil || e.ID != GlobalIDMin || e.Contract != "https://github.com/tacplus-dev/doppelganger.git" || e.Visibility != "public" {
		t.Fatalf("saved entry = %+v", e)
	}
}

func TestRegisterReportsGlobalExhaustion(t *testing.T) {
	g, err := Load(writeRegistry(t, "scope: global\nservices:\n    - {name: last, id: 1023, contract: r, visibility: public}\n"))
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
	lock, err := Load(writeRegistry(t, "registries:\n    - https://github.com/tacplus-dev/registry.git\nservices: []\n"))
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
	if !strings.Contains(string(data), "- https://github.com/tacplus-dev/registry.git") {
		t.Fatalf("saved lock lost its authority:\n%s", data)
	}
}

func TestSyncFromGlobalNeverRenumbers(t *testing.T) {
	g, err := Load(writeRegistry(t, globalSeed))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Load(writeRegistry(t, "registries: [g]\nservices:\n    - {name: tally, id: 12}\n"))
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
	lock, err := Load(writeRegistry(t, "registries: [g]\nservices:\n    - {name: tally, id: 79}\n"))
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
	for id, want := range map[int32]bool{0: false, 1: true, 99: true, 100: false, 101: true, 1023: true, 1024: true, 2047: true, 2048: false} {
		if LegalID(id) != want {
			t.Errorf("LegalID(%d) = %v", id, !want)
		}
	}
}

// A private authority allocates from its own range and never from the global
// one; an authority without a scope cannot allocate at all.
func TestRegisterScopes(t *testing.T) {
	priv, err := Load(writeRegistry(t, "scope: private\nservices:\n    - {name: stratum, id: 34, contract: r, visibility: private}\n"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := priv.Register("service", "ours", "r", "private")
	if err != nil || id != PrivateIDMin {
		t.Fatalf("private registration = %d, %v; want %d", id, err, PrivateIDMin)
	}
	noScope, err := Load(writeRegistry(t, "services: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noScope.Register("service", "x", "r", "public"); err == nil || !strings.Contains(err.Error(), "no scope") {
		t.Fatalf("scopeless registration err = %v", err)
	}
	lock, _ := Load(writeRegistry(t, "registries: [g]\nservices: []\n"))
	if _, err := lock.Register("service", "x", "r", "public"); err == nil || !strings.Contains(err.Error(), "is a lock") {
		t.Fatalf("registering into a lock err = %v", err)
	}
}

// Registries a fleet loads are disjoint: a name or an id in two of them is an
// error, never a precedence call.
func TestUnionRefusesCollisions(t *testing.T) {
	g, _ := Load(writeRegistry(t, globalSeed))
	p, _ := Load(writeRegistry(t, "scope: private\nservices:\n    - {name: ours, id: 1024, contract: r}\n"))
	u, err := Union([]*Registry{g, p}, []string{"global", "private"})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := u.ContractOf("ours"); !ok || c != "r" {
		t.Fatalf("union lacks the private entry: %q %v", c, ok)
	}
	if c, ok := u.ContractOf("kernel"); !ok || c != "git@gitlab.com:x/platform.git" {
		t.Fatalf("union lacks the global entry: %q %v", c, ok)
	}
	sameName, _ := Load(writeRegistry(t, "scope: private\nservices:\n    - {name: kernel, id: 1025, contract: ours}\n"))
	if _, err := Union([]*Registry{g, sameName}, []string{"global", "private"}); err == nil || !strings.Contains(err.Error(), `name "kernel"`) {
		t.Fatalf("same name in two registries err = %v", err)
	}
	sameID, _ := Load(writeRegistry(t, "scope: private\nservices:\n    - {name: other, id: 79, contract: ours}\n"))
	if _, err := Union([]*Registry{g, sameID}, []string{"global", "private"}); err == nil || !strings.Contains(err.Error(), "id 79") {
		t.Fatalf("same id in two registries err = %v", err)
	}
}
