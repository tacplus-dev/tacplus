// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

func TestJavaPublisherEntryUsesDeclaredBrokerMethod(t *testing.T) {
	cfg := &tactype.Config{
		JavaPackageBase: "com.example",
		SelfPackage:     "contractfixtures",
	}
	entry := javaPublisherEntryFromData(cfg, "", boundaries.EventPublisherData{
		TopicRef:          "contractfixtures.FixtureState.events",
		BrokerTag:         "contractfixtures",
		EventTypeFQN:      "contractfixtures.FixtureEvent",
		EventTypeName:     "FixtureEvent",
		RPCStem:           "ContractfixturesFixtureStateEvents",
		PublishFullMethod: "contractfixtures.EventBusTypedService/PublishContractfixturesFixtureStateEvents",
	})

	if entry.MethodName != "createContractfixturesFixtureStateEventsPublisher" {
		t.Fatalf("MethodName = %q", entry.MethodName)
	}
	if entry.TargetService != "contractfixtures" {
		t.Fatalf("TargetService = %q", entry.TargetService)
	}
	if entry.PublishFullMethod != "contractfixtures.EventBusTypedService/PublishContractfixturesFixtureStateEvents" {
		t.Fatalf("PublishFullMethod = %q", entry.PublishFullMethod)
	}
	if entry.TypeName != "FixtureEvent" {
		t.Fatalf("TypeName = %q", entry.TypeName)
	}
	if entry.TypeImport != "com.example.types.services.contractfixtures.Contractfixtures" {
		t.Fatalf("TypeImport = %q", entry.TypeImport)
	}
}

func TestJavaSubscriberUsesDeclaredBrokerService(t *testing.T) {
	outDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:       outDir,
		JavaPackageBase: "com.example",
		Subscriptions: []tactype.SubscriptionEntry{{
			SourcePackage:   "alpha",
			SourceStateRoot: "SharedEventBus",
			SourceField:     "event_stream",
			TargetService:   "events",
			SourceTypeFqn:   "alpha.Event",
			SourceProtoDir:  "services",
		}},
	}

	if err := generateJavaSubscribers(cfg, ""); err != nil {
		t.Fatalf("generateJavaSubscribers: %v", err)
	}

	path := filepath.Join(outDir, "src", "main", "java", "com", "example", "s2sm", "subscribe", "AlphaSharedEventBusEventStreamSubscriber.java")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated subscriber: %v", err)
	}
	rendered := string(data)
	if !strings.Contains(rendered, `private static final String FULL_METHOD = "events.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream";`) {
		t.Fatalf("subscriber did not use declared broker service:\n%s", rendered)
	}
	if strings.Contains(rendered, `"eventbus.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream"`) {
		t.Fatalf("subscriber fell back to hard-coded eventbus broker:\n%s", rendered)
	}
}
