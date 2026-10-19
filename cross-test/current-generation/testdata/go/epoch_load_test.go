// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the epoch-load fixture. NOT generated: run.sh copies this
// file into the generated per-store disk helpers package (s2sm/diskstore) before
// `go test ./...`.
//
// What it pins: Lease carries an Expiry whose exp is (spec.epoch_expiration),
// a write-time assertion (a future epoch). A record written while valid must
// still LOAD after its exp has passed, or a service cannot restart once any
// stored record ages out. The paired control: a non-time violation (an empty
// lease_id) must still fail the load, even when the record is also expired.
// This executes the generated LoadLease, which is the path DiskStores runs.

package diskstores

import (
	"testing"
	"time"

	diskstore "gitlab.com/example/dummy/epochload/tacplus/model/chassis/diskstore"
	epochload_pb "gitlab.com/example/dummy/epochload/tacplus/model/types/services/epochload"
)

// saveThenAge writes one Lease, then waits until its exp has passed.
func saveThenAge(t *testing.T, base string, lease *epochload_pb.Lease) {
	t.Helper()
	if err := SaveLease(base, "epochload", lease, diskstore.SnapshotOpts{}); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}
	for time.Now().Unix() <= lease.Expiry.Exp {
		time.Sleep(100 * time.Millisecond)
	}
}

func TestLoadLeaseAcceptsAnEpochThatExpiredAfterItWasWritten(t *testing.T) {
	base := t.TempDir()
	exp := time.Now().Unix() + 1
	saveThenAge(t, base, &epochload_pb.Lease{
		LeaseId: "lease-1",
		Expiry:  &epochload_pb.Expiry{Exp: exp},
	})

	got, ok, err := LoadLease(base, "epochload")
	if err != nil {
		t.Fatalf("LoadLease after exp %d has passed must succeed (data ages; the owner retires it), got: %v", exp, err)
	}
	if !ok {
		t.Fatal("LoadLease: snapshot not found")
	}
	if got.LeaseId != "lease-1" || got.Expiry.GetExp() != exp {
		t.Fatalf("LoadLease round-trip mismatch: got lease_id=%q exp=%d, want lease-1 exp=%d",
			got.LeaseId, got.Expiry.GetExp(), exp)
	}
}

func TestLoadLeaseStillRejectsANonTimeViolation(t *testing.T) {
	base := t.TempDir()
	exp := time.Now().Unix() + 1
	saveThenAge(t, base, &epochload_pb.Lease{
		LeaseId: "",
		Expiry:  &epochload_pb.Expiry{Exp: exp},
	})

	_, ok, err := LoadLease(base, "epochload")
	if err == nil || ok {
		t.Fatalf("LoadLease with an empty lease_id must fail even when exp has passed, got ok=%v err=%v", ok, err)
	}
}
