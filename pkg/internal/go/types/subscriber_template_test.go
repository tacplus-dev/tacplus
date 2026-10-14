// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestGoSubscriberDecodesRecordMetaEnvelope(t *testing.T) {
	rendered, err := loadTemplate("subscriber.go.tmpl")
	if err != nil {
		t.Fatalf("load subscriber template: %v", err)
	}

	for _, want := range []string{
		"decodeSubscribeRecord(data)",
		"proto.Unmarshal(metaBytes, r.Meta)",
		"proto.Unmarshal(payloadBytes, r.Payload)",
		"legacy raw {{.TypeName}} payload",
		"r.Meta = &eb.RecordMeta{}",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go subscriber must decode metadata-bearing EventBus subscribe envelopes; missing %q", want)
		}
	}
	if strings.Contains(rendered, "RecordMeta is not on the wire") ||
		strings.Contains(rendered, "it is always nil after Unmarshal") {
		t.Fatalf("Go subscriber must not pin raw-payload-only metadata behavior")
	}
}

func TestGoSubscriberUsesDeclaredBrokerService(t *testing.T) {
	outDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:      outDir,
		ModuleBasePath: "example.com/project/tacplus",
		Subscriptions: []tactype.SubscriptionEntry{{
			SourcePackage:   "alpha",
			SourceStateRoot: "SharedEventBus",
			SourceField:     "event_stream",
			TargetService:   "events",
			SourceTypeFqn:   "alpha.Event",
			SourceProtoDir:  "services",
		}},
	}

	if err := generateGoSubscribers(cfg); err != nil {
		t.Fatalf("generateGoSubscribers: %v", err)
	}

	path := filepath.Join(outDir, "subscribe", "alpha", "SharedEventBus", "event_stream", "subscriber.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated subscriber: %v", err)
	}
	rendered := string(data)
	// Per-topic: the subscribe path the generated proto declares. The broker
	// tag no longer appears in the METHOD name and must not — the per-topic
	// service is declared in the topic's own package whichever broker hosts
	// it. The declared tag still selects the CONNECTION: handler_gen resolves
	// TargetService via ServiceHealthState, which is the routing decision.
	// This test therefore still catches a subscriber wired to the wrong
	// topic, and no longer asserts a name no proto declares.
	if !strings.Contains(rendered, `fullMethod := "/alpha.SharedEventBusStateService/SubscribeToEventStream"`) {
		t.Fatalf("subscriber did not use declared broker service:\n%s", rendered)
	}
	if strings.Contains(rendered, `EventBusTypedService`) {
		t.Fatalf("subscriber fell back to hard-coded eventbus broker:\n%s", rendered)
	}
}
