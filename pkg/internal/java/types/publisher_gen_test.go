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
		BrokerLabel:         "contractfixtures",
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
	// Per-topic: the subscribe path the generated proto declares. The broker
	// tag no longer appears in the METHOD name and must not — the per-topic
	// service is declared in the topic's own package whichever broker hosts
	// it. The declared tag still selects the CONNECTION: handler_gen resolves
	// TargetService via ServiceHealthState, which is the routing decision.
	// This test therefore still catches a subscriber wired to the wrong
	// topic, and no longer asserts a name no proto declares.
	if !strings.Contains(rendered, `private static final String FULL_METHOD = "alpha.SharedEventBusStateService/SubscribeToEventStream";`) {
		t.Fatalf("subscriber did not use declared broker service:\n%s", rendered)
	}
	if strings.Contains(rendered, `EventBusTypedService`) {
		t.Fatalf("subscriber fell back to hard-coded eventbus broker:\n%s", rendered)
	}
}
