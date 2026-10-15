// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the quorum-write fixture. NOT generated: run.sh copies this
// file into the generated per-store disk helpers package (s2sm/diskstore) before
// `go test ./...`.
//
// What it pins: quorumvault.CredPair declares (spec.quorum_write) on two fields,
// one with the default floor (2) and one with min_durable_copies 3. A row is
// written whole, so the generated gate must hold the STRICTEST floor, 3, and
// refuse — FAILED_PRECONDITION, before any local effect — a write that can reach
// fewer durable copies. This executes the generated RequireCredPairQuorum, not
// the chassis gate alone.

package diskstores

import (
	"errors"
	"testing"

	diskstore "gitlab.com/example/dummy/quorumvault/tacplus/model/chassis/diskstore"
)

func TestRequireCredPairQuorumHoldsTheStrictestFloor(t *testing.T) {
	if MinDurableCopiesCredPair != 3 {
		t.Fatalf("MinDurableCopiesCredPair = %d, want 3 (the strictest declared floor)", MinDurableCopiesCredPair)
	}
	err := RequireCredPairQuorum(2)
	var dse *diskstore.DiskStoreError
	if !errors.As(err, &dse) || dse.Code != 9 {
		t.Fatalf("2 durable copies against a floor of 3 must be FAILED_PRECONDITION, got %v", err)
	}
	if err := RequireCredPairQuorum(3); err != nil {
		t.Fatalf("3 durable copies must pass, got %v", err)
	}
}
