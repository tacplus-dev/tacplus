// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanVServiceRPCsUsesDescriptorAnnotations(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	servicesDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("MkdirAll services: %v", err)
	}
	const eventBusService = `syntax = "proto3";

package events;

import "sys/spec.proto";

option (spec.service) = {name: "events"};

message PublishRequest { string payload = 1; }
message PublishResponse {}

service EventBusTypedService {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc Publish(PublishRequest) returns (PublishResponse);
}
`
	if err := os.WriteFile(filepath.Join(servicesDir, "events.proto"), []byte(eventBusService), 0644); err != nil {
		t.Fatalf("WriteFile service: %v", err)
	}

	vservicesDir := filepath.Join(dir, "vservices")
	if err := os.MkdirAll(vservicesDir, 0755); err != nil {
		t.Fatalf("MkdirAll vservices: %v", err)
	}
	const content = `syntax = "proto3";

package claudesensor;

import "sys/spec.proto";
import "services/events.proto";

option (spec.vservice) = {
  id: 30
  name: "claudesensor"
};

message AgenticCodeEvent {
  string id = 1;
}

message AgenticCodeEventBus {
  option (spec.service_state) = {
    name: "AgenticCodeEventBus"
    tag: "events"
    export: true
  };
  option (spec.service_event) = {};

  repeated AgenticCodeEvent event_stream = 1 [
    (spec.leader) = true,
    (spec.topic) = true
  ];
}
`
	if err := os.WriteFile(filepath.Join(vservicesDir, "claude-sensor.proto"), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reg, err := LoadServiceRegistry("")
	if err != nil {
		t.Fatalf("LoadServiceRegistry: %v", err)
	}
	got, err := ScanVServiceRPCs(dir, reg)
	if err != nil {
		t.Fatalf("ScanVServiceRPCs: %v", err)
	}
	want := []VServiceEntry{{
		Name:    "claudesensor",
		FieldID: 30,
		HostTag: "events",
		RPCs: []string{
			"PublishClaudesensorAgenticCodeEventBusEventStream",
			"SubscribeClaudesensorAgenticCodeEventBusEventStream",
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanVServiceRPCs() = %#v, want %#v", got, want)
	}
}
