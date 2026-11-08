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

// RustSec marks these crates unmaintained, with no fixed release: backoff
// (RUSTSEC-2025-0012) and instant (RUSTSEC-2024-0384, reached only through
// backoff). httpclient once declared backoff without using it (#319); its
// retries compute their own delay. No generated crate may declare either.
var unmaintainedCrateRe = regexp.MustCompile(`(?m)^\s*(backoff|instant)\s*=`)

func TestRustCargoTemplatesDeclareNoUnmaintainedCrate(t *testing.T) {
	root := "../../../templates/rust"
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "Cargo.toml") {
			return nil
		}
		seen++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := unmaintainedCrateRe.FindStringSubmatch(string(data)); m != nil {
			t.Errorf("%s declares the unmaintained crate %q", path, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if seen == 0 {
		t.Fatalf("no Cargo.toml template found under %s", root)
	}
}
