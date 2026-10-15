// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"strings"
)

// ValidateMinDurableCopiesAnnotations enforces the two invariants
// (spec.min_durable_copies) needs to mean anything: the count must be at least
// 1 (a value below that is a durability floor that can never be missed, i.e. no
// floor at all), and the field must also declare (spec.quorum_write), since
// min_durable_copies only has a subject to gate — the in-memory replica count a
// quorum_write ack must reach — when the write it modifies is itself declared.
// Both are rejected at generation time rather than silently passing through,
// matching every other annotation this generation validates.
func ValidateMinDurableCopiesAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			for fi := range msg.Fields {
				field := &msg.Fields[fi]
				av, ok := field.Annotations["min_durable_copies"]
				if !ok {
					continue
				}
				if av.IntValue < 1 {
					errs = append(errs, fmt.Sprintf("%s.%s.%s: (spec.min_durable_copies) must be at least 1, got %d",
						fd.Package, msg.Name, field.Name, av.IntValue))
				}
				if _, ok := field.Annotations["quorum_write"]; !ok {
					errs = append(errs, fmt.Sprintf("%s.%s.%s: (spec.min_durable_copies) requires the same field to declare (spec.quorum_write)",
						fd.Package, msg.Name, field.Name))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("min_durable_copies annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// DefaultMinDurableCopies is the floor a (spec.quorum_write) field carries when
// it declares no (spec.min_durable_copies) of its own — spec.proto's documented
// "Default 2 when unset".
const DefaultMinDurableCopies = 2

// StoreQuorum reads a store's write gate off its fields: whether any field
// declares (spec.quorum_write), and the floor to enforce. Every language's
// generated per-store quorum gate renders from it.
//
// A store row is written whole, so when several fields declare a floor the
// STRICTEST one governs — a per-field floor would need a per-field write, which
// no store surface has.
func StoreQuorum(msg *MessageDescriptor) (bool, int) {
	quorum := false
	min := 0
	for i := range msg.Fields {
		field := &msg.Fields[i]
		qw, ok := field.Annotations["quorum_write"]
		if !ok || !qw.BoolValue {
			continue
		}
		quorum = true
		floor := DefaultMinDurableCopies
		if mdc, ok := field.Annotations["min_durable_copies"]; ok {
			floor = int(mdc.IntValue)
		}
		if floor > min {
			min = floor
		}
	}
	return quorum, min
}
