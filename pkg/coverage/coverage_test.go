// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package coverage

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func skipIfNoProtoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not on PATH")
	}
}

// The manifest the binary embeds is the one the members compute: a member
// changed without `just kitchensink-manifest` would ship a stale coverage
// check.
func TestManifestIsCurrent(t *testing.T) {
	skipIfNoProtoc(t)
	computed, err := Compute(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	want, err := computed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, releaseManifest) {
		t.Fatalf("%s is stale: run `just kitchensink-manifest` and commit it", ManifestPath)
	}
}

const featureProto = `syntax = "proto3";

package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo"};

enum Kind {
  KIND_UNSPECIFIED = 0;
  KIND_A = 1;
}

message Req {
  string name = 1 [(spec.assert_required) = true, (spec.assert_max_size) = 8];
  optional bytes blob = 2 [(spec.assert_max_size) = 4];
  map<int32, int64> counts = 3 [(spec.assert_max_size) = 2];
  repeated Kind kinds = 4 [(spec.assert_max_size) = 2];
  oneof choice {
    sint64 left = 5;
    string right = 6;
  }
}

service DemoApi {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc Watch(Req) returns (stream Req) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, flags: [RPC_FLAG_AUTH_ANY_USER]};
  }
}
`

func TestProtoDirFeaturesNamesOptionsShapesAndCombinations(t *testing.T) {
	skipIfNoProtoc(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "services", "demo.proto"), []byte(featureProto), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := memberFeatures(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"file/service",
		"file/service.name",
		"field/assert_required",
		"field/assert_max_size",
		"field/string",
		"field/optional bytes",
		"field/map<int32,int64>",
		"field/repeated enum",
		"field/oneof",
		"field/sint64",
		"service/service_c2s.auth_entitled_users",
		"method/rpc.principal=RPC_PRINCIPAL_USER",
		"method/rpc.flags=RPC_FLAG_AUTH_ANY_USER",
		"method/rpc{exposure=RPC_EXPOSURE_EXTERNAL,principal=RPC_PRINCIPAL_USER}",
		"method/streaming=server",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("features lack %q; got %v", want, got)
		}
	}
	for _, f := range got {
		if f == "field/map_entry" || slices.Contains([]string{"file/go_package", "field/bytes"}, f) {
			t.Errorf("unexpected feature %q", f)
		}
	}
}

func TestUncoveredIsPerLanguage(t *testing.T) {
	m := &Manifest{Features: map[string][]string{"field/assert_gt": {"go", "java"}}}
	got := m.Uncovered("rust", []string{"field/assert_gt", "field/assert_sql"})
	if !slices.Equal(got, []string{"field/assert_gt", "field/assert_sql"}) {
		t.Errorf("Uncovered(rust) = %v", got)
	}
	if got := m.Uncovered("go", []string{"field/assert_gt"}); len(got) != 0 {
		t.Errorf("Uncovered(go) = %v, want none", got)
	}
}
