// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import "testing"

// Pins file-level (spec.vservice) extraction into the descriptor model
// (WS-M2 / OF-1): vservice discovery derives from the parsed annotation, not a
// regex over proto text.
func TestParseVService(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package claudesensor;

import "sys/spec.proto";

option (spec.vservice) = {
  id: 30
  name: "claudesensor"
};

message Ping { string m = 1; }
`
	writeTempProtoFile(t, dir, "claude-sensor.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("claude-sensor.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if fd.VServiceName != "claudesensor" {
		t.Errorf("VServiceName = %q, want claudesensor", fd.VServiceName)
	}
	if fd.VServiceID != 30 {
		t.Errorf("VServiceID = %d, want 30", fd.VServiceID)
	}
}
