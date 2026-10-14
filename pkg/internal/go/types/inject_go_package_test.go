// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The generated/*.proto files are added to the protoc compilation list and have
// their go_package injected in the same step. Swallowing an I/O failure there
// drops the file from compilation or leaves it without a go_package, which
// surfaces much later as a misleading protoc/import error. These tests pin that
// every failure except "the file was never generated" is propagated.

func TestInjectGoPackageMissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generated", "tenant_config.proto")
	exists, err := injectGoPackage(path, "tenant_config", "example.com/m/types/generated/tenant_config")
	if err != nil {
		t.Fatalf("a never-generated proto must not be an error: %v", err)
	}
	if exists {
		t.Fatal("exists must be false for a missing proto")
	}
}

func TestInjectGoPackageInjectsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenant_config.proto")
	if err := os.WriteFile(path, []byte("syntax = \"proto3\";\n\npackage tenant_config;\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	exists, err := injectGoPackage(path, "tenant_config", "example.com/m/types/generated/tenant_config")
	if err != nil || !exists {
		t.Fatalf("injectGoPackage(exists=%v) = %v", exists, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := "option go_package = \"example.com/m/types/generated/tenant_config\";"
	if !strings.Contains(string(content), want) {
		t.Fatalf("missing %q in:\n%s", want, content)
	}
	// A second pass must be a no-op, not a second option line.
	if _, err := injectGoPackage(path, "tenant_config", "example.com/m/types/generated/tenant_config"); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Count(string(again), "go_package") != 1 {
		t.Fatalf("go_package injected twice:\n%s", again)
	}
}

func TestInjectGoPackagePropagatesReadFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory in place of the proto file: ReadFile fails with something
	// other than fs.ErrNotExist, which must not be mistaken for "not generated".
	path := filepath.Join(dir, "tenant_config.proto")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	exists, err := injectGoPackage(path, "tenant_config", "example.com/m/types/generated/tenant_config")
	if err == nil {
		t.Fatal("expected an error when the generated proto cannot be read, got nil")
	}
	if exists {
		t.Fatal("exists must be false on failure")
	}
	if !strings.Contains(err.Error(), "failed to read generated proto") {
		t.Fatalf("error not wrapped with context: %v", err)
	}
}

func TestInjectGoPackagePropagatesWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only mode bits this test relies on")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tenant_config.proto")
	if err := os.WriteFile(path, []byte("package tenant_config;\n"), 0o444); err != nil {
		t.Fatalf("write: %v", err)
	}
	// The containing directory is read-only too, so os.WriteFile cannot truncate
	// or recreate the file.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	exists, err := injectGoPackage(path, "tenant_config", "example.com/m/types/generated/tenant_config")
	if err == nil {
		t.Fatal("expected an error when go_package cannot be written, got nil")
	}
	if exists {
		t.Fatal("exists must be false on failure")
	}
	if !strings.Contains(err.Error(), "failed to inject go_package") {
		t.Fatalf("error not wrapped with context: %v", err)
	}
}
