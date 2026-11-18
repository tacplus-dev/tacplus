// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// Every OpenOptions chain in the Rust templates that creates its file says
// whether it truncates: clippy::suspicious_open_options fails the generated
// code's security policy on a create(true) with neither truncate(..) nor
// append(true) (#853).
func TestRustTemplatesOpenOptionsCreateStatesTruncate(t *testing.T) {
	err := fs.WalkDir(templates.FS, "rust", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		src, err := templates.FS.ReadFile(path)
		if err != nil {
			return err
		}
		chains := strings.Split(string(src), "OpenOptions::new()")
		for _, chain := range chains[1:] {
			if end := strings.Index(chain, ".open("); end >= 0 {
				chain = chain[:end]
			}
			if strings.Contains(chain, ".create(true)") &&
				!strings.Contains(chain, ".truncate(") && !strings.Contains(chain, ".append(true)") {
				t.Errorf("%s: OpenOptions with create(true) must state truncate(..) or append(true):%s", path, chain)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk rust templates: %v", err)
	}
}
