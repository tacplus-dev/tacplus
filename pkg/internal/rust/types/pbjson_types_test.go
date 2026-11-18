// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// pbjson-types names chrono as a non-optional dependency, and chrono 0.4 is
// unmaintained (RUSTSEC-2026-0543): no generated source names pbjson_types, so
// the generated types crate does not depend on it, and the logger chassis,
// which never names chrono, does not depend on chrono (tacplus#855).
func TestGeneratedCratesDoNotDependOnPbjsonTypes(t *testing.T) {
	for _, c := range []struct{ path, dep string }{
		{"rust/types/types_cargo.toml.tmpl", "pbjson-types"},
		{"rust/chassis/logger/Cargo.toml.tmpl", "chrono"},
	} {
		raw, err := fs.ReadFile(templates.FS, c.path)
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		if strings.Contains(string(raw), c.dep) {
			t.Errorf("%s depends on %s:\n%s", c.path, c.dep, raw)
		}
	}
}
