// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateResidencyAnnotations enforces the invariants (spec.record).residency,
// (spec.record).mutability and (spec.record).retention.forever need in order to
// mean anything. It is shaped exactly like ValidateMinGroupSizeAnnotations:
// deterministic file ordering, errors accumulated, one aggregated error under a
// header.
//
//	VAL-1  residency.window = RESIDENCY_COLD                       -> fail
//	       Reserved, not implemented. Full text in residencyColdRefusal.
//	VAL-2  residency block present with RESIDENCY_UNSPECIFIED       -> fail
//	       An author who wrote the block meant something; silently defaulting to
//	       HOT is the fail-silent class this tree closes.
//	VAL-3  RESIDENCY_WARM without exactly one int64 (spec.store_epoch)
//	       field carrying (spec.query_field) = {op: QUERY_OP_RANGE} -> fail
//	       The window has nothing to measure and the disk leg has nothing to
//	       filter on. Same "the annotation must be readable by something or it is
//	       decoration" rule ValidateMinGroupSizeAnnotations applies.
//	VAL-4  MUTABILITY_APPEND_ONLY + retention.forever + duration    -> fail
//	       A dataset that declares it is never aged out cannot also carry a
//	       retention knob. Deliberately does NOT refuse residency.window_seconds:
//	       eviction from memory at window age is cache maintenance, never data
//	       loss, so a memory window on a forever dataset destroys nothing.
//	VAL-5  residency.window_seconds > 0 on anything but WARM        -> fail
//	       A knob no generator reads — the same rule the statedb tuning family
//	       gets (see statedb_unimplemented.go).
//	VAL-6  residency or mutability on a message with no record query
//	       plane in the fleet                                       -> fail
//	       Verbatim the second min_group_size invariant; hasRecordQueryPlane is
//	       shared, not restated.
//
// One rule that is NOT here, because it is not a residency rule: retention
// declaring forever:true AND duration_seconds > 0 is a self-contradiction
// regardless of mutability, and is rejected as VAL-4b below on every record.
func ValidateResidencyAnnotations(dp *DescriptorParser) error {
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
	return validateResidencyAnnotations(fds)
}

// residencyColdRefusal is the VAL-1 message. Three properties it must keep:
// it says "RESERVED and NOT IMPLEMENTED" in those words; it says what would
// otherwise happen silently (the fail-silent framing every other refusal in this
// tree uses); and it names both usable presets, so the author's next edit is
// obvious rather than merely forbidden.
func residencyColdRefusal(pkg, msg string) string {
	return fmt.Sprintf(
		"%s.%s: (spec.record).residency.window is RESIDENCY_COLD, which is RESERVED and "+
			"NOT IMPLEMENTED. tacplus generates no cold tier and stratum has no cold path, so the "+
			"declaration would silently store the record hot — the intent dropped with nothing "+
			"failing. Use RESIDENCY_WARM (in memory for a window, complete and queryable on disk "+
			"beneath it) or RESIDENCY_HOT (in memory forever, the default). Cold is reserved for a "+
			"future high-volume case that must not be held in memory at all.",
		pkg, msg)
}

// storeEpochFields returns the names of msg's (spec.store_epoch) fields that are
// well-formed for the residency window (int64, RANGE-queryable), and the names of
// those that are not, with the reason. VAL-3 needs both halves: "no epoch field"
// and "an epoch field the disk leg cannot filter on" are different author errors
// and get different sentences.
func storeEpochFields(msg *MessageDescriptor) (ok []string, malformed []string) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.Annotations["store_epoch"].BoolValue {
			continue
		}
		switch {
		case f.TypeKind != "scalar" || f.Type != "int64":
			malformed = append(malformed, fmt.Sprintf("%s is %s, not int64", f.Name, f.Type))
		case f.IsRepeated:
			malformed = append(malformed, fmt.Sprintf("%s is repeated; a row has one store epoch", f.Name))
		case f.Annotations["query_field"].StringValue != "range":
			malformed = append(malformed, fmt.Sprintf(
				"%s carries no (spec.query_field) = {op: QUERY_OP_RANGE}", f.Name))
		default:
			ok = append(ok, f.Name)
		}
	}
	return ok, malformed
}

