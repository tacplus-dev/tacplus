// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"
)

func opaEvalFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	return dir
}

// MS-5: a message that declares (spec.opa_eval) but no (spec.opa_eval_result) is
// a misconfiguration. It previously only WARNed and silently skipped, emitting a
// service missing the OpaEval RPC the annotation asks for; it must now fail loud.
func TestGenerateOpaEvalProtoFailsLoudOnMissingResult(t *testing.T) {
	dir := opaEvalFixtureDir(t)
	writeProto(t, dir, "badopa.proto", `syntax = "proto3";
package badopa;

import "sys/spec.proto";

message BadInput {
  option (spec.opa_eval) = {entry_point: "data.x"};
  string email = 1;
}
`)
	_, err := GenerateOpaEvalProto(t.TempDir(), dir)
	if err == nil {
		t.Fatal("expected a loud error for opa_eval without opa_eval_result, got nil")
	}
	if !strings.Contains(err.Error(), "BadInput") || !strings.Contains(err.Error(), "opa_eval_result") {
		t.Fatalf("error should name the message and the missing annotation, got: %v", err)
	}
}

// A well-formed opa_eval (entry_point + result) still generates without error.
func TestGenerateOpaEvalProtoAcceptsCompleteDefinition(t *testing.T) {
	dir := opaEvalFixtureDir(t)
	writeProto(t, dir, "goodopa.proto", `syntax = "proto3";
package goodopa;

import "sys/spec.proto";

message GoodInput {
  option (spec.opa_eval) = {entry_point: "data.x" result: "GoodResult"};
  string email = 1;
}

message GoodResult {
  bool allow = 1;
}
`)
	// appendOpaEvalToSourceFile reads/rewrites the source proto in tempProtoDir, so
	// it must be the dir that actually holds the fixtures.
	files, err := GenerateOpaEvalProto(dir, dir)
	if err != nil {
		t.Fatalf("expected a complete opa_eval definition to generate, got: %v", err)
	}
	if len(files) != 1 || files[0] != "goodopa.proto" {
		t.Fatalf("expected [goodopa.proto], got: %v", files)
	}
}

// MS-5: IsOpaServer accumulates across every file in the package instead of
// breaking at the first service-bearing file. A package whose service is declared
// in one file and whose sys/opalib.proto import lives in another must be
// recognized as an OPA server regardless of the (map-order-nondeterministic) file
// iteration — the old early-break returned false whenever the service file was
// visited before the opalib-importing file.
func TestIsOpaServerAccumulatesAcrossPackageFiles(t *testing.T) {
	dir := opaEvalFixtureDir(t)
	// File A: carries the service identity (file-level spec.service), no opalib import.
	writeProto(t, dir, "svc_api.proto", `syntax = "proto3";
package svceval;

import "sys/spec.proto";

option (spec.service) = {name: "svceval"};

message PingReq {}
message PingResp {}

service SvcEvalService {
  rpc Ping(PingReq) returns (PingResp);
}
`)
	// File B: same package, imports opalib — but declares no service.
	writeProto(t, dir, "svc_opa.proto", `syntax = "proto3";
package svceval;

import "sys/opalib.proto";

message Marker {
  string x = 1;
}
`)
	// Run repeatedly: the answer must be stable true across map-iteration orders.
	for i := 0; i < 8; i++ {
		got, err := IsOpaServer(dir, "svceval")
		if err != nil {
			t.Fatalf("IsOpaServer: %v", err)
		}
		if !got {
			t.Fatal("IsOpaServer must be true when the package has a service AND imports opalib in a sibling file")
		}
	}
}

// A package with a service but no opalib import is not an OPA server.
func TestIsOpaServerFalseWithoutOpalibImport(t *testing.T) {
	dir := opaEvalFixtureDir(t)
	writeProto(t, dir, "plain_api.proto", `syntax = "proto3";
package plainsvc;

import "sys/spec.proto";

option (spec.service) = {name: "plainsvc"};

message PingReq {}
message PingResp {}

service PlainService {
  rpc Ping(PingReq) returns (PingResp);
}
`)
	got, err := IsOpaServer(dir, "plainsvc")
	if err != nil {
		t.Fatalf("IsOpaServer: %v", err)
	}
	if got {
		t.Fatal("IsOpaServer must be false for a package that does not import opalib")
	}
}
