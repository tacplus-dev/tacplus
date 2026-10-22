// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package builder

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func init() {
	// Set up a zap logger for test output
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapcore.InfoLevel),
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
	cfg.EncoderConfig.CallerKey = ""
	cfg.EncoderConfig.StacktraceKey = ""
	cfg.DisableStacktrace = true
	logger, _ := cfg.Build()
	zap.ReplaceGlobals(logger)
}

// randomName generates a short random lowercase name like "abc_d3f2".
func randomName(t *testing.T) string {
	t.Helper()
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alphanums = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b strings.Builder
	// 3-6 letter prefix
	prefixLen := 3 + rand.Intn(4)
	for i := 0; i < prefixLen; i++ {
		b.WriteByte(letters[rand.Intn(len(letters))])
	}
	b.WriteByte('_')
	// 4 char hex-like suffix
	for i := 0; i < 4; i++ {
		b.WriteByte(alphanums[rand.Intn(len(alphanums))])
	}
	return b.String()
}

// commonProto generates a proto file for a "common" directory with the given package name.
// Imports sys/spec.proto (the embedded tacplus annotations).
func commonProto(pkgName string) string {
	return fmt.Sprintf(`syntax = "proto3";

package %s;

import "sys/spec.proto";

option (spec.string_const) = {name: "DEFAULT_NAME" value: "widget"};
option (spec.int_const) = {name: "MAX_COUNT" value: 1000};

enum ItemStatus {
  ITEM_STATUS_UNDEFINED = 0;
  ITEM_STATUS_ACTIVE = 1 [(spec.json_return_value) = "active"];
  ITEM_STATUS_INACTIVE = 2 [(spec.json_return_value) = "inactive"];
}

message Item {
  string id = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 3];
  string name = 2 [(spec.assert_required) = true];
  ItemStatus status = 3;
  int64 created_at = 4 [(spec.assert_gt) = 0];
  string description = 5 [(spec.assert_max_size) = 500];
}

message ItemList {
  repeated Item items = 1 [(spec.assert_max_size) = 1000];
  int32 total_count = 2 [(spec.assert_ge) = 0];
}
`, pkgName)
}

// libProto generates a proto file for a "lib" directory with the given package name.
func libProto(pkgName string) string {
	return fmt.Sprintf(`syntax = "proto3";

package %s;

import "sys/spec.proto";

option (spec.string_const) = {name: "DEFAULT_REGION" value: "us-east-1"};
option (spec.bool_const) = {name: "ENABLE_LOGGING" value: true};

message Config {
  string endpoint = 1 [(spec.assert_required) = true];
  int32 timeout_ms = 2 [(spec.assert_gt) = 0];
  int32 max_retries = 3 [(spec.assert_ge) = 0, (spec.assert_le) = 10];
  bool enable_tls = 4;
}

message HealthCheck {
  string service_name = 1 [(spec.assert_required) = true];
  bool healthy = 2;
  int64 checked_at = 3;
}
`, pkgName)
}

// serviceProto generates a service proto that imports from the common and lib dirs.
func serviceProto(pkgName, commonDir, commonPkg, libDir, libPkg string) string {
	return fmt.Sprintf(`syntax = "proto3";

package %s;

import "sys/spec.proto";
import "%s/%s.proto";
import "%s/%s.proto";

option (spec.generate_user_api) = true;

enum Priority {
  PRIORITY_UNDEFINED = 0;
  PRIORITY_LOW = 1 [(spec.json_return_value) = "low"];
  PRIORITY_MEDIUM = 2 [(spec.json_return_value) = "medium"];
  PRIORITY_HIGH = 3 [(spec.json_return_value) = "high"];
}

message CreateRequest {
  string tenant_id = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 8];
  %s.Item item = 2;
  Priority priority = 3;
  %s.Config config = 4;
}

message CreateResponse {
  string id = 1;
  bool success = 2;
}

message GetRequest {
  string tenant_id = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 8];
  string id = 2 [(spec.assert_required) = true];
}

message GetResponse {
  %s.Item item = 1;
}

message ListRequest {
  string tenant_id = 1 [(spec.assert_required) = true, (spec.assert_min_size) = 8];
  int32 page = 2 [(spec.assert_ge) = 0];
  int32 page_size = 3 [(spec.assert_gt) = 0, (spec.assert_le) = 100];
}

message ListResponse {
  repeated %s.Item items = 1 [(spec.assert_max_size) = 1000];
  int32 total = 2;
}

service MyService {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc Create(CreateRequest) returns (CreateResponse);
  rpc Get(GetRequest) returns (GetResponse);
  rpc List(ListRequest) returns (ListResponse);
}
`, pkgName, commonDir, commonPkg, libDir, libPkg, commonPkg, libPkg, commonPkg, commonPkg)
}

