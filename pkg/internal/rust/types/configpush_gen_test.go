// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// writeConfigPushFixture writes a proto dir where kernel owns one pushed
// tenant slice and stratum one durable slice.
func writeConfigPushFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"sys/spec.proto": string(spec),
		"registry.yaml": "registries: [g]\nservices:\n" +
			"    - {name: kernel, id: 8, tenant_slice_field: 1}\n" +
			"    - {name: stratum, id: 34, tenant_slice_field: 4}\n",
		"kernel/settings.proto": "syntax = \"proto3\";\n\npackage kernelsettings;\n\nimport \"sys/spec.proto\";\n\n" +
			"message KernelTenantSettings {\n  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: \"kernel\", delivery: CONFIG_SLICE_DELIVERY_PUSH};\n  string join_mode = 1;\n}\n",
		"stratum/tenant.proto": "syntax = \"proto3\";\n\npackage stratumtenant;\n\nimport \"sys/spec.proto\";\n\n" +
			"message StratumTenantConfig {\n  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: \"stratum\"};\n  string x = 1;\n}\n",
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The owner of a pushed slice gets its receiver and typed accessor
// (tacplus#927), in Rust as in Go and Java; any other service, the record
// store included, gets none.
func TestRustConfigPushSlicesAreTheOwners(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := writeConfigPushFixture(t)
	out := t.TempDir()
	cfg := func(self string) *tactype.Config {
		return &tactype.Config{InputDir: dir, FlatProtoDir: dir, SelfPackage: self, OutputDir: out}
	}
	kernel, err := rustConfigPushSlices(cfg("kernel"))
	if err != nil {
		t.Fatalf("rustConfigPushSlices: %v", err)
	}
	if len(kernel) != 1 || kernel[0].Field != 1 || kernel[0].MessageName != "KernelTenantSettings" ||
		kernel[0].FnName != "kernel_tenant_settings" || kernel[0].ModPath != "crate::types::kernel::kernelsettings" {
		t.Fatalf("kernel's pushed slices = %+v", kernel)
	}
	for _, other := range []string{"stratum", ""} {
		got, err := rustConfigPushSlices(cfg(other))
		if err != nil || len(got) != 0 {
			t.Fatalf("%q must own no pushed slice, got %+v, %v", other, got, err)
		}
	}

	if err := generateConfigPush(cfg("kernel"), smTemplateData{ConfigPushSlices: kernel}); err != nil {
		t.Fatalf("generateConfigPush: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(out, cfg("kernel").GetSmPrefix(), "config_push.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use crate::types::kernel::kernelsettings::KernelTenantSettings;",
		"pub fn kernel_tenant_settings(&self, tid: &str) -> Option<(Arc<KernelTenantSettings>, i64)>",
		"self.r.get(tid, 1)?",
		"m.validate()?;",
		"configpush::Slice { field: 1, decode: decode_kernel_tenant_settings_push }",
		"authorizer.authenticate_caller(header)",
		"r.set_trust_domain(&cfg.spiffe_trust_domain);",
		"self.0.holds_tenant(tid).await",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("s2sm/config_push.rs is missing %q:\n%s", want, src)
		}
	}

	// A service with no pushed slice gets no module: the ServiceManager
	// declares `pub mod config_push;` only with some.
	none := t.TempDir()
	if err := generateConfigPush(&tactype.Config{OutputDir: none}, smTemplateData{}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(none); len(entries) != 0 {
		t.Fatalf("a service with no pushed slice must get no config_push.rs, got %v", entries)
	}
}

// The ServiceManager serves the receiver beside its services, ends its stream
// at its token's exp, and gives it the Handler as its TenantRegistry, which the
// Handler trait then requires.
func TestRustServiceManagerServesConfigPush(t *testing.T) {
	for file, wants := range map[string][]string{
		"rust/types/service_manager.rs.tmpl": {
			"pub mod config_push;",
			"configpush::PUSH_PATH,",
			"\"configpush.ConfigPush\",",
			"let config_push = config_push::new_receiver(&cfg, authorizer.clone());",
			"self.config_push.set_tenant_registry(Arc::new(config_push::HandlerRegistry(handler.clone())));",
			"configpush::ConfigPushServer::new(self.config_push.clone())",
		},
		"rust/types/handler.rs.tmpl": {
			"async fn holds_tenant(&self, tid: &str) -> Result<bool, Box<dyn std::error::Error + Send + Sync>>;",
		},
	} {
		src, err := templates.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(src), want) {
				t.Fatalf("%s is missing %q", file, want)
			}
		}
	}
	// Served in both of run()'s router branches: with and without the
	// Handler's additional_services.
	src, _ := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if n := strings.Count(string(src), "configpush::ConfigPushServer::new(self.config_push.clone())"); n != 2 {
		t.Fatalf("the receiver is served in %d of run()'s 2 router branches", n)
	}
}

