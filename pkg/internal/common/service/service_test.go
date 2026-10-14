// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func makeServices() []ServiceInfo {
	return []ServiceInfo{
		{ProtoFile: "services/baseball.proto", ServiceName: "BaseballService", IsGrpc: true},
		{ProtoFile: "services/baseball.proto", ServiceName: "BaseballStateService", IsGrpc: false},
		{ProtoFile: "services/football.proto", ServiceName: "FootballService", IsGrpc: true},
		{ProtoFile: "services/basketball.proto", ServiceName: "BasketballService", IsGrpc: false},
	}
}

func TestFilterServicesByGrpcEntries(t *testing.T) {
	services := makeServices()

	tests := []struct {
		name        string
		grpcServers []tactype.GrpcEntry
		wantCount   int
		wantNames   []string
	}{
		{
			name: "single match",
			grpcServers: []tactype.GrpcEntry{
				{ProtoFile: "services/baseball.proto", ServiceName: "BaseballService"},
			},
			wantCount: 1,
			wantNames: []string{"BaseballService"},
		},
		{
			name: "multiple matches",
			grpcServers: []tactype.GrpcEntry{
				{ProtoFile: "services/baseball.proto", ServiceName: "BaseballService"},
				{ProtoFile: "services/football.proto", ServiceName: "FootballService"},
			},
			wantCount: 2,
			wantNames: []string{"BaseballService", "FootballService"},
		},
		{
			name:        "no matches",
			grpcServers: []tactype.GrpcEntry{{ProtoFile: "services/unknown.proto", ServiceName: "UnknownService"}},
			wantCount:   0,
		},
		{
			name:        "empty grpc servers",
			grpcServers: nil,
			wantCount:   0,
		},
		{
			name: "proto matches but service name doesn't",
			grpcServers: []tactype.GrpcEntry{
				{ProtoFile: "services/baseball.proto", ServiceName: "WrongService"},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterServicesByGrpcEntries(services, tt.grpcServers)
			if len(got) != tt.wantCount {
				t.Errorf("got %d services, want %d", len(got), tt.wantCount)
			}
			for i, name := range tt.wantNames {
				if i < len(got) && got[i].ServiceName != name {
					t.Errorf("service[%d] = %q, want %q", i, got[i].ServiceName, name)
				}
			}
		})
	}
}

func TestResolveGrpcRefsRequiresExistingService(t *testing.T) {
	files := map[string]*parser.FileDescriptor{
		"services/oauth.proto": {
			Package: "oauth",
			Services: []parser.ServiceDescriptor{
				{Name: "Auth"},
				{Name: "Session"},
			},
		},
	}

	got, err := resolveGrpcRefs([]string{"oauth.Auth"}, files)
	if err != nil {
		t.Fatalf("resolveGrpcRefs valid ref error: %v", err)
	}
	if len(got) != 1 || got[0].ProtoFile != "services/oauth.proto" || got[0].ServiceName != "Auth" {
		t.Fatalf("resolveGrpcRefs valid ref = %#v, want services/oauth.proto/Auth", got)
	}

	if _, err := resolveGrpcRefs([]string{"oauth.Token"}, files); err == nil {
		t.Fatal("resolveGrpcRefs missing service error = nil, want error")
	}
}

// A peer may declare a client against a trait's plane before the plane exists:
// the two sibling services are injected into the trait's proto after inference
// runs. The escape hatch resolves them off the DECLARATION — the trait names its
// own services — so nothing here knows what any plane is called.
func TestResolveGrpcRefsResolvesTraitPlaneBeforeInjection(t *testing.T) {
	files := map[string]*parser.FileDescriptor{
		"vservices/query.proto": {
			Package: "taskquery",
			Trait: &parser.TraitDescriptor{
				Name:       "query",
				Host:       "stratum",
				S2SService: "RecordMap",
				C2SService: "RecordQuery",
			},
		},
	}

	got, err := resolveGrpcRefs([]string{"taskquery.RecordMap"}, files)
	if err != nil {
		t.Fatalf("trait plane ref error: %v", err)
	}
	if len(got) != 1 || got[0].ProtoFile != "vservices/query.proto" || got[0].ServiceName != "RecordMap" {
		t.Fatalf("trait plane ref = %#v, want vservices/query.proto/RecordMap", got)
	}

	// Only the names the trait actually declares get the hatch.
	if _, err := resolveGrpcRefs([]string{"taskquery.SomethingElse"}, files); err == nil {
		t.Fatal("undeclared service in a trait package resolved; want error")
	}
}

