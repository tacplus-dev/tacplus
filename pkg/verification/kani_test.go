// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verification

import "testing"

// Kani 0.68 (the build image's, #193) names the harness on a "Checking
// harness" line and prints the verdict alone on its own line, with no
// "(HARNESS name)" suffix.
const kani068Output = `Kani Rust Verifier 0.68.0 (cargo plugin)
   Compiling smoke-proof v0.0.0 (/m)
    Finished ` + "`dev`" + ` profile [unoptimized + debuginfo] target(s) in 0.28s
Checking harness proof::every_u16_is_a_valid_port...
CBMC 6.11.0 (cbmc-6.11.0)

RESULTS:
Check 1: proof::every_u16_is_a_valid_port.assertion.1
	 - Status: SUCCESS
	 - Description: "assertion failed: super::valid_port(port as u32)"

SUMMARY:
 ** 0 of 1 failed

VERIFICATION:- SUCCESSFUL
Verification Time: 0.029366236s

Checking harness proof::port_overflows...
CBMC 6.11.0 (cbmc-6.11.0)

SUMMARY:
 ** 1 of 1 failed
Failed Checks: assertion failed: port < 10

VERIFICATION:- FAILED
Verification Time: 0.01s

Manual Harness Summary:
Verification failed for - proof::port_overflows
Complete - 1 successfully verified harnesses, 1 failures, 2 total.
`

func TestParseKaniOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   map[string]string // harness → status
	}{
		{"kani 0.68", kani068Output, map[string]string{
			"proof::every_u16_is_a_valid_port": "verified",
			"proof::port_overflows":            "failed",
		}},
		{"older kani, harness named on the verdict line",
			"VERIFICATION:- SUCCESSFUL (HARNESS verify_a)\nVERIFICATION:- FAILED (verify_b)\n",
			map[string]string{"verify_a": "verified", "verify_b": "failed"}},
	}
	for _, c := range cases {
		got := parseKaniOutput(c.output)
		if len(got) != len(c.want) {
			t.Errorf("%s: parsed %d harnesses, want %d: %v", c.name, len(got), len(c.want), got)
			continue
		}
		for _, h := range got {
			if want, ok := c.want[h.HarnessName]; !ok || h.Status != want {
				t.Errorf("%s: harness %q status %q, want %q", c.name, h.HarnessName, h.Status, c.want[h.HarnessName])
			}
		}
	}
}
