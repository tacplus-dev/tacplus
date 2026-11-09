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

// TestGenerateJavaStateClientQueueBoundaries is #456: a (spec.queue) and a
// (spec.priority_queue) field of a state root are backed by the state
// service's OfferTo*/ConsumeFrom*/Peek*/Size* RPCs (proto/state.go), not by
// a singleton's Get*/Watch*. The Java StateClient classified both as
// singletons, so it called stateStub.getTasks and stateAsyncStub.watchTasks,
// which do not exist, and the model did not compile. It now has the queue
// accessors Go has (Offer, Consume, Peek, Size) and none of a singleton's.
func TestGenerateJavaStateClientQueueBoundaries(t *testing.T) {
	protoDir := t.TempDir()
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	servicesDir := filepath.Join(protoDir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	protoSrc := `syntax = "proto3";

package helper;

import "sys/spec.proto";

option (spec.service) = {name: "helper"};

message Task { string task_id = 1; }
message Alert { string alert_id = 1; int32 severity = 2; }

message HelperState {
  option (spec.service_state) = {label: ["kernel"]};
  Task tasks = 5 [(spec.queue) = true];
  Alert alerts = 6 [(spec.priority_queue) = "severity"];
}
`
	if err := os.WriteFile(filepath.Join(servicesDir, "helper.proto"), []byte(protoSrc), 0644); err != nil {
		t.Fatalf("write fixture proto: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	relProtoFile := "services/helper.proto"
	fileDesc, err := dp.GetFileDescriptor(relProtoFile)
	if err != nil {
		t.Fatalf("GetFileDescriptor(%s): %v", relProtoFile, err)
	}
	var stateRoot *parser.MessageDescriptor
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == "HelperState" {
			stateRoot = &fileDesc.Messages[i]
		}
	}
	if stateRoot == nil {
		t.Fatalf("HelperState not found in descriptor")
	}

	svc := ServiceInfo{ProtoFile: relProtoFile, PackageName: "helper", ParentDir: "services"}
	render := func(t *testing.T, mode tactype.AccessMode) string {
		t.Helper()
		outputDir := t.TempDir()
		config := &tactype.Config{JavaPackageBase: "com.example.tacplus", OutputDir: outputDir, AllStates: true}
		client, err := generateJavaStateWrapper(config, svc, fileDesc, stateRoot, dp, mode)
		if err != nil {
			t.Fatalf("generateJavaStateWrapper: %v", err)
		}
		if client.HasSingletons {
			t.Fatalf("a queue-only state root reported singleton boundaries")
		}
		artifact, err := os.ReadFile(filepath.Join(outputDir, "src", "main", "java",
			"com", "example", "tacplus", "s2sm", "internal", "state", "services",
			"helper", "helperstate", "StateClient.java"))
		if err != nil {
			t.Fatalf("read generated StateClient.java: %v", err)
		}
		return string(artifact)
	}

	generated := render(t, tactype.AccessRW)
	for _, want := range []string{
		"import com.example.tacplus.types.sys.trace.Trace;",
		"public Helper.QueueReceipt offerToTasks(Helper.Task value)",
		"return stateStub.offerToTasks(value);",
		"public java.util.Iterator<Helper.Task> consumeFromTasks()",
		"return stateStub.consumeFromTasks(Trace.TraceRequest.getDefaultInstance());",
		"public Helper.Task peekTasks()",
		"return stateStub.peekTasks(Trace.TraceRequest.getDefaultInstance());",
		"public Helper.QueueSizeResponse sizeTasks()",
		"return stateStub.sizeTasks(Trace.TraceRequest.getDefaultInstance());",
		"public Helper.QueueReceipt offerToAlerts(Helper.Alert value)",
		"return stateStub.offerToAlerts(value);",
		"public java.util.Iterator<Helper.Alert> consumeFromAlerts()",
		"public Helper.Alert peekAlerts()",
		"public Helper.QueueSizeResponse sizeAlerts()",
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated StateClient.java missing %q:\n%s", want, generated)
		}
	}
	for _, bad := range []string{
		"getTasks", "watchTasks", "fetchTasks", "setTasks",
		"getAlerts", "watchAlerts", "fetchAlerts", "setAlerts",
		"builder.setTasks", "builder.setAlerts",
		"stateAsyncStub",
	} {
		if strings.Contains(generated, bad) {
			t.Fatalf("generated StateClient.java treats a queue as a singleton (%q):\n%s", bad, generated)
		}
	}

	// A read-only client consumes, peeks and sizes, but never offers (as Go).
	readOnly := render(t, tactype.AccessRO)
	if strings.Contains(readOnly, "offerTo") {
		t.Fatalf("read-only StateClient.java offers to a queue:\n%s", readOnly)
	}
	for _, want := range []string{"consumeFromTasks()", "peekAlerts()", "sizeTasks()"} {
		if !strings.Contains(readOnly, want) {
			t.Fatalf("read-only StateClient.java missing %q:\n%s", want, readOnly)
		}
	}
}
