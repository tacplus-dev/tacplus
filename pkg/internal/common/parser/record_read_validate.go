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
//
// THE SECOND PAIR. (spec.record).c2s_read and (spec.record_read).c2s are the
// same control one boundary further out: not "you may read my rows" but "you may
// DISCLOSE my rows to a person". Both halves are checked here rather than in a
// second function, because the two pairs share every lookup and because a c2s
// grant is only meaningful on top of an s2s one — the c2s handler's data path IS
// the s2s reader, so the pairs cannot be validated independently without one of
// them accepting a surface that cannot execute.
//
// Four refusals belong to the c2s pair alone, each for a reason the s2s pair does
// not have:
//
//   - ["*"] IS REFUSED. On s2s_read the wildcard says "any authenticated peer in
//     the fleet", which is broad but sayable. "Any service may show my rows to
//     any of its users" is not a statement anyone means to make.
//   - c2s_read WITHOUT s2s_read for the same service is refused, in both
//     directions, for the reason above: there would be no reader to disclose
//     through.
//   - C2S_SCOPE_PRINCIPAL WITH NO RESOLVABLE PRINCIPAL CRITERION is refused
//     rather than degraded to a tenant-wide read. This is the same stance
//     javaRecordPrincipalUnenforceable already takes, and it is load-bearing:
//     on ScottyActivity and AccountingRecord the declared `owner` is the
//     PUBLISHING SERVICE, so a surface that silently fell back to it would show
//     a person every row in their tenant while looking like it showed them their
//     own.
//   - AN UNQUERYABLE (spec.store_namespace) FIELD is refused. The tenant pin is
//     a criterion on the generated <T>Filter, and the record store derives which
//     criteria it evaluates from the record's own (spec.query_field)
//     annotations. A namespace field carrying none produces a filter the store
//     silently ignores — a tenant gate that is present, generated, and inert,
//     leaving the crashing egress backstop as the only boundary. Refused loudly
//     instead.
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

// recordC2SReadGrant is one record type's declared DISCLOSURE audience: the
// services allowed to re-expose its rows to their own end users. No wildcard
// field, because ["*"] is refused outright rather than carried.
type recordC2SReadGrant struct {
	fqn          string
	authServices []string
}

