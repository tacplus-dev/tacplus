// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// The generation end of the (spec.ejectionable) field marker.
//
// (spec.record).residency declares the memory-residency window for a record
// TYPE. (spec.ejectionable) names the field whose per-row value decides whether
// that window is consulted for a given ROW. The two are orthogonal exactly as
// mutability is orthogonal to residency: the type says how long, each row says
// whether the window applies to it at all.
//
// UNSET IS NEVER-EVICT, which is what RESIDENCY_HOT means and what every record
// does today, so the marker is additive in both directions: a record declaring
// no ejectionable field keeps its type-level window unchanged, and a record
// declaring one keeps the row resident for every row that leaves the field at
// EJECTIONABLE_UNSPECIFIED. The safe direction for a marker an author forgets
// is "keep the row"; the alternative silently drops data.
//
// This validator is shaped like ValidateResidencyAnnotations: deterministic
// file ordering, errors accumulated, one aggregated error under a header.
//
//	EJ-1  (spec.ejectionable) on a message that is not a (spec.record) -> fail
//	      Only a stored record has a residency window, so only a record has
//	      something for the gate to gate.
//	EJ-2  the marked field is not a non-repeated spec.Ejectionable       -> fail
//	      The gate is read as a tri-state enum; any other type has no
//	      EJECTIONABLE_UNSPECIFIED to mean "never evict".
//	EJ-3  more than one marked field on one message                      -> fail
//	      A row has one answer, and which field carries it is not decidable
//	      here. Verbatim the shape VAL-3 takes for two (spec.store_epoch)s.
//	EJ-4  the record declares no (spec.record).residency                 -> fail
//	      A record with no window has no eviction check to gate, so the
//	      declaration would be decoration. Same posture as residency VAL-6,
//	      which fails rather than silently accept an annotation nothing reads.

// ValidateEjectionableAnnotations enforces the invariants (spec.ejectionable)
// needs in order to mean anything.
func ValidateEjectionableAnnotations(dp *DescriptorParser) error {
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
	return validateEjectionableAnnotations(fds)
}

// ejectionableFields returns the names of msg's (spec.ejectionable) fields that
// are well-formed (a non-repeated spec.Ejectionable enum), and the names of
// those that are not, with the reason. EJ-2 and EJ-3 need both halves: "the
// wrong type" and "two of them" are different author errors.
func ejectionableFields(msg *MessageDescriptor) (ok []string, malformed []string) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.Annotations["ejectionable"].BoolValue {
			continue
		}
		switch {
		case f.IsRepeated:
			malformed = append(malformed, fmt.Sprintf("%s is repeated; a row has one answer", f.Name))
		case f.TypeKind != "enum" || f.Type != "spec.Ejectionable":
			malformed = append(malformed, fmt.Sprintf(
				"%s is %s, not spec.Ejectionable", f.Name, f.Type))
		default:
			ok = append(ok, f.Name)
		}
	}
	return ok, malformed
}

// validateEjectionableAnnotations is the file-list form of
// ValidateEjectionableAnnotations; fds must already be in a deterministic order
// so the error text is stable.
func validateEjectionableAnnotations(fds []*FileDescriptor) error {
	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			okFields, malformed := ejectionableFields(msg)
			if len(okFields)+len(malformed) == 0 {
				continue
			}

			// EJ-1: stated first — when the message is not a record, none of
			// the rules below have a window to talk about.
			if ann, isRecord := msg.Annotations["record"]; !isRecord || !ann.BoolValue {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares a (spec.ejectionable) field but is not a (spec.record); only a "+
						"stored record has a residency window, so there is no eviction check to gate",
					fd.Package, msg.Name))
				continue
			}

			if len(malformed) > 0 {
				sort.Strings(malformed)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: its (spec.ejectionable) field is unusable: %s. The gate is read as a "+
						"tri-state enum, and EJECTIONABLE_UNSPECIFIED is what makes an unmarked row "+
						"never evict. Declare `spec.Ejectionable ejectionable = N "+
						"[(spec.ejectionable) = true];`",
					fd.Package, msg.Name, strings.Join(malformed, "; ")))
			}

			// EJ-3.
			if len(okFields) > 1 {
				sort.Strings(okFields)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares %d (spec.ejectionable) fields (%s); a row has exactly one answer "+
						"to whether the residency window applies to it, and which field carries it is "+
						"not decidable here",
					fd.Package, msg.Name, len(okFields), strings.Join(okFields, ", ")))
			}

			// EJ-4.
			if msg.Annotations["record_residency"].RecordResidency == nil {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares a (spec.ejectionable) field but the record declares no "+
						"(spec.record).residency; the gate has no eviction window to gate and nothing "+
						"would read it. Add `residency: {window: RESIDENCY_WARM}` to the (spec.record) "+
						"option, or drop the (spec.ejectionable) marker",
					fd.Package, msg.Name))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("ejectionable annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