func TestInferGrpcClientsResolvesEventC2SStateServiceBeforeInjection(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../proto/testdata/eventbus_c2s_contract")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	if err := protogen.AppendEventBusWrappersToProtos(dir, dir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	files := dp.GetAllFiles()
	tasks := files["services/tasks.proto"]
	if tasks == nil {
		t.Fatal("services/tasks.proto not parsed")
	}
	injectedRef := false
	for _, ref := range tasks.GrpcC2SClients {
		if ref == "tasks.DelegationProbeBusStateService" {
			injectedRef = true
			break
		}
	}
	if !injectedRef {
		t.Fatalf("GrpcC2SClients = %#v, want injected DelegationProbeBusStateService ref", tasks.GrpcC2SClients)
	}
	for _, svc := range tasks.Services {
		if svc.Name == "DelegationProbeBusStateService" {
			t.Fatal("DelegationProbeBusStateService already materialized before inference")
		}
	}

	config := &tactype.Config{SelfPackage: "tasks"}
	if err := InferGrpcClientsFromAnnotations(config, dir); err != nil {
		t.Fatalf("InferGrpcClientsFromAnnotations before state injection: %v", err)
	}
	want := tactype.GrpcEntry{
		ProtoFile:   "services/tasks.proto",
		ServiceName: "DelegationProbeBusStateService",
	}
	found := false
	for _, entry := range config.GrpcC2SClients {
		if entry == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("GrpcC2SClients = %#v, want entry %#v", config.GrpcC2SClients, want)
	}

	if _, err := resolveGrpcRefs([]string{"tasks.UnrelatedStateService"}, files); err == nil {
		t.Fatal("unrelated missing service in EventBus package resolved; want error")
	}
}

func TestCollectOwnPackageGrpcAnnotationsMergesPackageFiles(t *testing.T) {
	files := map[string]*parser.FileDescriptor{
		"contract/contract.proto": {
			Package:        "contractclient",
			GrpcS2SClients: []string{"contractclient.ContractS2S"},
			GrpcC2SClients: []string{"contractclient.ContractApi"},
		},
		"services/contractaudit.proto": {
			Package:           "contractclient",
			GrpcW2SClients:    []string{"contractclient.ContractApi"},
			AllGrpcS2SClients: true,
		},
		"services/other.proto": {
			Package:        "other",
			GrpcC2SClients: []string{"other.OtherApi"},
		},
	}

	found, allS2S, s2s, c2s, w2s := collectOwnPackageGrpcAnnotations("contractclient", files)
	if !found {
		t.Fatal("own package not found")
	}
	if !allS2S {
		t.Fatal("all_grpc_s2s_clients flag was not merged")
	}
	if len(s2s) != 1 || s2s[0] != "contractclient.ContractS2S" {
		t.Fatalf("s2s refs = %#v, want ContractS2S", s2s)
	}
	if len(c2s) != 1 || c2s[0] != "contractclient.ContractApi" {
		t.Fatalf("c2s refs = %#v, want ContractApi", c2s)
	}
	if len(w2s) != 1 || w2s[0] != "contractclient.ContractApi" {
		t.Fatalf("w2s refs = %#v, want ContractApi", w2s)
	}
}

