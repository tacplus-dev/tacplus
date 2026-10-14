// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRecordNamespaceAnnotations enforces the one invariant that makes the
// namespace isolation backstop a backstop: EVERY (spec.record)-annotated message
// declares the field holding the row's owning namespace.
//
// This is deliberately unlike (spec.store_principal), which an author opts into
// per record. An opt-in isolation guard protects exactly the records somebody
// remembered to annotate, and the record that leaks is by definition the one
// nobody thought about. So the marker is mandatory and its absence FAILS
// GENERATION — the same posture the record query plane already takes for a
// trait declaring no auth_services (collectRecordQueryPlanes): a half-specified
// security surface is not emitted at all.
//
// The shape of the field itself (singular string, at most one per message) is
// enforced by ValidatePrincipalAnnotations alongside the principal pair.
func ValidateRecordNamespaceAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs []string
	for _, path := range paths {
		fd := all[path]
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			found := false
			for fi := range msg.Fields {
				if msg.Fields[fi].Annotations["store_namespace"].BoolValue {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Sprintf("%s.%s (%s): a (spec.record) declares no (spec.store_namespace) field; "+
					"give the string field holding the row's owning namespace (spec.store_namespace) = true so the "+
					"generated wrapper can hold the tenant boundary at egress",
					fd.Package, msg.Name, path))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("record namespace annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
