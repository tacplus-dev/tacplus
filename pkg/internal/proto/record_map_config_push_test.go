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

// A tenant's record store declaring (spec.record_store).config_push
// (tacplus#927) holds its pushed slices as the sys record
// configpush.TenantSliceRecord: the plane's S2S half carries its write surface
// and its change feed, the C2S half nothing of it.
func recordConfigPushFixture(t *testing.T, storeOptions string) string {
	t.Helper()
	dir := recordWriteFixture(t)
	writeProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"`+storeOptions+`};
option (spec.record_store) = {no_bus_ingest: true, config_push: true};

message Ping {
  string id = 1;
  string target_tenant = 2;
}

service StratumApi {
  option (spec.service_s2s) = {};
  rpc GetPing(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, route_field: "target_tenant"};
  }
}
`)
	return dir
}

func TestRecordPlaneOfConfigPushStoreCarriesTenantSliceRecord(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordConfigPushFixture(t, ", tenant_owned: true")
	if err := AppendRecordMapServiceToProtos(dir, dir, "ident"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, `import "sys/configpush.proto";`) {
		t.Error("the plane does not import sys/configpush.proto")
	}
	s2s, c2s := text, ""
	if i := strings.Index(text, "\nservice RecordQuery {"); i >= 0 {
		s2s, c2s = text[:i], text[i:]
	}
	for _, want := range []string{
		"rpc WatchTenantSliceRecordChanges(WatchTenantSliceRecordChangesRequest) returns (stream TenantSliceRecordWatchEvent)",
		"rpc GetTenantSliceRecord(GetTenantSliceRecordRequest) returns (GetTenantSliceRecordResponse)",
		"rpc PutTenantSliceRecord(PutTenantSliceRecordRequest) returns (RecordWriteResult)",
		"rpc DeleteTenantSliceRecord(DeleteTenantSliceRecordRequest) returns (RecordWriteResult)",
		"configpush.TenantSliceRecord item = 1;",
		"int32 slice_field = 1;",
	} {
		if !strings.Contains(s2s, want) {
			t.Errorf("the S2S half lacks %q", want)
		}
	}
	if strings.Contains(c2s, "TenantSliceRecord") {
		t.Error("the C2S half carries configpush.TenantSliceRecord; it is the tenant's services' and its publisher's alone")
	}
	// The fleet's own records keep their place on both halves.
	if !strings.Contains(c2s, "rpc ListXidRow(") {
		t.Error("the C2S half lost the fleet's own records")
	}
}

func TestRecordPlaneWithoutConfigPushCarriesNoTenantSliceRecord(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordTenantRouteFixture(t, "")
	if err := AppendRecordMapServiceToProtos(dir, dir, "ident"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "TenantSliceRecord") {
		t.Error("a store without config_push carries configpush.TenantSliceRecord")
	}
}

func TestConfigPushOnASystemRecordStoreFails(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordConfigPushFixture(t, "")
	err := AppendRecordMapServiceToProtos(dir, dir, "ident")
	if err == nil || !strings.Contains(err.Error(), "config_push but is not tenant-owned") {
		t.Fatalf("got %v, want the not-tenant-owned refusal", err)
	}
}

func TestConfigPushOnAStoreNoTraitHostsFails(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordConfigPushFixture(t, ", tenant_owned: true")
	if err := os.Remove(filepath.Join(dir, "query.proto")); err != nil {
		t.Fatal(err)
	}
	err := AppendRecordMapServiceToProtos(dir, dir, "ident")
	if err == nil || !strings.Contains(err.Error(), "no (spec.trait) hosts its record plane") {
		t.Fatalf("got %v, want the no-plane refusal", err)
	}
}
