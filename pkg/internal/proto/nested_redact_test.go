// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func nestedRedactFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	return dir
}

// NEW-4: (spec.redact) on a field of a NESTED message declaration is invisible to
// the descriptor→model conversion (it never recurses), so it would be silently
// dropped in every language. It must fail loud instead.
func TestValidateNoNestedRedactRejectsNestedField(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := nestedRedactFixtureDir(t)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Outer {
  message Inner {
    string secret = 1 [(spec.redact) = {mode: REDACT_MODE_DROP}];
  }
  Inner inner = 1;
}
`)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	err = dp.ValidateNoNestedRedact()
	if err == nil {
		t.Fatal("expected a loud error for redact on a nested-message field, got nil")
	}
	if !strings.Contains(err.Error(), "Inner.secret") || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("error should name the nested field, got: %v", err)
	}
}

// A top-level redact field must NOT trip this guard — it flows through the normal
// pipeline. Only nested-declaration redact is rejected here.
func TestValidateNoNestedRedactAllowsTopLevelField(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := nestedRedactFixtureDir(t)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message TopLevel {
  string secret = 1 [(spec.redact) = {mode: REDACT_MODE_DROP}];
  string id = 2;
}
`)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	if err := dp.ValidateNoNestedRedact(); err != nil {
		t.Fatalf("top-level redact must not trip the nested guard, got: %v", err)
	}
}
