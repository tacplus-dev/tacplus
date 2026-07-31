// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const credPairStoreProto = `syntax = "proto3";
package %s;

import "sys/spec.proto";

message %s {
  option (spec.service_store) = {tag: "state-store"};
  string client_id = 1 [(spec.store_key) = true];
  string client_secret = 2 [(spec.assert_max_size) = 64];
}
`

func writeStoreProto(t *testing.T, dir, pkg, msg string) {
	t.Helper()
	writeProto(t, dir, pkg+".proto", strings.ReplaceAll(strings.ReplaceAll(credPairStoreProto, "%s;", pkg+";"), "message %s", "message "+msg))
}

// Two packages each declaring a service_store message with the same name must
// be a hard generation error: both stores would collapse into one PersistedStore
// enum value and share generated identifiers and on-disk snapshot paths.
func TestGeneratePersistedStoreEnumRejectsDuplicateStoreNames(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeStoreProto(t, dir, "alpha", "CredPair")
	writeStoreProto(t, dir, "beta", "CredPair")

	err := GeneratePersistedStoreEnum(dir)
	if err == nil {
		t.Fatal("expected duplicate service_store error, got nil")
	}
	for _, want := range []string{"duplicate service_store message name", "PERSISTED_STORE_CRED_PAIR", "must be unique"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGeneratePersistedStoreEnumUniqueNames(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeStoreProto(t, dir, "alpha", "CredPair")
	writeStoreProto(t, dir, "beta", "TokenCache")

	if err := GeneratePersistedStoreEnum(dir); err != nil {
		t.Fatalf("GeneratePersistedStoreEnum: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "generated", "persisted_store.proto"))
	if err != nil {
		t.Fatalf("read generated enum: %v", err)
	}
	for _, want := range []string{"PERSISTED_STORE_CRED_PAIR = 1;", "PERSISTED_STORE_TOKEN_CACHE = 2;"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("generated enum missing %q:\n%s", want, b)
		}
	}
}
