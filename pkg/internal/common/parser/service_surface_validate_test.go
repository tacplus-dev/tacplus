// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSurfaceProtoDir writes spec.proto plus the given service protos into a
// temp dir and parses it with protoc, so the fixture exercises the real
// annotation parsing rather than a hand-built descriptor. Extra protos land as
// surface2.proto, surface3.proto, ... for cases whose subject is how one proto
// resolves against another in the same build.
func writeSurfaceProtoDir(t *testing.T, protoText string, extraProtoTexts ...string) *DescriptorParser {
	t.Helper()
	skipIfNoProtoc(t)
	dir := t.TempDir()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
	writeTempProtoFile(t, dir, "surface.proto", protoText)
	for i, extra := range extraProtoTexts {
		writeTempProtoFile(t, dir, fmt.Sprintf("surface%d.proto", i+2), extra)
	}
	dp, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return dp
}

// A service carrying both service_c2s and service_s2s must fail generation.
// Before this check the shape was accepted and then silently reinterpreted:
// rest/proxy dispatch generation short-circuits on IsS2S and emits no REST
// route or gateway dispatch arm, so the C2S half vanished from a clean,
// warning-free build.
func TestValidateServiceSurfaces_RejectsC2SAndS2SOnOneService(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package surfacedemo;

import "sys/spec.proto";

message PingRequest {}
message PingResponse {}

service Echo {
  option (spec.service_c2s) = {auth_entitled_users: true};
  option (spec.service_s2s) = {auth_services: ["tasks"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := ValidateServiceSurfaces(dp)
	if err == nil {
		t.Fatal("ValidateServiceSurfaces accepted a service declaring both service_c2s and service_s2s")
	}
	for _, want := range []string{"surfacedemo.Echo", "spec.service_c2s", "spec.service_s2s", "split it into two services"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
}

// service_c2d is the third caller surface and collapses the same way: the
// downstream consumers take all three flags and pick ONE classification
// (collection.TrustOf returns a single trust tier), so a service claiming both
// the unauthenticated unix-socket surface and a network surface silently
// becomes whichever one the classifier prefers. Reject it at the source.
func TestValidateServiceSurfaces_RejectsC2DAlongsideAnotherSurface(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package surfacedemo;

import "sys/spec.proto";

message PingRequest {}
message PingResponse {}

service Echo {
  option (spec.service_c2s) = {auth_entitled_users: true};
  option (spec.service_c2d) = {socket_path: "/run/surfacedemo.sock"};

  rpc Ping(PingRequest) returns (PingResponse);
}

service Daemon {
  option (spec.service_s2s) = {auth_services: ["tasks"]};
  option (spec.service_c2d) = {socket_path: "/run/surfacedemo-daemon.sock"};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := ValidateServiceSurfaces(dp)
	if err == nil {
		t.Fatal("ValidateServiceSurfaces accepted services declaring service_c2d alongside another surface")
	}
	for _, want := range []string{
		"surfacedemo.Echo", "spec.service_c2s",
		"surfacedemo.Daemon", "spec.service_s2s",
		"spec.service_c2d", "split it into two services",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err.Error(), want)
		}
	}
}

// A service declaring only service_c2d is a normal, supported shape and must
// keep validating clean — the rule is "at most one", not "never c2d".
func TestValidateServiceSurfaces_AllowsC2DAlone(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package surfacedemo;

import "sys/spec.proto";

message PingRequest {}
message PingResponse {}

service Daemon {
  option (spec.service_c2d) = {socket_path: "/run/surfacedemo-daemon.sock"};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	if err := ValidateServiceSurfaces(dp); err != nil {
		t.Fatalf("a c2d-only service must validate clean, got: %v", err)
	}
}

// The supported shape for two surfaces on related RPCs — one service per
// surface, as testapp's Echo (C2S) / Probe (S2S) split does — must keep
// validating clean. This is what the rejection message steers authors to.
func TestValidateServiceSurfaces_AllowsSplitServices(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package surfacedemo;

import "sys/spec.proto";

message PingRequest {}
message PingResponse {}

service Echo {
  option (spec.service_c2s) = {auth_entitled_users: true};

  rpc Ping(PingRequest) returns (PingResponse);
}

service Probe {
  option (spec.service_s2s) = {auth_services: ["tasks"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	if err := ValidateServiceSurfaces(dp); err != nil {
		t.Fatalf("the Echo/Probe split must validate clean, got: %v", err)
	}
}
