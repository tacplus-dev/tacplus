// Copyright (c) 2026 Second Star Cavalier LLC

package gittag

import (
	"fmt"
	"os/exec"
	"strings"
)

// Detect runs `git describe --tags --abbrev=0 --match "v*"` in dir
// and returns the most recent tag matching v* (e.g. "v1.2.3").
// Returns an error if no matching tags exist or git is not available.
func Detect(dir string) (string, error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0", "--match", "v*")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no git tag found: %w", err)
	}
	tag := strings.TrimSpace(string(out))
	if tag == "" {
		return "", fmt.Errorf("git describe returned empty tag")
	}
	return tag, nil
}

// ToMinorLatest converts a semver tag to its MAJOR.MINOR.latest form.
// Examples:
//
//	"v1.2.3"     → "1.2.latest"
//	"1.2.3"      → "1.2.latest"
//	"1.2"        → "1.2.latest"
//	"v1.2"       → "1.2.latest"
func ToMinorLatest(tag string) string {
	tag = strings.TrimPrefix(tag, "v")
	parts := strings.SplitN(tag, ".", 3)
	if len(parts) < 2 {
		return tag
	}
	return parts[0] + "." + parts[1] + ".latest"
}
