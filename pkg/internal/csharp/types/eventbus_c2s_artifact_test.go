// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestGenerateRecordC2SPublishClientArtifact(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "csharp")
	cfg := &tactype.Config{
		Language:          "csharp",
		InputDir:          filepath.Join("..", "..", "proto", "testdata", "eventbus_c2s_contract"),
		OutputDir:         outputDir,
		SelfPackage:       "tasks",
		CSharpPackageBase: "TasksClient",
	}
	if err := Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	facade, err := os.ReadFile(filepath.Join(outputDir, "C2SClient.cs"))
	if err != nil {
		t.Fatalf("read C2SClient.cs: %v", err)
	}
	if !strings.Contains(string(facade), "WithDelegationProbeBusStateService") {
		t.Fatalf("C2SClient.cs has no typed DelegationProbe publish client:\n%s", facade)
	}
	if strings.Contains(string(facade), "WithInternalAuditBusStateService") {
		t.Fatalf("C2SClient.cs exposed the internal-only EventBus service:\n%s", facade)
	}

	project, err := os.ReadFile(filepath.Join(outputDir, "TasksClient.csproj"))
	if err != nil {
		t.Fatalf("read TasksClient.csproj: %v", err)
	}
	if !strings.Contains(string(project), `<Protobuf Update="Protos/services/tasks.proto" GrpcServices="Client" />`) {
		t.Fatalf("TasksClient.csproj does not compile the synthesized C2S service as a client:\n%s", project)
	}

	protoArtifact, err := os.ReadFile(filepath.Join(outputDir, "Protos", "services", "tasks.proto"))
	if err != nil {
		t.Fatalf("read emitted tasks.proto: %v", err)
	}
	protoText := string(protoArtifact)
	start := strings.Index(protoText, "service DelegationProbeBusStateService {")
	if start < 0 {
		t.Fatalf("could not isolate emitted DelegationProbeBusStateService artifact:\n%s", protoText)
	}
	tail := protoText[start:]
	end := strings.Index(tail, "\n}")
	if end < 0 {
		t.Fatalf("could not isolate emitted DelegationProbeBusStateService artifact:\n%s", protoText)
	}
	delegationArtifact := tail[:end+2]
	if !strings.Contains(delegationArtifact, "rpc PublishToStream(stream DelegationProbe) returns (stream eventbus_support.PublishResponse);") {
		t.Fatalf("emitted C# proto artifact lacks BIDI PublishToStream:\n%s", delegationArtifact)
	}
	if strings.Contains(delegationArtifact, "SubscribeToStream") {
		t.Fatalf("emitted C# C2S proto artifact exposed SubscribeToStream:\n%s", delegationArtifact)
	}
}
