// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A merge conflict committed with its markers went unnoticed on release/2.0
// (cross-test/TESTS.md, #329): nothing read the file. No tracked file may hold
// one. A lone "=======" is a Markdown heading underline, so only the markers
// that open and close a conflict (and diff3's base marker) are looked for.
var conflictMarkerPrefixes = [][]byte{
	[]byte("<<<<<<<"),
	[]byte("|||||||"),
	[]byte(">>>>>>>"),
}

// conflictMarkerLines returns the 1-based lines of data that are conflict
// markers: one of the prefixes, then the end of the line or a space.
func conflictMarkerLines(data []byte) []int {
	var lines []int
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		for _, p := range conflictMarkerPrefixes {
			if rest, ok := bytes.CutPrefix(line, p); ok && (len(rest) == 0 || rest[0] == ' ') {
				lines = append(lines, i+1)
				break
			}
		}
	}
	return lines
}

func TestConflictMarkerLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want []int
	}{
		{"none", "a\nb\n", nil},
		{"heading underline", "Title\n=======\n", nil},
		{"conflict", "a\n<<<<<<< HEAD\nb\n=======\nc\n>>>>>>> 7de131d (msg)\n", []int{2, 6}},
		{"diff3", "<<<<<<< ours\nx\n||||||| base\ny\n=======\nz\n>>>>>>> theirs\n", []int{1, 3, 7}},
		{"bare markers, CRLF", "<<<<<<<\r\n>>>>>>>\r\n", []int{1, 2}},
		{"longer run", "<<<<<<<< not a marker\n", nil},
		{"indented", "  <<<<<<< HEAD\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := conflictMarkerLines([]byte(tc.data))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNoCommittedConflictMarkers(t *testing.T) {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	dir := string(bytes.TrimSpace(root))
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, string(name)))
		if err != nil {
			// A tracked file deleted in the working tree, or a submodule.
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue // binary
		}
		for _, line := range conflictMarkerLines(data) {
			t.Errorf("%s:%d: committed merge conflict marker", name, line)
		}
	}
}
