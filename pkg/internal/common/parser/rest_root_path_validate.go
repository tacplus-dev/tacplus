// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRestRootPath enforces at generation time that a proto tree declares at
// most one distinct value for the REST_ROOT_PATH string constant.
//
// REST_ROOT_PATH is resolved once per tree by taking the first file that
// declares it, walking protoregistry.Files.RangeFiles — whose iteration order is
// documented as undefined and is, in practice, Go map order over filesByPath. So
// when two files declare REST_ROOT_PATH with different values, which one prefixes
// every generated REST route is decided by map iteration, not by anything the
// proto author wrote: the same tree can generate different gateways on different
// runs, with no diagnostic. Rejecting the conflict here moves that silent race
// to `tacplus build`, where the competing files and values can be named. Once
// this gate passes, "first found wins" is unambiguous by construction.
//
// Declaring the same value in several files is harmless — every candidate
// resolves identically — and stays legal, matching the codebase's other
// duplicate checks, which fire on conflicting definitions rather than on
// redundant ones.
func ValidateRestRootPath(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	files := make(map[string][]string) // REST_ROOT_PATH value -> declaring file paths
	for path, fd := range dp.GetAllFiles() {
		if fd == nil {
			continue
		}
		for _, c := range fd.Constants {
			if c.Name != "REST_ROOT_PATH" || c.Type != "string" {
				continue
			}
			v := c.RawValue()
			files[v] = append(files[v], path)
		}
	}
	if len(files) < 2 {
		return nil
	}

	values := make([]string, 0, len(files))
	for v := range files {
		values = append(values, v)
	}
	sort.Strings(values)

	parts := make([]string, 0, len(values))
	for _, v := range values {
		paths := files[v]
		sort.Strings(paths)
		parts = append(parts, fmt.Sprintf("%q declared in %s", v, strings.Join(paths, ", ")))
	}
	return fmt.Errorf(
		"conflicting REST_ROOT_PATH string constants (a proto tree may declare only one value; "+
			"which one wins is otherwise undefined):\n  %s",
		strings.Join(parts, "\n  "))
}
