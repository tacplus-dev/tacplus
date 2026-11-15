// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// fakeProtoc writes a protoc stand-in that emits <out>/<proto minus .proto>.pb.go
// for --go_out=<out>, as protoc-gen-go with paths=source_relative does.
func fakeProtoc(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protoc")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in --go_out=*) out="${a#--go_out=}" ;; esac
  last="$a"
done
f="$out/${last%.proto}.pb.go"
mkdir -p "$(dirname "$f")" && printf 'package foo\n' > "$f"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake protoc: %v", err)
	}
	return path
}

// stagedFooProto stages services/foo.proto, a service proto, whose output goes
// to services/foo.
func stagedFooProto(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services", "foo.proto"), []byte("syntax = \"proto3\";\npackage foo;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGenerateProtoCodePlacesOutput(t *testing.T) {
	out := t.TempDir()
	config := &tactype.Config{ProtocPath: fakeProtoc(t)}
	if err := generateProtoCode(config, []string{"services/foo.proto"}, stagedFooProto(t), out); err != nil {
		t.Fatalf("generateProtoCode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "services", "foo", "foo.pb.go")); err != nil {
		t.Fatalf("protoc output not placed: %v", err)
	}
}

// A symlink under the output dir that leads out of it (a tree a previous run
// or a dependency left behind) makes generation fail; nothing is written at
// its target (#754).
func TestGenerateProtoCodeRefusesSymlinkOutOfOutputDir(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(out, "services")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	config := &tactype.Config{ProtocPath: fakeProtoc(t)}
	if err := generateProtoCode(config, []string{"services/foo.proto"}, stagedFooProto(t), out); err == nil {
		t.Fatal("expected an error writing through a symlink out of the output dir, got nil")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("generation wrote outside the output dir: %v", entries)
	}
}

// The staged proto copy is rewritten in place through a root too: a staged
// proto that is a symlink out of the staged dir is refused, not rewritten.
func TestInjectGoPackageRefusesSymlinkOutOfProtoDir(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "tenant_config.proto")
	const original = "package tenant_config;\n"
	if err := os.WriteFile(outside, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "tenant_config.proto")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := injectGoPackage(dir, "tenant_config.proto", "tenant_config", "example.com/m/types/generated/tenant_config"); err == nil {
		t.Fatal("expected an error rewriting a proto through a symlink out of the staged dir, got nil")
	}
	if got, _ := os.ReadFile(outside); string(got) != original {
		t.Fatalf("file outside the staged dir was rewritten: %q", got)
	}
}
