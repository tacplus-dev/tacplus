// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #130: a tenant's per-service config held by an exported service_state
// collection (kernel.ConfigState.tenant_configs) rather than by a grpc_config
// Config. Each slice owner is derived a read-only subscription to that ONE
// collection and a field-scoped, tenant-pinned read grant on it that the
// SERVING side enforces.

func pinTrue() model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
}

// pinFiles is, in memory, the kernel shape: keeper exports KeeperState whose
// tenant_configs collection holds the generated TenantConfig; gateway, billing
// and keeper each own a slice; billing ALSO declares a whole-root state import.
func pinFiles(mutate func(item *parser.MessageDescriptor, root *parser.MessageDescriptor)) map[string]*parser.FileDescriptor {
	item := parser.MessageDescriptor{Name: "TenantConfigEntry", Fields: []parser.FieldDescriptor{
		{Name: "tenant_name", Number: 1, Type: "string", TypeKind: "scalar", Annotations: map[string]model.AnnotationValue{"indexed": pinTrue()}},
		{Name: "config", Number: 2, Type: "tenant_config.TenantConfig", TypeKind: "message"},
	}}
	root := parser.MessageDescriptor{Name: "KeeperState",
		Annotations: map[string]model.AnnotationValue{
			"service_state":        {Type: model.AnnotationTypeString, StringValue: "KeeperState"},
			"service_state_export": pinTrue(),
		},
		Fields: []parser.FieldDescriptor{
			{Name: "tenants", Number: 1, Type: "keeper.TenantRecord", TypeKind: "message", IsRepeated: true},
			{Name: "tenant_configs", Number: 2, Type: "keeper.TenantConfigEntry", TypeKind: "message", IsRepeated: true},
		}}
	if mutate != nil {
		mutate(&item, &root)
	}
	return map[string]*parser.FileDescriptor{
		"services/keeper.proto": {Package: "keeper", Messages: []parser.MessageDescriptor{
			root, item, {Name: "TenantRecord", Fields: []parser.FieldDescriptor{{Name: "name", Number: 1, Type: "string", TypeKind: "scalar"}}},
		}},
		"generated/tenant_config.proto": {Package: "tenant_config", Messages: []parser.MessageDescriptor{
			{Name: "TenantConfig", Annotations: map[string]model.AnnotationValue{"per_service_config": pinTrue()}, Fields: []parser.FieldDescriptor{
				{Name: "billing", Number: 1, Type: "slices.BillingTenantConfig", TypeKind: "message"},
				{Name: "gateway", Number: 2, Type: "slices.GatewayTenantConfig", TypeKind: "message"},
				{Name: "keeper", Number: 3, Type: "slices.KeeperTenantConfig", TypeKind: "message"},
				{Name: "opa", Number: 4, Type: "slices.OpaTenantConfig", TypeKind: "message"},
			}},
		}},
		"services/billing.proto": {Package: "billing", FileStateImports: []string{"keeper.KeeperState"}},
	}
}

