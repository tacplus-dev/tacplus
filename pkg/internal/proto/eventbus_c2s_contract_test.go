// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func prepareEventBusC2SContractFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/eventbus_c2s_contract")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := AppendEventBusWrappersToProtos(dir, dir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}
	if _, err := EnsureEventBusSupportProto(dir, EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := AppendStateServiceToProtos(dir, dir); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}
	return dir
}

func findService(t *testing.T, fd *parser.FileDescriptor, name string) parser.ServiceDescriptor {
	t.Helper()
	for _, svc := range fd.Services {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("generated artifact has no service %s", name)
	return parser.ServiceDescriptor{}
}

func findMethod(t *testing.T, svc parser.ServiceDescriptor, name string) parser.MethodDescriptor {
	t.Helper()
	for _, method := range svc.Methods {
		if method.Name == name {
			return method
		}
	}
	t.Fatalf("generated service %s has no method %s", svc.Name, name)
	return parser.MethodDescriptor{}
}

func hasMethod(svc parser.ServiceDescriptor, name string) bool {
	for _, method := range svc.Methods {
		if method.Name == name {
			return true
		}
	}
	return false
}

func TestRecordC2SGeneratesRuntimeEventBusContractArtifact(t *testing.T) {
	dir := prepareEventBusC2SContractFixture(t)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("parse generated proto tree: %v", err)
	}
	fd, err := dp.GetFileDescriptor("services/tasks.proto")
	if err != nil {
		t.Fatalf("get generated tasks descriptor: %v", err)
	}

	svc := findService(t, fd, "DelegationProbeBusStateService")
	if !svc.IsC2S || svc.IsS2S {
		t.Fatalf("DelegationProbeBusStateService surface = c2s:%v s2s:%v, want c2s only", svc.IsC2S, svc.IsS2S)
	}
	if svc.EventLabel != "eventbus" {
		t.Fatalf("DelegationProbeBusStateService event_label = %q, want eventbus", svc.EventLabel)
	}

	publish := findMethod(t, svc, "PublishToStream")
	if publish.InputType != "tasks.DelegationProbe" || publish.OutputType != "eventbus_support.PublishResponse" {
		t.Fatalf("PublishToStream types = %s -> %s, want tasks.DelegationProbe -> eventbus_support.PublishResponse", publish.InputType, publish.OutputType)
	}
	if !publish.ClientStreaming || !publish.ServerStreaming {
		t.Fatalf("PublishToStream streaming = client:%v server:%v, want BIDI", publish.ClientStreaming, publish.ServerStreaming)
	}
	if !slices.Contains(fd.GrpcC2SClients, "tasks.DelegationProbeBusStateService") {
		t.Fatalf("generated grpc_c2s_client inputs = %v, want tasks.DelegationProbeBusStateService", fd.GrpcC2SClients)
	}
	if hasMethod(svc, "SubscribeToStream") {
		t.Fatal("record c2s producer declaration exposed SubscribeToStream through the ordinary C2S service")
	}

	generated, err := os.ReadFile(filepath.Join(dir, "services", "tasks.proto"))
	if err != nil {
		t.Fatalf("read generated tasks.proto: %v", err)
	}
	if strings.Contains(string(generated), "TopicPublishReceipt") {
		t.Fatalf("generated EventBus contract retained false TopicPublishReceipt declaration:\n%s", generated)
	}
}

func TestInternalOnlyEventDoesNotGenerateC2SSurfaceArtifact(t *testing.T) {
	dir := prepareEventBusC2SContractFixture(t)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("parse generated proto tree: %v", err)
	}
	fd, err := dp.GetFileDescriptor("services/tasks.proto")
	if err != nil {
		t.Fatalf("get generated tasks descriptor: %v", err)
	}

	svc := findService(t, fd, "InternalAuditBusStateService")
	if svc.IsC2S {
		t.Fatal("internal-only InternalAudit gained a C2S service")
	}
	if slices.Contains(fd.GrpcC2SClients, "tasks.InternalAuditBusStateService") {
		t.Fatalf("internal-only InternalAudit gained a C2S client input: %v", fd.GrpcC2SClients)
	}
	for _, method := range []string{"PublishToStream", "SubscribeToStream"} {
		m := findMethod(t, svc, method)
		if !m.ClientStreaming || !m.ServerStreaming {
			t.Fatalf("internal-only %s streaming = client:%v server:%v, want existing BIDI S2S surface", method, m.ClientStreaming, m.ServerStreaming)
		}
	}
}

func TestDualSurfaceEventAddsC2SPublishWithoutSubscribe(t *testing.T) {
	dir := prepareEventBusC2SContractFixture(t)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("parse generated proto tree: %v", err)
	}
	fd, err := dp.GetFileDescriptor("services/tasks.proto")
	if err != nil {
		t.Fatalf("get generated tasks descriptor: %v", err)
	}

	svc := findService(t, fd, "DualSurfaceEventBusStateService")
	if !svc.IsC2S || svc.IsS2S {
		t.Fatalf("DualSurfaceEventBusStateService surface = c2s:%v s2s:%v, want ordinary C2S publish descriptor", svc.IsC2S, svc.IsS2S)
	}
	publish := findMethod(t, svc, "PublishToStream")
	if publish.InputType != "tasks.DualSurfaceEvent" || publish.OutputType != "eventbus_support.PublishResponse" || !publish.ClientStreaming || !publish.ServerStreaming {
		t.Fatalf("dual-surface PublishToStream = %s -> %s, client:%v server:%v; want tasks.DualSurfaceEvent -> eventbus_support.PublishResponse BIDI", publish.InputType, publish.OutputType, publish.ClientStreaming, publish.ServerStreaming)
	}
	if hasMethod(svc, "SubscribeToStream") {
		t.Fatal("dual-surface event exposed SubscribeToStream through the ordinary C2S service")
	}
	if !slices.Contains(fd.GrpcC2SClients, "tasks.DualSurfaceEventBusStateService") {
		t.Fatalf("generated grpc_c2s_client inputs = %v, want dual-surface C2S publish client", fd.GrpcC2SClients)
	}

	publishers, err := boundaries.CollectActorEventPublisherData(dp, dir, []string{"services/tasks.proto"}, "tasks")
	if err != nil {
		t.Fatalf("collect dual-surface S2S publisher data: %v", err)
	}
	foundS2S := false
	for _, publisher := range publishers {
		if publisher.TopicRef != "tasks.DualSurfaceEventBus.stream" {
			continue
		}
		foundS2S = true
		if publisher.PublishFullMethod != "tasks.DualSurfaceEventBusStateService/PublishToStream" || publisher.PublishPath != "/tasks.DualSurfaceEventBusStateService/PublishToStream" {
			t.Fatalf("dual-surface S2S publisher path = %q (%q), want canonical per-topic PublishToStream", publisher.PublishFullMethod, publisher.PublishPath)
		}
	}
	if !foundS2S {
		t.Fatal("dual-surface event lost its generated S2S publisher path")
	}
}
