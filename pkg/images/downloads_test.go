// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package images holds tests over the image definitions in images/.
package images

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pipedDownload matches a curl whose output is piped into another command in
// the same && segment. curl --retry cannot resend bytes already written to the
// pipe, so a mid-stream failure fails the step (issue #23).
var pipedDownload = regexp.MustCompile(`curl\b[^&;]*\|`)

// TestDockerfilesDownloadToFile fails on any image build step that streams a
// download straight into a consumer. Each download must land in a file first,
// so --retry can resend it.
func TestDockerfilesDownloadToFile(t *testing.T) {
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
		// Join backslash-continued lines so a pipe on a continuation line is
		// seen in the same command as the curl it follows.
		joined := strings.ReplaceAll(string(data), "\\\n", " ")
		for _, segment := range strings.Split(joined, "&&") {
			if pipedDownload.MatchString(segment) {
				t.Errorf("%s: piped download cannot retry a mid-stream failure; download to a file first: %s",
					filepath.Base(file), strings.TrimSpace(segment))
			}
		}
	}
}
