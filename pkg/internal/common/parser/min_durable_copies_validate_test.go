// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func minDurableCopiesFile(fieldName string, minDurableCopies *int64, quorumWrite bool) *DescriptorParser {
	ann := map[string]model.AnnotationValue{}
	if minDurableCopies != nil {
		ann["min_durable_copies"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: *minDurableCopies}
	}
	if quorumWrite {
		ann["quorum_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	return &DescriptorParser{
		descriptors: map[string]*FileDescriptor{
			"services/state.proto": {
				Path:    "services/state.proto",
				Package: "durabledemo",
				Messages: []MessageDescriptor{{
					Name: "Balance",
					Fields: []FieldDescriptor{{
						Name:        fieldName,
						Annotations: ann,
					}},
				}},
			},
		},
	}
}

func intp(v int64) *int64 { return &v }

// Positive control: min_durable_copies alongside quorum_write, at the legal
// floor of 1 (the deliberate single-node dev setting), must generate cleanly.
func TestValidateMinDurableCopiesAnnotationsAcceptsValidPair(t *testing.T) {
	dp := minDurableCopiesFile("amount", intp(1), true)
	if err := ValidateMinDurableCopiesAnnotations(dp); err != nil {
		t.Fatalf("min_durable_copies=1 with quorum_write must generate, got: %v", err)
	}
}

// A field with no min_durable_copies at all (the common case, relying on the
// documented default of 2) must generate cleanly regardless of quorum_write.
func TestValidateMinDurableCopiesAnnotationsAcceptsUnset(t *testing.T) {
	dp := minDurableCopiesFile("amount", nil, true)
	if err := ValidateMinDurableCopiesAnnotations(dp); err != nil {
		t.Fatalf("a field with no min_durable_copies must generate, got: %v", err)
	}
}

// Red control: a value below 1 is a durability floor that can never be missed,
// i.e. no floor at all, and generation must refuse it.
func TestValidateMinDurableCopiesAnnotationsRejectsZero(t *testing.T) {
	dp := minDurableCopiesFile("amount", intp(0), true)
	err := ValidateMinDurableCopiesAnnotations(dp)
	if err == nil {
		t.Fatal("min_durable_copies=0 must fail generation")
	}
	for _, want := range []string{"durabledemo.Balance.amount", "at least 1", "got 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Red control, the other invariant: min_durable_copies declared on a field with
// no quorum_write is a knob nothing reads, and generation must refuse it.
func TestValidateMinDurableCopiesAnnotationsRejectsWithoutQuorumWrite(t *testing.T) {
	dp := minDurableCopiesFile("amount", intp(2), false)
	err := ValidateMinDurableCopiesAnnotations(dp)
	if err == nil {
		t.Fatal("min_durable_copies without quorum_write must fail generation")
	}
	if !strings.Contains(err.Error(), "requires the same field to declare (spec.quorum_write)") {
		t.Fatalf("refusal must name the missing requirement, got: %v", err)
	}
}

func quorumField(name string, quorum bool, minDurableCopies int) FieldDescriptor {
	ann := map[string]model.AnnotationValue{}
	if quorum {
		ann["quorum_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if minDurableCopies > 0 {
		ann["min_durable_copies"] = model.AnnotationValue{
			Type: model.AnnotationTypeInt, IntValue: int64(minDurableCopies)}
	}
	return FieldDescriptor{Name: name, Type: "string", Annotations: ann}
}

func TestStoreQuorumDefaultsToTwoWhenUndeclared(t *testing.T) {
	msg := &MessageDescriptor{
		Name:   "CredPair",
		Fields: []FieldDescriptor{quorumField("client_secret", true, 0)},
	}
	quorum, min := StoreQuorum(msg)
	if !quorum {
		t.Fatal("a field declaring quorum_write must gate the store")
	}
	if min != 2 {
		t.Fatalf("quorum_write with no min_durable_copies must default to 2, got %d", min)
	}
}

func TestStoreQuorumHonoursTheDeclaredSingleNodeFloor(t *testing.T) {
	msg := &MessageDescriptor{
		Name:   "CredPair",
		Fields: []FieldDescriptor{quorumField("client_secret", true, 1)},
	}
	quorum, min := StoreQuorum(msg)
	if !quorum || min != 1 {
		t.Fatalf("min_durable_copies:1 is the deliberate single-node setting, got quorum=%v min=%d", quorum, min)
	}
}

// A row is written whole, so the strictest declared floor is the one the write
// has to clear; taking the first or the last would let a 3-copy field ship under
// a 2-copy gate depending on field order.
func TestStoreQuorumTakesTheStrictestFloor(t *testing.T) {
	msg := &MessageDescriptor{
		Name: "CredPair",
		Fields: []FieldDescriptor{
			quorumField("client_secret", true, 3),
			quorumField("refresh_token", true, 2),
		},
	}
	if _, min := StoreQuorum(msg); min != 3 {
		t.Fatalf("strictest floor must govern, got %d", min)
	}
}

func TestStoreQuorumIsAbsentWithoutTheAnnotation(t *testing.T) {
	msg := &MessageDescriptor{
		Name:   "CredPair",
		Fields: []FieldDescriptor{quorumField("client_secret", false, 0)},
	}
	if quorum, min := StoreQuorum(msg); quorum || min != 0 {
		t.Fatalf("a store declaring no quorum_write must get no gate, got quorum=%v min=%d", quorum, min)
	}
}
