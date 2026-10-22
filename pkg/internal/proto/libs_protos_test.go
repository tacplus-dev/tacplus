// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSysIsSysProto(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "sys/spec.proto", input: "sys/spec.proto", expected: true},
		{name: "sys/errors.proto", input: "sys/errors.proto", expected: true},
		{name: "sys/httpclient.proto", input: "sys/httpclient.proto", expected: true},
		{name: "sys/tags.proto", input: "sys/tags.proto", expected: true},
		{name: "common/foo.proto is NOT sys", input: "common/foo.proto", expected: false},
		{name: "services/bar.proto is NOT sys", input: "services/bar.proto", expected: false},
		{name: "bare spec.proto is NOT sys", input: "spec.proto", expected: false},
		{name: "empty string is NOT sys", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSysProto(tt.input)
			if got != tt.expected {
				t.Errorf("IsSysProto(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsGeneratedProto(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "generated/services.proto", input: "generated/services.proto", expected: true},
		{name: "generated/entitlements.proto", input: "generated/entitlements.proto", expected: true},
		{name: "generated/services_config.proto", input: "generated/services_config.proto", expected: true},
		{name: "sys/spec.proto is NOT generated", input: "sys/spec.proto", expected: false},
		{name: "services/bar.proto is NOT generated", input: "services/bar.proto", expected: false},
		{name: "bare services.proto is NOT generated", input: "services.proto", expected: false},
		{name: "empty string is NOT generated", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsGeneratedProto(tt.input)
			if got != tt.expected {
				t.Errorf("IsGeneratedProto(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsInfraProto(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "sys/spec.proto is infra", input: "sys/spec.proto", expected: true},
		{name: "generated/services.proto is infra", input: "generated/services.proto", expected: true},
		{name: "services/bar.proto is NOT infra", input: "services/bar.proto", expected: false},
		{name: "common/foo.proto is NOT infra", input: "common/foo.proto", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsInfraProto(tt.input)
			if got != tt.expected {
				t.Errorf("IsInfraProto(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestIsServicesFileName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "services", input: "services", expected: true},
		{name: "state", input: "state", expected: false},
		{name: "baseball", input: "baseball", expected: false},
		{name: "service", input: "service", expected: false},
		{name: "empty", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsServicesFileName(tt.input)
			if got != tt.expected {
				t.Errorf("IsServicesFileName(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSysCopySysProtos(t *testing.T) {
	targetDir := t.TempDir()

	err := CopySysProtos(targetDir)
	if err != nil {
		t.Fatalf("CopySysProtos() returned error: %v", err)
	}

	// Verify sys/ subdirectory was created
	sysDir := filepath.Join(targetDir, "sys")
	info, err := os.Stat(sysDir)
	if err != nil {
		t.Fatalf("sys/ directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("sys/ is not a directory")
	}

	// Verify .proto files exist in sys/
	entries, err := os.ReadDir(sysDir)
	if err != nil {
		t.Fatalf("failed to read sys/ directory: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no files found in sys/ directory")
	}

	// Check that all entries are .proto files
	protoCount := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".proto" {
			t.Errorf("unexpected non-proto file in sys/: %s", entry.Name())
		}
		protoCount++
	}

	if protoCount == 0 {
		t.Fatal("no .proto files found in sys/ directory")
	}

	// Verify specific known proto files exist
	expectedFiles := []string{"spec.proto", "errors.proto", "httpclient.proto", "tags.proto"}
	for _, name := range expectedFiles {
		path := filepath.Join(sysDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file sys/%s not found: %v", name, err)
		}
	}

	// Verify files have non-zero content
	for _, name := range expectedFiles {
		path := filepath.Join(sysDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("failed to read sys/%s: %v", name, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("sys/%s is empty", name)
		}
	}
}

func TestSysCopySysProtosDoesNotOverwrite(t *testing.T) {
	targetDir := t.TempDir()

	// Create sys/ dir with a pre-existing file
	sysDir := filepath.Join(targetDir, "sys")
	if err := os.MkdirAll(sysDir, 0755); err != nil {
		t.Fatalf("failed to create sys/ directory: %v", err)
	}

	// Write a custom spec.proto that should NOT be overwritten
	customContent := []byte("// custom user version\n")
	customPath := filepath.Join(sysDir, "spec.proto")
	if err := os.WriteFile(customPath, customContent, 0644); err != nil {
		t.Fatalf("failed to write custom spec.proto: %v", err)
	}

	err := CopySysProtos(targetDir)
	if err != nil {
		t.Fatalf("CopySysProtos() returned error: %v", err)
	}

	// Verify the custom file was NOT overwritten
	got, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatalf("failed to read spec.proto after copy: %v", err)
	}
	if string(got) != string(customContent) {
		t.Errorf("CopySysProtos() overwrote existing file; got %q, want %q", string(got), string(customContent))
	}

	// Verify other proto files WERE created (since they didn't already exist)
	errorsPath := filepath.Join(sysDir, "errors.proto")
	if _, err := os.Stat(errorsPath); err != nil {
		t.Errorf("errors.proto was not created: %v", err)
	}
}

// tokenrequest.proto now lives under sys/ (folded from the eliminated common/
// dir, MS-11); its copy is covered by CopySysProtos and TestSysCopySysProtos.
func TestSysProtosIncludeTokenRequest(t *testing.T) {
	targetDir := t.TempDir()
	if err := CopySysProtos(targetDir); err != nil {
		t.Fatalf("CopySysProtos() returned error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(targetDir, "sys", "tokenrequest.proto"))
	if err != nil {
		t.Fatalf("sys/tokenrequest.proto not copied: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("sys/tokenrequest.proto is empty")
	}
}

func TestSysCopyAndPrepareProtoDir(t *testing.T) {
	// Create a temp input directory with test .proto files
	inputDir := t.TempDir()
	targetDir := t.TempDir()

	// Create a services directory with a test proto
	servicesDir := filepath.Join(inputDir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("failed to create services/ directory: %v", err)
	}

	testProtoContent := []byte(`syntax = "proto3";
package testservice;

message TestRequest {
  string id = 1;
}

service TestService {
  rpc GetTest (TestRequest) returns (TestRequest);
}
`)
	testProtoPath := filepath.Join(servicesDir, "testservice.proto")
	if err := os.WriteFile(testProtoPath, testProtoContent, 0644); err != nil {
		t.Fatalf("failed to write test proto: %v", err)
	}

	// Create a common directory with a proto
	commonDir := filepath.Join(inputDir, "common")
	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatalf("failed to create common/ directory: %v", err)
	}

	commonProtoContent := []byte(`syntax = "proto3";
package shared;

message CommonType {
  string name = 1;
}
`)
	commonProtoPath := filepath.Join(commonDir, "shared.proto")
	if err := os.WriteFile(commonProtoPath, commonProtoContent, 0644); err != nil {
		t.Fatalf("failed to write common proto: %v", err)
	}

	// Call CopyAndPrepareProtoDir
	protoDir, err := CopyAndPrepareProtoDir(inputDir, targetDir)
	if err != nil {
		t.Fatalf("CopyAndPrepareProtoDir() returned error: %v", err)
	}

	// Verify it returns targetDir/proto
	expectedProtoDir := filepath.Join(targetDir, "proto")
	if protoDir != expectedProtoDir {
		t.Errorf("CopyAndPrepareProtoDir() returned %q, want %q", protoDir, expectedProtoDir)
	}

	// Verify the proto directory was created
	info, err := os.Stat(protoDir)
	if err != nil {
		t.Fatalf("proto directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("proto path is not a directory")
	}

	// Verify user proto files were copied
	copiedServiceProto := filepath.Join(protoDir, "services", "testservice.proto")
	if _, err := os.Stat(copiedServiceProto); err != nil {
		t.Errorf("service proto not copied to proto/ directory: %v", err)
	}

	copiedCommonProto := filepath.Join(protoDir, "common", "shared.proto")
	if _, err := os.Stat(copiedCommonProto); err != nil {
		t.Errorf("common proto not copied to proto/ directory: %v", err)
	}

	// Verify content is preserved
	got, err := os.ReadFile(copiedServiceProto)
	if err != nil {
		t.Fatalf("failed to read copied service proto: %v", err)
	}
	if string(got) != string(testProtoContent) {
		t.Errorf("copied service proto content mismatch;\ngot:  %q\nwant: %q", string(got), string(testProtoContent))
	}

	// Verify sys protos were overlaid
	sysDir := filepath.Join(protoDir, "sys")
	if _, err := os.Stat(sysDir); err != nil {
		t.Errorf("sys/ directory not created in proto/ directory: %v", err)
	}

	specProto := filepath.Join(sysDir, "spec.proto")
	if _, err := os.Stat(specProto); err != nil {
		t.Errorf("sys/spec.proto not created: %v", err)
	}

	tokenRequestProto := filepath.Join(sysDir, "tokenrequest.proto")
	if _, err := os.Stat(tokenRequestProto); err != nil {
		t.Errorf("sys/tokenrequest.proto not created: %v", err)
	}
}

func TestSysCopyAndPrepareProtoDirSkipsSysInput(t *testing.T) {
	// Verify that user-provided sys/ protos in inputDir are skipped
	// (the embedded versions are used instead).
	inputDir := t.TempDir()
	targetDir := t.TempDir()

	// Create a sys/ directory in the input with a custom proto
	sysDir := filepath.Join(inputDir, "sys")
	if err := os.MkdirAll(sysDir, 0755); err != nil {
		t.Fatalf("failed to create input sys/ directory: %v", err)
	}

	customContent := []byte("// this should be skipped\n")
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), customContent, 0644); err != nil {
		t.Fatalf("failed to write custom sys/spec.proto: %v", err)
	}

	protoDir, err := CopyAndPrepareProtoDir(inputDir, targetDir)
	if err != nil {
		t.Fatalf("CopyAndPrepareProtoDir() returned error: %v", err)
	}

	// The embedded version should be used, NOT the user's custom version
	got, err := os.ReadFile(filepath.Join(protoDir, "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("failed to read sys/spec.proto: %v", err)
	}

	// The embedded version should contain actual proto syntax, not our custom content
	if string(got) == string(customContent) {
		t.Error("CopyAndPrepareProtoDir() used user's sys/ proto instead of embedded version")
	}
}
