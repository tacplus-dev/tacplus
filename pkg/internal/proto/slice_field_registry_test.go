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
