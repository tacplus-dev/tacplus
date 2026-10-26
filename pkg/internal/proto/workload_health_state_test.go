// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A workload — a program whose own proto declares only event bindings (a
// pigeon: no server, no state root) — still gets the service-health state,
// because its publishers and its broker discovery ride it. A proto with
// neither bindings nor a server does not.
func TestEventOnlyWorkloadCountsForHealthState(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services/pigeon.proto", `syntax = "proto3";
package pigeon;
import "sys/spec.proto";
option (spec.service) = {name: "pigeon"};
message Thread {
  option (spec.event_publish) = {topic_ref: "coop.PigeonMessageBus.stream"};
  option (spec.event_watch) = {topic_ref: "coop.PigeonMessageBus.stream" method_s2s: {auth_services: ["[^/]+/workloads/pigeon-[a-z0-9]+"]}};
}
`)
	write("services/other.proto", `syntax = "proto3";
package other;
import "sys/spec.proto";
option (spec.service) = {name: "other"};
message Thing { string id = 1; }
`)
	if !hasOwnEventBindings(&tactype.Config{SelfPackage: "pigeon"}, dir) {
		t.Fatal("an actor whose own proto declares event bindings is a workload and must count")
	}
	if hasOwnEventBindings(&tactype.Config{SelfPackage: "other"}, dir) {
		t.Fatal("another actor's bindings must not count for this one")
	}
	if hasOwnEventBindings(&tactype.Config{SelfPackage: ""}, dir) || hasOwnEventBindings(nil, dir) {
		t.Fatal("no actor, no bindings")
	}
}