func TestDeriveServiceAuthFlagsIncludesTargetNamespace(t *testing.T) {
	anyUser, hasMethodAuth := deriveServiceAuthFlags(parser.ServiceDescriptor{
		Name: "ContractApi",
		Methods: []parser.MethodDescriptor{
			{Name: "Read", TargetNamespaceField: "tenant_namespace"},
		},
	})
	if anyUser {
		t.Fatal("target_namespace_field alone must not mark the service auth_any_user")
	}
	if !hasMethodAuth {
		t.Fatal("target_namespace_field must force method auth registration")
	}
}

func TestDetectServicesForWrapperCarriesEventLabel(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../proto/testdata/eventbus_c2s_contract")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	if err := protogen.AppendEventBusWrappersToProtos(dir, dir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}
	if _, err := protogen.EnsureEventBusSupportProto(dir, protogen.EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := protogen.AppendStateServiceToProtos(dir, dir); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}

	services, err := DetectServicesForWrapper(&tactype.Config{}, dir, func(*tactype.Config, string, string, string) string {
		return ""
	})
	if err != nil {
		t.Fatalf("DetectServicesForWrapper: %v", err)
	}
	for _, svc := range services {
		if svc.PackageName == "tasks" && svc.ServiceName == "DelegationProbeBusStateService" {
			if svc.EventLabel != "eventbus" {
				t.Fatalf("EventLabel = %q, want eventbus", svc.EventLabel)
			}
			return
		}
	}
	t.Fatal("DelegationProbeBusStateService not detected")
}

// Client targets emit NO service runtime: server wrappers never resolve for
// them, even with GrpcServerAll / own-package services / explicit GrpcServers
// (the taxonomy guard that stopped py/TS emitting broken s2sm surfaces).
func TestResolveServerServices_ClientTargetsGetNoServers(t *testing.T) {
	services := makeServices()
	for _, lang := range []string{"python", "typescript", "swift", "csharp"} {
		for name, config := range map[string]*tactype.Config{
			"GrpcServerAll": {Language: lang, GrpcServerAll: true},
			"own-package":   {Language: lang, SelfPackage: "baseball"},
			"explicit": {Language: lang, GrpcServers: []tactype.GrpcEntry{
				{ProtoFile: "services/football.proto", ServiceName: "FootballService"},
			}},
		} {
			if got := ResolveServerServices(config, services); len(got) != 0 {
				t.Errorf("%s/%s: got %d server services, want 0 (client targets emit no service runtime)", lang, name, len(got))
			}
		}
	}
}

func TestResolveServerServices_GrpcServerAll(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{GrpcServerAll: true}

	got := ResolveServerServices(config, services)

	// GrpcServerAll returns all services from proto files that have at least one gRPC service.
	// baseball.proto has BaseballService (gRPC), so both BaseballService and BaseballStateService are included.
	// football.proto has FootballService (gRPC), so it's included.
	// basketball.proto has no gRPC service, so it's excluded.
	if len(got) != 3 {
		t.Errorf("got %d services, want 3", len(got))
		for _, s := range got {
			t.Logf("  %s.%s (grpc=%v)", s.ProtoFile, s.ServiceName, s.IsGrpc)
		}
	}
}

func TestResolveServerServices_SpecificFrontends(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{
		GrpcServers: []tactype.GrpcEntry{
			{ProtoFile: "services/football.proto", ServiceName: "FootballService"},
		},
	}

	got := ResolveServerServices(config, services)
	if len(got) != 1 {
		t.Fatalf("got %d services, want 1", len(got))
	}
	if got[0].ServiceName != "FootballService" {
		t.Errorf("got %q, want FootballService", got[0].ServiceName)
	}
}

func TestResolveServerServices_NoConfig(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{}

	got := ResolveServerServices(config, services)
	if got != nil {
		t.Errorf("got %d services, want nil", len(got))
	}
}