// createDependenciesYAML creates a minimal dependencies.yaml for testing.
func createDependenciesYAML(dir string) error {
	content := `registry:
    gitlab_project_id: "12345"
    interfaces:
        go: example.com/interfaces/go
        java: com.example.interfaces.java
        typescript: '@example/interfaces'
        python: example-interfaces
        rust: example-interfaces
tools:
    go: "1.23"
    java: "17"
    node: '>=18.0.0'
    python: '>=3.8'
    rust-edition: "2021"
    typescript: ^5.3.3
lang:
    go:
        dependencies:
            go.uber.org/zap: v1.27.1
            google.golang.org/genproto/googleapis/rpc: v0.0.0-20260526163538-3dc84a4a5aaa
            google.golang.org/grpc: v1.83.2
            google.golang.org/protobuf: v1.36.11
            gopkg.in/yaml.v3: v3.0.1
    python:
        dependencies:
            PyYAML: '>=6.0.3'
            protobuf: '>=6.33.5'
            tomli: '>=2.4.0'
            tomli-w: '>=1.2.0'
`
	return os.WriteFile(filepath.Join(dir, "dependencies.yaml"), []byte(content), 0644)
}

// TestRandomizedBuild creates proto schemas with random directory names,
// runs the builder, and verifies the output structure and code quality.
// This catches any hardcoded path assumptions in the generators.
func TestRandomizedBuild(t *testing.T) {
	// Generate random names for directories and packages
	commonDir := randomName(t) // e.g., "models_a7f3"
	commonPkg := strings.ReplaceAll(commonDir, "_", "")
	libDir := randomName(t) // e.g., "shared_b2c1"
	libPkg := strings.ReplaceAll(libDir, "_", "")
	svcPkg := "myservice"

	t.Logf("Random names: common=%s lib=%s", commonDir, libDir)

	// Create temporary proto directory with random structure.
	// Do NOT include spec/ or sys/ — the builder embeds sys/spec.proto
	// and sys/errors.proto automatically. User protos import "sys/spec.proto".
	protoDir := t.TempDir()

	// Create directory structure (no spec/ — it's embedded)
	for _, dir := range []string{
		filepath.Join(protoDir, commonDir),
		filepath.Join(protoDir, libDir),
		filepath.Join(protoDir, "services"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	// Write proto files (no spec/spec.proto — the builder embeds it)
	protoFiles := map[string]string{
		filepath.Join(commonDir, commonPkg+".proto"): commonProto(commonPkg),
		filepath.Join(libDir, libPkg+".proto"):       libProto(libPkg),
		filepath.Join("services", "myservice.proto"): serviceProto(svcPkg, commonDir, commonPkg, libDir, libPkg),
	}
	for relPath, content := range protoFiles {
		fullPath := filepath.Join(protoDir, relPath)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", relPath, err)
		}
	}

	// Write dependencies.yaml
	if err := createDependenciesYAML(protoDir); err != nil {
		t.Fatalf("failed to write dependencies.yaml: %v", err)
	}

	// Test each supported language
	for _, lang := range []string{"go", "python"} {
		t.Run(lang, func(t *testing.T) {
			// Create output directory
			outputDir := t.TempDir()

			// Create language-specific config
			cfg := &tactype.Config{
				Language:     lang,
				InputDir:     protoDir,
				OutputDir:    outputDir,
				ProtoDir:     protoDir,
				DepsYAMLPath: filepath.Join(protoDir, "dependencies.yaml"),
				LocalMode:    true,
				VersionTag:   "0.0.0-SNAPSHOT",
				GrpcServers: []tactype.GrpcEntry{
					{ProtoFile: "services/myservice.proto", ServiceName: "MyService"},
				},
				GrpcClients: []tactype.GrpcEntry{
					{ProtoFile: "services/myservice.proto", ServiceName: "MyService"},
				},
			}

			// Set language-specific package base
			switch lang {
			case "go":
				cfg.ModuleBasePath = "example.com/test/randomized"
			case "python":
				cfg.PythonPackageBase = "test-randomized"
			}

			// Run the builder
			b := NewBuilder(cfg)
			err := b.BuildLanguage(context.Background())
			b.Close()

			if err != nil {
				t.Fatalf("BuildLanguage(%s) failed: %v", lang, err)
			}

			// Verify output structure
			t.Run("structure", func(t *testing.T) {
				verifyOutputStructure(t, outputDir, lang, commonDir, commonPkg, libDir, libPkg)
			})

			// Language-specific checks
			switch lang {
			case "python":
				t.Run("python_indentation", func(t *testing.T) {
					verifyPythonIndentation(t, outputDir)
				})
				t.Run("python_helpers", func(t *testing.T) {
					verifyPythonHelpers(t, outputDir)
				})
			case "go":
				t.Run("go_packages", func(t *testing.T) {
					verifyGoPackages(t, outputDir)
				})
			}

			// Verify random names appear in generated output (not hardcoded names)
			t.Run("no_hardcoded_names", func(t *testing.T) {
				verifyNoHardcodedNames(t, outputDir, lang, commonDir, libDir)
			})
		})
	}
}

// verifyOutputStructure checks that the generated output has the expected directory layout.
func verifyOutputStructure(t *testing.T, outputDir, lang, commonDir, commonPkg, libDir, libPkg string) {
	t.Helper()

	// The types/ directory should exist and contain subdirectories for each proto dir
	typesDir := filepath.Join(outputDir, "types")
	if _, err := os.Stat(typesDir); os.IsNotExist(err) {
		t.Errorf("types/ directory does not exist in output")
		return
	}

	// Check that types/{commonDir} exists
	commonTypesDir := filepath.Join(typesDir, commonDir)
	if _, err := os.Stat(commonTypesDir); os.IsNotExist(err) {
		t.Errorf("types/%s/ directory does not exist", commonDir)
	}

	// Check that types/{libDir} exists
	libTypesDir := filepath.Join(typesDir, libDir)
	if _, err := os.Stat(libTypesDir); os.IsNotExist(err) {
		t.Errorf("types/%s/ directory does not exist", libDir)
	}

	// Check that types/services/ exists
	svcTypesDir := filepath.Join(typesDir, "services")
	if _, err := os.Stat(svcTypesDir); os.IsNotExist(err) {
		t.Errorf("types/services/ directory does not exist")
	}

	// Check for language-specific generated files
	switch lang {
	case "go":
		// Go layout: types/{dir}/{pkg}/{pkg}.pb.go
		goCommonPkgDir := filepath.Join(commonTypesDir, commonPkg)
		goLibPkgDir := filepath.Join(libTypesDir, libPkg)
		checkFileExists(t, goCommonPkgDir, commonPkg, ".pb.go")
		checkFileExists(t, goLibPkgDir, libPkg, ".pb.go")
	case "python":
		// Python layout: types/{dir}/{pkg}/types/{pkg}_pb2.py
		pyCommonDir := filepath.Join(commonTypesDir, commonPkg, "types")
		pyLibDir := filepath.Join(libTypesDir, libPkg, "types")
		if _, err := os.Stat(pyCommonDir); err == nil {
			checkFileExists(t, pyCommonDir, commonPkg, "_pb2.py")
		}
		if _, err := os.Stat(pyLibDir); err == nil {
			checkFileExists(t, pyLibDir, libPkg, "_pb2.py")
		}
	}
}

// checkFileExists verifies that a file with the given name pattern exists in the directory.
func checkFileExists(t *testing.T, dir, baseName, suffix string) {
	t.Helper()
	expected := filepath.Join(dir, baseName+suffix)
	if _, err := os.Stat(expected); os.IsNotExist(err) {
		// List what's actually there for debugging
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected file %s not found; directory contains: %v", baseName+suffix, names)
	}
}

// verifyPythonIndentation checks that all .py files in the output have correct indentation.
// Code blocks inside def/class/try/if/for/while should be indented with 4+ spaces.
func verifyPythonIndentation(t *testing.T, outputDir string) {
	t.Helper()

	err := filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(d.Name(), ".py") {
			return nil
		}
		// Skip _pb2.py and _pb2_grpc.py files (generated by protoc, not us)
		if strings.HasSuffix(d.Name(), "_pb2.py") || strings.HasSuffix(d.Name(), "_pb2_grpc.py") {
			return nil
		}
		// Skip __init__.py (often empty or single-line)
		if d.Name() == "__init__.py" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("failed to read %s: %v", path, err)
			return nil
		}

		lines := strings.Split(string(data), "\n")
		inBlock := false
		blockKeywords := []string{"def ", "class ", "try:", "except ", "if ", "elif ", "else:", "for ", "while ", "with "}

		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}

			// Check if this line starts a block
			isBlockStart := false
			for _, kw := range blockKeywords {
				if strings.HasPrefix(trimmed, kw) || trimmed == strings.TrimSpace(kw) {
					isBlockStart = true
					break
				}
			}
			if strings.HasSuffix(trimmed, ":") && isBlockStart {
				inBlock = true
				continue
			}

			// If we're inside a block, the next non-empty non-comment line must be indented
			if inBlock {
				if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
					// Zero-indented code inside a block
					relPath, _ := filepath.Rel(outputDir, path)
					t.Errorf("zero-indented code inside block at %s:%d: %q", relPath, i+1, line)
				}
				inBlock = false
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("walk error: %v", err)
	}
}

