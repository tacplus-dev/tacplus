// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendTimeseriesGroupCountsEmitsTypedGroupCounts(t *testing.T) {
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
  int64 start_ms = 1;
  int64 end_ms = 2;
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	err := appendTimeseriesGroupCountsToServiceProto(dir, relPath, []tsGroupProjection{
		{name: "ToolUseRecord", groupCols: []string{"event_type", "tool_name"}},
	})
	if err != nil {
		t.Fatalf("appendTimeseriesGroupCountsToServiceProto failed: %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)

	for _, want := range []string{
		`message TimeseriesGroupCount {`,
		`string key = 1;`,
		`int64 count = 2;`,
		`enum ToolUseRecordGroupField {`,
		`TOOL_USE_RECORD_GROUP_FIELD_UNSPECIFIED = 0;`,
		`TOOL_USE_RECORD_GROUP_FIELD_EVENT_TYPE = 1;`,
		`TOOL_USE_RECORD_GROUP_FIELD_TOOL_NAME = 2;`,
		`message GetToolUseRecordGroupCountsRequest {`,
		`ToolUseRecordGroupField group_by = 1;`,
		`TimeRange time_range = 2;`,
		`message GetToolUseRecordGroupCountsResponse {`,
		`repeated TimeseriesGroupCount counts = 1;`,
		`service TimeseriesGroupCounts {`,
		`option (spec.service_s2s) = {};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["argus"]};`,
		`rpc GetToolUseRecordGroupCounts(GetToolUseRecordGroupCountsRequest) returns (GetToolUseRecordGroupCountsResponse) {`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated proto missing %q:\n%s", want, got)
		}
	}
}
