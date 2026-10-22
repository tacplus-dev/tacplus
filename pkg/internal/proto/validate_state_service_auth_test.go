// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStateSvcProto(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// A generated state service with an empty spec.service_s2s is the exact shape
// that fails closed at runtime on every S2S call with no build signal.
func TestValidateGeneratedStateServiceAuthRejectsEmptyAuthServices(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service state
service ThingStateService {
	option (spec.service_s2s) = {};
	rpc GetConfig(Empty) returns (Empty);
}
`)

	err := ValidateGeneratedStateServiceAuth(dir, nil)
	if err == nil {
		t.Fatal("expected an error for a state service with no auth_services")
	}
	if !strings.Contains(err.Error(), "ThingStateService") {
		t.Fatalf("error should name the offending service, got: %v", err)
	}
}

func TestValidateGeneratedStateServiceAuthAcceptsResolvedCallers(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service state
service ThingStateService {
	option (spec.service_s2s) = {auth_services: ["thing", "other"]};
	rpc GetConfig(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedStateServiceAuth(dir, nil); err != nil {
		t.Fatalf("ValidateGeneratedStateServiceAuth: %v", err)
	}
}

// disable_auth_message roots skip token validation entirely, so they carry no
// caller list by design and must not be flagged.
func TestValidateGeneratedStateServiceAuthExemptsDisableAuthRoots(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service state
service ThingStateService {
	option (spec.service_s2s) = {};
	rpc GetConfig(Empty) returns (Empty);
}
`)

	exempt := map[string]bool{"svc/thing.proto/ThingStateService": true}
	if err := ValidateGeneratedStateServiceAuth(dir, exempt); err != nil {
		t.Fatalf("exempt state service should pass: %v", err)
	}
}

// Hand-authored S2S services carry no generated-state sentinel and are none of
// this check's business — it must not widen into a general auth linter.
func TestValidateGeneratedStateServiceAuthIgnoresHandAuthoredServices(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

service HandWritten {
  option (spec.service_s2s) = {};
  rpc DoThing(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedStateServiceAuth(dir, nil); err != nil {
		t.Fatalf("hand-authored service must not be flagged: %v", err)
	}
}

// The store synthesis path carries the same post-condition: an empty
// auth_services on a generated store service is the shape that fails closed at
// runtime on every S2S call with no build signal.
func TestValidateGeneratedStoreServiceAuthRejectsEmptyAuthServices(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service store
service ThingStoreService {
	option (spec.service_s2s) = {};
	rpc Get(Empty) returns (Empty);
}
`)

	err := ValidateGeneratedStoreServiceAuth(dir)
	if err == nil {
		t.Fatal("expected an error for a store service with no auth_services")
	}
	if !strings.Contains(err.Error(), "ThingStoreService") {
		t.Fatalf("error should name the offending service, got: %v", err)
	}
}

func TestValidateGeneratedStoreServiceAuthAcceptsResolvedCallers(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service store
service ThingStoreService {
	option (spec.service_s2s) = {auth_services: ["thing"]};
	rpc Get(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedStoreServiceAuth(dir); err != nil {
		t.Fatalf("ValidateGeneratedStoreServiceAuth: %v", err)
	}
}

func TestValidateGeneratedOpaEvalServiceAuthRejectsEmptyAuthServices(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service opaeval
service OpaEval {
	option (spec.service_s2s) = {};
	rpc EvalThing(Empty) returns (Empty);
}
`)

	err := ValidateGeneratedOpaEvalServiceAuth(dir)
	if err == nil {
		t.Fatal("expected an error for an OpaEval service with no auth_services")
	}
	if !strings.Contains(err.Error(), "OpaEval") {
		t.Fatalf("error should name the offending service, got: %v", err)
	}
}

func TestValidateGeneratedOpaEvalServiceAuthAcceptsResolvedCallers(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service opaeval
service OpaEval {
	option (spec.service_s2s) = {auth_services: ["thing"]};
	rpc EvalThing(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedOpaEvalServiceAuth(dir); err != nil {
		t.Fatalf("ValidateGeneratedOpaEvalServiceAuth: %v", err)
	}
}

// Each synthesis path's validator matches only its own sentinel: a store
// service's empty auth_services must not be reported by the state validator
// (and vice versa), or a state-path exemption set would silently apply to it.
func TestValidateGeneratedServiceAuthIsScopedToItsOwnSentinel(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service store
service ThingStoreService {
	option (spec.service_s2s) = {};
	rpc Get(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedStateServiceAuth(dir, nil); err != nil {
		t.Fatalf("state validator must ignore a store sentinel, got: %v", err)
	}
	if err := ValidateGeneratedOpaEvalServiceAuth(dir); err != nil {
		t.Fatalf("opa eval validator must ignore a store sentinel, got: %v", err)
	}
}

// The body scan must find the service's own closing brace by depth, not the
// first unindented "}" — a multi-line option literal earlier in the body ends
// with one of its own, and stopping there would truncate the body before a
// later, real auth_services declaration and misreport a passing service as a
// violation.
func TestValidateGeneratedServiceAuthHandlesNestedBraceInOption(t *testing.T) {
	dir := t.TempDir()
	writeStateSvcProto(t, dir, "svc/thing.proto", `syntax = "proto3";
package thing;

// tacplus:generated-service state
service ThingStateService {
	option (spec.custom) = {
foo: "bar"
};
	option (spec.service_s2s) = {auth_services: ["thing"]};
	rpc GetConfig(Empty) returns (Empty);
}
`)

	if err := ValidateGeneratedStateServiceAuth(dir, nil); err != nil {
		t.Fatalf("ValidateGeneratedStateServiceAuth: %v", err)
	}
}