// validateRecordReadAnnotations is the file-list form of
// ValidateRecordReadAnnotations; fds must already be in a deterministic order so
// the error text is stable.
func validateRecordReadAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)

	// Every (spec.record) in the fleet, and the read grant each declares.
	records := make(map[string]bool)
	// recordMsg keeps the descriptor for every record, because the c2s checks
	// below ask questions about the record's FIELDS (which one names the owning
	// principal, whether the namespace field is queryable) and not only about the
	// grant it declares.
	recordMsg := make(map[string]*MessageDescriptor)
	grants := make(map[string]*recordReadGrant)
	c2sGrants := make(map[string]*recordC2SReadGrant)
	var errs []string

	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			fqn := fd.Package + "." + msg.Name
			records[fqn] = true
			recordMsg[fqn] = msg

			// The DISCLOSURE grant, collected before the read grant because its
			// refusals stand on their own: a c2s_read that names nobody, or names
			// everybody, is wrong regardless of what s2s_read says.
			if cr, ok := msg.Annotations["record_c2s_read"]; ok && cr.BoolValue {
				switch {
				case len(cr.StringSliceValue) == 0:
					errs = append(errs, fmt.Sprintf(
						"%s: (spec.record).c2s_read declares no auth_services; the record's client-facing read surface would be emitted for nobody "+
							"(name the disclosing services, or remove the block)",
						fqn))
				case containsWildcard(cr.StringSliceValue):
					errs = append(errs, fmt.Sprintf(
						"%s: (spec.record).c2s_read may not use [\"*\"]. On s2s_read the wildcard means \"any authenticated peer in the fleet\"; "+
							"here it would mean \"any service may show my rows to any of its users\", which is not a statement a record owner makes "+
							"(name the disclosing services explicitly)",
						fqn))
				default:
					c2sGrants[fqn] = &recordC2SReadGrant{fqn: fqn, authServices: cr.StringSliceValue}
				}
			}

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
					"%s: (spec.record).s2s_read declares a read audience but no (spec.trait) hosts a record query plane, so nothing would enforce it%s",
					fqn, noRecordQueryPlaneHint))
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
	c2sUsed := make(map[string]bool)
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
		for _, decl := range fd.RecordReads {
			rr := decl.Record
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
			if decl.C2S != nil {
				c2sUsed[rr] = true
				errs = append(errs, validateRecordReadC2S(reader, rr, decl.C2S, c2sGrants[rr], recordMsg[rr])...)
			}
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

	// The same rule for the DISCLOSURE grant, with no wildcard exemption because
	// there is no wildcard: a c2s_read naming services none of which asked to
	// disclose is a standing permission to re-expose the record to end users that
	// nothing exercises, which is the worse of the two unused-grant shapes.
	c2sUnused := make([]string, 0, len(c2sGrants))
	for fqn, g := range c2sGrants {
		if c2sUsed[fqn] {
			continue
		}
		c2sUnused = append(c2sUnused, fmt.Sprintf(
			"%s: (spec.record).c2s_read grants disclosure to [%s], but no service in this build declares "+
				"(spec.record_read) = {record: %q, c2s: {...}} — an unexercised disclosure grant says this record's rows "+
				"may be shown to end users while nothing shows them "+
				"(add the c2s block to the reading service's (spec.record_read), or drop the grant)",
			fqn, strings.Join(g.authServices, ", "), fqn))
	}
	sort.Strings(c2sUnused)
	errs = append(errs, c2sUnused...)

	if len(errs) > 0 {
		return fmt.Errorf("record read annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// containsWildcard reports whether an auth_services list carries the "*" entry.
func containsWildcard(list []string) bool {
	for _, a := range list {
		if a == "*" {
			return true
		}
	}
	return false
}

// validateRecordReadC2S checks one reader's (spec.record_read).c2s declaration
// against the record it names.
//
// Everything here is a refusal of a surface that would GENERATE but not MEAN
// what it says. None of it can be deferred to runtime: a scope that resolves to
// no criterion, or a tenant pin the store will not evaluate, produces a
// client-facing read that returns other people's rows while looking correct.
func validateRecordReadC2S(reader, fqn string, use *RecordReadC2S, grant *recordC2SReadGrant, msg *MessageDescriptor) []string {
	var errs []string

	// The GRANT half. A missing one is refused here rather than by the
	// unused-grant sweep, so the error names the reader that wanted it.
	if grant == nil {
		errs = append(errs, fmt.Sprintf(
			"%s: (spec.record_read) asks for a client-facing read of %q, but that record declares no (spec.record).c2s_read "+
				"granting anyone disclosure (add c2s_read: {auth_services: [%q]} to %s). c2s_read is a SEPARATE grant from "+
				"s2s_read on purpose: reading a record type to make an internal decision and showing it to a person are "+
				"different rights, and the record owner decides the second one",
			reader, fqn, reader, fqn))
	} else {
		granted := false
		for _, a := range grant.authServices {
			if a == reader {
				granted = true
				break
			}
		}
		if !granted {
			errs = append(errs, fmt.Sprintf(
				"%s: (spec.record_read) asks for a client-facing read of %q, but that record's (spec.record).c2s_read grants "+
					"disclosure to [%s] and not to %q",
				reader, fqn, strings.Join(grant.authServices, ", "), reader))
		}
	}

	// The SCOPE. C2S_SCOPE_NAMESPACE is declared in the enum but not implemented,
	// and says so — the same stance RESIDENCY_COLD takes, so the value cannot be
	// invented a second time with a different meaning while it waits.
	switch use.Scope {
	case "C2S_SCOPE_PRINCIPAL":
		errs = append(errs, validateC2SPrincipalScope(reader, fqn, msg)...)
	case "C2S_SCOPE_NAMESPACE":
		errs = append(errs, fmt.Sprintf(
			"%s: (spec.record_read).c2s on %q declares scope C2S_SCOPE_NAMESPACE, which is declared but NOT IMPLEMENTED. "+
				"It means \"every row in the caller's tenant\" — a real shape, but a disclosure decision with no consumer "+
				"today, so the generator refuses it rather than guessing at it (use C2S_SCOPE_PRINCIPAL)",
			reader, fqn))
	default:
		errs = append(errs, fmt.Sprintf(
			"%s: (spec.record_read).c2s on %q declares an unknown scope %q", reader, fqn, use.Scope))
	}

	// The TENANT PIN. Independent of scope: every value of C2SScope pins the
	// caller's namespace, so every one of them needs a criterion to pin it into.
	errs = append(errs, validateC2SNamespaceCriterion(reader, fqn, msg)...)
	return errs
}

// validateC2SPrincipalScope refuses C2S_SCOPE_PRINCIPAL on a record whose owning
// principal cannot become an EQ criterion on the generated <T>Filter.
//
// Resolution order is the egress row filter's, exactly: (spec.store_principal)
// on a field first, (spec.record).owner as the fallback. The extra condition —
// that the resolved field is ALSO an EQ (spec.query_field) of string type — is
// what makes it expressible as a predicate rather than only as an after-the-fact
// row drop, and it is the whole reason this is a generation failure. On
// ScottyActivity and AccountingRecord the declared owner is the PUBLISHING
// SERVICE; a surface that quietly fell back to filtering on it would hand a
// person every row in their tenant.
func validateC2SPrincipalScope(reader, fqn string, msg *MessageDescriptor) []string {
	if msg == nil {
		return nil
	}
	field := ""
	via := ""
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_principal"].BoolValue {
			field = msg.Fields[i].Name
			via = "(spec.store_principal)"
			break
		}
	}
	if field == "" {
		field = msg.Annotations["record_owner"].StringValue
		via = "(spec.record).owner"
	}
	if field == "" {
		return []string{fmt.Sprintf(
			"%s: (spec.record_read).c2s on %q declares scope C2S_SCOPE_PRINCIPAL, but %s names no owning principal at all "+
				"(no field carries (spec.store_principal) and (spec.record).owner is unset), so there is no row to call the "+
				"caller's own",
			reader, fqn, fqn)}
	}
	if !hasEqStringQueryField(msg, field) {
		return []string{fmt.Sprintf(
			"%s: (spec.record_read).c2s on %q declares scope C2S_SCOPE_PRINCIPAL, and %s resolves the owning principal to "+
				"field %q — but that field is not an EQ string (spec.query_field), so it has no criterion on %sFilter to pin "+
				"the caller's subject into. Give it (spec.query_field) = {op: QUERY_OP_EQ}, or name a different principal "+
				"field. Generation fails here rather than falling back to a tenant-wide read, which would show a person "+
				"every row in their tenant while looking like it showed them their own",
			reader, fqn, via, field, msg.Name)}
	}
	return nil
}

// validateC2SNamespaceCriterion refuses a client-facing read over a record whose
// (spec.store_namespace) field is not an EQ criterion on the generated filter.
//
// The tenant gate is a PIN, applied before the store is asked, and a pin needs a
// criterion. It is not enough for the criterion to exist in the emitted <T>Filter
// message: the record store derives which criteria it actually evaluates from the
// record's own (spec.query_field) annotations, so an unannotated field yields a
// filter the store accepts and ignores. That failure is silent, and what it
// leaves behind is a tenant boundary enforced only by the crashing egress
// backstop — the one arrangement the design rules out, because it makes a
// cross-tenant read a client-triggerable process abort instead of an empty page.
func validateC2SNamespaceCriterion(reader, fqn string, msg *MessageDescriptor) []string {
	if msg == nil {
		return nil
	}
	field := ""
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_namespace"].BoolValue {
			field = msg.Fields[i].Name
			break
		}
	}
	if field == "" {
		// ValidateRecordNamespaceAnnotations has already refused this build with a
		// message naming the missing field; a second one here would only be worse.
		return nil
	}
	if !hasEqStringQueryField(msg, field) {
		return []string{fmt.Sprintf(
			"%s: (spec.record_read).c2s on %q needs %s's (spec.store_namespace) field %q to be an EQ string "+
				"(spec.query_field), and it is not. The tenant gate is pinned into the query predicate BEFORE the store is "+
				"asked, and the store evaluates only the criteria the record annotates; without one the pin is generated and "+
				"silently ignored, leaving the aborting egress backstop as the only tenant boundary. "+
				"Add (spec.query_field) = {op: QUERY_OP_EQ} to %s. The field stays uncallable by clients — the emitted "+
				"request messages carry no namespace field at all",
			reader, fqn, fqn, field, field)}
	}
	return nil
}

