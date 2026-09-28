// Copyright (c) 2026 Second Star Cavalier LLC

package boundaries

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

func TestIsTraceEventPublisherDataRequiresCanonicalFQN(t *testing.T) {
	if !IsTraceEventPublisherData(EventPublisherData{EventTypeFQN: TraceEventTypeFQN, EventTypeName: "TraceEvent"}) {
		t.Fatal("canonical trace.TraceEvent publisher should be recognized")
	}
	if IsTraceEventPublisherData(EventPublisherData{EventTypeFQN: "audit.TraceEvent", EventTypeName: "TraceEvent"}) {
		t.Fatal("bare TraceEvent simple name must not be enough to select the trace publisher")
	}
}

func TestActorOwnsFileByDeclaredServiceName(t *testing.T) {
	fd := &parser.FileDescriptor{
		Package:     "contractclient",
		ServiceName: "contract",
	}
	if !actorOwnsFile(fd, "contract") {
		t.Fatal("file-level (spec.service).name must establish actor ownership when proto package differs")
	}
}

// PC-C-6: actorOwnsFile is the ownership filter that keeps an IMPORTED topic
// proto from turning the importer into a producer. An event_publish/subscribe
// annotation only binds when the file is owned — same proto package, declared
// (spec.service).name, or a hosting vservice tag — never merely because the
// actor imports the file.
func TestActorOwnsFileRejectsImportedForeignProto(t *testing.T) {
	// A foreign topic proto: different package, its own unrelated service, no
	// vservice host tag matching the importer.
	imported := &parser.FileDescriptor{
		Package:     "eventbus",
		ServiceName: "eventbus",
		Services:    []parser.ServiceDescriptor{{Name: "EventBusService", HostLabel: ""}},
	}
	if actorOwnsFile(imported, "contract") {
		t.Fatal("importing a foreign topic proto must NOT make the importer its producer/owner")
	}
}

// The selfPackage=="" fallback is owns-all: when the caller does not scope to a
// package (whole-build passes), every file is in scope. Pinned so a change to
// the fallback is caught.
func TestActorOwnsFileEmptySelfPackageOwnsAll(t *testing.T) {
	foreign := &parser.FileDescriptor{Package: "somethingelse", ServiceName: "other"}
	if !actorOwnsFile(foreign, "") {
		t.Fatal("an empty selfPackage is the owns-all whole-build fallback")
	}
}

// A vservice hosted BY the actor is owned via the host tag, even though the
// proto package differs — the complement of the imported-foreign case.
func TestActorOwnsFileByVserviceHostTag(t *testing.T) {
	hosted := &parser.FileDescriptor{
		Package:  "topics",
		Services: []parser.ServiceDescriptor{{Name: "MyTopics", HostLabel: "contract"}},
	}
	if !actorOwnsFile(hosted, "contract") {
		t.Fatal("a vservice whose host tag is the actor must be owned")
	}
}

// MS-3: (spec.subscribe) on a non-state message must return an error, not
// silently drop the subscription.
func TestCollectMessageSubscriptionsRejectsNonStateSubscribe(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name: "MyMessage",
		Annotations: map[string]model.AnnotationValue{
			"subscribe": {
				SubscriptionEntries: []model.SubscriptionEntry{
					{Source: "pkg.SomeState.some_field"},
				},
			},
			// Deliberately NO "service_state" annotation.
		},
	}
	subs, err := CollectMessageSubscriptions(msg)
	if err == nil {
		t.Fatal("expected error for subscribe without service_state, got nil")
	}
	if !strings.Contains(err.Error(), "requires (spec.service_state)") {
		t.Fatalf("error message should mention service_state requirement, got: %s", err.Error())
	}
	if subs != nil {
		t.Fatal("expected nil subscriptions on error")
	}
}

// MS-3: a message with both subscribe and service_state should still succeed.
func TestCollectMessageSubscriptionsAcceptsStateSubscribe(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name: "MyState",
		Annotations: map[string]model.AnnotationValue{
			"service_state": {StringValue: "MyState"},
			"subscribe": {
				SubscriptionEntries: []model.SubscriptionEntry{
					{Source: "pkg.OtherState.some_field"},
				},
			},
		},
	}
	subs, err := CollectMessageSubscriptions(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
}

// MS-3: a message with no subscribe annotation should return nil, nil.
func TestCollectMessageSubscriptionsNoSubscribeReturnsNil(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name:        "PlainMessage",
		Annotations: map[string]model.AnnotationValue{},
	}
	subs, err := CollectMessageSubscriptions(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subs != nil {
		t.Fatal("expected nil subscriptions for message without subscribe")
	}
}

// privateStoreSourceMessage is a record message as the parser hands it to
// collectStoreSourceFQNs: private is the "record_private" annotation (never bare
// "private"), and the subscribe grant rides EventPublishEntries[0] of
// "event_surface". grant nil means the record declares no s2s_subscribe at all.
func privateStoreSourceMessage(private bool, grant *model.EventBusMethodS2S) parser.MessageDescriptor {
	anns := map[string]model.AnnotationValue{
		"record": {StringValue: "secret_id"},
	}
	if private {
		anns["record_private"] = model.AnnotationValue{BoolValue: true}
	}
	if grant != nil {
		anns["event_surface"] = model.AnnotationValue{
			EventPublishEntries: []model.EventBusBindingEntry{{MethodS2SSubscribe: grant}},
		}
	}
	return parser.MessageDescriptor{Name: "VaultSecretRow", Annotations: anns}
}

// A PRIVATE record is a store-ingest source only for the stores its
// s2s_subscribe actually names. Deriving one for a store the grant omits wires
// that store to a topic it is fail-closed-denied on at runtime — a subscriber
// that compiles, runs, and silently receives nothing.
func TestPrivateRecordGrantsStoreOnlyNamedStores(t *testing.T) {
	granted := &model.EventBusMethodS2S{AuthServices: []string{"egressstratum"}}

	if !privateRecordGrantsStore(privateStoreSourceMessage(true, granted), "egressstratum") {
		t.Fatal("a private record naming this store must derive an ingest source for it")
	}
	if privateRecordGrantsStore(privateStoreSourceMessage(true, granted), "egressvault") {
		t.Fatal("a private record NOT naming this store must not derive an ingest source for it")
	}
	if privateRecordGrantsStore(privateStoreSourceMessage(true, nil), "egressstratum") {
		t.Fatal("a private record declaring no s2s_subscribe authorizes no store, so derives no ingest source")
	}
}

// A NON-private record is unrestricted: every record store in the build ingests
// it, exactly as before. The grant plays no part in its derivation.
func TestPrivateRecordGrantsStoreLeavesNonPrivateAlone(t *testing.T) {
	if !privateRecordGrantsStore(privateStoreSourceMessage(false, nil), "egressstratum") {
		t.Fatal("a non-private record must remain a source for every store, grant or no grant")
	}
	granted := &model.EventBusMethodS2S{AuthServices: []string{"somewhereelse"}}
	if !privateRecordGrantsStore(privateStoreSourceMessage(false, granted), "egressstratum") {
		t.Fatal("a non-private record's s2s_subscribe must not narrow which stores ingest it")
	}
}
