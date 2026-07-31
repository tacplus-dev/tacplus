// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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
	if !strings.Contains(rendered, `fullMethod := "/events.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream"`) {
		t.Fatalf("subscriber did not use declared broker service:\n%s", rendered)
	}
	if strings.Contains(rendered, `"/eventbus.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream"`) {
		t.Fatalf("subscriber fell back to hard-coded eventbus broker:\n%s", rendered)
	}
}
