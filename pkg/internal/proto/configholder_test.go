// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestDeclaredConfigStateRootsFollowGrpcConfigDeclarations(t *testing.T) {
	files := map[string]*parser.FileDescriptor{
		"services/gateway.proto": {
			Package:     "gateway",
			ServiceName: "gateway",
			Messages: []parser.MessageDescriptor{
				{
					Name: "Config",
					Annotations: map[string]model.AnnotationValue{
						"grpc_config": {Type: model.AnnotationTypeString, StringValue: "gateway"},
					},
				},
				{
					Name:   "GatewayConfigState",
					Fields: []parser.FieldDescriptor{{Name: "config"}},
					Annotations: map[string]model.AnnotationValue{
						"service_state": {Type: model.AnnotationTypeString, StringValue: "GatewayConfigState"},
					},
				},
				{
					Name:   "AuditConfigState",
					Fields: []parser.FieldDescriptor{{Name: "config"}},
					Annotations: map[string]model.AnnotationValue{
						"service_state": {Type: model.AnnotationTypeString, StringValue: "AuditConfigState"},
					},
				},
			},
		},
		"sys/grpcconfig.proto": {
			Package: "grpcconfig",
			Messages: []parser.MessageDescriptor{{
				Name: "ServiceConfigState",
				Annotations: map[string]model.AnnotationValue{
					"service_state": {Type: model.AnnotationTypeString, StringValue: "ServiceConfigState"},
				},
			}},
		},
	}

	roots := declaredConfigStateRoots(files)
	for _, want := range []string{
		"services/gateway.proto\x00GatewayConfigState",
		"sys/grpcconfig.proto\x00ServiceConfigState",
	} {
		if !roots[want] {
			t.Fatalf("declaredConfigStateRoots missing %q: %#v", want, roots)
		}
	}
	if roots["services/gateway.proto\x00AuditConfigState"] {
		t.Fatalf("shape-only config state was selected: %#v", roots)
	}
}

func TestGenerateConfigSlicesFromDeclaredConfigSlice(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed config-slice test")
	}

	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)

	protoText := `syntax = "proto3";

package configdemo;

import "sys/spec.proto";

message IdentityTenantConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: "identity"};
  string issuer = 1;
}

message BillingProjectConfig {
  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_PROJECT, service_name: "billing"};
  string plan = 1;
}
`
	if err := os.WriteFile(filepath.Join(dir, "config_slices.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write config_slices.proto: %v", err)
	}

	if err := GenerateTenantConfig(dir, dir, dir); err != nil {
		t.Fatalf("GenerateTenantConfig: %v", err)
	}
	tenantData, err := os.ReadFile(filepath.Join(dir, "generated", "tenant_config.proto"))
	if err != nil {
		t.Fatalf("read generated tenant config: %v", err)
	}
	tenantProto := string(tenantData)
	if !strings.Contains(tenantProto, "configdemo.IdentityTenantConfig identity = 1;") {
		t.Fatalf("tenant_config.proto missing declared identity owner:\n%s", tenantProto)
	}
	if strings.Contains(tenantProto, "BillingProjectConfig") {
		t.Fatalf("tenant_config.proto included project slice:\n%s", tenantProto)
	}

	if err := GenerateProjectConfig(dir, dir, dir); err != nil {
		t.Fatalf("GenerateProjectConfig: %v", err)
	}
	projectData, err := os.ReadFile(filepath.Join(dir, "generated", "project_config.proto"))
	if err != nil {
		t.Fatalf("read generated project config: %v", err)
	}
	projectProto := string(projectData)
	if !strings.Contains(projectProto, "configdemo.BillingProjectConfig billing = 1;") {
		t.Fatalf("project_config.proto missing declared billing owner:\n%s", projectProto)
	}
	if strings.Contains(projectProto, "IdentityTenantConfig") {
		t.Fatalf("project_config.proto included tenant slice:\n%s", projectProto)
	}
}

