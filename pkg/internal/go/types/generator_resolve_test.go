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

// A hosted VSERVICE — a service defined in another package whose
// (spec.service_s2s).label names this package as its physical host — gets a
// server wrapper, exactly as it does in Java and Rust through
// service.ResolveServerServices. Go resolved only the own-package half until
// this landed, so a Go host of the record query plane generated clients to its
// own plane and no server for it: the plane's RPCs were unroutable and the
// (spec.store_namespace) s2s seam was generated nowhere in a Go build.
func TestResolveServerServices_HostedVService(t *testing.T) {
	cfg := &tactype.Config{SelfPackage: "egressstratum"}
	services := []ServiceInfo{
		{ProtoFile: "services/egressstratum.proto", PackageName: "egressstratum", ServiceName: "EgressStratum", GrpcName: "egressstratum"},
		{ProtoFile: "vservices/egressquery.proto", PackageName: "egressquery", ServiceName: "EgressRecordMap", GrpcName: "egressquery", HostLabel: "egressstratum"},
		{ProtoFile: "vservices/egressquery.proto", PackageName: "egressquery", ServiceName: "EgressRecordQuery", GrpcName: "egressquery", HostLabel: "egressstratum"},
		// Labeled onto SOMEONE ELSE: not this build's to serve.
		{ProtoFile: "vservices/otherquery.proto", PackageName: "otherquery", ServiceName: "OtherRecordMap", GrpcName: "otherquery", HostLabel: "someoneelse"},
		// A vservice with no host label at all.
		{ProtoFile: "vservices/unhosted.proto", PackageName: "unhosted", ServiceName: "Unhosted", GrpcName: "unhosted"},
	}

	got, err := resolveServerServices(cfg, services)
	if err != nil {
		t.Fatalf("resolveServerServices: %v", err)
	}
	var names []string
	for _, svc := range got {
		names = append(names, svc.ServiceName)
	}
	want := []string{"EgressStratum", "EgressRecordMap", "EgressRecordQuery"}
	if len(names) != len(want) {
		t.Fatalf("server services = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("server services = %v, want %v", names, want)
		}
	}
}

// The other half of the same rule: a service's OWN package is never resolved
// through the host tag, so a package that labels itself changes nothing.
func TestResolveServerServices_OwnPackageIsNotAHostedVService(t *testing.T) {
	cfg := &tactype.Config{SelfPackage: "egressstratum"}
	got, err := resolveServerServices(cfg, []ServiceInfo{
		{ProtoFile: "services/egressstratum.proto", PackageName: "egressstratum", ServiceName: "EgressStratum", GrpcName: "egressstratum", HostLabel: "egressstratum"},
	})
	if err != nil {
		t.Fatalf("resolveServerServices: %v", err)
	}
	if len(got) != 1 || got[0].ServiceName != "EgressStratum" {
		t.Fatalf("own-package service must be resolved exactly once, got %v", got)
	}
}
