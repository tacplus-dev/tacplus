// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// buildImagePin is the form the build pipeline (tacplus-dev/looper's
// looper-build plan) accepts in build.lock, narrowed to this repository's own
// build image at an exact release: a tag alone, or no digest, is a fail
// verdict there, so it fails here first.
var buildImagePin = regexp.MustCompile(`^ghcr\.io/tacplus-dev/tacplus/build:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$`)

// TestBuildLock: the repository's root build.lock pins the build image its
// gates run in by digest (tacplus#340), in exactly one build_image line.
func TestBuildLock(t *testing.T) {
	data, err := os.ReadFile("../build.lock")
	if err != nil {
		t.Fatalf("build.lock: %v (the build pipeline refuses a commit with no pin)", err)
	}
	var images []string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "build_image:"); ok {
			images = append(images, strings.TrimSpace(v))
		}
	}
	if len(images) != 1 {
		t.Fatalf("build.lock: %d build_image lines, want 1", len(images))
	}
	if !buildImagePin.MatchString(images[0]) {
		t.Fatalf("build.lock: build_image %q must be ghcr.io/tacplus-dev/tacplus/build:X.Y.Z@sha256:<64 hex>", images[0])
	}
}
