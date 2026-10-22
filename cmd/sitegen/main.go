// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command sitegen writes the generated part of the tacplus.dev site into the
// MkDocs docs directory (site/docs). It is deterministic and runs before every
// `mkdocs build`:
//
//   - The user-facing repository documents (docs/THEORY.md, docs/USER.md, ...)
//     are staged into the site with their relative links rewritten: a link to
//     another published document becomes a link to its page, a link to any
//     other file in the repository becomes a link to that file on GitHub, and a
//     link to a file that does not exist is an error. The repository files stay
//     the only source; the staged copies are git-ignored build output.
//   - The annotation reference (reference/annotations/) is generated from
//     pkg/proto/sys/spec.proto, comments included, via protoc's descriptor set.
//
// Run from the repository root: go run ./cmd/sitegen [-root .] [-out site/docs]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "site/docs", "MkDocs docs directory to write into")
	flag.Parse()
	if err := run(*root, *out); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func run(root, out string) error {
	// sitegen owns these directories under the docs directory outright: they
	// are removed and rewritten on every run so a document dropped from the
	// published set disappears from the site too.
	for _, dir := range generatedDirs {
		if err := os.RemoveAll(filepath.Join(out, dir)); err != nil {
			return err
		}
	}
	if err := stageDocs(root, out); err != nil {
		return err
	}
	pages, err := annotationPages(root)
	if err != nil {
		return err
	}
	for page, md := range pages {
		if err := writeFile(filepath.Join(out, filepath.FromSlash(page)), md); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
