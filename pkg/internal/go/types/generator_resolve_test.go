// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"path/filepath"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// Pins fail-closed wrapper detection: when descriptor parsing fails during
// gRPC service resolution, generation must error instead of silently skipping
// every server/client wrapper at exit 0.
func TestResolveServices_FailClosedOnParserError(t *testing.T) {
	cfg := &tactype.Config{
		GrpcServerAll:    true,
		GrpcS2SClientAll: true,
		// A flat proto dir that cannot be walked forces descriptor-parser
		// construction to fail.
		FlatProtoDir: filepath.Join(t.TempDir(), "does-not-exist"),
	}
	services := []ServiceInfo{{ProtoFile: "svc.proto", ServiceName: "Svc"}}

	if _, err := resolveServerServices(cfg, services); err == nil {
		t.Error("resolveServerServices: expected error when descriptor parser cannot be created, got nil")
	}
	if _, err := resolveClientServices(cfg, services); err == nil {
		t.Error("resolveClientServices: expected error when descriptor parser cannot be created, got nil")
	}
}

// An unset flat proto dir is not an error — it just resolves no services.
func TestFilterGrpcAnnotated_EmptyFlatProtoDir(t *testing.T) {
	cfg := &tactype.Config{}
	got, err := filterGrpcAnnotated(cfg, []ServiceInfo{{ProtoFile: "svc.proto", ServiceName: "Svc"}})
	if err != nil {
		t.Fatalf("expected nil error for empty FlatProtoDir, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil services for empty FlatProtoDir, got %v", got)
	}
}
