// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package images

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDockerfilesDeclareArgsWhereUsed fails on a stage ARG declared before an
// instruction that does not use it. A RUN after an ARG declaration sees its
// value, so a changed pin misses the cache on the first RUN after the ARG,
// not on the first that uses it: SWIFT_VERSION at the top of base's final
// stage rebuilt every layer of it on a Swift bump, and the images gate gave
// #947 no verdict. Each ARG goes just before the instruction that first uses
// it, so a bump rebuilds only its own layers onward.
func TestDockerfilesDeclareArgsWhereUsed(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "images", "*.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no images/*.Dockerfile found")
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range argsNotUsedNext(string(data)) {
			t.Errorf("%s: %s", filepath.Base(file), bad)
		}
	}
}

// argsNotUsedNext lists each ARG declared in a stage (after a FROM) whose next
// instruction other than an ARG does not reference it.
func argsNotUsedNext(dockerfile string) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(dockerfile, "\\\n", " "), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	var bad, pending []string
	inStage := false
	for _, l := range lines {
		word := strings.ToUpper(strings.Fields(l)[0])
		switch {
		case word == "FROM":
			bad = append(bad, unused(pending, l)...)
			pending, inStage = nil, true
		case word == "ARG":
			if inStage {
				name, _, _ := strings.Cut(strings.Fields(l)[1], "=")
				pending = append(pending, name)
			}
		default:
			bad = append(bad, unused(pending, l)...)
			pending = nil
		}
	}
	return append(bad, unused(pending, "the end of the file")...)
}

func unused(names []string, next string) []string {
	var bad []string
	for _, n := range names {
		if !regexp.MustCompile(`\$\{?` + regexp.QuoteMeta(n) + `\b`).MatchString(next) {
			bad = append(bad, "ARG "+n+" is declared before an instruction that does not use it ("+next+"); declare it just before its first use")
		}
	}
	return bad
}

func TestArgsNotUsedNext(t *testing.T) {
	for _, tc := range []struct {
		name, dockerfile string
		bad              int
	}{
		{"global args are free", "ARG A=1\nFROM x:${A}\nRUN true\n", 0},
		{"used next", "FROM x\nARG A\nRUN echo ${A}\n", 0},
		{"bare dollar", "FROM x\nARG A\nRUN echo $A\n", 0},
		{"two args, one instruction", "FROM x\nARG A\nARG B\nRUN echo $A $B\n", 0},
		{"continuation", "FROM x\nARG A\nRUN true \\\n    && echo ${A}\n", 0},
		{"comments skipped", "FROM x\nARG A\n# why\nRUN echo ${A}\n", 0},
		{"declared too early", "FROM x\nARG A\nRUN apt-get update\nRUN echo ${A}\n", 1},
		{"prefix is not a use", "FROM x\nARG A\nRUN echo ${AB}\n", 1},
		{"unused at the end", "FROM x\nRUN true\nARG A\n", 1},
		{"next stage", "FROM x\nARG A\nFROM y\nRUN echo ${A}\n", 1},
	} {
		if got := argsNotUsedNext(tc.dockerfile); len(got) != tc.bad {
			t.Errorf("%s: %d findings, want %d: %v", tc.name, len(got), tc.bad, got)
		}
	}
}