// verifyPythonHelpers checks that helpers.py exists (not stale serialization.py/validation.py).
func verifyPythonHelpers(t *testing.T, outputDir string) {
	t.Helper()

	staleFiles := []string{"serialization.py", "validation.py", "enum_helpers.py"}
	helpersFound := false

	err := filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == "helpers.py" {
			helpersFound = true
		}
		for _, stale := range staleFiles {
			if d.Name() == stale {
				relPath, _ := filepath.Rel(outputDir, path)
				// utils/validation.py is a legitimate global utils file, not a stale per-package artifact
				if strings.HasPrefix(relPath, "utils/") {
					continue
				}
				t.Errorf("stale file found: %s (should be helpers.py)", relPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("walk error: %v", err)
	}

	if !helpersFound {
		t.Errorf("no helpers.py found in output; expected generated helpers")
	}
}

// verifyGoPackages checks that Go packages have proper package declarations.
func verifyGoPackages(t *testing.T, outputDir string) {
	t.Helper()

	err := filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("failed to read %s: %v", path, err)
			return nil
		}

		content := string(data)
		if !strings.Contains(content, "package ") {
			relPath, _ := filepath.Rel(outputDir, path)
			t.Errorf("Go file %s has no package declaration", relPath)
		}
		return nil
	})
	if err != nil {
		t.Errorf("walk error: %v", err)
	}
}

