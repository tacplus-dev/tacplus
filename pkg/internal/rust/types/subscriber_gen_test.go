// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// The Rust subscriber must expose SubscribeOptions and subscribe_with so
// handlers can control the start position (offset, latest, earliest).
func TestRustSubscriberExposesSubscribeOptions(t *testing.T) {
	tmplStr, err := loadTemplate("subscriber.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"pub struct SubscribeOptions",
		"pub fn earliest() -> Self",
		"pub fn latest() -> Self",
		"pub fn offset(off: i64) -> Self",
		"pub async fn subscribe_with(&self, handler: Handler, opts: SubscribeOptions)",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust subscriber must expose SubscribeOptions; missing %q", want)
		}
	}
}

// The Rust ServiceManager must expose audience-scoped token acquisition so
// handlers can build custom subscribers without duplicating the identity plane.
func TestRustServiceManagerExposesSubscriberPlumbing(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"pub async fn token_for_service(&self, audience: &str)",
		"pub fn token_provider_for_service(&self, audience: &str) -> TokenProvider",
		"pub async fn resolve_service(&self, service_name: &str) -> Channel",
		"pub async fn create_channel(&self, target: &str)",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust ServiceManager must expose subscriber plumbing; missing %q", want)
		}
	}
}

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
