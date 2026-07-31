// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

func TestRustSubscriberUsesDeclaredBrokerService(t *testing.T) {
	outDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir: outDir,
		Subscriptions: []tactype.SubscriptionEntry{{
			SourcePackage:   "alpha",
			SourceStateRoot: "SharedEventBus",
			SourceField:     "event_stream",
			TargetService:   "events",
			SourceTypeFqn:   "alpha.Event",
			SourceProtoDir:  "services",
		}},
	}

	if err := generateSubscribers(cfg); err != nil {
		t.Fatalf("generateSubscribers: %v", err)
	}

	path := filepath.Join(outDir, "s2sm", "subscribe", "alpha_shared_event_bus_event_stream.rs")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated subscriber: %v", err)
	}
	rendered := string(data)
	if !strings.Contains(rendered, `"/events.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream"`) {
		t.Fatalf("subscriber did not use declared broker service:\n%s", rendered)
	}
	if strings.Contains(rendered, `"/eventbus.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream"`) {
		t.Fatalf("subscriber fell back to hard-coded eventbus broker:\n%s", rendered)
	}
}
