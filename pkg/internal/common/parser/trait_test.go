// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import "testing"

// Pins file-level (spec.trait) extraction into the descriptor model. A trait
// carries the same identity shape as a vservice and lands in the same
// descriptor fields, so discovery derives from the parsed annotation.
func TestParseTrait(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
};

message Ping { string m = 1; }
`
	writeTempProtoFile(t, dir, "query.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("query.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if fd.VServiceName != "query" {
		t.Errorf("VServiceName = %q, want query", fd.VServiceName)
	}
	if fd.VServiceID != 40 {
		t.Errorf("VServiceID = %d, want 40", fd.VServiceID)
	}
}

// A file with no (spec.trait) and no (spec.vservice) leaves the identity empty.
func TestParseTraitAbsent(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package plain;

import "sys/spec.proto";

message Ping { string m = 1; }
`
	writeTempProtoFile(t, dir, "plain.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("plain.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if fd.VServiceName != "" || fd.VServiceID != 0 {
		t.Errorf("VServiceName/ID = %q/%d, want empty", fd.VServiceName, fd.VServiceID)
	}
}
