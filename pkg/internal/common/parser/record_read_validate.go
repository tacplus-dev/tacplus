// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRecordReadAnnotations enforces the invariants the per-service
// own-record read mechanism needs to mean anything.
//
// The mechanism is a PAIRED CONTROL across two protos that no single author
// necessarily owns:
//
//	(spec.record).s2s_read on the RECORD    — the grant: who may read my rows
//	(spec.record_read)     on the READER    — the use:   I read that record
//
// Either half alone is a bug that this function refuses to let compile, and the
// two failures it prevents are not symmetric:
//
//   - USE WITHOUT GRANT generates a client that is denied at runtime. The reader
//     gets a typed reader on its service manager, calls it, and takes
//     PERMISSION_DENIED from the store — a failure that surfaces in production
//     rather than in a build, against a surface the author had every reason to
//     believe was wired up.
//   - GRANT WITHOUT USE is a standing authorization nobody exercises. It is not
//     a runtime failure, which is exactly what makes it worth failing the build
//     over: an unused read grant on a record type is invisible, survives the
//     deletion of whatever once used it, and widens the store's blast radius for
//     as long as nobody notices. This tree already fails generation for an
//     annotation nothing reads (ValidateMinGroupSizeAnnotations' "decoration"
//     rule); an unread GRANT is that same failure with a security consequence
//     attached.
//
// It also refuses the two locally-detectable nonsense declarations: a grant that
// names nobody, and a use that names something that is not a record.
func ValidateRecordReadAnnotations(dp *DescriptorParser) error {
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
	return validateRecordReadAnnotations(fds)
}

// recordReadGrant is one record type's declared read audience, keyed by the
// fully-qualified record name the reader side spells.
type recordReadGrant struct {
	fqn          string
	authServices []string
	wildcard     bool
}

// validateRecordReadAnnotations is the file-list form of
// ValidateRecordReadAnnotations; fds must already be in a deterministic order so
// the error text is stable.
func validateRecordReadAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)

	// Every (spec.record) in the fleet, and the read grant each declares.
	records := make(map[string]bool)
	grants := make(map[string]*recordReadGrant)
	var errs []string

	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			fqn := fd.Package + "." + msg.Name
			records[fqn] = true

			sr, ok := msg.Annotations["record_s2s_read"]
			if !ok || !sr.BoolValue {
				continue
			}
			// A declared-but-empty list is refused rather than folded back into
			// the trait-wide default. collectRecordQueryPlanes takes exactly this
			// posture on the trait's own auth_services, and for the same reason: a
			// surface callable by nobody is a declaration that did not mean what
			// it says, and silently substituting the fleet-wide list for it would
			// turn an attempt to NARROW access into the widest grant available.
			if len(sr.StringSliceValue) == 0 {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record).s2s_read declares no auth_services; the record's read surface would be callable by nobody "+
						"(name the reading services, or use [\"*\"] for any authenticated service, or remove the block to keep the trait-wide default)",
					fqn))
				continue
			}
			// The grant is enforced on the record query plane's S2S half, and that
			// plane exists only where a (spec.trait) declares a record store as its
			// host. Without one there is no surface to scope — the same "unread
			// annotation is a generation failure" rule min_group_size applies.
			if !planeExists {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record).s2s_read declares a read audience but no (spec.trait) hosts a record query plane, so nothing would enforce it",
					fqn))
				continue
			}
			g := &recordReadGrant{fqn: fqn, authServices: sr.StringSliceValue}
			for _, a := range g.authServices {
				if a == "*" {
					g.wildcard = true
				}
			}
			grants[fqn] = g
		}
	}

	// The USE half, and the grant it must be covered by.
	used := make(map[string]bool)
	for _, fd := range fds {
		if len(fd.RecordReads) == 0 {
			continue
		}
		reader := fd.ServiceName
		if reader == "" {
			// A proto with no service identity cannot be an authorized caller of
			// anything: auth_services names SERVICES. Refused here rather than
			// generating a reader whose identity no grant could ever match.
			errs = append(errs, fmt.Sprintf(
				"%s: declares (spec.record_read) but the file declares no service identity; only a service can be an authorized reader",
				fd.Package))
			continue
		}
		for _, rr := range fd.RecordReads {
			if rr == "" {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record_read) declares an empty record name", reader))
				continue
			}
			if !records[rr] {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record_read) names %q, which is not a (spec.record) message in this build",
					reader, rr))
				continue
			}
			used[rr] = true
			g, ok := grants[rr]
			if !ok {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record_read) names %q, but that record declares no (spec.record).s2s_read granting anyone read access "+
						"(add s2s_read: {auth_services: [%q]} to %s)",
					reader, rr, reader, rr))
				continue
			}
			if g.wildcard {
				continue
			}
			granted := false
			for _, a := range g.authServices {
				if a == reader {
					granted = true
					break
				}
			}
			if !granted {
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record_read) names %q, but that record's (spec.record).s2s_read grants read access to [%s] and not to %q "+
						"— the generated reader would be denied at runtime",
					reader, rr, strings.Join(g.authServices, ", "), reader))
			}
		}
	}

	// GRANT WITHOUT USE. A wildcard grant is exempt: ["*"] is a deliberate
	// statement that the reader set is open-ended and not enumerable here, so
	// there is no specific unused name to point at.
	unused := make([]string, 0, len(grants))
	for fqn, g := range grants {
		if g.wildcard || used[fqn] {
			continue
		}
		unused = append(unused, fmt.Sprintf(
			"%s: (spec.record).s2s_read grants read access to [%s], but no service in this build declares (spec.record_read) = {record: %q} "+
				"— an unexercised read grant widens the store's reach with nothing to show for it "+
				"(add the (spec.record_read) option to the reading service, or drop the grant)",
			fqn, strings.Join(g.authServices, ", "), fqn))
	}
	sort.Strings(unused)
	errs = append(errs, unused...)

	if len(errs) > 0 {
		return fmt.Errorf("record read annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
