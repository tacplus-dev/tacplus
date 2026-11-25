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
// build image at an immutable tag: an exact release (`X.Y.Z`) or a line's
// nightly rebuild (`<yyyymmdd>-<sha7>`, #966; docs/VERSIONING.md), never the
// floating `X.Y`. A tag alone, or no digest, is a fail verdict there, so it
// fails here first.
var buildImagePin = regexp.MustCompile(`^ghcr\.io/tacplus-dev/tacplus/build:([0-9]+\.[0-9]+\.[0-9]+|[0-9]{8}-[0-9a-f]{7})@sha256:[0-9a-f]{64}$`)

// TestBuildImagePinForms: the immutable tags pass, the floating ones and a
// pin without its digest do not.
func TestBuildImagePinForms(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		pin  string
		want bool
	}{
		{"ghcr.io/tacplus-dev/tacplus/build:2.6.14" + digest, true},
		{"ghcr.io/tacplus-dev/tacplus/build:20261123-57a6521" + digest, true},
		{"ghcr.io/tacplus-dev/tacplus/build:2.6" + digest, false},
		{"ghcr.io/tacplus-dev/tacplus/build:latest" + digest, false},
		{"ghcr.io/tacplus-dev/tacplus/build:20261123-57a6521", false},
		{"ghcr.io/tacplus-dev/tacplus/build:2026112-57a6521" + digest, false},
		{"ghcr.io/tacplus-dev/tacplus/base:2.6.14" + digest, false},
	} {
		if got := buildImagePin.MatchString(tc.pin); got != tc.want {
			t.Errorf("buildImagePin(%q) = %v, want %v", tc.pin, got, tc.want)
		}
	}
}

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
		t.Fatalf("build.lock: build_image %q must be ghcr.io/tacplus-dev/tacplus/build:X.Y.Z or :<yyyymmdd>-<sha7>, @sha256:<64 hex>", images[0])
	}
}
