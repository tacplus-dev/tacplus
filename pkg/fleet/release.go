// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A deployment that rolls a service to its main release, rather than its
// line's newest tag, names it in releases.yaml beside fleet.yaml: the commit
// main was green at and the image built from it, by digest, as the release
// record tacplus-dev/build publishes gives them (#981). The service is locked
// at that commit as it would be at a tag's: its contract, depends,
// contributions and inputs digest are all read there. The file is the
// deployment's, written by a job from the records; fleet.yaml keeps the
// lines a human chose.

// ReleasesFile is the file beside the manifest naming main releases.
const ReleasesFile = "releases.yaml"

// Release is one main release releases.yaml names.
type Release struct {
	Name   string `yaml:"name"`
	Commit string `yaml:"commit"`
	Image  string `yaml:"image"`
}

// Version is what fleet.lock records for a main release: main-<12 hex>, the
// tag the release's image carries.
func (r Release) Version() string { return "main-" + r.Commit[:12] }

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// digestImageRe is an image by digest: a name, an optional tag, and
	// @sha256:<64 hex>.
	digestImageRe = regexp.MustCompile(`^([^@\s]+)@sha256:[0-9a-f]{64}$`)
)

// ReadReleases reads releases.yaml in dir, keyed by service name; none when
// there is no file. Each release names a service m runs, once, at a full
// commit, with an image by digest whose tag, when it has one, is the
// commit's main-<12 hex>: the record's two lines must belong together.
func ReadReleases(dir string, m *Manifest) (map[string]Release, error) {
	data, err := os.ReadFile(filepath.Join(dir, ReleasesFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: %w", err)
	}
	var f struct {
		Releases []Release `yaml:"releases"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("fleet: parsing %s: %w", ReleasesFile, err)
	}
	runs := map[string]bool{}
	for _, s := range m.Services {
		runs[s.Name] = true
	}
	out := map[string]Release{}
	for i, r := range f.Releases {
		switch {
		case !runs[r.Name]:
			return nil, fmt.Errorf("fleet: %s: releases[%d] names %q, which %s does not run", ReleasesFile, i, r.Name, ManifestFile)
		case out[r.Name] != Release{}:
			return nil, fmt.Errorf("fleet: %s names %q twice", ReleasesFile, r.Name)
		case !commitRe.MatchString(r.Commit):
			return nil, fmt.Errorf("fleet: %s: service %q: commit %q must be the full commit, 40 hex", ReleasesFile, r.Name, r.Commit)
		}
		im := digestImageRe.FindStringSubmatch(r.Image)
		if im == nil {
			return nil, fmt.Errorf("fleet: %s: service %q: image %q must be by digest, <name>@sha256:<64 hex>", ReleasesFile, r.Name, r.Image)
		}
		name := im[1]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if _, tag, ok := strings.Cut(name, ":"); ok && tag != r.Version() {
			return nil, fmt.Errorf("fleet: %s: service %q: image %s is tagged %s, not %s: it is not commit %s's release", ReleasesFile, r.Name, r.Image, tag, r.Version(), r.Commit)
		}
		out[r.Name] = r
	}
	return out, nil
}
