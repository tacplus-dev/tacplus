// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// README.md is generated from README.src.md so the pieces the README shares
// with the site (docs/snippets/) are written once. A line of the form
//
//	--8<-- "docs/snippets/proto.md"
//
// is replaced by the contents of that repository file, exactly as the site's
// pymdownx.snippets extension does for the same marker in site/docs. Only
// this one-line, one-file form is supported; the extension's other forms
// (blocks, sections, URLs, disabled markers) are errors here.
const (
	readmeSrc = "README.src.md"
	readmeOut = "README.md"
	// readmeNotice heads the generated README so an edit lands in the source.
	readmeNotice = "<!-- Generated from README.src.md by cmd/sitegen: do not edit. -->\n"
)

var snippetLine = regexp.MustCompile(`^--8<-- "([^":;]+)"$`)

// readme returns README.md as generated from README.src.md in the repository
// repo.
func readme(repo *os.Root) (string, error) {
	src, err := repo.ReadFile(readmeSrc)
	if err != nil {
		return "", err
	}
	out, err := expandSnippets(repo, string(src))
	if err != nil {
		return "", fmt.Errorf("%s: %w", readmeSrc, err)
	}
	return readmeNotice + out, nil
}

// expandSnippets replaces every snippet marker line in doc with the contents
// of the named file, a repository path under repo. A path that leaves repo,
// through ".." or a symbolic link, is an error and is not read.
func expandSnippets(repo *os.Root, doc string) (string, error) {
	lines := strings.Split(doc, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "--8<--") {
			continue
		}
		m := snippetLine.FindStringSubmatch(line)
		if m == nil {
			return "", fmt.Errorf("line %d: %q: only the one-line form --8<-- \"path\" is supported", i+1, line)
		}
		data, err := repo.ReadFile(m[1])
		if err != nil {
			return "", fmt.Errorf("line %d: %w", i+1, err)
		}
		lines[i] = strings.TrimSuffix(string(data), "\n")
	}
	return strings.Join(lines, "\n"), nil
}
