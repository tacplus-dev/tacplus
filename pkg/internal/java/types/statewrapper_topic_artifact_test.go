// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestGenerateJavaInternalTopicStateClientArtifact is a red-first test for the
// internal (non-C2S) Java state-client Topics contract: an "event" message
// with s2s auth only (no c2s) synthesizes a "<Event>Bus" state root whose
// "stream" field carries (spec.topic) = true (mirrors the real
// agentsensor.AgentEventBus / services.tasks.TaskEventBus wrappers that broke
// the eventbus/kernel fleet build).
//
// This exercises the REAL generator pipeline end to end for the function
// under test — real sys protos (proto.CopySysProtos), real EventBus wrapper
// synthesis (proto.AppendEventBusWrappersToProtos, the same pass Generate()
// runs), a real eventbus_support.proto (proto.EnsureEventBusSupportProto), a
// real descriptor parse, and the real generateJavaStateWrapper — then reads
// the actual StateClient.java bytes written to disk, rather than asserting
// against a hand-built javaStateWrapperData fixture or a hand-written string.
func TestGenerateJavaInternalTopicStateClientArtifact(t *testing.T) {
	protoDir := t.TempDir()
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}

	servicesDir := filepath.Join(protoDir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	protoSrc := `syntax = "proto3";

package agentsensor;

import "sys/spec.proto";

option (spec.service) = {name: "agentsensor"};

message AgentEvent {
  option (spec.event) = {s2s: {auth_services: ["agentsensor"]}};

  string id = 1 [(spec.assert_required) = true];
}
`
	protoPath := filepath.Join(servicesDir, "agentsensor.proto")
	if err := os.WriteFile(protoPath, []byte(protoSrc), 0644); err != nil {
		t.Fatalf("write fixture proto: %v", err)
	}

	// generated/accounting_method.proto and friends: sys/accounting.proto
	// (brought in by CopySysProtos) imports it, so it must exist before any
	// descriptor parse walks the whole proto tree below.
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}

	// Synthesizes the AgentEventBus topic wrapper the same way the real
	// pipeline does — the state root under test (AgentEventBus, with its
	// "topic"-annotated "stream" field) is a real generator artifact, not a
	// hand-written fixture message.
	if err := protogen.AppendEventBusWrappersToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}
	if _, err := protogen.EnsureEventBusSupportProto(protoDir, protogen.EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	relProtoFile := filepath.ToSlash(filepath.Join("services", "agentsensor.proto"))
	fileDesc, err := dp.GetFileDescriptor(relProtoFile)
	if err != nil {
		t.Fatalf("GetFileDescriptor(%s): %v", relProtoFile, err)
	}

	var stateRoot *parser.MessageDescriptor
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == "AgentEventBus" {
			stateRoot = &fileDesc.Messages[i]
		}
	}
	if stateRoot == nil {
		names := make([]string, len(fileDesc.Messages))
		for i := range fileDesc.Messages {
			names[i] = fileDesc.Messages[i].Name
		}
		t.Fatalf("synthesized AgentEventBus wrapper not found in descriptor, messages: %v", names)
	}

	outputDir := t.TempDir()
	config := &tactype.Config{
		JavaPackageBase: "com.example.tacplus",
		OutputDir:       outputDir,
		AllStates:       true,
	}

	svc := ServiceInfo{
		ProtoFile:   relProtoFile,
		PackageName: "agentsensor",
		ParentDir:   "services",
	}

	if _, err := generateJavaStateWrapper(config, svc, fileDesc, stateRoot, dp, tactype.AccessRW); err != nil {
		t.Fatalf("generateJavaStateWrapper: %v", err)
	}

	artifactPath := filepath.Join(outputDir, "src", "main", "java",
		"com", "example", "tacplus", "s2sm", "internal", "state", "services",
		"agentsensor", "agenteventbus", "StateClient.java")
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read generated StateClient.java: %v", err)
	}
	generated := string(artifact)

	for _, bad := range []string{
		"TopicPublishReceipt",
		"stateStub.publishToStream(",
		"stateStub.subscribeToStream(",
		"Trace.TraceRequest.getDefaultInstance()",
	} {
		if strings.Contains(generated, bad) {
			t.Fatalf("generated StateClient.java retained dead pre-Piece-C EventBus contract %q:\n%s", bad, generated)
		}
	}

	for _, want := range []string{
		"stateAsyncStub",
		"EventbusSupport.PublishResponse publishToStream(",
		"stateAsyncStub.publishToStream(",
		"EventbusSupport.SubscribeRequest.getDefaultInstance()",
		"stateAsyncStub.subscribeToStream(",
		"java.util.Iterator<Agentsensor.AgentEventBusStreamSubscribeRecord> subscribeToStreamRecords()",
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated StateClient.java missing %q:\n%s", want, generated)
		}
	}
}
