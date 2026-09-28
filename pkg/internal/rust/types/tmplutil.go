// Copyright (c) 2026 Second Star Cavalier LLC

package types

import "gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"

// loadTemplate loads a template from the embedded templates filesystem for Rust.
//
// The four language copies of this differed only in the subdirectory string, so
// the implementation is shared; this stays as a thin local alias to keep the
// call sites unchanged. See docs/NAMING_CONVENTIONS.md rule 2.
func loadTemplate(name string) (string, error) {
	return tmplutil.LoadTemplate("rust/types", name)
}
