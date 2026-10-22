// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The committed README.md must be what README.src.md expands to, so an edit
// to README.src.md or docs/snippets/ without regenerating fails `just test`.
func TestReadmeIsCurrent(t *testing.T) {
	want, err := readme(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(repoRoot, readmeOut))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("README.md is stale: it differs from the expansion of README.src.md.\n"+
			"Edit README.src.md or docs/snippets/ (never README.md), then run `go run ./cmd/sitegen` and commit the result.")
	}
}

func TestExpandSnippetsRejectsOtherForms(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := expandSnippets(root, "x\n--8<-- \"a.md\"\ny\n")
	if err != nil || got != "x\nA\ny\n" {
		t.Fatalf("simple form: got %q, %v", got, err)
	}
	for _, doc := range []string{
		"--8<--\na.md\n--8<--\n", // block form
		"; --8<-- \"a.md\"\n",    // disabled marker
		"--8<-- \"a.md:1:2\"\n",  // section form
		"  --8<-- \"a.md\"\n",    // indented
		"--8<-- \"missing.md\"\n",
	} {
		if _, err := expandSnippets(root, doc); err == nil {
			t.Errorf("%q: expected an error", doc)
		} else if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%q: error should name the line: %v", doc, err)
		}
	}
}