// The receiver chassis keeps the rules the Go and Java receivers keep.
func TestRustConfigPushChassisKeepsItsRules(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/chassis/configpush/src/lib.rs.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"spiffe::parse_spiffe_id(subject)",
		"p.reconstruct() != subject",
		"version > prev.version",
		"Status::failed_precondition(\"the first frame is a snapshot\")",
		"let Some(registry) = registry else {",
		"pub const PUSH_PATH: &str = \"/configpush.ConfigPush/Push\";",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("chassis/configpush is missing %q", want)
		}
	}
}

// A tenant's record store holds a publisher of every owner its registry gives
// a tenant_slice_field, each stream on its owner's connection with a token for
// that owner (tacplus#927).
func TestRecordStoreConfigPushPublisherModule(t *testing.T) {
	out := t.TempDir()
	cfg := &tactype.Config{OutputDir: out}
	data := smTemplateData{ConfigPushOwners: []stateproto.ConfigPushOwner{
		{SliceField: 1, Owner: "kernel"}, {SliceField: 2, Owner: "audit"},
	}}
	if err := generateConfigPush(cfg, data); err != nil {
		t.Fatalf("generateConfigPush: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, cfg.GetSmPrefix(), "config_push.rs")); err == nil {
		t.Fatal("a record store that owns no pushed slice must get no receiver module")
	}
	src, err := os.ReadFile(filepath.Join(out, cfg.GetSmPrefix(), "config_push_publisher.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`(1, "kernel".to_string()),`,
		`(2, "audit".to_string()),`,
		".get_token_for(&owner)",
		"ConfigPushClient::new(channel).push(req)",
		"configpush::Publisher::new(owners(), opener)",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("s2sm/config_push_publisher.rs is missing %q:\n%s", want, src)
		}
	}
}

// A (spec.record_store).config_push store's ServiceManager feeds its publisher
// from its own configpush.TenantSliceRecord entries, watched through the
// Handler's watch_tenant_slice_record_changes on the plane it hosts
// (tacplus#927).
func TestRustRecordStoreConfigPushFeed(t *testing.T) {
	plane := &stateproto.ConfigPushFeedPlane{Package: "tquery", S2SService: "TenantRecordMap"}
	entries := []grpcServiceEntry{
		{GrpcServiceName: "tstratum.TStratum", GetterName: "t_stratum_grpc_impl"},
		{GrpcServiceName: "tquery.TenantRecordMap", GetterName: "tenant_record_map_grpc_impl", ModPath: "crate::types::vservices::tquery::tquery"},
	}
	feed, err := rustConfigPushFeedFor(plane, entries)
	if err != nil {
		t.Fatal(err)
	}
	want := rustConfigPushFeed{Method: "watch_tenant_slice_record_changes", GetterName: "tenant_record_map_grpc_impl",
		ModPath: "crate::types::vservices::tquery::tquery", Request: "WatchTenantSliceRecordChangesRequest"}
	if *feed != want {
		t.Fatalf("feed = %+v\nwant %+v", *feed, want)
	}
	if _, err := rustConfigPushFeedFor(plane, entries[:1]); err == nil {
		t.Fatal("a store that hosts no plane to watch must fail generation")
	}
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		".{{.Method}}(None, {{.ModPath}}::{{.Request}} { target_tenant: tid, ..Default::default() })",
		"deleted: c.r#type == {{.ModPath}}::RecordChangeType::Delete as i32,",
		"self.config_push_publisher.feed(watch);",
	} {
		if !strings.Contains(string(src), w) {
			t.Fatalf("service_manager.rs.tmpl is missing %q", w)
		}
	}
}
