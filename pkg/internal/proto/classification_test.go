// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func TestFileStateClassification(t *testing.T) {
	userState := &parser.FileDescriptor{
		Messages: []parser.MessageDescriptor{{
			Name: "DemoState",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "DemoState"},
			},
		}},
	}
	if !DeclaresServiceState(userState) {
		t.Fatal("file with service_state message should declare service state")
	}
	if IsSyntheticServiceOnlyFile(userState) {
		t.Fatal("file with service_state message must not be treated as synthetic-only")
	}

	syntheticOnly := &parser.FileDescriptor{
		Services: []parser.ServiceDescriptor{{
			Name:        "DemoStateService",
			Synthesized: true,
		}},
	}
	if !HasSynthesizedServices(syntheticOnly) {
		t.Fatal("file with synthesized service should report synthesized services")
	}
	if !IsSyntheticServiceOnlyFile(syntheticOnly) {
		t.Fatal("file with synthesized service and no service_state roots should be synthetic-only")
	}

	mixed := &parser.FileDescriptor{
		Messages: userState.Messages,
		Services: syntheticOnly.Services,
	}
	if IsSyntheticServiceOnlyFile(mixed) {
		t.Fatal("file with both state roots and synthesized services is still a state source")
	}
}

func TestDeclaresServiceStateModel(t *testing.T) {
	pf := model.ProtoFile{
		Messages: []model.Message{{
			Name: "DemoState",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "DemoState"},
			},
		}},
	}
	if !DeclaresServiceStateModel(pf) {
		t.Fatal("model proto file with service_state message should declare service state")
	}

	pf.Messages[0].Annotations["service_state"] = model.AnnotationValue{Type: model.AnnotationTypeString}
	if DeclaresServiceStateModel(pf) {
		t.Fatal("empty service_state annotation value should not declare service state")
	}
}

func TestDeclaresServiceStateSource(t *testing.T) {
	for _, source := range []string{
		`message DemoState { option (spec.service_state) = "DemoState"; }`,
		`message DemoEvent { option ( spec.service_event ) = { label: "demo" }; }`,
	} {
		if !DeclaresServiceStateSource(source) {
			t.Fatalf("source should declare service state: %s", source)
		}
	}

	if DeclaresServiceStateSource(`message Regular { string id = 1; }`) {
		t.Fatal("regular source should not declare service state")
	}
}

// MS-10 class: DeclaresServiceStateSource scans raw user proto text; its regex
// was unanchored and false-matched a commented-out (spec.service_state)
// annotation as an active declaration. Now anchored to line-start.
func TestDeclaresServiceStateSource_IgnoresCommentedOut(t *testing.T) {
	commented := `syntax = "proto3";
package foo;
message Real {
  // option (spec.service_state) = {name: "Ghost"};
  string x = 1;
}
`
	if DeclaresServiceStateSource(commented) {
		t.Error("a commented-out (spec.service_state) must not count as a declaration")
	}
	active := `syntax = "proto3";
package foo;
message Real {
  option (spec.service_state) = {name: "Real"};
  string x = 1;
}
`
	if !DeclaresServiceStateSource(active) {
		t.Error("an active (spec.service_state) must be detected")
	}
	// service_event is the other accepted marker.
	ev := "message E {\n  option (spec.service_event) = {};\n}\n"
	if !DeclaresServiceStateSource(ev) {
		t.Error("an active (spec.service_event) must be detected")
	}
}