// The ConfigState that InjectGrpcConfigState injects for a (spec.grpc_config)
// message must protoc-compile. Regression guard for NEW-12: the injected text used
// to emit the removed (spec.state_consistency)/(spec.consistency) options, which
// broke every grpc_config build — invisible because no fixture exercises grpc_config.
func TestInjectGrpcConfigStateProducesCompilableProto(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping grpc_config injection test")
	}
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: demo\n    id: 5\n"), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
	writeProto(t, dir, "demo.proto", `syntax = "proto3";

package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo", store: true};

message DemoConfig {
  option (spec.grpc_config) = {service_name: "demo"};
  string setting = 1;
}
`)

	if _, err := InjectGrpcConfigState(dir, dir, dir); err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	// Re-parse the injected result with protoc — the injected DemoConfigState must
	// compile (it must not reference removed options).
	if _, err := parser.NewDescriptorParser(dir); err != nil {
		t.Fatalf("injected grpc_config ConfigState does not compile: %v", err)
	}
}

// With more than one store: true service, config-state routing must be
// DETERMINISTIC — the old code broke at the first ServiceStore in a map range, so
// the ConfigState tag flipped between stores across identical builds. It must now
// route to the alphabetically-first store regardless of map-iteration order.
func TestInjectGrpcConfigStateRoutesToDeclaredConfigStore(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: demo\n    id: 5\n  - name: alpha\n    id: 6\n  - name: zeta\n    id: 7\n"), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
	// Two backing stores in distinct packages. "zeta" declares config_store, and
	// "alpha" sorts first — so alphabetical-first selection would pick the wrong
	// one. That is exactly the defect config_store exists to remove: on the real
	// platform set it silently routed every service's config through eventbus
	// (< kernel) because eventbus is a store for its own topic state.
	writeProto(t, dir, "zeta.proto", `syntax = "proto3";
package zeta;
import "sys/spec.proto";
option (spec.service) = {name: "zeta", store: true, config_store: true};
message ZetaState { option (spec.service_state) = {name: "ZetaState", label: ["zeta"]}; string id = 1; }
`)
	writeProto(t, dir, "alpha.proto", `syntax = "proto3";
package alpha;
import "sys/spec.proto";
option (spec.service) = {name: "alpha", store: true};
message AlphaState { option (spec.service_state) = {name: "AlphaState", label: ["alpha"]}; string id = 1; }
`)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;
import "sys/spec.proto";
option (spec.service) = {name: "demo"};
message DemoConfig {
  option (spec.grpc_config) = {service_name: "demo"};
  string setting = 1;
}
`)

	if _, err := InjectGrpcConfigState(dir, dir, dir); err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	injected, err := os.ReadFile(filepath.Join(dir, "demo.proto"))
	if err != nil {
		t.Fatalf("read injected demo.proto: %v", err)
	}
	text := string(injected)
	if !strings.Contains(text, `label: ["zeta"]`) {
		t.Fatalf("config state must route to the declared config_store (zeta):\n%s", text)
	}
	if strings.Contains(text, `label: ["alpha"]`) {
		t.Fatalf("config state must not route to a store that only declares store: true (alpha):\n%s", text)
	}
}

// Several stores and no config_store is ambiguous: whatever the generator picked
// would be an arbitrary architectural decision made by sort order. Fail loud.
func TestInjectGrpcConfigStateRejectsAmbiguousStores(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: demo\n    id: 5\n  - name: alpha\n    id: 6\n  - name: zeta\n    id: 7\n"), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
	writeProto(t, dir, "zeta.proto", `syntax = "proto3";
package zeta;
import "sys/spec.proto";
option (spec.service) = {name: "zeta", store: true};
message ZetaState { option (spec.service_state) = {name: "ZetaState", label: ["zeta"]}; string id = 1; }
`)
	writeProto(t, dir, "alpha.proto", `syntax = "proto3";
package alpha;
import "sys/spec.proto";
option (spec.service) = {name: "alpha", store: true};
message AlphaState { option (spec.service_state) = {name: "AlphaState", label: ["alpha"]}; string id = 1; }
`)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;
import "sys/spec.proto";
option (spec.service) = {name: "demo"};
message DemoConfig {
  option (spec.grpc_config) = {service_name: "demo"};
  string setting = 1;
}
`)

	_, err := InjectGrpcConfigState(dir, dir, dir)
	if err == nil {
		t.Fatal("expected a loud error when several stores exist and none declares config_store")
	}
	if !strings.Contains(err.Error(), "config_store") {
		t.Fatalf("error must name the annotation that resolves it, got: %v", err)
	}
}

