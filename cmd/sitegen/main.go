// Copyright (c) 2026 Seabeam LLC
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
	"path"
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
	// Every file sitegen reads or writes is opened through an os.Root, so a
	// path from repository content (a link, a snippet marker) that leaves the
	// repository, or the docs directory, is an error rather than resolved.
	repo, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer repo.Close()
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	docs, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer docs.Close()
	// sitegen owns these directories under the docs directory outright: they
	// are removed and rewritten on every run so a document dropped from the
	// published set disappears from the site too.
	for _, dir := range generatedDirs {
		if err := docs.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := stageDocs(repo, docs); err != nil {
		return err
	}
	pages, err := annotationPages(root)
	if err != nil {
		return err
	}
	for page, md := range pages {
		if err := writeFile(docs, page, md); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes data to the slash-separated path rel under r, creating its
// directory.
func writeFile(r *os.Root, rel string, data []byte) error {
	if err := r.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return err
	}
	return r.WriteFile(rel, data, 0o644)
}