// TNA-001 / handoff Issue 1: a user-defined own-package S2S service with empty
// service-level auth_services (e.g. method-level-only auth, like ConnectorStatus)
// must be kept for server generation; only tacplus-synthesized services
// (Synthesized=true) are dropped. The old "S2S && empty auth_services" heuristic
// wrongly dropped the user service.
func TestResolveServerServices_KeepsUserS2SWithEmptyAuth_DropsSynthesized(t *testing.T) {
	config := &tactype.Config{SelfPackage: "conn"}
	services := []ServiceInfo{
		{ProtoFile: "services/conn.proto", PackageName: "conn", ServiceName: "ConnectorStatus", GrpcName: "conn", IsGrpc: true, IsS2S: true, AuthServices: nil, Synthesized: false},
		{ProtoFile: "services/conn.proto", PackageName: "conn", ServiceName: "ConnStateState", GrpcName: "conn", IsGrpc: true, IsS2S: true, AuthServices: nil, Synthesized: true},
	}
	got := ResolveServerServices(config, services)
	var names []string
	for _, s := range got {
		names = append(names, s.ServiceName)
	}
	if len(got) != 1 || names[0] != "ConnectorStatus" {
		t.Fatalf("want only [ConnectorStatus] (user S2S kept, synthesized dropped), got %v", names)
	}
}

// A category-A MCP-only service (no service_s2s/service_c2s/service_c2d, so
// IsGrpc is false) must still get a server wrapper generated: that's the only
// path that reaches mcp_dispatch_gen.go. filterByPackage previously dropped
// any non-gRPC service outright, starving MCP-only services of a wrapper even
// after the per-language detect step learned to keep them around this far.
func TestResolveServerServices_KeepsMcpOnlyService(t *testing.T) {
	config := &tactype.Config{SelfPackage: "docmanageragent"}
	services := []ServiceInfo{
		{ProtoFile: "services/docmanageragent.proto", PackageName: "docmanageragent", ServiceName: "DocManagerAgent", GrpcName: "", IsGrpc: false, HasMcp: true},
	}
	got := ResolveServerServices(config, services)
	if len(got) != 1 || got[0].ServiceName != "DocManagerAgent" {
		t.Fatalf("want [DocManagerAgent] kept (MCP-only service), got %v", got)
	}
}

func TestResolveClientServices_GrpcS2SClientAll(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{GrpcS2SClientAll: true}

	got := ResolveClientServices(config, services)
	if len(got) != 3 {
		t.Errorf("got %d services, want 3", len(got))
	}
}

func TestResolveClientServices_SpecificClients(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{
		GrpcS2SClients: []tactype.GrpcEntry{
			{ProtoFile: "services/baseball.proto", ServiceName: "BaseballService"},
		},
	}

	got := ResolveClientServices(config, services)
	if len(got) != 1 {
		t.Fatalf("got %d services, want 1", len(got))
	}
	if got[0].ServiceName != "BaseballService" {
		t.Errorf("got %q, want BaseballService", got[0].ServiceName)
	}
}

func TestResolveClientServices_NoConfig(t *testing.T) {
	services := makeServices()
	config := &tactype.Config{}

	got := ResolveClientServices(config, services)
	if got != nil {
		t.Errorf("got %d services, want nil", len(got))
	}
}

func TestResolveC2SClientServices_DrivenByProtoRefs(t *testing.T) {
	services := makeServices()

	// No grpc_c2s_client refs → no C2S surface.
	config := &tactype.Config{}
	if got := ResolveC2SClientServices(config, services); got != nil {
		t.Errorf("no grpc_c2s_client refs returned %d services, want nil", len(got))
	}

	// A declared grpc_c2s_client ref produces the C2S surface for that service.
	config.GrpcC2SClients = []tactype.GrpcEntry{
		{ProtoFile: "services/baseball.proto", ServiceName: "BaseballService"},
	}
	got := ResolveC2SClientServices(config, services)
	if len(got) != 1 {
		t.Fatalf("declared C2S ref returned %d services, want 1", len(got))
	}
	if got[0].ServiceName != "BaseballService" {
		t.Errorf("got %q, want BaseballService", got[0].ServiceName)
	}
}

