// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// #32: a stream that ends with OK reset the backoff and looped straight back to
// channelSupplier.get() with no sleep, so a peer that closes every stream
// cleanly drove a re-resolve per round trip. The clean-close branch must sleep
// the minimum backoff before it reconnects, as go and rust do.
// cross-test/subscriber-clean-close exercises the behavior.
func TestJavaSubscriberCleanCloseWaitsBeforeReconnect(t *testing.T) {
	sub := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")

	start := strings.Index(sub, "// Stream ended cleanly")
	if start < 0 {
		t.Fatal("Subscriber must keep the clean-close branch marked \"Stream ended cleanly\"")
	}
	branch := sub[start:]
	end := strings.Index(branch, "backoff = MIN_BACKOFF_MS;")
	if end < 0 {
		t.Fatal("clean-close branch must reset backoff to MIN_BACKOFF_MS")
	}
	if !strings.Contains(branch[:end], "Thread.sleep({{$.ReconnectBackoffFQN}}.jitterMs(MIN_BACKOFF_MS));") {
		t.Fatalf("clean-close branch must sleep the minimum backoff before it reconnects:\n%s", branch[:end])
	}
}
