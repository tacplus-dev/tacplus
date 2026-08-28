// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// NEW-13 residue: buildStateService used to skip only SCALAR-valued map fields,
// so a message-valued map on a state root fell through to boundary
// classification and synthesized a state service over the internal map-entry
// type — a proto protoc rejects ("map_entry should not be set explicitly").
//
// ValidateStateConsistency rejects every map on a state root before generation
// runs, so this path is defence in depth; the point of the pin is that the three
// mechanisms (validation, boundary collector, proto synthesis) must not disagree
// about what a map field is again.
func TestBuildStateServiceSkipsMessageValuedMapFields(t *testing.T) {
	stateRoot := &parser.MessageDescriptor{
		Name: "DemoState",
		Fields: []parser.FieldDescriptor{
			{
				Name:             "records",
				Type:             "DemoRecordEntry",
				TypeKind:         "message",
				IsMap:            true,
				MapKeyType:       "string",
				MapValueType:     "DemoRecord",
				MapValueTypeKind: "message",
			},
			{
				Name:     "config",
				Type:     "Config",
				TypeKind: "message",
			},
		},
	}

	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name, nil, nil, nil)

	if strings.Contains(result.serviceProto, "Records") || strings.Contains(result.serviceProto, "records") {
		t.Errorf("message-valued map field became a state boundary:\n%s", result.serviceProto)
	}
	if result.singletons != 1 || result.collections != 0 {
		t.Errorf("boundary counts = %d singletons / %d collections, want 1 / 0 (only the config field is a boundary)",
			result.singletons, result.collections)
	}
	if !strings.Contains(result.serviceProto, "rpc GetConfig(") {
		t.Errorf("non-map boundary must still generate its RPCs:\n%s", result.serviceProto)
	}
}

// The scalar-valued case was already skipped; keep it pinned alongside so a
// future narrowing of the skip is caught for both value kinds.
func TestBuildStateServiceSkipsScalarValuedMapFields(t *testing.T) {
	stateRoot := &parser.MessageDescriptor{
		Name: "DemoState",
		Fields: []parser.FieldDescriptor{{
			Name:             "labels",
			Type:             "LabelsEntry",
			TypeKind:         "message",
			IsMap:            true,
			MapKeyType:       "string",
			MapValueType:     "string",
			MapValueTypeKind: "scalar",
		}},
	}

	result := buildStateService(&parser.FileDescriptor{Package: "demo"}, stateRoot, stateRoot.Name, nil, nil, nil)
	if result.singletons != 0 || result.collections != 0 {
		t.Errorf("scalar-valued map produced %d singletons / %d collections, want 0 / 0",
			result.singletons, result.collections)
	}
}
