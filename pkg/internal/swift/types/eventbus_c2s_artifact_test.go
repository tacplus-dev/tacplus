// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

func TestGenerateRecordC2SPublishClientArtifact(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "swift")
	cfg := &tactype.Config{
		Language:         "swift",
		InputDir:         filepath.Join("..", "..", "proto", "testdata", "eventbus_c2s_contract"),
		OutputDir:        outputDir,
		SelfPackage:      "tasks",
		SwiftPackageBase: "TasksClient",
	}
	if err := Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	facade, err := os.ReadFile(filepath.Join(outputDir, "Sources", "TasksClient", "C2SClient.swift"))
	if err != nil {
		t.Fatalf("read C2SClient.swift: %v", err)
	}
	if !strings.Contains(string(facade), "withDelegationProbeBusStateService") {
		t.Fatalf("C2SClient.swift has no typed DelegationProbe publish client:\n%s", facade)
	}
	if strings.Contains(string(facade), "withInternalAuditBusStateService") {
		t.Fatalf("C2SClient.swift exposed the internal-only EventBus service:\n%s", facade)
	}

	var grpcArtifact string
	err = filepath.Walk(filepath.Join(outputDir, "Sources", "TasksClient", "Generated"), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".grpc.swift") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(content), "DelegationProbeBusStateService") {
			grpcArtifact = string(content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk generated Swift artifacts: %v", err)
	}
	if grpcArtifact == "" {
		t.Fatal("generated Swift artifacts have no DelegationProbeBusStateService client")
	}
	start := strings.Index(grpcArtifact, "// MARK: - tasks.DelegationProbeBusStateService")
	if start < 0 {
		t.Fatalf("could not isolate generated DelegationProbeBusStateService artifact:\n%s", grpcArtifact)
	}
	tail := grpcArtifact[start:]
	end := strings.Index(tail[1:], "// MARK: - tasks.InternalAuditBusStateService")
	if end < 0 {
		t.Fatalf("could not isolate generated DelegationProbeBusStateService artifact:\n%s", grpcArtifact)
	}
	delegationArtifact := tail[:end+1]
	for _, want := range []string{"PublishToStream", "bidirectionalStreaming"} {
		if !strings.Contains(delegationArtifact, want) {
			t.Fatalf("generated Swift C2S artifact missing %q:\n%s", want, delegationArtifact)
		}
	}
	if strings.Contains(delegationArtifact, "SubscribeToStream") {
		t.Fatalf("generated Swift C2S artifact exposed SubscribeToStream:\n%s", delegationArtifact)
	}
}
