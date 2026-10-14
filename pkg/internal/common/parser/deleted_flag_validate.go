// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// The generation end of the (spec.deleted_flag) field marker.
//
// The marker names the one bool field that records a row's soft-delete state.
// Deletion is an ordinary republish that sets it — the row's last write — and the
// generated query plane excludes flagged-true rows from List / Watch / Count /
// Sample by default. A flagged row older than
// (spec.record).residency.deleted_max_age_seconds is purged at the next sweep.
//
// ZERO MARKED FIELDS IS VALID. A record with no soft-delete state (an append-only
// type, say) simply has nothing to exclude, so this is additive: every record in
// the fleet today generates exactly what it generates now, and a record that
// gains the marker gains the exclusion.
//
// This validator is shaped like ValidateResidencyAnnotations: deterministic file
// ordering, errors accumulated, one aggregated error under a header. It replaced
// ValidateEjectionableAnnotations on 2026-09-20, whose EJ-1..EJ-3 are these rules
// and whose EJ-4 (a marker needs a residency block to gate) has no equivalent:
// soft-delete state is meaningful on its own.
//
//	DF-1  (spec.deleted_flag) on a message that is not a (spec.record) -> fail
//	      Only a stored record has a query plane to exclude rows from.
//	DF-2  the marked field is not a non-repeated bool                  -> fail
//	      The state is two-valued; a row is flagged or it is not.
//	DF-3  more than one marked field on one message                    -> fail
//	      A row has one soft-delete state, and which field carries it is not
//	      decidable here. Verbatim the shape residency VAL-3 takes for two
//	      (spec.store_epoch)s.

// ValidateDeletedFlagAnnotations enforces the invariants (spec.deleted_flag)
// needs in order to mean anything.
func ValidateDeletedFlagAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateDeletedFlagAnnotations(fds)
}

// DeletedFlagField returns msg's (spec.deleted_flag) field and true when it
// declares exactly one well-formed marker, or nil and false otherwise.
//
// This is the lookup the query/handler generators use to find the field they
// exclude on; it is not a second opinion. Well-formedness is enforced once, at
// parse time, by ValidateDeletedFlagAnnotations, so a malformed or duplicated
// marker never reaches generation — and if one somehow did, this answers "no
// marker", which generates the pre-marker query plane rather than a wrong one.
func DeletedFlagField(msg *MessageDescriptor) (*FieldDescriptor, bool) {
	var found *FieldDescriptor
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.Annotations["deleted_flag"].BoolValue {
			continue
		}
		if f.IsRepeated || f.TypeKind != "scalar" || f.Type != "bool" {
			return nil, false
		}
		if found != nil {
			return nil, false
		}
		found = f
	}
	return found, found != nil
}

// deletedFlagFields returns the names of msg's (spec.deleted_flag) fields that
// are well-formed (a non-repeated bool), and the names of those that are not,
// with the reason. DF-2 and DF-3 need both halves: "the wrong type" and "two of
// them" are different author errors.
func deletedFlagFields(msg *MessageDescriptor) (ok []string, malformed []string) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.Annotations["deleted_flag"].BoolValue {
			continue
		}
		switch {
		case f.IsRepeated:
			malformed = append(malformed, fmt.Sprintf("%s is repeated; a row has one soft-delete state", f.Name))
		case f.TypeKind != "scalar" || f.Type != "bool":
			malformed = append(malformed, fmt.Sprintf("%s is %s, not bool", f.Name, f.Type))
		default:
			ok = append(ok, f.Name)
		}
	}
	return ok, malformed
}

// validateDeletedFlagAnnotations is the file-list form of
// ValidateDeletedFlagAnnotations; fds must already be in a deterministic order so
// the error text is stable.
func validateDeletedFlagAnnotations(fds []*FileDescriptor) error {
	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			okFields, malformed := deletedFlagFields(msg)
			if len(okFields)+len(malformed) == 0 {
				continue
			}

			// DF-1: stated first — when the message is not a record, neither rule
			// below has a query plane to talk about.
			if ann, isRecord := msg.Annotations["record"]; !isRecord || !ann.BoolValue {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares a (spec.deleted_flag) field but is not a (spec.record); only a "+
						"stored record has a query plane, so there are no rows to exclude from one",
					fd.Package, msg.Name))
				continue
			}

			// DF-2.
			if len(malformed) > 0 {
				sort.Strings(malformed)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: its (spec.deleted_flag) field is unusable: %s. The soft-delete state is "+
						"two-valued — a row is flagged or it is not. Declare "+
						"`bool deleted = N [(spec.deleted_flag) = true];`",
					fd.Package, msg.Name, strings.Join(malformed, "; ")))
			}

			// DF-3.
			if len(okFields) > 1 {
				sort.Strings(okFields)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares %d (spec.deleted_flag) fields (%s); a row has exactly one "+
						"soft-delete state, and which field carries it is not decidable here",
					fd.Package, msg.Name, len(okFields), strings.Join(okFields, ", ")))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("deleted_flag annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
