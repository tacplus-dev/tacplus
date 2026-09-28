// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// A synthesized <Event>Bus wrapper hosts BOTH halves of a topic, and until
// (spec.record).s2s_subscribe existed only the publish half was declarable. The
// subscribe half could therefore only ever get the state service's service-level
// list, which stateServiceAuthServices resolves from the owning PACKAGE — so the
// record store, which is in no record's package, was denied on every topic it
// exists to read the moment per-topic authorization went live.
func TestTopicSubscribeRPCCarriesDeclaredS2SSubscribeAuthServices(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2s:          &model.EventBusMethodS2S{AuthServices: []string{"*"}},
			s2sSubscribe: &model.EventBusMethodS2S{AuthServices: []string{"stratum"}},
			declared:     true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces)

	want := "rpc SubscribeToEvents(stream eventbus_support.SubscribeRequest) returns (stream DemoEventBusEventsSubscribeRecord) {\n\t\toption (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: [\"stratum\"]};\n\t}"
	if !strings.Contains(result.serviceProto, want) {
		t.Fatalf("subscribe RPC missing declared s2s_subscribe auth_services %q in:\n%s", want, result.serviceProto)
	}
}

// The two halves are independent: a record may be published by anyone and read
// by exactly one service. Propagating the producer list onto the consumer RPC
// (or the reverse) would grant a boundary its author never wrote.
func TestTopicPublishAndSubscribeSurfacesAreIndependent(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2s:          &model.EventBusMethodS2S{AuthServices: []string{"*"}},
			s2sSubscribe: &model.EventBusMethodS2S{AuthServices: []string{"stratum"}},
			declared:     true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces)

	publishLine := "rpc PublishToEvents(stream DemoEvent) returns (stream eventbus_support.PublishResponse) {\n\t\toption (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: [\"*\"]};\n\t}"
	if !strings.Contains(result.serviceProto, publishLine) {
		t.Fatalf("publish RPC must keep its own declared list, not the subscribe one:\n%s", result.serviceProto)
	}
	if strings.Count(result.serviceProto, "auth_services: [\"stratum\"]") != 1 {
		t.Fatalf("the subscribe list must appear on exactly one RPC:\n%s", result.serviceProto)
	}
}

// Declaring only the consumer half is legitimate — a record whose publish
// boundary is the package default but whose stream is read by the store.
func TestTopicSubscribeOnlyDeclarationLeavesPublishAtServiceDefault(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2sSubscribe: &model.EventBusMethodS2S{AuthServices: []string{"stratum"}},
			declared:     true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces)

	if !strings.Contains(result.serviceProto,
		"rpc PublishToEvents(stream DemoEvent) returns (stream eventbus_support.PublishResponse);") {
		t.Fatalf("publish RPC must carry no per-RPC option when only s2s_subscribe is declared:\n%s", result.serviceProto)
	}
	if !strings.Contains(result.serviceProto, "auth_services: [\"stratum\"]") {
		t.Fatalf("subscribe RPC must still carry the declared consumer list:\n%s", result.serviceProto)
	}
}

// Absent s2s_subscribe is additive-neutral: the topic generates exactly what it
// generated before the field existed.
func TestTopicWithoutS2SSubscribeKeepsSubscribeAtServiceDefault(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2s:      &model.EventBusMethodS2S{AuthServices: []string{"*"}},
			declared: true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces)

	if !strings.Contains(result.serviceProto,
		"rpc SubscribeToEvents(stream eventbus_support.SubscribeRequest) returns (stream DemoEventBusEventsSubscribeRecord);") {
		t.Fatalf("undeclared consumer surface must emit a bare subscribe RPC:\n%s", result.serviceProto)
	}
}