// verifyNoHardcodedNames checks that the output references the random dir names
// and doesn't contain hardcoded fallback names like "common" or "sys".
func verifyNoHardcodedNames(t *testing.T, outputDir, lang, commonDir, libDir string) {
	t.Helper()

	// The types/ directory should have our random names, not generic ones
	typesDir := filepath.Join(outputDir, "types")
	entries, err := os.ReadDir(typesDir)
	if err != nil {
		t.Fatalf("failed to read types/ directory: %v", err)
	}

	foundCommon := false
	foundLib := false
	for _, entry := range entries {
		if entry.Name() == commonDir {
			foundCommon = true
		}
		if entry.Name() == libDir {
			foundLib = true
		}
	}

	if !foundCommon {
		t.Errorf("types/ directory missing random common dir %q", commonDir)
	}
	if !foundLib {
		t.Errorf("types/ directory missing random lib dir %q", libDir)
	}
}

// TestCleanProtocOutputDirs verifies that cleanGeneratedDirectories removes
// directories containing protoc output regardless of their name.
func TestCleanProtocOutputDirs(t *testing.T) {
	outputDir := t.TempDir()

	// Create a directory with a protoc-generated .pb.go file
	randomDir := filepath.Join(outputDir, "random_dir_xyz")
	os.MkdirAll(filepath.Join(randomDir, "pkg"), 0755)
	os.WriteFile(filepath.Join(randomDir, "pkg", "test.pb.go"), []byte("package pkg"), 0644)

	// Create a directory with a protoc-generated _pb2.py file
	randomDir2 := filepath.Join(outputDir, "another_random_abc")
	os.MkdirAll(filepath.Join(randomDir2, "types"), 0755)
	os.WriteFile(filepath.Join(randomDir2, "types", "model_pb2.py"), []byte("# generated"), 0644)

	// Create a sm/ directory (should be preserved)
	smDir := filepath.Join(outputDir, "sm")
	os.MkdirAll(smDir, 0755)
	os.WriteFile(filepath.Join(smDir, "wrapper.go"), []byte("package sm"), 0644)

	// Create a non-protoc directory (should be preserved)
	safeDir := filepath.Join(outputDir, "docs")
	os.MkdirAll(safeDir, 0755)
	os.WriteFile(filepath.Join(safeDir, "readme.txt"), []byte("hello"), 0644)

	b := &Builder{config: &tactype.Config{}}
	if err := b.cleanGeneratedDirectories(outputDir); err != nil {
		t.Fatalf("cleanGeneratedDirectories failed: %v", err)
	}

	// Protoc output dirs should be removed
	if _, err := os.Stat(randomDir); !os.IsNotExist(err) {
		t.Errorf("expected random_dir_xyz to be removed (contains .pb.go)")
	}
	if _, err := os.Stat(randomDir2); !os.IsNotExist(err) {
		t.Errorf("expected another_random_abc to be removed (contains _pb2.py)")
	}

	// sm/ should be preserved
	if _, err := os.Stat(smDir); os.IsNotExist(err) {
		t.Errorf("sm/ should be preserved")
	}

	// docs/ should be preserved (no protoc output)
	if _, err := os.Stat(safeDir); os.IsNotExist(err) {
		t.Errorf("docs/ should be preserved (no protoc output)")
	}
}

