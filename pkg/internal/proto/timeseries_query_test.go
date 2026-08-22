// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendTimeseriesQueryToServiceProtoEmitsKeyQueryContract(t *testing.T) {
	dir := t.TempDir()
	relPath := filepath.Join("services", "timedb.proto")
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`syntax = "proto3";
package timedb;

import "sys/spec.proto";

option (spec.service) = {};
option (spec.timeseries_store) = {};

message TimeRange {
  int64 from_ms = 1;
  int64 to_ms = 2;
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	err := appendTimeseriesQueryToServiceProto(dir, relPath, []tsQueryProjection{{
		typeName: "SessionRecord",
		orderBy:  "event_id",
		keyType:  "string",
	}})
	if err != nil {
		t.Fatalf("appendTimeseriesQueryToServiceProto failed: %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)

	for _, want := range []string{
		`message ListSessionRecordRequest`,
		`TimeRange time_range = 1;`,
		`int32 page_size = 2;`,
		`string page_token = 3;`,
		`message ListSessionRecordResponse`,
		// keys only — for federation + hydration, never rows.
		`repeated string record_keys = 1;`,
		`string next_page_token = 2;`,
		`int64 total_count = 3;`,
		`service TimeseriesQuery {`,
		`option (spec.service_s2s) = {};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["argus"]};`,
		`rpc ListSessionRecord(ListSessionRecordRequest) returns (ListSessionRecordResponse)`,
		`option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: ["event_id"], default_page_size: 100};`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated proto missing %q:\n%s", want, got)
		}
	}
}
