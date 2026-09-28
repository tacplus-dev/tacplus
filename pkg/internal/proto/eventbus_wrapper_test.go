// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os/exec"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// MS-1: a genuine EventBus topic wrapper is identified by a (spec.topic) field,
// not by its name ending in "Bus". Protoc-free pin.
func TestIsEventBusTopicWrapper(t *testing.T) {
	wrapper := &parser.MessageDescriptor{
		Name: "FooBus",
		Fields: []parser.FieldDescriptor{{
			Name:        "stream",
			Annotations: map[string]model.AnnotationValue{"topic": {Type: model.AnnotationTypeBool, BoolValue: true}},
		}},
	}
	if !isEventBusTopicWrapper(wrapper) {
		t.Fatal("a message with a (spec.topic) field must be recognized as a wrapper")
	}

	// Unrelated message that merely shares the FooBus name — no topic field.
	unrelated := &parser.MessageDescriptor{
		Name:   "FooBus",
		Fields: []parser.FieldDescriptor{{Name: "id"}},
	}
	if isEventBusTopicWrapper(unrelated) {
		t.Fatal("a message with no (spec.topic) field must NOT be treated as a wrapper")
	}
}

// MS-1: an unrelated message colliding on the synthesized <Event>Bus name must
// fail loud rather than silently suppress the event's topic.
func TestAppendEventBusWrappersFailsLoudOnNameCollision(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := eventBusWrapperFixtureDir(t)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo"};

message Foo {
  option (spec.event) = {s2s: {}};
  string id = 1;
}

// Unrelated message that collides on the synthesized wrapper name — NOT a topic
// wrapper (no (spec.topic) field).
message FooBus {
  string unrelated = 1;
}
`)
	err := AppendEventBusWrappersToProtos(dir, dir)
	if err == nil {
		t.Fatal("expected a loud collision error, got nil")
	}
	if !strings.Contains(err.Error(), "FooBus") || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("error should name the colliding wrapper and the collision, got: %v", err)
	}
}

// MS-1: a genuine hand-written wrapper (mid-migration, carries a (spec.topic)
// field) is left in place — synthesis is a no-op, no error.
func TestAppendEventBusWrappersSkipsGenuineWrapper(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := eventBusWrapperFixtureDir(t)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo"};

message Foo {
  option (spec.event) = {s2s: {}};
  string id = 1;
}

// Genuine hand-written wrapper (mid-migration): carries the topic field.
message FooBus {
  option (spec.service_event) = {label: "eventbus", export: true};
  Foo stream = 1 [(spec.topic) = true];
}
`)
	if err := AppendEventBusWrappersToProtos(dir, dir); err != nil {
		t.Fatalf("a genuine wrapper must be left in place without error, got: %v", err)
	}
}

func eventBusWrapperFixtureDir(t *testing.T) string {
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
