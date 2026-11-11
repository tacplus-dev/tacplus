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
//   - README.md is generated from README.src.md by expanding its snippet
//     markers (see readme.go), so the README and the site share
//     docs/snippets/. It is committed, and TestReadmeIsCurrent fails when it
//     is stale.
//
// With -check-links it writes nothing: it checks the relative links of every
// tracked Markdown document instead (`just docs`). The site's own pages,
// site/docs/, link to site paths, and `just site` checks those.
//
// Run from the repository root: go run ./cmd/sitegen [-root .] [-out site/docs] [-check-links]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", "site/docs", "MkDocs docs directory to write into")
	links := flag.Bool("check-links", false, "only check the relative links of every tracked Markdown document")
	flag.Parse()
	var err error
	if *links {
		err = checkRepoLinks(*root)
	} else {
		err = run(*root, *out)
	}
	if err != nil {
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
	md, err := readme(root)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(root, readmeOut), []byte(md))
}

// checkRepoLinks checks the links of the Markdown documents git tracks under
// root, but for the site's pages.
func checkRepoLinks(root string) error {
	ls, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.md", ":!:site/docs/").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	files := strings.Split(strings.TrimSuffix(string(ls), "\x00"), "\x00")
	if err := checkLinks(root, files); err != nil {
		return err
	}
	fmt.Printf("sitegen: the links of %d documents resolve\n", len(files))
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
