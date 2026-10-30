// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// line is this release branch's MAJOR.MINOR.
const line = "2.1"

// The install instructions and "what you pin" text name this release's own
// image line, not an older one: a consumer who copies them from a 2.1 checkout
// must get 2.1 (tacplus#201).
func TestDocsPinThisLine(t *testing.T) {
	image := regexp.MustCompile(`\btacplus:([0-9]+\.[0-9]+)\b`)
	for _, dir := range []string{"README.md", "docs", "site/docs"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, l := range strings.Split(string(data), "\n") {
				for _, m := range image.FindAllStringSubmatch(l, -1) {
					if m[1] != line {
						rel, _ := filepath.Rel(repoRoot, path)
						t.Errorf("%s:%d pins tacplus:%s, want tacplus:%s: %s", rel, i+1, m[1], line, strings.TrimSpace(l))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
