// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package model

// DefaultsKey identifies a message across the whole parsed descriptor set by its
// package and simple name. Message names are only unique within a package, so a
// bare name cannot key a set that spans files.
func DefaultsKey(pkg, name string) string {
	return pkg + "\x00" + name
}

// ExpandDefaults grows direct, the set of messages that carry a default of their
// own (keyed by DefaultsKey), to every message that reaches one through a
// non-repeated, non-map message field. The field may name a type in the same
// package or in another one: a message whose field holds a type with defaults
// must apply them too, or its validation sees the zero values the defaults were
// meant to fill.
func ExpandDefaults(allFiles []ProtoFile, direct map[string]bool) map[string]bool {
	out := make(map[string]bool, len(direct))
	for key := range direct {
		out[key] = true
	}

	changed := true
	for changed {
		changed = false
		for _, pf := range allFiles {
			for _, msg := range pf.Messages {
				key := DefaultsKey(pf.Package, msg.Name)
				if out[key] {
					continue
				}
				for _, field := range msg.Fields {
					if !field.IsMessage || field.IsRepeated || field.IsMap {
						continue
					}
					fieldPkg := field.TypePackage
					if fieldPkg == "" {
						fieldPkg = pf.Package
					}
					if out[DefaultsKey(fieldPkg, field.Type)] {
						out[key] = true
						changed = true
						break
					}
				}
			}
		}
	}
	return out
}