func TestResolveW2SClientServices_DrivenByProtoRefs(t *testing.T) {
	services := makeServices()

	// No grpc_w2s_client refs → no W2S surface.
	config := &tactype.Config{}
	if got := ResolveW2SClientServices(config, services); got != nil {
		t.Errorf("no grpc_w2s_client refs returned %d services, want nil", len(got))
	}

	// A declared grpc_w2s_client ref produces the W2S surface for that service.
	config.GrpcW2SClients = []tactype.GrpcEntry{
		{ProtoFile: "services/football.proto", ServiceName: "FootballService"},
	}
	got := ResolveW2SClientServices(config, services)
	if len(got) != 1 {
		t.Fatalf("declared W2S ref returned %d services, want 1", len(got))
	}
	if got[0].ServiceName != "FootballService" {
		t.Errorf("got %q, want FootballService", got[0].ServiceName)
	}
}

func TestResolveClientSurfacesWithoutDeclaredRefs(t *testing.T) {
	services := makeServices()
	// With no proto-declared client refs, no client surface is generated.
	config := &tactype.Config{}
	if got := ResolveC2SClientServices(config, services); got != nil {
		t.Fatalf("C2S surface without proto refs returned %d services, want nil", len(got))
	}
	if got := ResolveW2SClientServices(config, services); got != nil {
		t.Fatalf("W2S surface without proto refs returned %d services, want nil", len(got))
	}
}

// A trait-hosted C2S surface is grouped onto its host the same way a hosted
// S2S vservice is: both carry the host in HostLabel, so filterByHostTag needs
// no C2S-specific branch. The host's server build must pick up the trait's
// client-facing RPCs, or the trait declares a surface nothing serves.
func TestResolveServerServicesIncludesTraitHostedC2S(t *testing.T) {
	services := []ServiceInfo{
		{ProtoFile: "services/stratum.proto", PackageName: "stratum", ServiceName: "RecordStore", GrpcName: "stratum", IsGrpc: true, IsS2S: true},
		{ProtoFile: "vservices/query.proto", PackageName: "taskquery", ServiceName: "QueryApi", GrpcName: "taskquery", IsGrpc: true, IsC2S: true, HostLabel: "stratum"},
		{ProtoFile: "vservices/other.proto", PackageName: "othertrait", ServiceName: "OtherApi", GrpcName: "othertrait", IsGrpc: true, IsC2S: true, HostLabel: "elsewhere"},
	}
	got := ResolveServerServices(&tactype.Config{Language: "rust", SelfPackage: "stratum"}, services)

	var names []string
	for _, svc := range got {
		names = append(names, svc.ServiceName)
	}
	want := []string{"RecordStore", "QueryApi"}
	if len(names) != len(want) {
		t.Fatalf("server services = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("server services = %v, want %v", names, want)
		}
	}
}

// The host label selects the host: a C2S surface labelled for another service
// must not land in this service's server build.
func TestResolveServerServicesExcludesOtherHostsC2S(t *testing.T) {
	services := []ServiceInfo{
		{ProtoFile: "vservices/query.proto", PackageName: "taskquery", ServiceName: "QueryApi", GrpcName: "taskquery", IsGrpc: true, IsC2S: true, HostLabel: "stratum"},
	}
	if got := ResolveServerServices(&tactype.Config{Language: "rust", SelfPackage: "elsewhere"}, services); len(got) != 0 {
		t.Fatalf("got %d server services, want 0", len(got))
	}
}

// writeTestProto writes one proto into the fixture tree.
func writeTestProto(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// recordReadPlusExplicitClientFixture builds a fleet in which ONE service
// declares both halves of the collision: (spec.record_read), which DERIVES a
// virtual client for the record plane's s2s half, and an explicit
// (spec.grpc_s2s_client), which must be resolved from the annotation.
func recordReadPlusExplicitClientFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	writeTestProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};
`)
	writeTestProto(t, dir, "owner.proto", `syntax = "proto3";
