// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func boolAnn() model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
}

func strAnn(v string) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: v}
}

// The emitted <T>Filter's principal criterion is what a c2s wrapper pins to the
// caller at Create<T>Query, which is the only thing that narrows the plane's
// SIZE-ONLY RPCs — no egress row filter can reach a bare count.
func TestRecordPrincipalCriterion(t *testing.T) {
	eq := func(names ...string) []recordFilter {
		var out []recordFilter
		for _, n := range names {
			out = append(out, recordFilter{name: n, protoType: "string"})
		}
		return out
	}

	// (spec.record).owner, EQ-queryable: the ordinary case, and the one that
	// makes this apply to a record that declares nothing else.
	msg := &parser.MessageDescriptor{
		Name:        "AgentEvent",
		Annotations: map[string]model.AnnotationValue{"record_owner": strAnn("spiffe_id")},
		Fields:      []parser.FieldDescriptor{{Name: "spiffe_id", Type: "string"}},
	}
	if got := recordPrincipalCriterion(msg, eq("event_id", "spiffe_id")); got != "spiffe_id" {
		t.Fatalf("owner criterion: got %q, want spiffe_id", got)
	}

	// (spec.store_principal) overrides the owner: a record whose declared owner
	// is the emitting SERVICE names its human principal on a different field,
	// and pinning the owner would narrow every human to nothing.
	msg = &parser.MessageDescriptor{
		Name:        "AccountingRecord",
		Annotations: map[string]model.AnnotationValue{"record_owner": strAnn("spiffe_id")},
		Fields: []parser.FieldDescriptor{
			{Name: "spiffe_id", Type: "string"},
			{Name: "principal_sub", Type: "string", Annotations: map[string]model.AnnotationValue{"store_principal": boolAnn()}},
		},
	}
	if got := recordPrincipalCriterion(msg, eq("spiffe_id", "principal_sub")); got != "principal_sub" {
		t.Fatalf("store_principal override: got %q, want principal_sub", got)
	}

	// Declared but not EQ-queryable: there is no criterion in the filter to carry
	// the pin, so the plane emits none rather than one it cannot express.
	if got := recordPrincipalCriterion(msg, eq("spiffe_id")); got != "" {
		t.Fatalf("un-queryable principal: got %q, want empty", got)
	}
	rangeOnly := []recordFilter{{name: "principal_sub", protoType: "string", rangeOp: true}}
	if got := recordPrincipalCriterion(msg, rangeOnly); got != "" {
		t.Fatalf("a RANGE criterion cannot express \"equal to me\": got %q, want empty", got)
	}

	// No principal declared at all.
	msg = &parser.MessageDescriptor{Name: "Orphan", Fields: []parser.FieldDescriptor{{Name: "id", Type: "string"}}}
	if got := recordPrincipalCriterion(msg, eq("id")); got != "" {
		t.Fatalf("undeclared principal: got %q, want empty", got)
	}
}
