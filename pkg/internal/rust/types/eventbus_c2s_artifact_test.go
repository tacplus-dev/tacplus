// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestGenerateRecordC2SPublishClientArtifact(t *testing.T) {
	inputDir := filepath.Join("..", "..", "proto", "testdata", "eventbus_c2s_contract")
	flatProtoDir := filepath.Join(t.TempDir(), "flat")
	if err := os.CopyFS(flatProtoDir, os.DirFS(inputDir)); err != nil {
		t.Fatalf("copy flat proto fixture: %v", err)
	}
	if err := protogen.CopySysProtos(flatProtoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(flatProtoDir, flatProtoDir); err != nil {
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
		SelfPackage:   "tasks",
		CrateBasePath: "tasks-model",
	}
	if err := Generate(config); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	artifactPath := filepath.Join(outputDir, "c2sm", "internal", "grpc", "clients", "services", "tasks", "delegation_probe_bus_state_service.rs")
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read generated DelegationProbeBusStateService client: %v", err)
	}
	generated := string(artifact)
	for _, want := range []string{
		"pub fn publish_to_stream",
		"PublishToStreamBidiHandle",
		"DelegationProbe",
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated Rust C2S client missing %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "subscribe_to_stream") || strings.Contains(generated, "SubscribeToStream") {
		t.Fatalf("generated Rust C2S client exposed SubscribeToStream:\n%s", generated)
	}
}