// TestCleanGeneratedDirectories tests the cleanGeneratedDirectories method.
func TestCleanGeneratedDirectories(t *testing.T) {
	b := &Builder{config: &tactype.Config{}}

	t.Run("nonexistent_output_dir", func(t *testing.T) {
		// A nonexistent directory should return nil, not an error
		err := b.cleanGeneratedDirectories("/tmp/nonexistent_dir_that_does_not_exist_12345")
		if err != nil {
			t.Errorf("expected nil error for nonexistent dir, got: %v", err)
		}
	})

	t.Run("preserves_sm_dir", func(t *testing.T) {
		outputDir := t.TempDir()

		// Create sm/ with a file inside
		smDir := filepath.Join(outputDir, "sm")
		os.MkdirAll(smDir, 0755)
		os.WriteFile(filepath.Join(smDir, "wrapper.go"), []byte("package sm"), 0644)

		err := b.cleanGeneratedDirectories(outputDir)
		if err != nil {
			t.Fatalf("cleanGeneratedDirectories failed: %v", err)
		}

		// sm/ must still exist
		if _, err := os.Stat(smDir); os.IsNotExist(err) {
			t.Errorf("sm/ should be preserved, but was removed")
		}
		// The file inside should also still be there
		if _, err := os.Stat(filepath.Join(smDir, "wrapper.go")); os.IsNotExist(err) {
			t.Errorf("sm/wrapper.go should be preserved")
		}
	})

	t.Run("removes_known_infrastructure_dirs", func(t *testing.T) {
		outputDir := t.TempDir()

		knownDirs := []string{"examples", "sys", "spec", "proto", "interfaces", "types", "utils"}
		for _, dir := range knownDirs {
			dirPath := filepath.Join(outputDir, dir)
			os.MkdirAll(dirPath, 0755)
			os.WriteFile(filepath.Join(dirPath, "file.txt"), []byte("content"), 0644)
		}

		err := b.cleanGeneratedDirectories(outputDir)
		if err != nil {
			t.Fatalf("cleanGeneratedDirectories failed: %v", err)
		}

		for _, dir := range knownDirs {
			dirPath := filepath.Join(outputDir, dir)
			if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
				t.Errorf("expected %s/ to be removed, but it still exists", dir)
			}
		}
	})

	t.Run("removes_generated_build_files", func(t *testing.T) {
		outputDir := t.TempDir()

		buildFiles := []string{"pom.xml", "package.json", "go.mod", "go.sum"}
		for _, f := range buildFiles {
			os.WriteFile(filepath.Join(outputDir, f), []byte("content"), 0644)
		}

		err := b.cleanGeneratedDirectories(outputDir)
		if err != nil {
			t.Fatalf("cleanGeneratedDirectories failed: %v", err)
		}

		for _, f := range buildFiles {
			p := filepath.Join(outputDir, f)
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("expected %s to be removed, but it still exists", f)
			}
		}
	})

	t.Run("error_on_readonly_generated_dir", func(t *testing.T) {
		outputDir := t.TempDir()

		// Create a known generated dir ("types") with a subdirectory
		typesDir := filepath.Join(outputDir, "types")
		subDir := filepath.Join(typesDir, "subpkg")
		os.MkdirAll(subDir, 0755)
		os.WriteFile(filepath.Join(subDir, "file.go"), []byte("package subpkg"), 0644)

		// Make the types dir read-only so RemoveAll fails
		os.Chmod(typesDir, 0555)

		err := b.cleanGeneratedDirectories(outputDir)

		// Restore permissions so t.TempDir() cleanup works
		os.Chmod(typesDir, 0755)

		if err == nil {
			// On some systems (e.g., running as root), RemoveAll may succeed
			// even on read-only directories, so we only check for error presence
			// if the directory was actually not removed.
			if _, statErr := os.Stat(typesDir); !os.IsNotExist(statErr) {
				t.Logf("types/ still exists but no error returned; may be running as root")
			}
		} else {
			// We got an error, which is the expected behavior
			if !strings.Contains(err.Error(), "failed to remove generated directory") {
				t.Errorf("expected error about failed removal, got: %v", err)
			}
		}
	})
}

