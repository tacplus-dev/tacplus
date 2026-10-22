// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// writeTraitScanDir lays out a scan directory holding sys/, a physical service,
// and one vservices/ proto with the given body.
func writeTraitScanDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	servicesDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(servicesDir, 0755); err != nil {
		t.Fatalf("MkdirAll services: %v", err)
	}
	const host = `syntax = "proto3";

package stratum;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};

message PingRequest {}
message PingResponse {}

service StratumService {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`
	if err := os.WriteFile(filepath.Join(servicesDir, "stratum.proto"), []byte(host), 0644); err != nil {
		t.Fatalf("WriteFile host service: %v", err)
	}
	vservicesDir := filepath.Join(dir, "vservices")
	if err := os.MkdirAll(vservicesDir, 0755); err != nil {
		t.Fatalf("MkdirAll vservices: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vservicesDir, "query.proto"), []byte(body), 0644); err != nil {
		t.Fatalf("WriteFile trait: %v", err)
	}
	return dir
}

// A trait whose RPCs are plain request/response calls — no exported topics —
// must still get a VServiceEntry. Without one, `aud=<trait name>` never reaches
// the host's accepted-audience set (java/types/generator.go builds that set from
// these entries), so the host rejects calls the client authenticated correctly.
func TestScanVServiceRPCsEntryForNonTopicTrait(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := writeTraitScanDir(t, `syntax = "proto3";

package stratumquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["stratum"]
};

message CreateQueryRequest { string filter = 1; }
message QueryHandle { string handle = 1; }

service RecordQuery {
  option (spec.service_c2s) = {
    label: "stratum"
    auth_entitled_users: true
  };

  rpc CreateQuery(CreateQueryRequest) returns (QueryHandle);
}
`)

	reg, err := LoadServiceRegistry("")
	if err != nil {
		t.Fatalf("LoadServiceRegistry: %v", err)
	}
	got, err := ScanVServiceRPCs(dir, reg)
	if err != nil {
		t.Fatalf("ScanVServiceRPCs: %v", err)
	}
	want := []VServiceEntry{{
		Name:      "query",
		FieldID:   40,
		HostLabel: "stratum",
		// (spec.trait), not (spec.vservice) — the generated enum comment names
		// the kind, so this flag is what keeps registry.json from calling the
		// query trait a vservice.
		IsTrait: true,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanVServiceRPCs() = %#v, want %#v", got, want)
	}
}

// A trait that declares no RPCs of its own but names a host still gets an entry:
// its plane's RPCs are appended to this proto later in the pipeline (record_map),
// after this scan has already run, so RPC presence in the scanned tree cannot be
// the only signal.
func TestScanVServiceRPCsEntryForHostedTraitWithoutDeclaredService(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := writeTraitScanDir(t, `syntax = "proto3";

package stratumquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["stratum"]
};

message CreateQueryRequest { string filter = 1; }
`)

	reg, err := LoadServiceRegistry("")
	if err != nil {
		t.Fatalf("LoadServiceRegistry: %v", err)
	}
	got, err := ScanVServiceRPCs(dir, reg)
	if err != nil {
		t.Fatalf("ScanVServiceRPCs: %v", err)
	}
	want := []VServiceEntry{{
		Name:      "query",
		FieldID:   40,
		HostLabel: "stratum",
		// (spec.trait), not (spec.vservice) — the generated enum comment names
		// the kind, so this flag is what keeps registry.json from calling the
		// query trait a vservice.
		IsTrait: true,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanVServiceRPCs() = %#v, want %#v", got, want)
	}
}

// An identity-only trait asks for nothing: no topics, no RPCs, no host. It must
// not get an entry — an entry with no host label co-scopes against nothing and
// would put a name in no host's accepted-audience set.
func TestScanVServiceRPCsNoEntryForEmptyTrait(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := writeTraitScanDir(t, `syntax = "proto3";

package stratumquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
};

message CreateQueryRequest { string filter = 1; }
`)

	reg, err := LoadServiceRegistry("")
	if err != nil {
		t.Fatalf("LoadServiceRegistry: %v", err)
	}
	got, err := ScanVServiceRPCs(dir, reg)
	if err != nil {
		t.Fatalf("ScanVServiceRPCs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ScanVServiceRPCs() = %#v, want no entries for an identity-only trait", got)
	}
}
