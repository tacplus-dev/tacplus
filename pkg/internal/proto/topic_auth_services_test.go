// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// A record's declared (spec.record).s2s.auth_services is the topic's caller
// policy. Before this pin the synthesized <Event>Bus state service carried only
// the service-level list, which stateServiceAuthServices resolves from the
// owning PACKAGE — so a record declaring ["*"] shipped a topic policy of
// ["<package>"], and a legitimate cross-package publisher was denied once the
// per-topic authorizer started enforcing it.
func TestTopicRPCsCarryDeclaredS2SAuthServices(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2s:      &model.EventBusMethodS2S{AuthServices: []string{"*"}},
			declared: true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces, nil)

	want := "rpc PublishToEvents(stream DemoEvent) returns (stream eventbus_support.PublishResponse) {\n\t\toption (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: [\"*\"]};\n\t}"
	if !strings.Contains(result.serviceProto, want) {
		t.Fatalf("publish RPC missing declared auth_services %q in:\n%s", want, result.serviceProto)
	}

	// The declaration is documented as the PRODUCER surface ("exposes the
	// record's publish RPC to internal services"), so the consumer RPC keeps the
	// service-level default rather than inheriting a policy nobody declared.
	if !strings.Contains(result.serviceProto,
		"rpc SubscribeToEvents(stream eventbus_support.SubscribeRequest) returns (stream DemoEventBusEventsSubscribeRecord);") {
		t.Fatalf("subscribe RPC must carry no per-RPC auth option:\n%s", result.serviceProto)
	}
}

// An explicit narrow list is propagated verbatim: the fix stops the package-name
// substitution, it does not change what a declared list means.
func TestTopicRPCsPropagateNarrowS2SAuthServicesVerbatim(t *testing.T) {
	stateRoot := topicStateRootForTest()
	surfaces := map[string]stateEventSurface{
		"DemoEvent": {
			s2s:      &model.EventBusMethodS2S{AuthServices: []string{"scotty", "argus"}},
			declared: true,
		},
	}
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, surfaces, nil)

	want := "option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: [\"scotty\", \"argus\"]};"
	if strings.Count(result.serviceProto, want) != 1 {
		t.Fatalf("expected the declared list verbatim on the publish RPC, got:\n%s", result.serviceProto)
	}
}

// A topic whose payload declares no s2s surface keeps the service-level default.
// TraceEventBus is that case on the live platform: its payload is trace.TraceEvent,
// which carries no spec.event/spec.record annotation.
func TestTopicWithoutDeclaredS2SSurfaceKeepsServiceLevelDefault(t *testing.T) {
	stateRoot := topicStateRootForTest()
	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name,
		nil, []string{"demo"}, nil, nil)

	if strings.Contains(result.serviceProto, "option (spec.rpc)") {
		t.Fatalf("undeclared topic must emit no per-RPC auth option:\n%s", result.serviceProto)
	}
	if !strings.Contains(result.serviceProto, "option (spec.service_s2s) = {auth_services: [\"demo\"]};") {
		t.Fatalf("undeclared topic must keep the service-level default:\n%s", result.serviceProto)
	}
}
