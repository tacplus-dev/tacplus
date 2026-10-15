// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The standalone c2d host (ServiceManager::serve_c2d_standalone) is emitted for
// a c2d-only, cluster-free build and for nothing else; cross-test/c2d-server
// runs it. Go and Java twins: TestGoServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild,
// TestJavaServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild.
func TestRustServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild(t *testing.T) {
	svcs := []service.ServiceInfo{
		{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedApi", ParentDir: "svc", IsGrpc: true, IsS2S: true},
		{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedDaemon", ParentDir: "svc", IsGrpc: true, IsC2D: true, C2DSocketPath: "/run/mixed.sock"},
	}
	hosts := func(cfg *tactype.Config, s []service.ServiceInfo) bool {
		t.Helper()
		data, err := buildSMTemplateData(cfg, s, nil, nil)
		if err != nil {
			t.Fatalf("buildSMTemplateData: %v", err)
		}
		return data.HostsC2DStandalone
	}
	// The ServiceManager data builder scans the flat proto dir (requires_device,
	// MCP); a sys-only dir is the smallest one it accepts.
	dir := t.TempDir()
	if err := protogen.CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(dir, dir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	cfg := &tactype.Config{CrateBasePath: "mixed-model", SelfPackage: "mixed", FlatProtoDir: dir, AnnotatedProtoDir: dir}
	if !hosts(cfg, svcs[1:]) {
		t.Fatal("c2d-only, cluster-free build must get the standalone host")
	}
	if hosts(cfg, svcs) {
		t.Fatal("a build that also hosts a TLS service keeps run only")
	}
	if hosts(&tactype.Config{CrateBasePath: "mixed-model", SelfPackage: "mixed", FlatProtoDir: dir, AnnotatedProtoDir: dir, AllStates: true}, svcs[1:]) {
		t.Fatal("a c2d-only build that needs state keeps run only")
	}
}
