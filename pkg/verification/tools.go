// Copyright (c) 2026 Second Star Cavalier LLC

package verification

import (
	"os/exec"
	"strings"
)

// toolAvailable checks whether a command exists on PATH.
func toolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// toolVersion runs a command and returns its first line of output as a version string.
// Returns "unknown" if the command fails.
func toolVersion(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if line == "" {
		return "unknown"
	}
	return line
}
