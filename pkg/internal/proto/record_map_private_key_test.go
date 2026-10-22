// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A (spec.record).private record's key reaches its filter, and every other
// record's key still reaches its filter ONLY through (spec.record).s2s_read.
//
// The key carries no (spec.query_field) on any record in the fleet, so it is
// folded in by the injector or not at all. Gating that fold on s2s_read alone
// kept it additive, but a private record is FORBIDDEN to declare s2s_read
// (ValidateRecordPrivateAnnotations) and still has a reader: (spec.record_read).c2s
// at C2S_SCOPE_PRINCIPAL, whose generated point read sets the key criterion by
// name. Without the second gate that read compiles against a field the plane
// never emits — one of the two gaps that made the shape unimplementable.
//
// Descriptor-backed because the gate lives in the injector's own record scan,
// not in appendKeyFilterCriterion (which is pinned protoc-free in
// record_read_test.go).
func privateKeyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};

message Ping {
  string id = 1;
}

service StratumApi {
  option (spec.service_s2s) = {};
  rpc GetPing(Ping) returns (Ping);
}
`)
	writeProto(t, dir, "query.proto", `syntax = "proto3";

package query;

import "sys/spec.proto";

option (spec.trait) = {
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["reader"]
};
`)
	writeProto(t, dir, "vault.proto", `syntax = "proto3";

package vault;

import "sys/spec.proto";

option (spec.service) = {name: "vault"};

// PRIVATE, so it declares no s2s_read and can never declare one.
message SecretRow {
  option (spec.record) = {
    key: "secret_id"
    owner: "owner"
    s2s: {auth_services: ["*"]}
    private: true
  };

  string secret_id = 1;
  string owner = 2 [(spec.query_field) = {op: QUERY_OP_EQ}, (spec.store_principal) = true];
}

// The ADDITIVE CONTROL: not private, declares no s2s_read, so its key must stay
// off its filter exactly as it did before the private gate existed.
message PlainRow {
  option (spec.record) = {
    key: "row_id"
    owner: "owner"
    s2s: {auth_services: ["*"]}
  };

  string row_id = 1;
  string owner = 2 [(spec.query_field) = {op: QUERY_OP_EQ}];
}
`)
	return dir
}

func TestPrivateRecordKeyIsFoldedIntoItsFilterWithoutS2SRead(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := privateKeyFixture(t)

	if err := AppendRecordMapServiceToProtos(dir, dir, "vault"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatalf("read injected trait proto: %v", err)
	}
	text := string(out)

	secret := filterBody(t, text, "SecretRowFilter")
	if !strings.Contains(secret, "secret_id") {
		t.Errorf("a private record's key must be an EQ criterion on its filter; got:\n%s", secret)
	}
	plain := filterBody(t, text, "PlainRowFilter")
	if strings.Contains(plain, "row_id") {
		t.Errorf("a non-private record declaring no s2s_read must keep its key OFF its filter; got:\n%s", plain)
	}
}

// filterBody returns the body of the named <T>Filter message in the injected
// plane text.
func filterBody(t *testing.T, text, name string) string {
	t.Helper()
	start := strings.Index(text, "message "+name+" {")
	if start == -1 {
		t.Fatalf("%s was not emitted:\n%s", name, text)
	}
	rest := text[start:]
	end := strings.Index(rest, "\n}")
	if end == -1 {
		t.Fatalf("%s is unterminated:\n%s", name, rest)
	}
	return rest[:end]
}

// A reader of a PRIVATE record derives a second plane client: the query plane's
// C2S half. That half is the private record's whole data path — record_map.go
// emits no rpcs for a private type on the s2s half, so a reader pointed there
// would generate against methods that do not exist.
//
// The s2s client is still derived alongside it: one service may read both kinds
// of record, and the reader for an ordinary record is unchanged.
func TestPrivateRecordReadDerivesC2SPlaneClient(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := privateKeyFixture(t)
	writeProto(t, dir, "reader.proto", `syntax = "proto3";

package reader;

import "sys/spec.proto";

option (spec.service) = {name: "reader"};
option (spec.record_read) = {
  record: "vault.SecretRow"
  c2s: {scope: C2S_SCOPE_PRINCIPAL exposure: RPC_EXPOSURE_EXTERNAL}
};
`)

	entries, err := DeriveVirtualServiceClients(dir, "reader")
	if err != nil {
		t.Fatalf("DeriveVirtualServiceClients: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.ServiceName] = true
	}
	for _, want := range []string{"RecordMap", "RecordQuery"} {
		if !seen[want] {
			t.Errorf("a private record's reader must derive a client for %s, got %+v", want, entries)
		}
	}
}

// The second client is derived ONLY for a private read. A reader of ordinary
// records alone must emit exactly the clients it emitted before — the c2s half
// is a whole extra generated client, not a default.
func TestPlainRecordReadDerivesNoC2SPlaneClient(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := privateKeyFixture(t)
	writeProto(t, dir, "reader.proto", `syntax = "proto3";

package reader;

import "sys/spec.proto";

option (spec.service) = {name: "reader"};
option (spec.record_read) = {record: "vault.PlainRow"};
`)

	entries, err := DeriveVirtualServiceClients(dir, "reader")
	if err != nil {
		t.Fatalf("DeriveVirtualServiceClients: %v", err)
	}
	for _, e := range entries {
		if e.ServiceName == "RecordQuery" {
			t.Fatalf("a non-private record's reader must not derive the plane's C2S client, got %+v", entries)
		}
	}
}

// A record-read client must name the plane's HOST as its target service, not
// the plane's proto package. The package ("query") is a path segment, not a
// service identity: Go's and Java's audienceFromMethod fall through to the path
// package when no mapping names it, so the reader mints aud=query for
// /query.RecordMap/... and the token authority refuses the exchange — the
// letsencrypt/blobstore record-read PermissionDenied (2026-09-27). Rust already
// routed to the host via the surface's host_label; TargetService is what makes
// all three agree.
func TestRecordReadClientTargetsThePlaneHost(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := privateKeyFixture(t)
	writeProto(t, dir, "reader.proto", `syntax = "proto3";

package reader;

import "sys/spec.proto";

option (spec.service) = {name: "reader"};
option (spec.record_read) = {record: "vault.PlainRow"};
option (spec.record_read) = {
  record: "vault.SecretRow"
  c2s: {scope: C2S_SCOPE_PRINCIPAL exposure: RPC_EXPOSURE_EXTERNAL}
};
`)

	entries, err := DeriveVirtualServiceClients(dir, "reader")
	if err != nil {
		t.Fatalf("DeriveVirtualServiceClients: %v", err)
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.ServiceName] = e.TargetService
	}
	// Both halves of the plane are served by the trait's host.
	for _, name := range []string{"RecordMap", "RecordQuery"} {
		got, ok := seen[name]
		if !ok {
			t.Fatalf("no %s client was derived, got %+v", name, entries)
		}
		if got != "stratum" {
			t.Errorf("%s client TargetService = %q, want the trait host %q", name, got, "stratum")
		}
	}
}
