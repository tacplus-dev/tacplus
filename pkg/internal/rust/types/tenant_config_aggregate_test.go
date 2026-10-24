// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestGenerateCompilesTenantConfigAggregate pins issue #129: the TenantConfig
// prost compiles must be the GENERATED aggregate carrying every declared
// tenant slice, not the empty stub. The config-holder re-scan in Generate
// rewrites generated/tenant_config.proto with the stub, so the aggregate has to
// be regenerated after it, as Go and Java do.
func TestGenerateCompilesTenantConfigAggregate(t *testing.T) {
	// A full Generate needs the protoc-gen-prost protoc plugin, which the Go-only
	// CI runners do not carry; the nightly gates container does.
	if _, err := exec.LookPath("protoc-gen-prost"); err != nil && os.Getenv("PROTOC_RUST_BINARY") == "" {
		t.Skip("protoc-gen-prost not on PATH")
	}
	// Generate saves registry.yaml back into InputDir; work on a copy.
	inputDir := filepath.Join(t.TempDir(), "input")
	if err := os.CopyFS(inputDir, os.DirFS(filepath.Join("..", "..", "..", "..", "cross-test", "proto-tenantslice"))); err != nil {
		t.Fatalf("copy proto-tenantslice fixture: %v", err)
	}
	flatProtoDir := filepath.Join(t.TempDir(), "flat")
	if err := os.CopyFS(flatProtoDir, os.DirFS(inputDir)); err != nil {
		t.Fatalf("copy flat proto fixture: %v", err)
	}
	if err := protogen.CopySysProtos(flatProtoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(inputDir, flatProtoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	packages.SetProtoDir(filepath.Join("..", "..", "..", "..", "cross-test"))
	defer packages.SetProtoDir("")

	outputDir := filepath.Join(t.TempDir(), "rust")
	config := &tactype.Config{
		Language:      "rust",
		InputDir:      inputDir,
		FlatProtoDir:  flatProtoDir,
		OutputDir:     outputDir,
		SelfPackage:   "ledger",
		CrateBasePath: "ledger-model",
	}
	if err := Generate(config); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	path := filepath.Join(outputDir, "types", "generated", "tenant_config", "tenant_config.rs")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated TenantConfig: %v", err)
	}
	if !strings.Contains(string(src), "pub ledger:") {
		t.Fatalf("generated Rust TenantConfig is missing ledger's tenant slice (compiled the stub?):\n%s", src)
	}
}
