// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"mime"
	"sort"
)

// ValidateVolumeGroups enforces the blob-store volume group contract at
// generation time.
//
// A volume group is declared by its owning service with (spec.volume_group) and
// never created at runtime: the operator renders every declared group into the
// blob store's config from registry.json, and an upload naming an unknown group
// is refused. Two consequences are checked here, fleet-wide, because neither is
// visible from a single proto file:
//
//   - Group names must be unique across the whole fleet. The blob store keys
//     its layout on <group>/<ns>/<sha256>, so two services declaring the same
//     name would silently share one store of bytes and one quota namespace.
//     Generation FAILS naming both declarations rather than emitting a config
//     whose last writer wins.
//   - Every content_types entry must be a well-formed media type. This is a
//     SYNTAX check only (mime.ParseMediaType); the runtime service still sniffs
//     magic bytes per upload. A typo caught here is a typo that would otherwise
//     become an allow-list entry no upload can ever match.
//
// Declaring no volume group is fine — the annotation is optional.
func ValidateVolumeGroups(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}

	type declaration struct {
		file    string
		service string
	}

	// Iterate files in a stable order so the error names the same pair of
	// colliding declarations on every run.
	files := dp.GetAllFiles()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	seen := make(map[string]declaration)
	for _, path := range paths {
		fd := files[path]
		for si := range fd.Services {
			svc := &fd.Services[si]
			for _, vg := range svc.VolumeGroups {
				here := declaration{file: fd.Path, service: fd.Package + "." + svc.Name}

				if vg.Name == "" {
					return fmt.Errorf(
						"volume group error: %s (%s) declares a (spec.volume_group) with an "+
							"empty name — every group needs a name unique across the fleet",
						here.service, here.file)
				}
				if prev, ok := seen[vg.Name]; ok {
					return fmt.Errorf(
						"volume group error: duplicate volume group name %q declared by %s (%s) "+
							"and %s (%s) — group names must be unique across the whole fleet, "+
							"because the blob store keys its layout and its per-tenant quota on "+
							"the group name alone",
						vg.Name, prev.service, prev.file, here.service, here.file)
				}
				seen[vg.Name] = here

				for _, ct := range vg.ContentTypes {
					if _, _, err := mime.ParseMediaType(ct); err != nil {
						return fmt.Errorf(
							"volume group error: %s (%s) volume group %q: content_types entry %q "+
								"is not a well-formed media type (%v); each entry must be a media "+
								"type such as \"application/pdf\"",
							here.service, here.file, vg.Name, ct, err)
					}
				}
			}
		}
	}

	return nil
}
