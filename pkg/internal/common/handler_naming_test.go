// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"
)

// A generated handler method name must never depend on how many OTHER
// subscriptions a service happens to have.
//
// Every emitter used to append the topic stem only when more than one
// subscription shared a field name. That made a method's identity a function of
// its siblings: retiring three unrelated streams renamed the survivor from
// on_event_stream_agentsensor_agent_event_bus_event_stream to on_event_stream,
// and timedb's hand-written build.rs stopped compiling with
// "method ... is not a member of trait `Handler`". Any hand-written impl of a
// generated trait is exposed to that, and the failure appears in a file nobody
// touched, for a change that looks unrelated.
//
// The stem is now always appended. This test pins that across all five
// languages, because the conditional existed in all five and re-introducing it
// in one is exactly how this comes back.
func TestHandlerMethodNamesAreAlwaysTopicQualified(t *testing.T) {
	emitters := []string{
		"../go/types/handler_gen.go",
		"../java/types/handler_gen.go",
		"../python/types/handler_gen.go",
		"../rust/types/handler_gen.go",
		"../typescript/types/handler_gen.go",
	}
	for _, path := range emitters {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(src)

		// The sibling count is what made the name unstable. It must be gone
		// entirely — not merely unused.
		if strings.Contains(body, "fieldCount") {
			t.Errorf("%s still counts sibling subscriptions; a handler name that depends on "+
				"its siblings renames itself when an unrelated stream is added or removed", path)
		}
		if strings.Contains(body, "Disambiguate handler method name when multiple subscriptions") {
			t.Errorf("%s still carries the disambiguate-only-on-collision comment", path)
		}
		// And the stem must actually be in the name.
		if !strings.Contains(body, "RPCStemForTopicParts") {
			t.Errorf("%s no longer derives the topic stem; the name cannot be qualified without it", path)
		}
	}
}
