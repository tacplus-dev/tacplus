// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package chassis

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const rustTemplatesDir = "../../../templates/rust"

// cargoTemplates returns every Cargo.toml template under pkg/templates/rust,
// keyed by its path relative to that directory.
func cargoTemplates(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(rustTemplatesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(d.Name(), "cargo.toml.tmpl") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rustTemplatesDir, path)
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", rustTemplatesDir, err)
	}
	if len(out) == 0 {
		t.Fatalf("no Cargo.toml templates under %s", rustTemplatesDir)
	}
	return out
}

// A chassis crate's own manifest is what it promises when built or published on
// its own, so a literal floor like tokio "1" or rustls "0.23" admits every
// advisory since that release (tokio RUSTSEC-2021-0124, rustls RUSTSEC-2024-0336,
// regex RUSTSEC-2022-0013, chrono RUSTSEC-2020-0159), whatever the workspace root
// declares (#338). These crates are in every generated workspace, so they are
// taken from it, which follows dependencies.yaml.
func TestRustCargoTemplatesTakeWorkspaceCratesFromWorkspace(t *testing.T) {
	literal := regexp.MustCompile(`(?m)^(tokio|rustls|regex|chrono) *= *("|\{ *version)`)
	for path, s := range cargoTemplates(t) {
		for _, m := range literal.FindAllStringSubmatch(s, -1) {
			t.Errorf("%s declares %s with its own version; use %s = { workspace = true }", path, m[1], m[1])
		}
	}
}

// Crates the workspace does not declare keep a literal version, which must be
// at least the first release free of the advisories at the old floor (#338):
// bzip2 0.4.4 (RUSTSEC-2023-0004), rand 0.8.6 (RUSTSEC-2026-0097).
func TestRustCargoTemplatesFloorsAdmitNoKnownAdvisory(t *testing.T) {
	floors := map[string]string{
		"bzip2": "0.4.4",
		"rand":  "0.8.6",
	}
	decl := regexp.MustCompile(`(?m)^(bzip2|rand) *= *(?:"([^"]*)"|\{[^}\n]*version *= *"([^"]*)")`)
	for path, s := range cargoTemplates(t) {
		for _, m := range decl.FindAllStringSubmatch(s, -1) {
			got := m[2] + m[3]
			if want := floors[m[1]]; !versionAtLeast(got, want) {
				t.Errorf("%s declares %s = %q; the floor must be at least %q", path, m[1], got, want)
			}
		}
	}
}

// versionAtLeast compares dotted numeric versions, a missing component being 0
// (Cargo reads "0.8" as 0.8.0).
func versionAtLeast(got, want string) bool {
	g, w := strings.Split(got, "."), strings.Split(want, ".")
	for i := 0; i < len(w); i++ {
		var gi, wi int
		if i < len(g) {
			gi = atoi(g[i])
		}
		wi = atoi(w[i])
		if gi != wi {
			return gi > wi
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
