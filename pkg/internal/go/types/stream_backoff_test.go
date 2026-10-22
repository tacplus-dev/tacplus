// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// The cluster streams (Log, Subscribe, WatchJwks) must reset their reconnect
// backoff on the first DELIVERED frame, never on the stream open.
//
// Opening a server-streaming RPC does not contact the server: the client call
// returns a stream handle with a nil error even when the method does not exist
// on the far side. The failure appears on the first Recv. So resetting the
// backoff right after the open resets it on every attempt, and the delay never
// grows past streamInitialBackoff.
//
// That is not theoretical. kernel serves ClusterService but never implemented
// WatchJwks, so every Go service reconnected roughly once per second forever --
// graphdb and opa each logged ~213 warnings per four minutes. The same shape in
// the Log and Subscribe loops would turn any kernel outage into a fleet-wide
// 1/sec retry storm, which is precisely what the backoff exists to prevent.
func TestClusterStreamsResetBackoffOnDeliveryNotOnOpen(t *testing.T) {
	src, err := templates.FS.ReadFile("go/types/servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("read servicemanager_lib template: %v", err)
	}
	tmpl := string(src)

	// Each of the three loops guards the reset behind a first-delivery flag.
	resets := strings.Count(tmpl, "delivered = true\n\t\t\t\t\tbackoff = streamInitialBackoff")
	if resets != 3 {
		t.Errorf("expected all 3 cluster streams (Log, Subscribe, WatchJwks) to reset backoff "+
			"on first delivery, found %d", resets)
	}
	if n := strings.Count(tmpl, "delivered := false"); n != 3 {
		t.Errorf("expected 3 first-delivery flags, found %d", n)
	}

	// The regression is a bare reset sitting between the open and the Recv loop.
	if strings.Contains(tmpl, "\t\t\tbackoff = streamInitialBackoff\n\t\t\tfor {") {
		t.Error("a cluster stream resets its backoff immediately after opening the stream; " +
			"the open always succeeds, so the reconnect delay can never grow")
	}

	// UNIMPLEMENTED cannot be fixed by reconnecting, so it must be said plainly
	// rather than hidden behind the generic "stream ended" line.
	if !strings.Contains(tmpl, "status.Code(err) == codes.Unimplemented") {
		t.Error("WatchJwks must distinguish UNIMPLEMENTED; reconnecting cannot fix a method " +
			"the server does not have, and the generic message hid that for the whole fleet")
	}
}
