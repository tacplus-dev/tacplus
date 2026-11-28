// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"go/parser"
	"go/token"
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
// (tacplus#927); any other service, the record store included, gets none.
func TestGoConfigPushSlicesAreTheOwners(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := writeConfigPushFixture(t)
	cfg := func(self string) *tactype.Config {
		return &tactype.Config{InputDir: dir, FlatProtoDir: dir, SelfPackage: self, ModuleBasePath: "example.com/m/model"}
	}
	kernel, err := goConfigPushSlices(cfg("kernel"))
	if err != nil {
		t.Fatalf("goConfigPushSlices: %v", err)
	}
	if len(kernel) != 1 || kernel[0].Field != 1 || kernel[0].MessageName != "KernelTenantSettings" ||
		kernel[0].PkgImport != "example.com/m/model/types/kernel/kernelsettings" {
		t.Fatalf("kernel's pushed slices = %+v", kernel)
	}
	for _, other := range []string{"stratum", ""} {
		got, err := goConfigPushSlices(cfg(other))
		if err != nil || len(got) != 0 {
			t.Fatalf("%q must own no pushed slice, got %+v, %v", other, got, err)
		}
	}

	out := t.TempDir()
	if err := generateConfigPushFile(out, serviceManagerTemplateData{ModuleBasePath: "example.com/m/model", ConfigPushSlices: kernel}, "configpush"); err != nil {
		t.Fatalf("generateConfigPushFile: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(out, "configpush.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "configpush.go", src, 0); err != nil {
		t.Fatalf("sm/configpush.go does not parse: %v\n%s", err, src)
	}
	for _, want := range []string{
		"func (c *ConfigPushSlices) KernelTenantSettings(tid string) (*kernelsettings_push_pb.KernelTenantSettings, int64, bool)",
		"c.r.Get(tid, 1)",
		"kernelsettings_push_pb.ValidateKernelTenantSettings(m)",
		"{Field: 1, Decode: decodeKernelTenantSettingsPush}",
		"r.SetTrustDomain(sm.cfg.GetSpiffeTrustDomain())",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("sm/configpush.go is missing %q:\n%s", want, src)
		}
	}
}

// The ServiceManager serves the receiver beside its services, with the Handler
// as its TenantRegistry, which the Handler interface then requires.
func TestServiceManagerServesConfigPush(t *testing.T) {
	for file, wants := range map[string][]string{
		"go/types/service_manager.go.tmpl": {
			"sm.configPush = newConfigPushReceiver(sm)",
			"sm.configPush.SetTenantRegistry(h)",
			"mw.impls = append(mw.impls, sm.configPush)",
		},
		"go/types/handler.go.tmpl": {
			"HoldsTenant(ctx context.Context, tid string) (bool, error)",
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
}

// The receiver chassis renders to Go that parses, and keeps its rules.
func TestGoConfigPushChassisRenders(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/configpush/configpush.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.ReplaceAll(string(src), "{{.ModuleBasePath}}", "example.com/m/model")
	if _, err := parser.ParseFile(token.NewFileSet(), "configpush.go", rendered, 0); err != nil {
		t.Fatalf("chassis/configpush does not parse: %v", err)
	}
	for _, want := range []string{
		"wrapperSpiffe.ParseSpiffeID(subject)",
		"version > prev.Version",
		"codes.FailedPrecondition",
		"if reg == nil {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("chassis/configpush is missing %q", want)
		}
	}
}

// A tenant's record store holds a publisher of every owner its registry gives
// a tenant_slice_field, each stream on its owner's connection with a token for
// that owner (tacplus#927).
func TestRecordStoreConfigPushPublisherFile(t *testing.T) {
	out := t.TempDir()
	data := serviceManagerTemplateData{ModuleBasePath: "example.com/m/model", ConfigPushOwners: []stateproto.ConfigPushOwner{
		{SliceField: 1, Owner: "kernel"}, {SliceField: 2, Owner: "audit"},
	}}
	if err := generateConfigPushFile(out, data, "configpush_publisher"); err != nil {
		t.Fatalf("generateConfigPushFile: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(out, "configpush_publisher.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "configpush_publisher.go", src, 0); err != nil {
		t.Fatalf("sm/configpush_publisher.go does not parse: %v\n%s", err, src)
	}
	for _, want := range []string{
		"func (sm *ServiceManager) ConfigPushPublisher() *configpush.Publisher",
		`1: "kernel",`,
		`2: "audit",`,
		"sm.identity.GetTokenFor(owner)",
		"serviceconnection.NewChannelProvider(conn)",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("sm/configpush_publisher.go is missing %q:\n%s", want, src)
		}
	}
}

// A (spec.record_store).config_push store feeds its publisher from its own
// configpush.TenantSliceRecord entries, watched through the Handler's
// Watch<T>Changes on the record plane it hosts (tacplus#927).
func TestRecordStoreConfigPushFeed(t *testing.T) {
	plane := &stateproto.ConfigPushFeedPlane{Package: "tquery", S2SService: "TenantRecordMap"}
	svcs := []ServiceInfo{{PackageName: "tstratum", ServiceName: "TStratum"}, {PackageName: "tquery", ServiceName: "TenantRecordMap"}}
	entries := []GrpcServiceEntry{
		{HandlerType: "TStratumHandler", GetterName: "GetTStratumGrpcImpl"},
		{HandlerType: "TenantRecordMapHandler", GetterName: "GetTenantRecordMapGrpcImpl", WrapperAlias: "tquery_fe",
			WrapperImportPath: "example.com/m/model/s2sm/grpc/servers/vservices/tquery", TypeAlias: "tquery_pb",
			TypeImportPath: "example.com/m/model/types/vservices/tquery"},
	}
	feed, err := goConfigPushFeedFor(plane, svcs, entries)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goConfigPushFeedFor(plane, svcs[:1], entries[:1]); err == nil {
		t.Fatal("a store that hosts no plane to watch must fail generation")
	}
	if none, err := goConfigPushFeedFor(nil, svcs, entries); none != nil || err != nil {
		t.Fatalf("no config_push, no feed: %v, %v", none, err)
	}

	out := t.TempDir()
	data := serviceManagerTemplateData{ModuleBasePath: "example.com/m/model",
		ConfigPushOwners: []stateproto.ConfigPushOwner{{SliceField: 1, Owner: "kernel"}}, ConfigPushFeed: feed}
	if err := generateConfigPushFile(out, data, "configpush_publisher"); err != nil {
		t.Fatalf("generateConfigPushFile: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(out, "configpush_publisher.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "configpush_publisher.go", src, 0); err != nil {
		t.Fatalf("sm/configpush_publisher.go does not parse: %v\n%s", err, src)
	}
	for _, want := range []string{
		"func (sm *ServiceManager) feedConfigPushPublisher(plane tquery_fe.TenantRecordMapHandler)",
		"plane.WatchTenantSliceRecordChanges(nil, &tquery_pb.WatchTenantSliceRecordChangesRequest{TargetTenant: tid})",
		"c.GetType() == tquery_pb.RecordChangeType_RECORD_CHANGE_TYPE_DELETE",
		"sm.configPushPublisher.Feed(",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("sm/configpush_publisher.go is missing %q:\n%s", want, src)
		}
	}
	tmpl, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tmpl), "sm.feedConfigPushPublisher(h.{{.ConfigPushFeed.GetterName}}())") {
		t.Fatal("the ServiceManager does not start the feed")
	}
}