// hasEqStringQueryField reports whether a record field carries an EQ
// (spec.query_field) and is a plain string — the shape a pinned criterion needs.
// A RANGE criterion cannot express "equal to me", and a non-string field cannot
// carry a subject or a namespace.
func hasEqStringQueryField(msg *MessageDescriptor, name string) bool {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != name {
			continue
		}
		if f.IsRepeated || f.IsMap || f.Type != "string" {
			return false
		}
		qf, ok := f.Annotations["query_field"]
		return ok && qf.StringValue != "range"
	}
	return false
}

// recordReadC2SSupportedLanguages are the targets whose generated service
// wrapper carries BOTH record egress row filters — (spec.store_principal) and
// (spec.store_namespace) — on a c2s method returning record rows.
//
// Rust has them in rust/types/principal_gen.go and namespace_gen.go; Java has
// them in java/types/recordprincipal_gen.go and recordnamespace_gen.go, and both
// are general to any c2s method whose response carries record rows rather than
// being specific to the record store.
//
// GO HAS NEITHER. go/types/servicewrapper.go carries no principal or namespace
// egress seam at all; the only place the Go generator has one is
// go/types/mcp_dispatch_gen.go. A Go service declaring (spec.record_read).c2s
// would therefore ship a client-facing read surface with no row filter and no
// tenant backstop behind the pin — the one failure this plane exists to make
// impossible. Refused by name rather than emitted.
//
// This is a PRE-EXISTING hole in the Go generator, not one this plane opens: any
// Go service today with a c2s RPC returning (spec.record) rows has the same gap.
// Closing it is real work and belongs to whoever first needs a Go reader.
var recordReadC2SSupportedLanguages = map[string]bool{
	"rust": true,
	"java": true,
}

