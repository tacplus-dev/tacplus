// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// recordStoreHost is the physical service that backs the record query plane:
// a file-level (spec.service) identity plus (spec.record_store).
func recordStoreHost() *FileDescriptor {
	return &FileDescriptor{
		Path:        "services/stratum.proto",
		Package:     "stratum",
		ServiceName: "stratum",
		RecordStore: true,
	}
}

// recordPlaneTrait is a trait proto asking for the record query plane: it names
// the record store as its host, which is what the record-map injector resolves
// a plane on.
func recordPlaneTrait(host string) *FileDescriptor {
	return &FileDescriptor{
		Path:    "vservices/query.proto",
		Package: "taskquery",
		Trait: &TraitDescriptor{
			Name:       "query",
			ID:         40,
			Host:       host,
			S2SService: "RecordMap",
			C2SService: "RecordQuery",
		},
	}
}

// recordWithMinGroupSize is a (spec.record) message declaring a threshold.
func recordWithMinGroupSize(n int64) *FileDescriptor {
	return &FileDescriptor{
		Path:    "services/tasks.proto",
		Package: "tasks",
		Messages: []MessageDescriptor{{
			Name: "TaskActivity",
			Annotations: map[string]model.AnnotationValue{
				"record":                {Type: model.AnnotationTypeBool, BoolValue: true},
				"record_min_group_size": {Type: model.AnnotationTypeInt, IntValue: n},
			},
		}},
	}
}

// The correct declaration generates: a positive threshold on a record whose
// fleet carries a trait hosting the record query plane.
func TestValidateMinGroupSizeAcceptsGuardedRecord(t *testing.T) {
	fds := []*FileDescriptor{recordStoreHost(), recordPlaneTrait("stratum"), recordWithMinGroupSize(5)}
	if err := validateMinGroupSizeAnnotations(fds); err != nil {
		t.Fatalf("a positive threshold on a record with a hosted query plane must generate: %v", err)
	}
}

// A record that declares no threshold is untouched, plane or no plane — the
// annotation is additive.
func TestValidateMinGroupSizeIgnoresUndeclaredRecord(t *testing.T) {
	plain := &FileDescriptor{
		Path:    "services/tasks.proto",
		Package: "tasks",
		Messages: []MessageDescriptor{{
			Name:        "TaskActivity",
			Annotations: map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}},
		}},
	}
	if err := validateMinGroupSizeAnnotations([]*FileDescriptor{plain}); err != nil {
		t.Fatalf("a record declaring no min_group_size must generate: %v", err)
	}
}

// Red control (a): a negative threshold is a guard that can never refuse.
func TestValidateMinGroupSizeRejectsNegative(t *testing.T) {
	fds := []*FileDescriptor{recordStoreHost(), recordPlaneTrait("stratum"), recordWithMinGroupSize(-1)}
	err := validateMinGroupSizeAnnotations(fds)
	if err == nil {
		t.Fatal("a negative (spec.record).min_group_size must fail generation")
	}
	if !strings.Contains(err.Error(), "tasks.TaskActivity") || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("error must name the record and the negative threshold, got: %v", err)
	}
}

// Red control (b): a threshold declared into a fleet with no trait hosting the
// record query plane guards nothing.
func TestValidateMinGroupSizeRejectsUnguardedRecord(t *testing.T) {
	fds := []*FileDescriptor{recordStoreHost(), recordWithMinGroupSize(5)}
	err := validateMinGroupSizeAnnotations(fds)
	if err == nil {
		t.Fatal("a min_group_size with no trait hosting a record query plane must fail generation")
	}
	if !strings.Contains(err.Error(), "tasks.TaskActivity") || !strings.Contains(err.Error(), "spec.trait") {
		t.Fatalf("error must name the record and the missing plane, got: %v", err)
	}
}

// A trait exists but hosts something that is not a record store: still no
// record query plane, so the threshold is still unguarded.
func TestValidateMinGroupSizeRejectsTraitOnNonRecordStore(t *testing.T) {
	other := &FileDescriptor{Path: "services/other.proto", Package: "other", ServiceName: "other"}
	fds := []*FileDescriptor{recordStoreHost(), other, recordPlaneTrait("other"), recordWithMinGroupSize(5)}
	err := validateMinGroupSizeAnnotations(fds)
	if err == nil {
		t.Fatal("a trait hosted by a non-record-store must not count as a guarded surface")
	}
	if !strings.Contains(err.Error(), "spec.trait") {
		t.Fatalf("error must name the missing plane, got: %v", err)
	}
}
