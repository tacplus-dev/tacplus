// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// A repository pins, in tacplus.lock beside its tacplus.yaml, the exact tacplus
// release its builds run and the build image they run in, by digest:
//
//	tacplus: 2.6.1
//	build_image: ghcr.io/tacplus-dev/tacplus/build:2.6.1@sha256:<64 hex>
//
// A build is then fully determined by its commit
// (tacplus-dev/roadmap:decisions/deterministic-builds-2026-10-27.md): the build
// image's tacplus (images/tacplus.sh) runs exactly this version, verified
// against the release's checksums.txt, and never resolves "newest". Moving the
// pin is `tacplus pin`, in a commit; a build never moves it.
//
// The floor (tacplus: in tacplus.yaml) stays what it was: the oldest
// MAJOR.MINOR whose features the repository uses. The lock is what it runs.

// LockFilename is the pin file, beside tacplus.yaml.
const LockFilename = "tacplus.lock"

// Lock is the tacplus.lock schema.
type Lock struct {
	Tacplus    string `yaml:"tacplus"`
	BuildImage string `yaml:"build_image"`
}

var (
	lockVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	// An image reference by digest, optionally with the tag it was resolved
	// from: <name>[:<tag>]@sha256:<64 hex>. The digest is what pulls.
	lockImageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(?::([A-Za-z0-9_][A-Za-z0-9._-]*))?@sha256:[0-9a-f]{64}$`)
)

// Validate refuses a lock that does not pin both an exact release and an
// image digest.
func (l Lock) Validate() error {
	vm := lockVersionRe.FindStringSubmatch(l.Tacplus)
	if vm == nil {
		return fmt.Errorf("%s: tacplus: %q must be an exact MAJOR.MINOR.PATCH release", LockFilename, l.Tacplus)
	}
	im := lockImageRe.FindStringSubmatch(l.BuildImage)
	if im == nil {
		return fmt.Errorf("%s: build_image: %q must name the image by digest (<name>[:<tag>]@sha256:<64 hex>)", LockFilename, l.BuildImage)
	}
	// A tag that names a version must name the pin's line: the image's tacplus
	// refuses a pin of another line.
	if tm := lockVersionRe.FindStringSubmatch(im[1]); tm != nil && (tm[1] != vm[1] || tm[2] != vm[2]) {
		return fmt.Errorf("%s: build_image %q is of line %s.%s, but tacplus %s is of line %s.%s", LockFilename, l.BuildImage, tm[1], tm[2], l.Tacplus, vm[1], vm[2])
	}
	return nil
}

// Line returns the pin's MAJOR.MINOR. The lock must be valid.
func (l Lock) Line() string {
	vm := lockVersionRe.FindStringSubmatch(l.Tacplus)
	if vm == nil {
		return ""
	}
	return vm[1] + "." + vm[2]
}

// ReadLock reads and validates dir/tacplus.lock. A missing file is an error
// satisfying errors.Is(err, os.ErrNotExist).
func ReadLock(dir string) (*Lock, error) {
	path := filepath.Join(dir, LockFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

// WriteLock validates l and writes it to dir/tacplus.lock. The format is
// fixed, one `key: value` line per field, because images/tacplus.sh reads it
// with sed.
func WriteLock(dir string, l Lock) error {
	if err := l.Validate(); err != nil {
		return err
	}
	content := "# The tacplus release and build image every build of this repository runs, exactly.\n" +
		"# Written by `tacplus pin`; move it with `tacplus pin`, in a commit. A build never moves it.\n" +
		"tacplus: " + l.Tacplus + "\n" +
		"build_image: " + l.BuildImage + "\n"
	return os.WriteFile(filepath.Join(dir, LockFilename), []byte(content), 0o644)
}
