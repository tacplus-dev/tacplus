// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func pushSliceProto(pkg, msg, scope, service string) string {
	return "syntax = \"proto3\";\n\npackage " + pkg + ";\n\nimport \"sys/spec.proto\";\n\n" +
		"message " + msg + " {\n  option (spec.config_slice) = {scope: " + scope + ", service_name: \"" + service + "\", delivery: CONFIG_SLICE_DELIVERY_PUSH};\n  string x = 1;\n}\n"
}

// A pushed tenant slice (tacplus#927) is identified on the wire by its owner's
// registered tenant_slice_field, and is no field of the generated
// TenantConfig: the owner holds it in memory, never in a state root. The
// owner's durable slice keeps its TenantConfig field, and the pushed one is
// carried beside it, under the same registered number, in configpush.
func TestPushedSliceIsNoTenantConfigField(t *testing.T) {
	skipWithoutProtoc(t)
	dir := t.TempDir()
	writeSliceFixture(t, dir, "registries: [g]\nservices:\n"+
		"    - {name: kernel, id: 8, tenant_slice_field: 1}\n"+
		"    - {name: stratum, id: 34, tenant_slice_field: 4}\n", map[string]string{
		"kernel_tenant.proto":   sliceProto("kernel_tenant", "KernelTenantConfig", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
		"kernel_settings.proto": pushSliceProto("kernel_settings", "KernelTenantSettings", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
		"stratum_tenant.proto":  sliceProto("stratum_tenant", "StratumTenantConfig", "CONFIG_SLICE_SCOPE_TENANT", "stratum"),
	})
	if err := GenerateTenantConfig(dir, dir, dir); err != nil {
		t.Fatalf("GenerateTenantConfig: %v", err)
	}
	tenant, _ := os.ReadFile(filepath.Join(dir, "generated", "tenant_config.proto"))
	if !strings.Contains(string(tenant), "kernel_tenant.KernelTenantConfig kernel = 1;") || !strings.Contains(string(tenant), "stratum = 4;") {
		t.Fatalf("the durable slices must stay TenantConfig fields:\n%s", tenant)
	}
	if strings.Contains(string(tenant), "KernelTenantSettings") || strings.Contains(string(tenant), "kernel_settings.proto") {
		t.Fatalf("a pushed slice must not be a TenantConfig field:\n%s", tenant)
	}

	reg, err := LoadServiceRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := PushedConfigSlices(dp, reg)
	if err != nil {
		t.Fatalf("PushedConfigSlices: %v", err)
	}
	if len(pushed) != 1 {
		t.Fatalf("want one pushed slice, got %+v", pushed)
	}
	got := pushed[0]
	if got.Owner != "kernel" || got.SliceField != 1 || got.MessageFullName != "kernel_settings.KernelTenantSettings" || got.ProtoFile != "kernel_settings.proto" {
		t.Fatalf("pushed slice = %+v", got)
	}
}

// A pushed slice whose owner has no registered tenant_slice_field is refused,
// in a lock and in a legacy registry alike: the number is the slice's identity
// between two builds, which only the registry may give.
func TestPushedSliceNeedsARegisteredField(t *testing.T) {
	skipWithoutProtoc(t)
	files := map[string]string{
		"kernel_settings.proto": pushSliceProto("kernel_settings", "KernelTenantSettings", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
	}
	for name, reg := range map[string]string{
		"lock":   "registries: [g]\nservices:\n    - {name: kernel, id: 8}\n",
		"legacy": "services:\n    - {name: kernel, id: 8}\n",
	} {
		dir := t.TempDir()
		writeSliceFixture(t, dir, reg, files)
		err := GenerateTenantConfig(dir, dir, dir)
		if err == nil || !strings.Contains(err.Error(), "tacplus registry register kernel --tenant-slice") {
			t.Fatalf("%s: an unregistered pushed slice must fail the build, got %v", name, err)
		}
	}
}

// PUSH is for a tenant slice delivered to a /system owner: a project slice, a
// tenant-owned owner and a second pushed slice for one owner are refused.
func TestPushedSliceRefusals(t *testing.T) {
	skipWithoutProtoc(t)
	cases := []struct {
		name  string
		files map[string]string
		gen   func(dir string) error
		want  string
	}{
		{
			name: "project scope",
			files: map[string]string{
				"kernel_project.proto": pushSliceProto("kernel_project", "KernelProjectSettings", "CONFIG_SLICE_SCOPE_PROJECT", "kernel"),
			},
			gen:  func(dir string) error { return GenerateProjectConfig(dir, dir, dir) },
			want: "CONFIG_SLICE_DELIVERY_PUSH is for a tenant-scope slice",
		},
		{
			name: "tenant-owned owner",
			files: map[string]string{
				"crew.proto": "syntax = \"proto3\";\n\npackage crew;\n\nimport \"sys/spec.proto\";\n\n" +
					"option (spec.service) = {name: \"crew\", tenant_owned: true};\n\nmessage Noop {}\n",
				"crew_settings.proto": pushSliceProto("crew_settings", "CrewSettings", "CONFIG_SLICE_SCOPE_TENANT", "crew"),
			},
			gen:  func(dir string) error { return GenerateTenantConfig(dir, dir, dir) },
			want: "is tenant-owned",
		},
		{
			name: "two pushed slices",
			files: map[string]string{
				"kernel_settings.proto": pushSliceProto("kernel_settings", "KernelTenantSettings", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
				"kernel_more.proto":     pushSliceProto("kernel_more", "KernelMoreSettings", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
			},
			gen:  func(dir string) error { return GenerateTenantConfig(dir, dir, dir) },
			want: "two pushed slices",
		},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeSliceFixture(t, dir, "registries: [g]\nservices:\n"+
			"    - {name: kernel, id: 8, tenant_slice_field: 1, project_slice_field: 1}\n"+
			"    - {name: crew, id: 9, tenant_slice_field: 2}\n", c.files)
		err := c.gen(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: want an error containing %q, got %v", c.name, c.want, err)
		}
	}
}
