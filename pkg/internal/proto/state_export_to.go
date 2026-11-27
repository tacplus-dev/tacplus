// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// protoPackageName is a proto package: dot-separated identifiers.
var protoPackageName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// stateExportTo returns a state root's (spec.service_state).export_to: the
// packages allowed to import it, or nil when any package may.
func stateExportTo(msg *parser.MessageDescriptor) []string {
	return msg.Annotations["service_state_export_to"].StringSliceValue
}

// stateExportAllows reports whether importerPkg may import a root whose
// export_to is exportTo. An empty list is export's own meaning: any package.
func stateExportAllows(exportTo []string, importerPkg string) bool {
	return len(exportTo) == 0 || slices.Contains(exportTo, importerPkg)
}

// formatExportTo renders an export_to list as it is written in proto.
func formatExportTo(exportTo []string) string {
	quoted := make([]string, len(exportTo))
	for i, pkg := range exportTo {
		quoted[i] = fmt.Sprintf("%q", pkg)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// validateStateExportTo checks a state root's export_to on its own: it narrows
// an export, so it needs export: true; each entry is a distinct package other
// than the owner's (the owner holds RW); and the root has no topic boundary,
// since an exported topic root is a client-reachable event surface
// (stateServiceIsEventC2S) that export_to does not narrow. An entry naming a
// package absent from this build is accepted: the importer may be built
// elsewhere.
func validateStateExportTo(relPath, ownerPkg string, msg parser.MessageDescriptor) []string {
	exportTo := stateExportTo(&msg)
	if len(exportTo) == 0 {
		return nil
	}
	var errs []string
	if exp, ok := msg.Annotations["service_state_export"]; !ok || !exp.BoolValue {
		errs = append(errs, fmt.Sprintf(
			"%s: state root %s sets export_to without export: true — export_to narrows an export; add export: true",
			relPath, msg.Name))
	}
	seen := make(map[string]bool, len(exportTo))
	for _, pkg := range exportTo {
		switch {
		case !protoPackageName.MatchString(pkg):
			errs = append(errs, fmt.Sprintf(
				"%s: state root %s export_to entry %q is not a proto package name",
				relPath, msg.Name, pkg))
		case pkg == ownerPkg:
			errs = append(errs, fmt.Sprintf(
				"%s: state root %s export_to lists its own package %q — the owner holds RW on its state; remove it",
				relPath, msg.Name, pkg))
		case seen[pkg]:
			errs = append(errs, fmt.Sprintf(
				"%s: state root %s export_to lists %q twice",
				relPath, msg.Name, pkg))
		}
		seen[pkg] = true
	}
	for _, field := range msg.Fields {
		if field.Annotations["topic"].BoolValue {
			errs = append(errs, fmt.Sprintf(
				"%s: state root %s sets export_to on a state root with topic boundaries (%s) — "+
					"an exported topic root is a client-reachable event surface export_to cannot narrow; "+
					"put the restricted state in a root of its own",
				relPath, msg.Name, field.Name))
			break
		}
	}
	return errs
}