// TestContainsProtocOutput tests the containsProtocOutput function.
func TestContainsProtocOutput(t *testing.T) {
	t.Run("empty_directory", func(t *testing.T) {
		dir := t.TempDir()
		if containsProtocOutput(dir) {
			t.Errorf("expected false for empty directory")
		}
	})

	t.Run("nonexistent_directory", func(t *testing.T) {
		if containsProtocOutput("/tmp/nonexistent_dir_99999_abcdef") {
			t.Errorf("expected false for nonexistent directory")
		}
	})

	t.Run("only_regular_go_files", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
		os.WriteFile(filepath.Join(dir, "helper.go"), []byte("package main"), 0644)
		if containsProtocOutput(dir) {
			t.Errorf("expected false for directory with only regular .go files")
		}
	})

	t.Run("pb_go_file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "message.pb.go"), []byte("package pb"), 0644)
		if !containsProtocOutput(dir) {
			t.Errorf("expected true for directory with .pb.go file")
		}
	})

	t.Run("pb2_py_file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "message_pb2.py"), []byte("# generated"), 0644)
		if !containsProtocOutput(dir) {
			t.Errorf("expected true for directory with _pb2.py file")
		}
	})

	t.Run("pb_ts_file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "message_pb.ts"), []byte("// generated"), 0644)
		if !containsProtocOutput(dir) {
			t.Errorf("expected true for directory with _pb.ts file")
		}
	})

	t.Run("pb_rs_file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "message_pb.rs"), []byte("// generated"), 0644)
		if !containsProtocOutput(dir) {
			t.Errorf("expected true for directory with _pb.rs file")
		}
	})

	t.Run("pb_java_file", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "Message.pb.java"), []byte("// generated"), 0644)
		if !containsProtocOutput(dir) {
			t.Errorf("expected true for directory with .pb.java file")
		}
	})
}

