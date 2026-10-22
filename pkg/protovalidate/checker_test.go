// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package protovalidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProtoFile writes a .proto file into the given directory.
func writeProtoFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create directory for %s: %v", filename, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

// --- ValidateProtoDirectory tests ---

func TestValidateProtoDirectory_ValidProtos(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;

message Foo {
  string name = 1;
}
`)

	count, err := ValidateProtoDirectory(dir)
	if err != nil {
		t.Errorf("valid proto should not produce error, got: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 proto file, got %d", count)
	}
}

func TestValidateProtoDirectory_NoProtoFiles(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "readme.txt", "not a proto")

	count, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected error for directory with no proto files")
	}
	if count != 0 {
		t.Errorf("expected 0 proto files, got %d", count)
	}
}

func TestValidateProtoDirectory_MissingPackage(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";

message Foo {
  string name = 1;
}
`)

	count, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected validation error for missing package declaration")
	}
	if count != 1 {
		t.Errorf("expected 1 proto file, got %d", count)
	}
}

func TestValidateProtoDirectory_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "a.proto", `syntax = "proto3";
package a;
message A { string x = 1; }
`)
	writeProtoFile(t, dir, "b.proto", `syntax = "proto3";
package b;
message B { int32 y = 1; }
`)

	count, err := ValidateProtoDirectory(dir)
	if err != nil {
		t.Errorf("valid protos should not produce error, got: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 proto files, got %d", count)
	}
}

// --- ValidateProtoDirectory blocked field name tests ---

func TestValidateProtoDirectory_BlockedFieldName_Reset(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message RateLimitInfo {
  int32 limit = 1;
  int64 reset = 2;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected validation error for blocked field name 'reset'")
	}
	if !strings.Contains(err.Error(), "blocked name") {
		t.Errorf("error should mention blocked name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "reset") {
		t.Errorf("error should mention the field name 'reset', got: %v", err)
	}
}

func TestValidateProtoDirectory_BlockedFieldName_Type(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message Event {
  string type = 1;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected validation error for blocked field name 'type'")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Errorf("error should mention the field name 'type', got: %v", err)
	}
}

func TestValidateProtoDirectory_SafeFieldNames(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message Event {
  string event_type = 1;
  int64 reset_at = 2;
  string class_name = 3;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err != nil {
		t.Errorf("safe field names should not produce error, got: %v", err)
	}
}

// --- ValidateProtoDirectory infrastructure field number tests ---

func TestValidateProtoDirectory_InfrastructureFieldNumber_200(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message Event {
  string name = 1;
  bool custom_trace = 200;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected validation error for field number >= 200")
	}
	if !strings.Contains(err.Error(), "used by tacplus infrastructure") {
		t.Errorf("error should mention infrastructure field numbers, got: %v", err)
	}
}

func TestValidateProtoDirectory_InfrastructureFieldNumber_250(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message Event {
  string name = 1;
  string metadata = 250;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err == nil {
		t.Fatal("expected validation error for field number >= 200")
	}
}

func TestValidateProtoDirectory_FieldNumber_199_OK(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "test.proto", `syntax = "proto3";
package test;
message Event {
  string name = 1;
  string data = 199;
}
`)
	_, err := ValidateProtoDirectory(dir)
	if err != nil {
		t.Errorf("field number 199 should be allowed, got: %v", err)
	}
}
