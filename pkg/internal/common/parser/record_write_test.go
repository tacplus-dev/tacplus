// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// recordWriteFds builds a record store, a trait hosting the record query plane
// on it (when withPlane), and one record carrying the given s2s_write grant.
func recordWriteFds(grant []string, private, withPlane bool) []*FileDescriptor {
	rec := MessageDescriptor{
		Name: "Row",
		Annotations: map[string]model.AnnotationValue{
			"record":           {Type: model.AnnotationTypeBool, BoolValue: true},
			"record_s2s_write": {Type: model.AnnotationTypeBool, BoolValue: true, StringSliceValue: grant},
		},
	}
	if private {
		rec.Annotations["record_private"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	fds := []*FileDescriptor{
		{Path: "store.proto", Package: "store", ServiceName: "stratum", RecordStore: true},
		{Path: "owner.proto", Package: "owner", ServiceName: "owner", Messages: []MessageDescriptor{rec}},
	}
	if withPlane {
		fds = append(fds, &FileDescriptor{Path: "query.proto", Package: "query", Trait: &TraitDescriptor{
			Name: "query", Host: "stratum", S2SService: "RecordMap", AuthServices: []string{"stratum"},
		}})
	}
	return fds
}

func TestRecordWriteGrantAccepted(t *testing.T) {
	if err := validateRecordWriteAnnotations(recordWriteFds([]string{"identity"}, false, true)); err != nil {
		t.Fatalf("a named write audience on a hosted plane must validate, got: %v", err)
	}
}

// A declared-but-empty list is refused, never folded into the trait-wide list.
func TestRecordWriteEmptyGrantRejected(t *testing.T) {
	err := validateRecordWriteAnnotations(recordWriteFds(nil, false, true))
	if err == nil || !strings.Contains(err.Error(), "s2s_write declares no auth_services") || !strings.Contains(err.Error(), "owner.Row") {
		t.Fatalf("an empty s2s_write must be refused naming the record, got: %v", err)
	}
}

// A private record gets no peer-facing write surface, so the grant is refused.
func TestRecordWritePrivateRejected(t *testing.T) {
	err := validateRecordWriteAnnotations(recordWriteFds([]string{"identity"}, true, true))
	if err == nil || !strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "s2s_write") {
		t.Fatalf("s2s_write on a private record must be refused, got: %v", err)
	}
}

// No trait hosting the plane: nothing would emit the surface the grant scopes.
func TestRecordWriteWithoutPlaneRejected(t *testing.T) {
	err := validateRecordWriteAnnotations(recordWriteFds([]string{"identity"}, false, false))
	if err == nil || !strings.Contains(err.Error(), "no (spec.trait) hosts a record query plane") {
		t.Fatalf("s2s_write with no plane must be refused, got: %v", err)
	}
}
