// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanVServiceNamesResolvesEmbeddedGeneratedImports(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}

	dir := t.TempDir()
	vservicesDir := filepath.Join(dir, "vservices")
	if err := os.MkdirAll(vservicesDir, 0755); err != nil {
		t.Fatalf("mkdir vservices: %v", err)
	}
	const content = `syntax = "proto3";

package claudesensor;

import "sys/spec.proto";
import "sys/trace.proto";

option (spec.vservice) = {
  id: 42
  name: "claudesensor"
};

message TraceCarrier {
  trace.TraceEvent event = 1;
}
`
	if err := os.WriteFile(filepath.Join(vservicesDir, "claude-sensor.proto"), []byte(content), 0644); err != nil {
		t.Fatalf("write vservice proto: %v", err)
	}

	got, err := ScanVServiceNames(dir)
	if err != nil {
		t.Fatalf("ScanVServiceNames: %v", err)
	}
	want := []string{"claudesensor"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanVServiceNames() = %#v, want %#v", got, want)
	}
}