func TestResolveTenantPinnedBoundariesFromServiceStateExport(t *testing.T) {
	got, err := ResolveTenantPinnedBoundaries(pinFiles(nil))
	if err != nil {
		t.Fatalf("ResolveTenantPinnedBoundaries: %v", err)
	}
	want := []TenantPinnedBoundary{{
		Package: "keeper", StateRoot: "KeeperState", ProtoFile: "services/keeper.proto",
		Field: "tenant_configs", KeyField: "tenant_name", ConfigField: "config",
		// keeper is the owner and billing declared a whole-root import: neither is pinned.
		Slices: []TenantPinnedSlice{{Caller: "gateway", SliceField: "gateway"}, {Caller: "opa", SliceField: "opa"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned boundaries:\n got %+v\nwant %+v", got, want)
	}
	if rpcs := got[0].ReadRPCs(); len(rpcs) != 8 || rpcs[0].Name != "GetTenantConfigs" || rpcs[7].Name != "WatchQueryTenantConfigs" {
		t.Fatalf("read RPCs = %+v", rpcs)
	}
}

func TestResolveTenantPinnedBoundariesSkipsUnexportedRoot(t *testing.T) {
	got, err := ResolveTenantPinnedBoundaries(pinFiles(func(_, root *parser.MessageDescriptor) {
		delete(root.Annotations, "service_state_export")
	}))
	if err != nil || len(got) != 0 {
		t.Fatalf("an unexported root grants nothing to anyone: got %+v, %v", got, err)
	}
}

func TestResolveTenantPinnedBoundariesRefusesUnpinnableShapes(t *testing.T) {
	cases := map[string]func(item, root *parser.MessageDescriptor){
		"no indexed key": func(item, _ *parser.MessageDescriptor) {
			item.Fields[0].Annotations = nil
		},
		"non-string key": func(item, _ *parser.MessageDescriptor) {
			item.Fields[0].Type = "int64"
		},
		"auth disabled": func(_, root *parser.MessageDescriptor) {
			root.Annotations["disable_auth_message"] = pinTrue()
		},
		"repeated config": func(item, _ *parser.MessageDescriptor) {
			item.Fields[1].IsRepeated = true
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ResolveTenantPinnedBoundaries(pinFiles(mutate)); err == nil {
				t.Fatalf("a per-service config the server cannot pin must fail generation, got %+v", got)
			}
		})
	}
}

func TestNarrowStateRootKeepsOnlyTheGrantedBoundary(t *testing.T) {
	files := pinFiles(nil)
	root := &files["services/keeper.proto"].Messages[0]
	config := &tactype.Config{States: []tactype.StateEntry{{
		ProtoFile: "services/keeper.proto", MessageName: "KeeperState", AccessMode: tactype.AccessRO, Boundaries: []string{"tenant_configs"},
	}}}
	narrowed := NarrowStateRoot(config, "services/keeper.proto", root)
	if len(narrowed.Fields) != 1 || narrowed.Fields[0].Name != "tenant_configs" {
		t.Fatalf("narrowed client fields = %+v, want only tenant_configs", narrowed.Fields)
	}
	if len(root.Fields) != 2 {
		t.Fatal("NarrowStateRoot must not mutate the descriptor it narrows")
	}
	config.States[0].Boundaries = nil
	if NarrowStateRoot(config, "services/keeper.proto", root) != root {
		t.Fatal("an un-narrowed subscription gets the whole root")
	}
}

// pinTree writes the same shape as real protos and prepares the flat tree every
// generator parses, generated TenantConfig included.
func pinTree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	src := t.TempDir()
	writeProto(t, src, "services/keeper.proto", `syntax = "proto3";
package keeper;
import "sys/spec.proto";
import "generated/tenant_config.proto";
option (spec.service) = {name: "keeper", store: true};
message TenantConfigEntry {
  string tenant_name = 1 [(spec.indexed) = true, (spec.assert_max_size) = 64];
  tenant_config.TenantConfig config = 2;
}
message TenantRecord {
  string name = 1 [(spec.indexed) = true, (spec.assert_max_size) = 64];
}
message KeeperState {
  option (spec.service_state) = {label: ["keeper"], export: true};
  repeated TenantRecord tenants = 1 [(spec.assert_max_size) = 100];
  repeated TenantConfigEntry tenant_configs = 2 [(spec.assert_max_size) = 100];
}
`)
	writeProto(t, src, "platform/slices.proto", `syntax = "proto3";
package slices;
import "sys/spec.proto";
message GatewayTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "gateway"};
  string route = 1 [(spec.assert_max_size) = 64];
}
message BillingTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "billing"};
  string plan = 1 [(spec.assert_max_size) = 64];
}
message KeeperTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "keeper"};
  string secret = 1 [(spec.assert_max_size) = 64];
}
`)
	writeProto(t, src, "services/gateway.proto", `syntax = "proto3";
package gateway;
import "sys/spec.proto";
option (spec.service) = {name: "gateway"};
`)
	writeProto(t, src, "services/billing.proto", `syntax = "proto3";
package billing;
import "sys/spec.proto";
option (spec.service) = {name: "billing"};
option (spec.state_imports) = {source: "keeper.KeeperState"};
`)
	// ServiceHealth comes from the sys contract (sys/servicehealth.proto), not from a service proto.
	flat, err := CopyAndPrepareProtoDir(src, t.TempDir())
	if err != nil {
		t.Fatalf("CopyAndPrepareProtoDir: %v", err)
	}
	return flat
}

func TestResolveServiceStatesSubscribesSliceOwnerToThePinnedCollectionOnly(t *testing.T) {
	flat := pinTree(t)
	entries, err := InjectGrpcConfigState(flat, flat, flat)
	if err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	for self, want := range map[string][]string{
		"gateway": {"tenant_configs"}, // tenant-pinned: the one collection
		"billing": nil,                // declared a whole-root import: unchanged
	} {
		config := &tactype.Config{SelfPackage: self, FlatProtoDir: flat}
		if err := ResolveServiceStates(config, entries); err != nil {
			t.Fatalf("%s: ResolveServiceStates: %v", self, err)
		}
		var got *tactype.StateEntry
		for i := range config.States {
			if config.States[i].MessageName == "KeeperState" {
				got = &config.States[i]
			}
		}
		if got == nil {
			t.Fatalf("%s owns a slice of the TenantConfig KeeperState holds but was not subscribed: %+v", self, config.States)
		}
		if got.AccessMode != tactype.AccessRO || !reflect.DeepEqual(got.Boundaries, want) {
			t.Fatalf("%s: subscription = %+v, want read-only over %v", self, got, want)
		}
	}
}

func TestStateServiceGivesPinnedCallersTheCollectionReadRPCsOnly(t *testing.T) {
	flat := pinTree(t)
	if err := AppendStateServiceToProtos(flat, flat); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(flat, "services/keeper.proto"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	const pinned = `principal: RPC_PRINCIPAL_SERVICE, auth_services: ["keeper", "billing", "gateway"]`
	for _, rpc := range []string{"GetTenantConfigs", "ContainsKeyTenantConfigs", "GetAllTenantConfigs", "KeySetTenantConfigs",
		"SizeTenantConfigs", "WatchTenantConfigs", "QueryTenantConfigs", "WatchQueryTenantConfigs"} {
		re := regexp.MustCompile(`rpc ` + rpc + `\([^)]*\) returns \([^)]*\) \{\s*option \(spec\.rpc\) = \{([^}]*)\};\s*\}`)
		m := re.FindStringSubmatch(src)
		if m == nil || m[1] != pinned {
			t.Fatalf("%s must carry the pinned caller on top of the service-level list; got %q", rpc, m)
		}
	}
	for _, rpc := range []string{"PutTenantConfigs", "SetTenantConfigs", "DeleteTenantConfigs", "ClearTenantConfigs", "GetAllTenants", "WatchTenants"} {
		if !regexp.MustCompile(`rpc ` + rpc + `\([^)]*\) returns \([^)]*\);`).MatchString(src) {
			t.Fatalf("%s is not a read of the pinned collection and must keep the service-level caller list", rpc)
		}
	}
	if !strings.Contains(src, `option (spec.service_s2s) = {auth_services: ["keeper", "billing"]};`) {
		t.Fatal("the service-level caller list must stay the owner and its declared importers")
	}
}

func TestSecurityPolicyCarriesTenantPinnedReadsOutsideBoundaryGrants(t *testing.T) {
	flat := pinTree(t)
	policies, err := ComputeSecurityPolicies(flat)
	if err != nil {
		t.Fatalf("ComputeSecurityPolicies: %v", err)
	}
	for _, p := range policies {
		if p.Package != "keeper" || p.Name != "KeeperState" {
			continue
		}
		want := []TenantPinnedRead{{Boundary: "tenant_configs", KeyField: "tenant_name", ConfigField: "config",
			Slices: []TenantPinnedReadSlice{{Caller: "gateway", SliceField: "gateway"}}}}
		if !reflect.DeepEqual(p.TenantPinnedReads, want) {
			t.Fatalf("tenantPinnedReads = %+v, want %+v", p.TenantPinnedReads, want)
		}
		// A boundaryGrants entry grants the whole boundary on a consumer that
		// ignores its type, so the pinned read must never be carried there.
		if len(p.BoundaryGrants) != 0 {
			t.Fatalf("boundaryGrants = %+v, want none", p.BoundaryGrants)
		}
		if !reflect.DeepEqual(p.RO, []string{"service:keeper", "service:billing"}) {
			t.Fatalf("RO = %v: a pinned caller holds no whole-root read", p.RO)
		}
		return
	}
	t.Fatal("no policy for keeper.KeeperState")
}

// A tree that declares an MCP tool re-runs the config-holder scan after the
// carrier is injected, and that scan rewrites every generated/ stub. The
// TenantConfig aggregate generated before it was clobbered back to the stub,
// so on the platform (which declares MCP tools) no slice subscription — of
// either owner shape — was ever derived. The aggregate must survive.
func TestCopyAndPrepareProtoDirKeepsTenantConfigAggregateWithAnMcpTool(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	src := t.TempDir()
	writeProto(t, src, "services/scotty.proto", strings.Replace(mcpCarrierToolProto,
		`import "sys/spec.proto";`, `import "sys/spec.proto";

option (spec.service) = {name: "scotty"};

message ScottyTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "scotty"};
  string persona = 1 [(spec.assert_max_size) = 64];
}`, 1))
	flat, err := CopyAndPrepareProtoDir(src, t.TempDir())
	if err != nil {
		t.Fatalf("CopyAndPrepareProtoDir: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(flat, "services/scotty.proto")); err != nil || !strings.Contains(string(b), "McpSession(") {
		t.Fatalf("precondition: the MCP carrier was not injected (%v)", err)
	}
	b, err := os.ReadFile(filepath.Join(flat, "generated/tenant_config.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "option (spec.per_service_config) = true;") || !strings.Contains(string(b), "scotty.ScottyTenantConfig scotty = ") {
		t.Fatalf("the flattened TenantConfig is the stub, not the generated aggregate:\n%s", b)
	}
}

// Issue #135: the same per-service config held by a grpc_config Config
// (cross-test/proto-tenantslice: tenancy holds map<string, TenantConfig>
// tenants) rather than by an exported service_state collection. The derived
// subscriber got a read client on TenancyConfigState but was never an allowed
// caller of the service, so its Watch was refused at runtime.

// pinConfigFiles is, in memory, the grpc_config shape: keeper's Config holds a
// map of tenant → generated TenantConfig and its injected KeeperConfigState
// wraps it as the singleton `config`.
func pinConfigFiles(mutate func(cfg *parser.MessageDescriptor)) map[string]*parser.FileDescriptor {
	cfg := parser.MessageDescriptor{Name: "KeeperConfig",
		Annotations: map[string]model.AnnotationValue{"grpc_config": {Type: model.AnnotationTypeString, StringValue: "keeper"}},
		Fields: []parser.FieldDescriptor{
			{Name: "region", Number: 1, Type: "string", TypeKind: "scalar"},
			{Name: "tenants", Number: 2, Type: "keeper.KeeperConfig.TenantsEntry", TypeKind: "message", IsMap: true,
				MapKeyType: "string", MapValueType: "tenant_config.TenantConfig", MapValueTypeKind: "message"},
		}}
	if mutate != nil {
		mutate(&cfg)
	}
	files := pinFiles(nil)
	files["services/keeper.proto"] = &parser.FileDescriptor{Package: "keeper", Messages: []parser.MessageDescriptor{
		cfg,
		{Name: "KeeperConfigState",
			Annotations: map[string]model.AnnotationValue{"service_state": {Type: model.AnnotationTypeString, StringValue: "KeeperConfigState"}},
			Fields:      []parser.FieldDescriptor{{Name: "config", Number: 1, Type: "keeper.KeeperConfig", TypeKind: "message"}}},
	}}
	delete(files, "services/billing.proto")
	return files
}

func TestResolveTenantPinnedBoundariesFromGrpcConfig(t *testing.T) {
	got, err := ResolveTenantPinnedBoundaries(pinConfigFiles(nil))
	if err != nil {
		t.Fatalf("ResolveTenantPinnedBoundaries: %v", err)
	}
	want := []TenantPinnedBoundary{{
		Package: "keeper", StateRoot: "KeeperConfigState", ProtoFile: "services/keeper.proto",
		Field: "config", MapField: "tenants", ConfigType: "KeeperConfig",
		// keeper owns the Config: it is not pinned on it.
		Slices: []TenantPinnedSlice{{Caller: "billing", SliceField: "billing"}, {Caller: "gateway", SliceField: "gateway"}, {Caller: "opa", SliceField: "opa"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned boundaries:\n got %+v\nwant %+v", got, want)
	}
	rpcs := got[0].ReadRPCs()
	if len(rpcs) != 2 || rpcs[0].Name != "GetConfig" || rpcs[1].Name != "WatchConfig" {
		t.Fatalf("read RPCs = %+v, want GetConfig and WatchConfig only", rpcs)
	}
}

func TestResolveTenantPinnedBoundariesRefusesUnpinnableGrpcConfig(t *testing.T) {
	cases := map[string]func(cfg *parser.MessageDescriptor){
		"non-string map key": func(cfg *parser.MessageDescriptor) { cfg.Fields[1].MapKeyType = "int64" },
		"two maps": func(cfg *parser.MessageDescriptor) {
			second := cfg.Fields[1]
			second.Name, second.Number = "others", 3
			cfg.Fields = append(cfg.Fields, second)
		},
		// Reached only through a wrapper message: the server cannot filter it.
		"nested wrapper": func(cfg *parser.MessageDescriptor) {
			cfg.Fields[1] = parser.FieldDescriptor{Name: "tenants", Number: 2, Type: "keeper.Wrapper", TypeKind: "message"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			files := pinConfigFiles(mutate)
			if name == "nested wrapper" {
				fd := files["services/keeper.proto"]
				fd.Messages = append(fd.Messages, parser.MessageDescriptor{Name: "Wrapper", Fields: []parser.FieldDescriptor{
					{Name: "tenants", Number: 1, Type: "keeper.Wrapper.TenantsEntry", TypeKind: "message", IsMap: true,
						MapKeyType: "string", MapValueType: "tenant_config.TenantConfig", MapValueTypeKind: "message"}}})
			}
			if got, err := ResolveTenantPinnedBoundaries(files); err == nil {
				t.Fatalf("a Config the server cannot filter per tenant must fail generation, got %+v", got)
			}
		})
	}
}

// pinConfigTree writes the grpc_config shape as real protos.
func pinConfigTree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	src := t.TempDir()
	writeProto(t, src, "services/keeper.proto", `syntax = "proto3";
package keeper;
import "sys/spec.proto";
import "generated/tenant_config.proto";
option (spec.service) = {name: "keeper", store: true};
message KeeperConfig {
  option (spec.grpc_config) = {service_name: "keeper"};
  string region = 1;
  map<string, tenant_config.TenantConfig> tenants = 2 [(spec.assert_max_size) = 100];
}
`)
	writeProto(t, src, "platform/slices.proto", `syntax = "proto3";
package slices;
import "sys/spec.proto";
message GatewayTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "gateway"};
  string route = 1 [(spec.assert_max_size) = 64];
}
`)
	writeProto(t, src, "services/gateway.proto", `syntax = "proto3";
package gateway;
import "sys/spec.proto";
option (spec.service) = {name: "gateway"};
`)
	// ServiceHealth comes from the sys contract (sys/servicehealth.proto), not from a service proto.
	flat, err := CopyAndPrepareProtoDir(src, t.TempDir())
	if err != nil {
		t.Fatalf("CopyAndPrepareProtoDir: %v", err)
	}
	return flat
}

func TestResolveServiceStatesSubscribesSliceOwnerToGrpcConfig(t *testing.T) {
	flat := pinConfigTree(t)
	entries, err := InjectGrpcConfigState(flat, flat, flat)
	if err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	config := &tactype.Config{SelfPackage: "gateway", FlatProtoDir: flat}
	if err := ResolveServiceStates(config, entries); err != nil {
		t.Fatalf("ResolveServiceStates: %v", err)
	}
	n := 0
	for _, s := range config.States {
		if s.MessageName == "KeeperConfigState" {
			n++
			if s.AccessMode != tactype.AccessRO || !reflect.DeepEqual(s.Boundaries, []string{"config"}) {
				t.Fatalf("subscription = %+v, want read-only over config", s)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one KeeperConfigState subscription, got %d: %+v", n, config.States)
	}
}

// The reproduction of #135: before the fix the service-level caller list was
// ["keeper"] alone, so the subscriber's Watch was PERMISSION_DENIED.
func TestConfigStateServiceGivesPinnedSubscriberTheReadRPCsOnly(t *testing.T) {
	flat := pinConfigTree(t)
	if _, err := InjectGrpcConfigState(flat, flat, flat); err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	if err := AppendStateServiceToProtos(flat, flat); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(flat, "services/keeper.proto"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	const pinned = `principal: RPC_PRINCIPAL_SERVICE, auth_services: ["keeper", "gateway"]`
	for _, rpc := range []string{"GetConfig", "WatchConfig"} {
		re := regexp.MustCompile(`rpc ` + rpc + `\([^)]*\) returns \((?:stream )?[^)]*\) \{\s*option \(spec\.rpc\) = \{([^}]*)\};\s*\}`)
		m := re.FindStringSubmatch(src)
		if m == nil || m[1] != pinned {
			t.Fatalf("%s must carry the pinned subscriber on top of the service-level list; got %q", rpc, m)
		}
	}
	if !regexp.MustCompile(`rpc SetConfig\([^)]*\) returns \([^)]*\);`).MatchString(src) {
		t.Fatal("SetConfig is a write and must keep the service-level caller list")
	}
	if !strings.Contains(src, `option (spec.service_s2s) = {auth_services: ["keeper"]};`) {
		t.Fatal("the service-level caller list must stay the owner alone")
	}
}

func TestSecurityPolicyCarriesTenantPinnedReadOfGrpcConfig(t *testing.T) {
	flat := pinConfigTree(t)
	if _, err := InjectGrpcConfigState(flat, flat, flat); err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	policies, err := ComputeSecurityPolicies(flat)
	if err != nil {
		t.Fatalf("ComputeSecurityPolicies: %v", err)
	}
	for _, p := range policies {
		if p.Package != "keeper" || p.Name != "KeeperConfigState" {
			continue
		}
		want := []TenantPinnedRead{{Boundary: "config", MapField: "tenants",
			Slices: []TenantPinnedReadSlice{{Caller: "gateway", SliceField: "gateway"}}}}
		if !reflect.DeepEqual(p.TenantPinnedReads, want) {
			t.Fatalf("tenantPinnedReads = %+v, want %+v", p.TenantPinnedReads, want)
		}
		if len(p.BoundaryGrants) != 0 || !reflect.DeepEqual(p.RO, []string{"service:keeper"}) {
			t.Fatalf("a pinned read grants no whole-root access: RO=%v grants=%+v", p.RO, p.BoundaryGrants)
		}
		return
	}
	t.Fatal("no policy for keeper.KeeperConfigState")
}