package owner;

import "sys/spec.proto";

option (spec.service) = {name: "owner"};

message Row {
  option (spec.record) = {
    key: "row_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["owner"]}
    s2s_read: {auth_services: ["reader"]}
  };
  string row_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
  string namespace = 3 [(spec.store_namespace) = true];
}
`)
	writeTestProto(t, dir, "lock.proto", `syntax = "proto3";
package lock;

import "sys/spec.proto";

option (spec.service) = {name: "kernel"};

service FencedLock {
  option (spec.service_s2s) = {};
  rpc Acquire(AcquireRequest) returns (AcquireResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}

message AcquireRequest {
  string trace_id = 200;
}

message AcquireResponse {
  string trace_id = 200;
}
`)
	writeTestProto(t, dir, "reader.proto", `syntax = "proto3";
package reader;

import "sys/spec.proto";

option (spec.service) = {name: "reader"};
option (spec.record_read) = {record: "owner.Row"};
option (spec.grpc_s2s_client) = {service: "lock.FencedLock"};

service Reader {
  option (spec.service_s2s) = {};
  rpc Ping(PingRequest) returns (PingResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}

message PingRequest {
  string trace_id = 200;
}

message PingResponse {
  string trace_id = 200;
}
`)
	writeTestProto(t, dir, filepath.Join("vservices", "query.proto"), `syntax = "proto3";
package query;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["stratum"]
};
`)
	return dir
}

// A service that declares BOTH (spec.record_read) and an explicit
// (spec.grpc_s2s_client) must end up with BOTH clients.
//
// The two are produced by different mechanisms that write the same field:
// DeriveVirtualServiceClients derives the record plane's s2s client from
// record_read, and InferGrpcClientsFromAnnotations resolves the explicit
// declaration — but the latter skips its explicit-declaration block whenever
// GrpcS2SClients is ALREADY non-empty, a guard meant to protect a YAML/CLI
// override. So the ORDER matters: derive-then-append made the derived entry
// look like an operator override and silently swallowed the explicit
// declaration, with no error and no warning.
//
// This pins the sequence every language generator runs: infer FIRST, then union
// the derived entries in. Reverting any generator to append-before-infer, or
// swapping MergeGrpcEntries back to a plain append, drops one of the two.
func TestExplicitS2SClientSurvivesRecordReadDerivation(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordReadPlusExplicitClientFixture(t)

	// Materializes generated/*.proto, which sys/accounting.proto imports.
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}

	// The generator's real sequence, in order.
	virtualClients, err := protogen.DeriveVirtualServiceClients(dir, "reader")
	if err != nil {
		t.Fatalf("DeriveVirtualServiceClients: %v", err)
	}
	config := &tactype.Config{SelfPackage: "reader"}
	if err := InferGrpcClientsFromAnnotations(config, dir); err != nil {
		t.Fatalf("InferGrpcClientsFromAnnotations: %v", err)
	}
	config.GrpcS2SClients = MergeGrpcEntries(config.GrpcS2SClients, virtualClients)

	// The derived half: the record plane's s2s service.
	// The explicit half: the client the proto actually declares.
	var sawPlane, sawExplicit bool
	for _, e := range config.GrpcS2SClients {
		switch e.ServiceName {
		case "RecordMap":
			sawPlane = true
		case "FencedLock":
			sawExplicit = true
		}
	}
	if !sawPlane {
		t.Errorf("record_read must still derive the plane client, got %#v", config.GrpcS2SClients)
	}
	if !sawExplicit {
		t.Errorf("explicit grpc_s2s_client was dropped, got %#v", config.GrpcS2SClients)
	}

	// A union, not a duplication: neither mechanism may emit the same client twice.
	seen := map[string]int{}
	for _, e := range config.GrpcS2SClients {
		seen[e.ProtoFile+"\x00"+e.ServiceName]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("client %q emitted %d times, want 1", key, n)
		}
	}
}
