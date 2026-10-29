// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// kaniHarnessResult is the regex for Kani's per-harness status line.
// Example: "VERIFICATION:- SUCCESSFUL (HARNESS verify_token_validate)"; current
// Kani prints the verdict alone ("VERIFICATION:- SUCCESSFUL").
var kaniResultRe = regexp.MustCompile(`VERIFICATION:-\s+(SUCCESSFUL|FAILED)(?:\s+\((?:HARNESS\s+)?(\S+)\))?`)

// kaniHarnessRe is the line current Kani (the build image's 0.68) names a
// harness on before its results; the verdict line then carries no name.
var kaniHarnessRe = regexp.MustCompile(`^Checking harness (\S+?)\.\.\.$`)

// runKani runs cargo kani on each Rust crate in the output directory
// that contains #[kani::proof] harnesses.
func runKani(outputDir string) (*spec.VerificationReport, error) {
	// Find all crates containing kani::proof harnesses
	zap.L().Info("searching for kani proof harnesses", zap.String("dir", outputDir))
	cratesWithProofs := findKaniCrates(outputDir)
	if len(cratesWithProofs) == 0 {
		zap.L().Info("no crates with #[kani::proof] found")
		return nil, nil
	}
	zap.L().Info("found crates with proofs", zap.Int("count", len(cratesWithProofs)), zap.Strings("crates", cratesWithProofs))

	// Get Kani version
	version := toolVersion("cargo", "kani", "--version")

	report := &spec.VerificationReport{
		Tool:        "kani",
		ToolVersion: version,
		Language:    "rust",
	}

	// Kani runs at the crate level — run once per crate
	for i, crateDir := range cratesWithProofs {
		zap.L().Info("running kani on crate",
			zap.Int("index", i+1),
			zap.Int("total", len(cratesWithProofs)),
			zap.String("crate", filepath.Base(crateDir)),
		)
		harnesses, err := runKaniOnCrate(crateDir)
		if err != nil {
			return nil, fmt.Errorf("kani on %s: %w", filepath.Base(crateDir), err)
		}
		report.Harnesses = append(report.Harnesses, harnesses...)
	}

	// Tally results
	for _, h := range report.Harnesses {
		report.Total++
		if h.Status == "verified" {
			report.Passed++
		} else {
			report.Failed++
		}
	}

	return report, nil
}

// findKaniCrates walks the output directory for Cargo.toml files whose crate
// contains at least one helpers.rs with #[kani::proof].
func findKaniCrates(outputDir string) []string {
	var crates []string
	filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() != "Cargo.toml" {
			return nil
		}
		crateDir := filepath.Dir(path)
		if crateHasKaniProofs(crateDir) {
			crates = append(crates, crateDir)
		}
		return nil
	})
	return crates
}

// crateHasKaniProofs checks if any .rs file under crateDir contains #[kani::proof].
func crateHasKaniProofs(crateDir string) bool {
	found := false
	filepath.Walk(crateDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || found || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".rs") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), "#[kani::proof]") {
			found = true
		}
		return nil
	})
	return found
}

// runKaniOnCrate runs `cargo kani` in the given crate directory and parses output.
func runKaniOnCrate(crateDir string) ([]*spec.VerificationHarness, error) {
	cmd := exec.Command("cargo", "kani")
	cmd.Dir = crateDir
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Parse harness results from output even if the command failed
	// (Kani exits non-zero when any harness fails)
	harnesses := parseKaniOutput(outputStr)

	if err != nil && len(harnesses) == 0 {
		return nil, fmt.Errorf("cargo kani failed: %w\n%s", err, outputStr)
	}

	return harnesses, nil
}

// parseKaniOutput extracts per-harness results from Kani stdout.
func parseKaniOutput(output string) []*spec.VerificationHarness {
	var harnesses []*spec.VerificationHarness

	// Also collect assumptions from the source (line-by-line scan for kani::assume)
	scanner := bufio.NewScanner(strings.NewReader(output))
	current := ""
	for scanner.Scan() {
		line := scanner.Text()
		if m := kaniHarnessRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			current = m[1]
			continue
		}
		matches := kaniResultRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		name := matches[2]
		if name == "" {
			name = current
		}
		if name == "" {
			continue
		}
		current = ""

		status := "verified"
		if matches[1] == "FAILED" {
			status = "failed"
		}

		harnesses = append(harnesses, &spec.VerificationHarness{
			HarnessName: name,
			Status:      status,
			Contracts:   []string{"validate() returns Ok(()) for all inputs satisfying assumptions"},
		})
	}

	return harnesses
}

// collectKaniAssumptions reads a helpers.rs file and extracts kani::assume() lines
// for a given harness function name.
func collectKaniAssumptions(filePath, harnessName string) (string, []string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil
	}

	var messageFqn string
	var assumptions []string
	lines := strings.Split(string(data), "\n")
	inHarness := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "fn "+harnessName+"(") {
			inHarness = true
			continue
		}
		if inHarness {
			if trimmed == "}" {
				break
			}
			if strings.HasPrefix(trimmed, "let msg:") {
				// Extract type name for FQN
				parts := strings.SplitN(trimmed, ":", 2)
				if len(parts) == 2 {
					typePart := strings.TrimSpace(strings.SplitN(parts[1], "=", 2)[0])
					messageFqn = typePart
				}
			}
			if strings.HasPrefix(trimmed, "kani::assume(") {
				assumption := strings.TrimPrefix(trimmed, "kani::assume(")
				assumption = strings.TrimSuffix(assumption, ");")
				assumptions = append(assumptions, assumption)
			}
		}
	}

	return messageFqn, assumptions
}