// validateResidencyAnnotations is the file-list form of
// ValidateResidencyAnnotations; fds must already be in a deterministic order so
// the error text is stable.
func validateResidencyAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)

	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			residency := msg.Annotations["record_residency"].RecordResidency
			mutability := msg.Annotations["record_mutability"].StringValue
			retention := msg.Annotations["record_retention"].RecordRetention

			// VAL-4b: a self-contradicting retention block, independent of
			// residency and mutability. Checked on every record so the
			// contradiction cannot hide behind an undeclared mutability.
			if retention != nil && retention.Forever && retention.DurationSeconds > 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).retention declares forever:true AND duration_seconds:%d; "+
						"a row cannot be both never aged out and aged out after %[3]d seconds",
					fd.Package, msg.Name, retention.DurationSeconds))
			}

			if residency == nil && mutability == "" {
				continue
			}

			// VAL-6: something must read it. Stated first, because when no plane
			// exists none of the rules below have a consumer either.
			if !planeExists {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record) declares residency/mutability but no (spec.trait) hosts a "+
						"record query plane, so nothing would read it",
					fd.Package, msg.Name))
			}

			// VAL-4: acceptance item 9. An append-only dataset declaring
			// "forever" refuses the retention knob. window_seconds is
			// deliberately NOT refused — evicting a forever dataset's rows from
			// memory destroys nothing.
			if mutability == "MUTABILITY_APPEND_ONLY" && retention != nil && retention.Forever &&
				(retention.DurationSeconds > 0 || retention.TimestampField != "") {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record) is MUTABILITY_APPEND_ONLY with retention.forever:true and also "+
						"declares a retention knob (timestamp_field:%q duration_seconds:%d); an append-only "+
						"dataset that is never aged out has nothing for the retention sweep to measure or "+
						"delete. Drop the knob, or drop forever:true and say how long rows live",
					fd.Package, msg.Name, retention.TimestampField, retention.DurationSeconds))
			}

			if residency == nil {
				continue
			}
			switch residency.Window {
			case "RESIDENCY_COLD":
				// VAL-1.
				errs = append(errs, residencyColdRefusal(fd.Package, msg.Name))
			case "RESIDENCY_UNSPECIFIED", "":
				// VAL-2.
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).residency is declared but its window is RESIDENCY_UNSPECIFIED; "+
						"an author who wrote the block meant something. Say RESIDENCY_HOT (in memory "+
						"forever, and also what an absent residency block means) or RESIDENCY_WARM "+
						"(in memory for a window, complete on disk beneath it)",
					fd.Package, msg.Name))
			case "RESIDENCY_WARM":
				// VAL-3.
				okFields, malformed := storeEpochFields(msg)
				switch {
				case len(okFields)+len(malformed) == 0:
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.record).residency is RESIDENCY_WARM but the type declares no "+
							"(spec.store_epoch) field; the residency window has no clock to measure "+
							"against and a disk leg would have nothing to filter on. Add "+
							"`int64 store_epoch = N [(spec.store_epoch) = true, (spec.query_field) = "+
							"{op: QUERY_OP_RANGE}];`",
						fd.Package, msg.Name))
				case len(malformed) > 0:
					sort.Strings(malformed)
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.record).residency is RESIDENCY_WARM and its (spec.store_epoch) "+
							"field is unusable: %s. The store epoch is Unix milliseconds and is the "+
							"predicate the warm partition ranges on",
						fd.Package, msg.Name, strings.Join(malformed, "; ")))
				case len(okFields) > 1:
					sort.Strings(okFields)
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.record) declares %d (spec.store_epoch) fields (%s); a row has "+
							"exactly one store epoch, and which one the window measures against is not "+
							"decidable here",
						fd.Package, msg.Name, len(okFields), strings.Join(okFields, ", ")))
				}
			}

			// VAL-5: window_seconds is a WARM-only knob.
			if residency.WindowSeconds != 0 && residency.Window != "RESIDENCY_WARM" {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).residency sets window_seconds:%d on %s; the window override is "+
						"read for RESIDENCY_WARM only (HOT is unbounded, COLD is never resident), so "+
						"nothing would honour it",
					fd.Package, msg.Name, residency.WindowSeconds, residency.Window))
			}
			if residency.WindowSeconds < 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).residency sets window_seconds:%d; a residency window cannot be "+
						"negative (0 = use the configured default)",
					fd.Package, msg.Name, residency.WindowSeconds))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("residency annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