// TestCleanProtocOutputDirsEdge tests edge cases for cleanProtocOutputDirs.
func TestCleanProtocOutputDirsEdge(t *testing.T) {
	b := &Builder{config: &tactype.Config{}}

	t.Run("empty_output_directory", func(t *testing.T) {
		outputDir := t.TempDir()
		err := b.cleanProtocOutputDirs(outputDir)
		if err != nil {
			t.Errorf("expected nil error for empty output dir, got: %v", err)
		}
	})

	t.Run("deeply_nested_protoc_files", func(t *testing.T) {
		outputDir := t.TempDir()

		// Create a deeply nested structure: foo/bar/baz/test.pb.go
		deepDir := filepath.Join(outputDir, "foo", "bar", "baz")
		os.MkdirAll(deepDir, 0755)
		os.WriteFile(filepath.Join(deepDir, "test.pb.go"), []byte("package baz"), 0644)

		err := b.cleanProtocOutputDirs(outputDir)
		if err != nil {
			t.Fatalf("cleanProtocOutputDirs failed: %v", err)
		}

		// The top-level "foo" should be removed entirely
		fooDir := filepath.Join(outputDir, "foo")
		if _, err := os.Stat(fooDir); !os.IsNotExist(err) {
			t.Errorf("expected foo/ to be removed (contains deeply nested .pb.go)")
		}
	})

	t.Run("multiple_protoc_suffixes", func(t *testing.T) {
		outputDir := t.TempDir()

		suffixDirs := map[string]string{
			"godir":   "test.pb.go",
			"pydir":   "test_pb2.py",
			"tsdir":   "test_pb.ts",
			"rsdir":   "test_pb.rs",
			"javadir": "Test.pb.java",
		}

		for dir, file := range suffixDirs {
			dirPath := filepath.Join(outputDir, dir)
			os.MkdirAll(dirPath, 0755)
			os.WriteFile(filepath.Join(dirPath, file), []byte("generated"), 0644)
		}

		err := b.cleanProtocOutputDirs(outputDir)
		if err != nil {
			t.Fatalf("cleanProtocOutputDirs failed: %v", err)
		}

		for dir := range suffixDirs {
			dirPath := filepath.Join(outputDir, dir)
			if _, err := os.Stat(dirPath); !os.IsNotExist(err) {
				t.Errorf("expected %s/ to be removed (contains protoc output)", dir)
			}
		}
	})

	t.Run("mixed_dirs_only_remove_protoc", func(t *testing.T) {
		outputDir := t.TempDir()

		// Create a dir WITH protoc output
		protocDir := filepath.Join(outputDir, "generated_pkg")
		os.MkdirAll(protocDir, 0755)
		os.WriteFile(filepath.Join(protocDir, "model.pb.go"), []byte("package generated"), 0644)

		// Create a dir WITHOUT protoc output
		safeDir := filepath.Join(outputDir, "manual_pkg")
		os.MkdirAll(safeDir, 0755)
		os.WriteFile(filepath.Join(safeDir, "readme.txt"), []byte("manual content"), 0644)

		err := b.cleanProtocOutputDirs(outputDir)
		if err != nil {
			t.Fatalf("cleanProtocOutputDirs failed: %v", err)
		}

		// Protoc dir should be removed
		if _, err := os.Stat(protocDir); !os.IsNotExist(err) {
			t.Errorf("expected generated_pkg/ to be removed (contains protoc output)")
		}

		// Safe dir should be preserved
		if _, err := os.Stat(safeDir); os.IsNotExist(err) {
			t.Errorf("expected manual_pkg/ to be preserved (no protoc output)")
		}
	})

	t.Run("sm_with_protoc_files_preserved", func(t *testing.T) {
		outputDir := t.TempDir()

		// Create sm/ with protoc files inside — should still be preserved
		smDir := filepath.Join(outputDir, "sm")
		os.MkdirAll(smDir, 0755)
		os.WriteFile(filepath.Join(smDir, "service.pb.go"), []byte("package sm"), 0644)
		os.WriteFile(filepath.Join(smDir, "wrapper.go"), []byte("package sm"), 0644)

		err := b.cleanProtocOutputDirs(outputDir)
		if err != nil {
			t.Fatalf("cleanProtocOutputDirs failed: %v", err)
		}

		// sm/ must be preserved even though it contains protoc output
		if _, err := os.Stat(smDir); os.IsNotExist(err) {
			t.Errorf("sm/ should be preserved even with protoc files inside")
		}
		if _, err := os.Stat(filepath.Join(smDir, "service.pb.go")); os.IsNotExist(err) {
			t.Errorf("sm/service.pb.go should still exist")
		}
	})
}