// ValidateRecordReadC2SLanguageSupport refuses a build that would emit the
// client-facing record read plane for a target language whose service wrapper
// has no record egress row filters.
//
// Language-scoped, so it lives beside ValidateRedactLanguageSupport in the
// per-language leg of the build rather than in the fleet-wide annotation sweep:
// the SAME protos are legal for a Rust reader and refused for a Go one, and only
// the build knows which is being produced.
func ValidateRecordReadC2SLanguageSupport(dp *DescriptorParser, selfPackage, language string) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	fds := make([]*FileDescriptor, 0, len(all))
	for _, fd := range all {
		fds = append(fds, fd)
	}
	return validateRecordReadC2SLanguageSupport(fds, selfPackage, language)
}

// validateRecordReadC2SLanguageSupport is the file-list form, so the refusal has
// a protoc-free unit pin.
func validateRecordReadC2SLanguageSupport(fds []*FileDescriptor, selfPackage, language string) error {
	if recordReadC2SSupportedLanguages[language] {
		return nil
	}
	var offenders []string
	for _, fd := range fds {
		if fd.Package != selfPackage {
			continue
		}
		for _, rr := range fd.RecordReads {
			if rr.C2S != nil {
				offenders = append(offenders, rr.Record)
			}
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf(
		"%s declares (spec.record_read).c2s over [%s], but the client-facing record read plane is not implemented for "+
			"target language %q: the %s service wrapper carries neither the (spec.store_principal) row filter nor the "+
			"(spec.store_namespace) tenant backstop, so the emitted read surface would return other principals' and other "+
			"tenants' rows unguarded. Implement both egress filters for %q, or drop the c2s block and keep the s2s reader",
		selfPackage, strings.Join(offenders, ", "), language, language, language)
}
