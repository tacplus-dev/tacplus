// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// TestGenerateCarriesSourceLayer proves registry.json names, on each service and
// on each entitlement grant (exact and wildcard), the layer of the source tree
// its package came from, so a consumer can tell platform grants from
// application grants without a hand-kept list.
func TestGenerateCarriesSourceLayer(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sys", "spec.proto"), specProto, 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(rel, pkg, svc string) {
		text := `syntax = "proto3";
package ` + pkg + `;
import "sys/spec.proto";
message Req { string id = 1; }
service ` + svc + ` {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc Get(Req) returns (Req);
}
`
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("kernel/kernel.proto", "kernel", "KernelService")
	write("fleet/fleet.proto", "fleet", "FleetService")
	write("loose/loose.proto", "loose", "LooseService")

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
		FileLayers: map[string]string{
			"kernel/kernel.proto": "platform",
			"fleet/fleet.proto":   "app",
		},
	}, []model.ProtoFile{
		{Package: "kernel", RelPath: "kernel/kernel.proto"},
		{Package: "fleet", RelPath: "fleet/fleet.proto"},
		{Package: "loose", RelPath: "loose/loose.proto"},
	}, dp)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root spec.PackageNode
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal registry.json: %v", err)
	}

	wantGrant := map[string]string{
		"KERNEL_KERNEL_SERVICE_GET": "platform",
		"KERNEL_ANY":                "platform",
		"FLEET_FLEET_SERVICE_GET":   "app",
		"FLEET_ANY":                 "app",
		"LOOSE_LOOSE_SERVICE_GET":   "", // source declared no layer
	}
	seen := map[string]bool{}
	for _, g := range root.GetEntitlementGrants() {
		want, ok := wantGrant[g.GetToken()]
		if !ok {
			continue
		}
		seen[g.GetToken()] = true
		if g.GetSource() != want {
			t.Errorf("grant %s source = %q, want %q", g.GetToken(), g.GetSource(), want)
		}
	}
	for tok := range wantGrant {
		if !seen[tok] {
			t.Errorf("grant %s missing from catalog", tok)
		}
	}

	wantSvc := map[string]string{"KernelService": "platform", "FleetService": "app", "LooseService": ""}
	var walk func(n *spec.PackageNode)
	walk = func(n *spec.PackageNode) {
		for _, s := range n.GetServices() {
			if want, ok := wantSvc[s.GetName()]; ok {
				if s.GetSource() != want {
					t.Errorf("service %s source = %q, want %q", s.GetName(), s.GetSource(), want)
				}
				delete(wantSvc, s.GetName())
			}
		}
		for _, c := range n.GetChildren() {
			walk(c)
		}
	}
	walk(&root)
	if len(wantSvc) != 0 {
		t.Errorf("services missing from registry: %v", wantSvc)
	}
}

func TestPackageLayersAmbiguousPackageHasNoLayer(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{Path: "a/one.proto", Package: "shared"},
		{Path: "b/two.proto", Package: "shared"},
		{Path: "a/three.proto", Package: "solo"},
	}
	got := packageLayers(fds, map[string]string{"a/one.proto": "platform", "b/two.proto": "app", "a/three.proto": "platform"})
	if got["shared"] != "" || got["solo"] != "platform" {
		t.Errorf("packageLayers = %v", got)
	}
	if packageLayers(fds, nil) != nil {
		t.Errorf("no layers should give nil")
	}
}
