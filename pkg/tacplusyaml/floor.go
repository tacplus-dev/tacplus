// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A consumer declares in tacplus.yaml the oldest tacplus MAJOR.MINOR whose
// features it uses:
//
//	tacplus: "2.2"
//
// The generator refuses to run below it. It is a floor, not a pin: MAJOR must
// match (a major bump is breaking) and the running MINOR must be at least the
// floor. The field carries MAJOR.MINOR only; a patch component is rejected,
// because the exact release a build runs is pinned in tacplus.lock (lock.go),
// not here. An absent field is no floor, so existing consumers are unaffected.

var (
	floorRe   = regexp.MustCompile(`^(\d+)\.(\d+)$`)
	versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?(?:[-+].*)?$`)
)

// unstampedVersion is what an unstamped local build reports (pkg/buildinfo).
// It cannot satisfy any floor: the check fails closed rather than guessing.
const unstampedVersion = "0.0.0"

// CheckFloor returns an error when the running tacplus version does not
// satisfy the consumer's declared floor. An empty floor always passes.
func CheckFloor(floor, running string) error {
	floor = strings.TrimSpace(floor)
	if floor == "" {
		return nil
	}
	fm := floorRe.FindStringSubmatch(floor)
	if fm == nil {
		return fmt.Errorf("%s: tacplus: %q must be MAJOR.MINOR (a floor, never a patch pin)", Filename, floor)
	}
	if strings.TrimPrefix(running, "v") == unstampedVersion {
		return fmt.Errorf("%s: tacplus: %q cannot be satisfied by an unstamped tacplus build (%s); install a stamped build (just install) or a release", Filename, floor, running)
	}
	rm := versionRe.FindStringSubmatch(running)
	if rm == nil {
		return fmt.Errorf("%s: tacplus: %q cannot be checked against unparseable tacplus version %q", Filename, floor, running)
	}
	fMajor, _ := strconv.Atoi(fm[1])
	fMinor, _ := strconv.Atoi(fm[2])
	rMajor, _ := strconv.Atoi(rm[1])
	rMinor, _ := strconv.Atoi(rm[2])
	if rMajor != fMajor || rMinor < fMinor {
		return fmt.Errorf("%s: this project needs tacplus %d.%d or a later %d.x; running %s", Filename, fMajor, fMinor, fMajor, running)
	}
	return nil
}
