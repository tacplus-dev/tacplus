// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanRegistryNamesResolvesEmbeddedGeneratedImports(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := t.TempDir()
	vservicesDir := filepath.Join(dir, "vservices")
	if err := os.MkdirAll(vservicesDir, 0755); err != nil {
		t.Fatalf("mkdir vservices: %v", err)
	}
	const content = `syntax = "proto3";

package telemetry;

import "sys/spec.proto";
import "sys/trace.proto";

option (spec.vservice) = {
  id: 42
  name: "telemetry"
};

message TraceCarrier {
  trace.TraceEvent event = 1;
}
`
	if err := os.WriteFile(filepath.Join(vservicesDir, "claude-sensor.proto"), []byte(content), 0644); err != nil {
		t.Fatalf("write vservice proto: %v", err)
	}

	_, got, err := ScanRegistryNames(dir)
	if err != nil {
		t.Fatalf("ScanRegistryNames: %v", err)
	}
	want := []string{"telemetry"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanRegistryNames() vservices = %#v, want %#v", got, want)
	}
}

// Finding 9: two entries with the same name make `ServiceID` resolve to
// whichever was inserted into the lookup map last, while generation and saving
// still iterate BOTH backing entries — the same registry means different things
// depending on the code path. Registry data integrity fails closed.
func TestLoadRejectsDuplicateServiceName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: billing\n    id: 1\n  - name: billing\n    id: 7\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted a registry with two services named \"billing\"; want an error")
	} else if !strings.Contains(err.Error(), "billing") {
		t.Errorf("error does not name the duplicate: %v", err)
	}
}

func TestLoadRejectsDuplicateVServiceName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services: []\nvservices:\n  - name: topics\n    field_id: 3\n  - name: topics\n    field_id: 8\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load accepted a registry with two vservices named \"topics\"; want an error")
	} else if !strings.Contains(err.Error(), "topics") {
		t.Errorf("error does not name the duplicate: %v", err)
	}
}

// A service and a vservice may legitimately share a name (see the cross-test
// contract fixtures), so the duplicate check must be per-kind.
func TestLoadAllowsSameNameAcrossKinds(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: fixtures\n    id: 1\nvservices:\n  - name: fixtures\n    field_id: 2\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load rejected a name shared between a service and a vservice: %v", err)
	}
}

// Finding 10: allocation used max(existing)+1, so a single manually assigned
// ID 99 made every later allocation fail as "all IDs 1-99 are taken" with
// 98 IDs free.
func TestServiceIDAllocatesBelowAManuallyAssignedMax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: pinned\n    id: 99\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	id, err := r.ServiceID("fresh")
	if err != nil {
		t.Fatalf("allocation failed with 98 IDs free: %v", err)
	}
	if id != 1 {
		t.Errorf("got id %d, want 1 (lowest unused at or above the floor)", id)
	}
}

// Normal allocation is max+1, not gap-filling: a gap left by IDs 60 and 62
// (61 free) must not be silently claimed by the next unrelated service, since
// registry IDs are permanent TokenEntitlements field numbers.
func TestNextIDUsesMaxPlusOneNotGapFill(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("id_min: 60\nservices:\n  - name: a\n    id: 60\n  - name: b\n    id: 62\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	id, err := r.ServiceID("c")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if id != 63 {
		t.Errorf("got id %d, want 63 (max+1); gap-filling would reuse ID 61's field number", id)
	}
}

// The floor still bounds a fresh registry: an app registry never hands out an
// ID below its id_min even on the very first allocation.
func TestNextIDStartsAtFloorWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("id_min: 60\nservices: []\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	id, err := r.ServiceID("a")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if id != 60 {
		t.Errorf("got id %d, want 60 (the floor)", id)
	}
}

// Exhaustion is still reported — but only when every ID in [floor, 99] is gone.
func TestServiceIDReportsRealExhaustion(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("services:\n")
	for i := 1; i <= 99; i++ {
		fmt.Fprintf(&b, "  - name: s%d\n    id: %d\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := r.ServiceID("overflow"); err == nil {
		t.Fatal("allocation succeeded with all of 1-99 taken; want an overflow error")
	}
}

// Finding 8: validation only looked for service-vs-vservice collisions, so a
// manually merged registry mapping two services onto the same entitlement
// field — or onto an illegal/reserved protobuf field number — passed.
func TestValidateRejectsDuplicateAndIllegalIDs(t *testing.T) {
	cases := []struct {
		name   string
		yaml   string
		wantIn string
	}{
		{
			name:   "duplicate service IDs",
			yaml:   "services:\n  - name: a\n    id: 5\n  - name: b\n    id: 5\n",
			wantIn: `service "b"`,
		},
		{
			name:   "duplicate vservice field IDs",
			yaml:   "services: []\nvservices:\n  - name: x\n    field_id: 4\n  - name: y\n    field_id: 4\n",
			wantIn: `vservice "y"`,
		},
		{
			name:   "zero ID",
			yaml:   "services:\n  - name: a\n    id: 0\n",
			wantIn: `service "a"`,
		},
		{
			name:   "negative ID",
			yaml:   "services:\n  - name: a\n    id: -3\n",
			wantIn: `service "a"`,
		},
		{
			name:   "ID above 99",
			yaml:   "services:\n  - name: a\n    id: 120\n",
			wantIn: `service "a"`,
		},
		{
			name:   "reserved field 100",
			yaml:   "services:\n  - name: a\n    id: 100\n",
			wantIn: `service "a"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "registry.yaml"), []byte(tc.yaml), 0644); err != nil {
				t.Fatalf("write registry: %v", err)
			}
			err := Validate(dir)
			if err == nil {
				t.Fatalf("Validate accepted a registry with %s; want an error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %s", err, tc.wantIn)
			}
		})
	}
}

// A registry with distinct, legal IDs still validates.
func TestValidateAcceptsLegalIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: a\n    id: 1\n  - name: b\n    id: 99\nvservices:\n  - name: v\n    field_id: 2\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if err := Validate(dir); err != nil {
		t.Fatalf("Validate rejected a legal registry: %v", err)
	}
}

// Finding 11: registry discovery hard-coded a walk of services/, while
// generation discovers service declarations from descriptors anywhere in the
// tree. A service declared under another package directory — the shape
// cross-test/consumer-contract/proto/contract/contract.proto already has — was
// never assigned an ID nor reported missing, even though the generator finds
// and consumes it.
func TestValidateFindsServiceDeclaredOutsideServicesDir(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := t.TempDir()
	contractDir := filepath.Join(dir, "contract")
	if err := os.MkdirAll(contractDir, 0755); err != nil {
		t.Fatalf("mkdir contract: %v", err)
	}
	const content = `syntax = "proto3";

package contract;

import "sys/spec.proto";

option (spec.service) = {name: "contract"};

message Ping {}
`
	if err := os.WriteFile(filepath.Join(contractDir, "contract.proto"), []byte(content), 0644); err != nil {
		t.Fatalf("write contract proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"), []byte("services: []\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	services, _, err := ScanRegistryNames(dir)
	if err != nil {
		t.Fatalf("ScanRegistryNames: %v", err)
	}
	if !reflect.DeepEqual(services, []string{"contract"}) {
		t.Errorf("ScanRegistryNames() services = %#v, want [contract]", services)
	}

	err = Validate(dir)
	if err == nil {
		t.Fatal(`Validate accepted a registry missing the service declared in contract/contract.proto; ` +
			`discovery is still limited to services/`)
	}
	if !strings.Contains(err.Error(), `service "contract"`) {
		t.Errorf("error does not report the missing service: %v", err)
	}
}
