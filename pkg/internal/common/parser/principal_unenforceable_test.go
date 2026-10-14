// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// An rpc that can never carry a verified caller must not demand one of every
// request. The annotation's whole contract is "bind this field to the
// authenticated caller"; on an auth_disabled or service_c2d rpc nothing
// authenticates one, so the generated wrapper would refuse every call with
// UNAUTHENTICATED and the rpc would be uncallable by construction.
//
// The refusal is language-agnostic: the predicate is the proto's
// (method.AuthDisabled || svc.AuthDisabled || svc.IsC2D), so this one check
// covers the Rust, Go and Java wrappers at once — there is no per-language
// variant to test.

func principalSurfaceProto(svcOption, rpcOption string) string {
	return `syntax = "proto3";

package pndemo;

import "sys/spec.proto";

message SendRequest {
  string sender = 1 [(spec.principal) = true];
  string body = 2;
}

message SendResponse { string id = 1; }

service Courier {
  ` + svcOption + `

  rpc Send(SendRequest) returns (SendResponse) {` + rpcOption + `}
}
`
}

// service_c2d: local IPC over a unix socket whose file permissions are the
// access boundary. It carries no token-authority-issued token and runs no
// authorizer, so there is no subject to stamp.
func TestPrincipalOnC2DRpcFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, principalSurfaceProto(
		`option (spec.service_c2d) = {socket_path: "/run/courier.sock"};`, ``))

	err := ValidatePrincipalAnnotations(dp)
	if err == nil {
		t.Fatal("(spec.principal) on a service_c2d rpc must fail generation")
	}
	for _, want := range []string{
		"principal annotation errors",
		"pndemo.Courier/Send",
		"(spec.principal) on SendRequest.sender",
		"service_c2d",
		"UNAUTHENTICATED",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// The per-rpc half: RPC_FLAG_PUBLIC_NO_AUTH on an otherwise authenticated
// service is the same contradiction, one rpc at a time.
func TestPrincipalOnAuthDisabledRpcFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, principalSurfaceProto(
		`option (spec.service_c2s) = {};`,
		`
    option (spec.rpc) = {flags: [RPC_FLAG_PUBLIC_NO_AUTH]};
  `))

	err := ValidatePrincipalAnnotations(dp)
	if err == nil {
		t.Fatal("(spec.principal) on an auth_disabled rpc must fail generation")
	}
	for _, want := range []string{"pndemo.Courier/Send", "auth_disabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// (spec.namespace) is the same marker bound to the tenant claim and fails for
// the same reason.
func TestNamespaceOnC2DRpcFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package pndemo;

import "sys/spec.proto";

message SendRequest {
  string namespace = 1 [(spec.namespace) = true];
}

message SendResponse { string id = 1; }

service Courier {
  option (spec.service_c2d) = {socket_path: "/run/courier.sock"};

  rpc Send(SendRequest) returns (SendResponse) {}
}
`)
	err := ValidatePrincipalAnnotations(dp)
	if err == nil {
		t.Fatal("(spec.namespace) on a service_c2d rpc must fail generation")
	}
	if !strings.Contains(err.Error(), "(spec.namespace) on SendRequest.namespace") {
		t.Fatalf("refusal must name the marker, got: %v", err)
	}
}

// Paired positive control. The IDENTICAL request on an authenticated surface
// generates — without this the red controls above would still pass if the
// fixture failed for an unrelated reason.
func TestPrincipalOnAuthenticatedRpcGenerates(t *testing.T) {
	dp := writeSurfaceProtoDir(t, principalSurfaceProto(`option (spec.service_c2s) = {};`, ``))

	if err := ValidatePrincipalAnnotations(dp); err != nil {
		t.Fatalf("(spec.principal) on an authenticated rpc must generate, got: %v", err)
	}
}

// The marker is only unenforceable where it is DEMANDED of a request. A c2d rpc
// whose request declares neither marker is untouched.
func TestC2DRpcWithoutMarkerGenerates(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package pndemo;

import "sys/spec.proto";

message SendRequest { string body = 1; }
message SendResponse { string id = 1; }

service Courier {
  option (spec.service_c2d) = {socket_path: "/run/courier.sock"};

  rpc Send(SendRequest) returns (SendResponse) {}
}
`)
	if err := ValidatePrincipalAnnotations(dp); err != nil {
		t.Fatalf("a c2d rpc with no marker must generate, got: %v", err)
	}
}
