// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// The owner of a pushed slice gets its receiver and typed accessor in Java
// (tacplus#927); any other service gets none.
func TestJavaConfigPushSlicesAreTheOwners(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"sys/spec.proto": string(spec),
		"registry.yaml":  "registries: [g]\nservices:\n    - {name: kernel, id: 8, tenant_slice_field: 1}\n",
		"kernel/settings.proto": "syntax = \"proto3\";\n\npackage kernelsettings;\n\nimport \"sys/spec.proto\";\n\noption java_outer_classname = \"KernelSettingsProto\";\n\n" +
			"message KernelTenantSettings {\n  option (spec.config_slice) = {scope: CONFIG_SLICE_SCOPE_TENANT, service_name: \"kernel\", delivery: CONFIG_SLICE_DELIVERY_PUSH};\n  string join_mode = 1;\n}\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := func(self string) *tactype.Config {
		return &tactype.Config{InputDir: dir, FlatProtoDir: dir, SelfPackage: self, JavaPackageBase: "com.example.model"}
	}
	got, err := javaConfigPushSlices(cfg("kernel"), dir)
	if err != nil {
		t.Fatalf("javaConfigPushSlices: %v", err)
	}
	if len(got) != 1 || got[0].Field != 1 || got[0].Accessor != "kernelTenantSettings" ||
		got[0].JavaType != "com.example.model.types.kernel.kernelsettings.KernelSettingsProto.KernelTenantSettings" ||
		got[0].Helpers != "com.example.model.types.kernel.kernelsettings.Helpers" {
		t.Fatalf("kernel's pushed slices = %+v", got)
	}
	if other, err := javaConfigPushSlices(cfg("stratum"), dir); err != nil || len(other) != 0 {
		t.Fatalf("stratum must own no pushed slice, got %+v, %v", other, err)
	}
}

// The ServiceManager serves the receiver beside its services, with the Handler
// as its tenant registry, which the Handler interface then requires.
func TestJavaServiceManagerServesConfigPush(t *testing.T) {
	for file, wants := range map[string][]string{
		"java/types/ServiceManager.java.tmpl": {
			"configPush.setTenantRegistry(handler::holdsTenant);",
			"configPush.setTrustDomain(config.getSpiffeTrustDomain());",
			"grpcServerBuilder.addService(configPush);",
			"{{$s.Helpers}}.validate{{$s.MessageName}}(m);",
		},
		"java/types/Handler.java.tmpl": {
			"boolean holdsTenant(String tid) throws Exception;",
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