// A single store is unambiguous — nothing to choose between — so config_store is
// not required there. Every cross-test fixture set is in this shape.
func TestInjectGrpcConfigStateAllowsSoleStoreWithoutConfigStore(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: demo\n    id: 5\n  - name: alpha\n    id: 6\n"), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
	writeProto(t, dir, "alpha.proto", `syntax = "proto3";
package alpha;
import "sys/spec.proto";
option (spec.service) = {name: "alpha", store: true};
message AlphaState { option (spec.service_state) = {name: "AlphaState", label: ["alpha"]}; string id = 1; }
`)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;
import "sys/spec.proto";
option (spec.service) = {name: "demo"};
message DemoConfig {
  option (spec.grpc_config) = {service_name: "demo"};
  string setting = 1;
}
`)

	if _, err := InjectGrpcConfigState(dir, dir, dir); err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	injected, err := os.ReadFile(filepath.Join(dir, "demo.proto"))
	if err != nil {
		t.Fatalf("read injected demo.proto: %v", err)
	}
	if !strings.Contains(string(injected), `label: ["alpha"]`) {
		t.Fatalf("config state must route to the sole store (alpha):\n%s", string(injected))
	}
}

// service_name on (spec.grpc_config) is REQUIRED — a nameless annotation was
// previously treated as absent, silently dropping the user's declared config
// holder. It must fail loud instead (maintainer decision 2026-07-04: required,
// never resolved implicitly from the file-level service). Protoc-free pin.
func TestParseGrpcConfigAnnotationRequiresServiceName(t *testing.T) {
	// Present with service_name → resolves.
	name, ok, err := parseGrpcConfigAnnotation(`option (spec.grpc_config) = {service_name: "demo"};`)
	if err != nil || !ok || name != "demo" {
		t.Fatalf("named annotation must resolve, got name=%q ok=%v err=%v", name, ok, err)
	}

	// Present WITHOUT service_name → loud error, not silent skip.
	_, _, err = parseGrpcConfigAnnotation(`option (spec.grpc_config) = {};`)
	if err == nil {
		t.Fatal("expected a loud error for (spec.grpc_config) without service_name")
	}
	if !strings.Contains(err.Error(), "service_name") {
		t.Fatalf("error should name the missing field, got: %v", err)
	}

	// Absent → not an error, just not a config holder.
	_, ok, err = parseGrpcConfigAnnotation(`message Plain { string x = 1; }`)
	if err != nil || ok {
		t.Fatalf("absent annotation must be a clean no, got ok=%v err=%v", ok, err)
	}
}

func TestMinimalGeneratedEntitlementsUseFlatGrantTokens(t *testing.T) {
	dir := t.TempDir()
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "generated", "entitlements.proto"))
	if err != nil {
		t.Fatalf("read generated entitlements stub: %v", err)
	}
	proto := string(data)
	if !strings.Contains(proto, "repeated string grants = 1;") {
		t.Fatalf("TokenEntitlements should use flat grant token strings:\n%s", proto)
	}
	if strings.Contains(proto, "message EntitlementGrant") || strings.Contains(proto, "repeated EntitlementGrant grants") {
		t.Fatalf("TokenEntitlements should not wrap each grant in a token object:\n%s", proto)
	}
}

func copySpecProtoForConfigSliceTest(t *testing.T, dir string) {
	t.Helper()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
}

// MS-10: the config-holder discovers annotations by raw-text regex (a second,
// pre-descriptor model-recovery mechanism). grpcConfigRe was unanchored, so a
// COMMENTED-OUT `option (spec.grpc_config)` was false-matched as active — a
// developer commenting the annotation out still got a ghost config holder
// generated. It is now anchored to line-start like serviceRe.
func TestParseGrpcConfigAnnotation_IgnoresCommentedOut(t *testing.T) {
	commented := `syntax = "proto3";
package foo;
message Real {
  // option (spec.grpc_config) = {service_name: "ghost"};
  string x = 1;
}
`
	if name, ok, err := parseGrpcConfigAnnotation(commented); ok || err != nil {
		t.Fatalf("a commented-out grpc_config must be ignored, got name=%q ok=%v err=%v", name, ok, err)
	}

	// A real (indented, inside a message) annotation must still be discovered.
	active := `syntax = "proto3";
package foo;
message Config {
  option (spec.grpc_config) = {service_name: "realsvc"};
  string x = 1;
}
`
	name, ok, err := parseGrpcConfigAnnotation(active)
	if err != nil || !ok || name != "realsvc" {
		t.Fatalf("an active grpc_config must be discovered, got name=%q ok=%v err=%v", name, ok, err)
	}

	// Single-line INLINE form must also be discovered (the line-start anchor
	// this replaced would have missed it).
	inline := "message Config { option (spec.grpc_config) = {service_name: \"inlinesvc\"}; }\n"
	if n, ok, err := parseGrpcConfigAnnotation(inline); !ok || err != nil || n != "inlinesvc" {
		t.Fatalf("inline single-line grpc_config must be discovered, got name=%q ok=%v err=%v", n, ok, err)
	}
}

// MS-10 (sibling scanners): the RPC-surface option scanners had the same
// unanchored comment-false-match — a commented (spec.service_c2s) / _c2d /
// event_label inside a service block would misclassify the surface. Pin that the
// anchored patterns ignore a commented option but still match an indented,
// active one.
func TestRpcSurfaceScanners_IgnoreCommentedOptions(t *testing.T) {
	cases := []struct {
		name string
		re   interface {
			MatchString(string) bool
		}
		option string
	}{
		{"service_c2s", serviceC2sRe, "option (spec.service_c2s) = {};"},
		{"service_c2d", serviceC2dRe, "option (spec.service_c2d) = {};"},
		{"event_label", eventLabelRe, "option (spec.event_label) = \"x\";"},
	}
	for _, tc := range cases {
		commented := "service Foo {\n  // " + tc.option + "\n  rpc Ping(Empty) returns (Empty);\n}\n"
		if tc.re.MatchString(commented) {
			t.Errorf("%s: a commented-out option must not match:\n%s", tc.name, commented)
		}
		active := "service Foo {\n  " + tc.option + "\n  rpc Ping(Empty) returns (Empty);\n}\n"
		if !tc.re.MatchString(active) {
			t.Errorf("%s: an active indented option must still match:\n%s", tc.name, active)
		}
	}
}

// MS-10 (findContainingMessage): the containing-message lookup used
// strings.Index for the literal "option (spec.grpc_config)", which would find a
// COMMENTED occurrence appearing before the real annotation and resolve the
// wrong message name. It now uses the comment-safe regex match index. This test
// pins that a commented occurrence in an earlier message does not steal the
// containing-message resolution from the real one.
func TestFindContainingMessageIgnoresCommentedGrpcConfig(t *testing.T) {
	text := `syntax = "proto3";
package foo;
message Decoy {
  // option (spec.grpc_config) = {service_name: "ghost"};
  string a = 1;
}
message RealConfig {
  option (spec.grpc_config) = {service_name: "realsvc"};
  string b = 1;
}
`
	loc := grpcConfigRe.FindStringIndex(text)
	if loc == nil {
		t.Fatal("real grpc_config must match")
	}
	if got := findContainingMessage(text, loc[0]); got != "RealConfig" {
		t.Fatalf("containing message = %q, want RealConfig (a commented decoy must not win)", got)
	}
	// Panic-safety: a negative index returns empty, not a slice panic.
	if got := findContainingMessage(text, -1); got != "" {
		t.Fatalf("negative index must return empty, got %q", got)
	}
}

// TestInjectTenantConfigSubscriptionsPropagatesParserError asserts that a
// descriptor-parser failure is surfaced instead of being swallowed (which left
// the service quietly missing its per-service config subscription).
func TestInjectTenantConfigSubscriptionsPropagatesParserError(t *testing.T) {
	config := &tactype.Config{SelfPackage: "gateway"}
	entries := []GrpcConfigEntry{{
		ServiceName:     "audit",
		ConfigType:      "audit.Config",
		ProtoImportPath: "services/audit.proto",
	}}

	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	err := InjectTenantConfigSubscriptions(config, missingDir, entries)
	if err == nil {
		t.Fatal("expected an error when the descriptor parser cannot be created, got nil")
	}
	if !strings.Contains(err.Error(), "tenant config subscriptions") {
		t.Fatalf("error not wrapped with context: %v", err)
	}
	if len(config.States) != 0 {
		t.Fatalf("expected no states to be injected on failure, got %v", config.States)
	}
}

// ResolveServiceStates is the one state-derivation sequence the Go, Java and
// Rust generators run. A service named by a field of another grpc_config
// service's (spec.per_service_config) message must come out subscribed,
// read-only, to that owner's ConfigState; a service the message does not name
// must not. Before the shared sequence only the Go generator ran the
// subscription step, so a Java or Rust service silently missed its slice.
func TestResolveServiceStatesSubscribesNamedServiceToItsConfigSlice(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	// The full sys tree: ResolveServiceStates also injects ServiceHealth.
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: audit\n    id: 5\n  - name: gateway\n    id: 6\n  - name: billing\n    id: 7\n"), 0o644); err != nil {
		t.Fatalf("write registry.yaml: %v", err)
	}
	writeProto(t, dir, "audit.proto", `syntax = "proto3";
package audit;
import "sys/spec.proto";
option (spec.service) = {name: "audit", store: true};
message GatewaySlice { string route = 1; }
message Slices {
  option (spec.per_service_config) = true;
  GatewaySlice gateway = 1;
}
message AuditConfig {
  option (spec.grpc_config) = {service_name: "audit"};
  map<string, Slices> tenants = 1;
}
`)
	writeProto(t, dir, "health.proto", `syntax = "proto3";
package health;
import "sys/healthreport.proto";
import "sys/spec.proto";
message ServiceHealth {
  option (spec.service_state) = {name: "ServiceHealth", label: ["state-store"], export: true};
  repeated healthreport.ServiceInstanceHealth instances = 1 [(spec.instance_keyed) = true, (spec.assert_max_size) = 10000];
}
`)
	entries, err := InjectGrpcConfigState(dir, dir, dir)
	if err != nil {
		t.Fatalf("InjectGrpcConfigState: %v", err)
	}
	owner := ConfigStateRootName("audit")

	for _, tc := range []struct {
		self       string
		subscribed bool
	}{
		{self: "gateway", subscribed: true},
		{self: "billing", subscribed: false},
	} {
		config := &tactype.Config{SelfPackage: tc.self, FlatProtoDir: dir}
		if err := ResolveServiceStates(config, entries); err != nil {
			t.Fatalf("%s: ResolveServiceStates: %v", tc.self, err)
		}
		var got *tactype.StateEntry
		for i := range config.States {
			if config.States[i].MessageName == owner {
				got = &config.States[i]
			}
		}
		if !tc.subscribed {
			if got != nil {
				t.Fatalf("%s is not named by the per_service_config message but was subscribed: %+v", tc.self, *got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("%s: no subscription to %s in %+v", tc.self, owner, config.States)
		}
		if got.AccessMode != tactype.AccessRO || got.ProtoFile != "audit.proto" {
			t.Fatalf("%s: subscription must be read-only on audit.proto, got %+v", tc.self, *got)
		}
	}
}
