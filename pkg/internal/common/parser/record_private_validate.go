// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ValidateRecordPrivateAnnotations enforces the three invariants
// (spec.record).private already implies before its generated owner-pin lands
// (tracked separately, see decisions/identity-people-tenant-devices-private-2026-09-21.md
// §4): a private record must declare (spec.store_principal), must never declare
// s2s_read, and may declare s2s_subscribe only to name the record stores that
// ingest it.
//
// STORE_PRINCIPAL IS MANDATORY, NOT OPT-IN, ON A PRIVATE RECORD. Unlike a plain
// record, where store_principal is an author's opt-in filter, a private record's
// entire guarantee is the owner pin, and (spec.store_principal) is the field the
// pin binds to (see pkg/internal/rust/types/principal_gen.go's
// storePrincipalField / rustResponsePrincipalFilterBlock — the generated pin,
// once it lands, is this same lookup made to abort rather than filter on
// mismatch). A private record naming no such field has nothing for that pin to
// bind to, which is exactly the "record nobody remembered to annotate" failure
// mode (spec.store_namespace)'s own mandatory-field posture exists to catch —
// same doctrine, applied here because `private` is a stronger claim than
// store_namespace's tenant boundary and deserves at least the same floor.
//
// NO S2S_SUBSCRIBE EXCEPT TO A RECORD STORE. A subscriber to a record's raw
// topic is a SERVICE, not a per-request caller — there is no principal in scope
// for a stream handler to pin against. Declaring both means every row of every
// owner ever written streams to any authorized peer unfiltered, which is exactly
// the fleet-wide
//
// The one subscriber that is not a peer reading someone else's rows is the
// RECORD STORE ingesting them: a service declaring (spec.record_store) is where
// a record's rows are persisted, and without a subscribe grant it is wired to
// the topic by collectStoreSourceFQNs and then fail-closed-denied on it (see
// spec.proto's "the record store that consumes every record topic is never in
// the owning package" note), so a private record could never legally be stored
// at all. This check therefore inspects the grant's CONTENTS rather than its
// presence: an s2s_subscribe is permitted if and only if EVERY name in
// auth_services resolves, in this build, to a service declaring
// (spec.record_store). auth_services: ["*"] and auth_disabled stay refused
// unconditionally — "any authenticated service" is exactly the fleet-wide
// broadcast private exists to prevent — and so does a grant naming nobody,
// which would otherwise satisfy "every name" vacuously. No new annotation: the
// grant every other stored record already declares IS the store designation
// (see decisions/private-record-store-subscribe-exemption.md).
//
// NO S2S_READ. Same reasoning one surface over. The record query plane's S2S
// half is peer-facing: the caller is a SERVICE identity, so there is no
// principal for the owner pin to bind to and the only gate the wrapper
// generators can apply is the (spec.store_namespace) tenant backstop. A private
// record's rows read through it are every principal's rows in the tenant,
// handed to every service the grant names. record_map.go therefore emits no S2S
// read RPC for a private type at all, and this is the paired half of that skip:
// the declaration fails here rather than being accepted and generating nothing.
// c2s_read falls with it, because the c2s reader's data path IS the s2s reader
// (see ValidateRecordReadAnnotations), and so does (spec.record_read), whose
// grant half is the s2s_read this refuses.
//
// s2s stays declarable: that is the WRITE topic, not a read. The read surface a
// private record keeps is the plane's C2S half, which carries a user token — the
// one identity the owner pin can bind to.
func ValidateRecordPrivateAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	recordStores := make(map[string]bool)
	for _, path := range paths {
		if fd := all[path]; fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}

	var errs []string
	for _, path := range paths {
		fd := all[path]
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record_private"]; !ok || !ann.BoolValue {
				continue
			}
			hasStorePrincipal := false
			for fi := range msg.Fields {
				if msg.Fields[fi].Annotations["store_principal"].BoolValue {
					hasStorePrincipal = true
					break
				}
			}
			if !hasStorePrincipal {
				errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true but it declares no "+
					"(spec.store_principal) field; give the string field holding the row's owner (spec.store_principal) "+
					"= true so the generated pin has a field to bind the caller's identity against",
					fd.Package, msg.Name, path))
			}
			if msg.Annotations["record_s2s_read"].BoolValue {
				errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true and it declares "+
					"s2s_read; the record query plane emits no peer-facing read for a private type, because a peer "+
					"calls with its own service identity and the owner pin has no caller principal to compare rows "+
					"against — the grant would authorize nothing. Read the type through the plane's c2s half, which "+
					"carries the user token the pin binds to; drop s2s_read (and with it c2s_read, which cannot "+
					"execute without it) or drop private",
					fd.Package, msg.Name, path))
			}
			surface, ok := msg.Annotations["event_surface"]
			if !ok || len(surface.EventPublishEntries) == 0 {
				continue
			}
			if sub := surface.EventPublishEntries[0].MethodS2SSubscribe; sub != nil {
				if open := privateSubscribeOpenGrant(sub); open != "" {
					errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true and it declares "+
						"s2s_subscribe %s; a subscriber to a record's topic is a service with no per-request caller "+
						"to pin a private row's owner against, so this is a fleet-wide broadcast of the exact data "+
						"private exists to keep to one principal — name the (spec.record_store) services that "+
						"ingest this record, drop s2s_subscribe, or drop private",
						fd.Package, msg.Name, path, open))
				} else if notStores := nonRecordStoreGrantees(sub.AuthServices, recordStores); len(notStores) > 0 {
					errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true and its "+
						"s2s_subscribe names %s, which declares no (spec.record_store) in this build; the only "+
						"subscriber a private record may authorize is a record store ingesting its rows, because "+
						"every other subscriber is a service with no per-request caller to pin a private row's "+
						"owner against — name only record stores, drop s2s_subscribe, or drop private%s",
						fd.Package, msg.Name, path, strings.Join(notStores, ", "), noRecordQueryPlaneHint))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("record private annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// privateSubscribeOpenGrant reports, as the text to quote back at the author,
// whether a private record's s2s_subscribe is an OPEN grant — one that names no
// specific record store and so can never be checked against the set of declared
// stores. auth_disabled and auth_services: ["*"] are the two open shapes the
// proto can spell; a grant naming nobody at all is the third, and it is refused
// here rather than allowed to satisfy the every-name check vacuously. Returns ""
// when the grant is a concrete list of service names.
func privateSubscribeOpenGrant(sub *model.EventBusMethodS2S) string {
	if sub.AuthDisabled {
		return "auth_disabled: true"
	}
	for _, name := range sub.AuthServices {
		if name == "*" {
			return `auth_services: ["*"]`
		}
	}
	if len(sub.AuthServices) == 0 {
		return "naming no service"
	}
	return ""
}

// nonRecordStoreGrantees returns the named services that do NOT declare
// (spec.record_store) in this build, in declaration order. Empty means every
// name is a declared record store and the grant is a store-ingest grant.
func nonRecordStoreGrantees(authServices []string, recordStores map[string]bool) []string {
	var out []string
	for _, name := range authServices {
		if !recordStores[name] {
			out = append(out, name)
		}
	}
	return out
}
