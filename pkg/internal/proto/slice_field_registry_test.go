// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeSliceFixture(t *testing.T, dir, registryYAML string, files map[string]string) {
	t.Helper()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"), []byte(registryYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func sliceProto(pkg, msg, scope, service string) string {
	return "syntax = \"proto3\";\n\npackage " + pkg + ";\n\nimport \"sys/spec.proto\";\n\n" +
		"message " + msg + " {\n  option (spec.config_slice) = {scope: " + scope + ", service_name: \"" + service + "\"};\n  string x = 1;\n}\n"
}

// A build whose registry is a lock takes each slice's TenantConfig /
// ProjectConfig field from it and never assigns one: two builds (stratum's,
// kernel's) that load different sets of slices would otherwise number the same
// slice differently on the wire (tacplus#352).
func TestSliceFieldsComeOnlyFromALock(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed config-slice test")
	}
	files := map[string]string{
		"kernel_tenant.proto":   sliceProto("kernel_tenant", "KernelTenantConfig", "CONFIG_SLICE_SCOPE_TENANT", "kernel"),
		"stratum_tenant.proto":  sliceProto("stratum_tenant", "StratumTenantConfig", "CONFIG_SLICE_SCOPE_TENANT", "stratum"),
		"stratum_project.proto": sliceProto("stratum_project", "StratumProjectConfig", "CONFIG_SLICE_SCOPE_PROJECT", "stratum"),
	}

	registered := t.TempDir()
	writeSliceFixture(t, registered, "registries: [g]\nservices:\n"+
		"    - {name: kernel, id: 8, tenant_slice_field: 1}\n"+
		"    - {name: stratum, id: 34, tenant_slice_field: 4, project_slice_field: 7}\n", files)
	if err := GenerateTenantConfig(registered, registered, registered); err != nil {
		t.Fatalf("GenerateTenantConfig: %v", err)
	}
	if err := GenerateProjectConfig(registered, registered, registered); err != nil {
		t.Fatalf("GenerateProjectConfig: %v", err)
	}
	tenant, _ := os.ReadFile(filepath.Join(registered, "generated", "tenant_config.proto"))
	if !strings.Contains(string(tenant), "kernel = 1;") || !strings.Contains(string(tenant), "stratum = 4;") {
		t.Fatalf("tenant_config.proto does not use the registered fields:\n%s", tenant)
	}
	project, _ := os.ReadFile(filepath.Join(registered, "generated", "project_config.proto"))
	if !strings.Contains(string(project), "stratum = 7;") {
		t.Fatalf("project_config.proto does not use the registered field:\n%s", project)
	}

	unregistered := t.TempDir()
	writeSliceFixture(t, unregistered, "registries: [g]\nservices:\n"+
		"    - {name: kernel, id: 8, tenant_slice_field: 1}\n"+
		"    - {name: stratum, id: 34}\n", files)
	err := GenerateTenantConfig(unregistered, unregistered, unregistered)
	if err == nil || !strings.Contains(err.Error(), "tacplus registry register stratum --tenant-slice") {
		t.Fatalf("an unregistered tenant slice must fail the build, got %v", err)
	}
	err = GenerateProjectConfig(unregistered, unregistered, unregistered)
	if err == nil || !strings.Contains(err.Error(), "tacplus registry register stratum --project-slice") {
		t.Fatalf("an unregistered project slice must fail the build, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(unregistered, "generated", "tenant_config.proto")); !os.IsNotExist(err) {
		t.Fatalf("a refused build still wrote tenant_config.proto: %v", err)
	}
}

// A legacy per-repo registry still assigns a new slice's field in the build,
// but in the order of the slices' files, not of a map's iteration: the same
// inputs number the same way every time.
func TestLegacySliceFieldAssignmentIsDeterministic(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed config-slice test")
	}
	files := map[string]string{}
	for _, svc := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		files[svc+"_tenant.proto"] = sliceProto(svc+"_tenant", "TenantConfig"+svc, "CONFIG_SLICE_SCOPE_TENANT", svc)
	}
	for i := 0; i < 8; i++ {
		dir := t.TempDir()
		writeSliceFixture(t, dir, "services: []\n", files)
		if err := GenerateTenantConfig(dir, dir, dir); err != nil {
			t.Fatalf("GenerateTenantConfig: %v", err)
		}
		tenant, _ := os.ReadFile(filepath.Join(dir, "generated", "tenant_config.proto"))
		for n, svc := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
			if want := svc + " = " + string(rune('1'+n)) + ";"; !strings.Contains(string(tenant), want) {
				t.Fatalf("run %d: want %q in\n%s", i, want, tenant)
			}
		}
	}
}
