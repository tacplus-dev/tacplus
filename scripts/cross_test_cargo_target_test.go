// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every cross-test leg that builds generated Rust compiles the same heavy
// dependency set (tonic, prost, tokio, rustls). A per-leg CARGO_TARGET_DIR
// compiles it once per leg and grew target/cross-test by tens of GB in one
// run, so legs share one directory (cross-test/lib/cargo-target.sh). A leg
// that must not share says so with a "separate-cargo-target:" comment on the
// line that sets it.
func TestCrossTestLegsShareOneCargoTarget(t *testing.T) {
	scripts, err := filepath.Glob("../cross-test/*/run.sh")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no cross-test run.sh found: %v", err)
	}
	setter := regexp.MustCompile(`CARGO_TARGET_DIR=("[^"]*"|\S+)`)
	invoker := regexp.MustCompile(`\bcargo (build|test|check|run)\b`)
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		usesCargo := false
		sourcesLib := false
		for i, line := range lines {
			if strings.Contains(line, "lib/cargo-target.sh") {
				sourcesLib = true
			}
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if invoker.MatchString(line) {
				usesCargo = true
			}
			m := setter.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if m[1] == `"$CROSS_TEST_CARGO_TARGET"` || strings.Contains(line, "separate-cargo-target:") {
				continue
			}
			t.Errorf("%s:%d sets its own CARGO_TARGET_DIR %s; use \"$CROSS_TEST_CARGO_TARGET\" or justify with a separate-cargo-target: comment", path, i+1, m[1])
		}
		if usesCargo && !sourcesLib {
			t.Errorf("%s runs cargo but does not source cross-test/lib/cargo-target.sh", path)
		}
	}
}
