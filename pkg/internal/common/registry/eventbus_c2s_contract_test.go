// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/encoding/protojson"
)

func generateEventBusC2SRegistryArtifact(t *testing.T) *spec.PackageNode {
	t.Helper()

	protoDir := t.TempDir()
	if err := os.CopyFS(protoDir, os.DirFS("../../proto/testdata/eventbus_c2s_contract")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	if err := protogen.AppendEventBusWrappersToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}
	if _, err := protogen.EnsureEventBusSupportProto(protoDir, protogen.EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := protogen.AppendStateServiceToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("parse generated proto tree: %v", err)
	}
	paths := []string{"services/eventbus.proto", "services/tasks.proto"}
	sort.Strings(paths)
	parsed := make([]model.ProtoFile, 0, len(paths))
	for _, path := range paths {
		pf, err := dp.ConvertToProtoFile(path)
		if err != nil {
			t.Fatalf("ConvertToProtoFile(%s): %v", path, err)
		}
		parsed = append(parsed, pf)
	}

	outDir := t.TempDir()
	if err := Generate(Config{OutputDir: outDir, InputDir: protoDir}, parsed, dp); err != nil {
		t.Fatalf("Generate registry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "registry.json"))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	var root spec.PackageNode
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &root); err != nil {
		t.Fatalf("parse registry.json: %v", err)
	}
	return &root
}

func registryService(root *spec.PackageNode, name string) *spec.ServiceInfo {
	if root == nil {
		return nil
	}
	for _, svc := range root.Services {
		if svc.Name == name {
			return svc
		}
	}
	for _, child := range root.Children {
		if svc := registryService(child, name); svc != nil {
			return svc
		}
	}
	return nil
}

func registryGrant(root *spec.PackageNode, token string) *spec.EntitlementGrantInfo {
	for _, grant := range root.GetEntitlementGrants() {
		if grant.GetToken() == token {
			return grant
		}
	}
	return nil
}

func TestRecordC2SGeneratedRegistryUsesOrdinaryMethodCatalog(t *testing.T) {
	root := generateEventBusC2SRegistryArtifact(t)
	svc := registryService(root, "DelegationProbeBusStateService")
	if svc == nil {
		t.Fatal("registry.json has no DelegationProbeBusStateService")
	}
	if len(svc.Rpcs) != 1 {
		t.Fatalf("DelegationProbeBusStateService RPCs = %v, want publish only", svc.Rpcs)
	}
	publish := svc.Rpcs[0]
	if publish.Rpc != "PublishToStream" {
		t.Fatalf("DelegationProbeBusStateService RPCs = %v, want PublishToStream", svc.Rpcs)
	}
	if publish.AuthModel != "c2s" || !publish.ClientStreaming || !publish.ServerStreaming {
		t.Fatalf("PublishToStream catalog = auth:%q client_stream:%v server_stream:%v, want c2s BIDI", publish.AuthModel, publish.ClientStreaming, publish.ServerStreaming)
	}
	if publish.InputType != "tasks.DelegationProbe" || publish.OutputType != "eventbus_support.PublishResponse" {
		t.Fatalf("PublishToStream catalog types = %s -> %s", publish.InputType, publish.OutputType)
	}
	if publish.GrpcPath != "/tasks.DelegationProbeBusStateService/PublishToStream" || publish.HttpPath == "" {
		t.Fatalf("PublishToStream routes = grpc:%q http:%q", publish.GrpcPath, publish.HttpPath)
	}
	if registryGrant(root, "TASKS_DELEGATION_PROBE_BUS_STATE_SERVICE_PUBLISH_TO_STREAM") == nil {
		t.Fatal("registry.json entitlement catalog has no exact DelegationProbe publish grant")
	}
	if registryGrant(root, "TASKS_DELEGATION_PROBE_BUS_STATE_SERVICE_SUBSCRIBE_TO_STREAM") != nil {
		t.Fatal("record c2s producer declaration granted SubscribeToStream in the ordinary C2S entitlement catalog")
	}
	if registryGrant(root, "EVENTBUS_DELEGATION_PROBE_BUS_STATE_SERVICE_SUBSCRIBE_TO_STREAM") != nil {
		t.Fatal("record c2s producer declaration granted an EventBus alias for SubscribeToStream")
	}
}

func TestInternalOnlyEventIsAbsentFromC2SCatalogArtifact(t *testing.T) {
	root := generateEventBusC2SRegistryArtifact(t)
	svc := registryService(root, "InternalAuditBusStateService")
	if svc == nil {
		t.Fatal("registry.json has no InternalAuditBusStateService control service")
	}
	for _, rpc := range svc.Rpcs {
		if rpc.AuthModel == "c2s" {
			t.Fatalf("internal-only RPC %s gained C2S auth in registry.json", rpc.Rpc)
		}
	}
	if registryGrant(root, "TASKS_INTERNAL_AUDIT_BUS_STATE_SERVICE_PUBLISH_TO_STREAM") != nil {
		t.Fatal("internal-only publish method gained a C2S entitlement catalog entry")
	}
}

func TestDualSurfaceEventPreservesS2SPublisherWhileAddingC2SPublish(t *testing.T) {
	root := generateEventBusC2SRegistryArtifact(t)
	svc := registryService(root, "DualSurfaceEventBusStateService")
	if svc == nil || len(svc.Rpcs) != 1 || svc.Rpcs[0].Rpc != "PublishToStream" || svc.Rpcs[0].AuthModel != "c2s" {
		t.Fatalf("dual-surface ordinary catalog service = %v, want one C2S PublishToStream", svc)
	}
	if registryGrant(root, "TASKS_DUAL_SURFACE_EVENT_BUS_STATE_SERVICE_SUBSCRIBE_TO_STREAM") != nil {
		t.Fatal("dual-surface c2s producer declaration granted SubscribeToStream")
	}
	if registryGrant(root, "EVENTBUS_DUAL_SURFACE_EVENT_BUS_STATE_SERVICE_SUBSCRIBE_TO_STREAM") != nil {
		t.Fatal("dual-surface c2s producer declaration granted an EventBus alias for SubscribeToStream")
	}

	foundS2S := false
	var walk func(*spec.PackageNode)
	walk = func(node *spec.PackageNode) {
		if node == nil {
			return
		}
		for _, publish := range node.EventPublishes {
			if publish.TopicRef == "tasks.DualSurfaceEventBus.stream" && publish.AuthModel == "s2s" && publish.BrokerLabel == "eventbus" {
				foundS2S = true
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	if !foundS2S {
		t.Fatal("registry.json lost the explicit S2S publisher edge for the dual-surface event")
	}
}
