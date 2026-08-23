// Copyright (c) 2026 Second Star Cavalier LLC

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
