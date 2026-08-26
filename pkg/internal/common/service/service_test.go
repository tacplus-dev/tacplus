// Copyright (c) 2026 Second Star Cavalier LLC

package service

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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
