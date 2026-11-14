// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// dependencyFloors are the lowest TypeScript and Rust versions a
// dependencies.yaml may pin, by language and package: below them the package
// carries advisories (#312). The generator writes these pins into the
// generated package.json and Cargo.toml as they stand, and dependencies.yaml
// is not a lockfile, so no scanner reads it: a stale pin tells a consumer
// whose lockfile holds the package back that the vulnerable version is fine.
var dependencyFloors = map[string]map[string]string{
	"typescript": {
		// GHSA-35jp-ww65-95wh, GHSA-3pq3-5fj3-cg6v, GHSA-542g-h47m-68v8,
		// GHSA-p92q-9vqr-4j8v and 30 more.
		"axios": "1.20.0",
		// GHSA-2883-xcg3-v3hh, GHSA-52cp-r559-cp3m, GHSA-5p4m-2wfm-xmqj,
		// GHSA-h67p-54hq-rp68, GHSA-mh29-5h37-fv8m.
		"js-yaml": "4.3.2",
		// GHSA-mrhx-c7xc-m3w5 (CVE-2023-54404).
		"zod": "4.6.6",
	},
	"rust": {
		// RUSTSEC-2026-0285, -0346, -0348, -0349.
		"rustls": "0.23.46",
		// RUSTSEC-2026-0386.
		"tokio": "1.51.6",
		// RUSTSEC-2026-0381 (CVE-2026-111943).
		"webpki-roots": "0.26.11",
		// RUSTSEC-2026-0190.
		"anyhow": "1.0.103",
		// GHSA-h395-gr6q-cpjc (CVE-2026-25537).
		"jsonwebtoken": "10.3.0",
	},
}

func TestDependencyPinsMeetTheirFloors(t *testing.T) {
	// Every checked-in copy: consumers copy example/proto's, and each
	// fixture's is what its generated output builds against.
	out, err := exec.Command("git", "-C", "..", "ls-files", "*dependencies.yaml").Output()
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("no dependencies.yaml found")
	}
	for _, path := range files {
		data, err := os.ReadFile(filepath.Join("..", path))
		if err != nil {
			t.Fatal(err)
		}
		var inventory struct {
			Lang map[string]struct {
				Dependencies map[string]string `yaml:"dependencies"`
			} `yaml:"lang"`
		}
		if err := yaml.Unmarshal(data, &inventory); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for lang, floors := range dependencyFloors {
			for name, floor := range floors {
				pin, ok := inventory.Lang[lang].Dependencies[name]
				if !ok {
					continue
				}
				if compareMavenVersions(strings.TrimLeft(pin, "^~=>"), floor) < 0 {
					t.Errorf("%s pins %s %s %s, below its floor %s", path, lang, name, pin, floor)
				}
			}
		}
	}
}

// TestHarnessCargoPinsMeetTheirFloors: the Cargo.toml a cross-test script
// writes for its own harness crate pins the Rust floors too.
func TestHarnessCargoPinsMeetTheirFloors(t *testing.T) {
	files, err := filepath.Glob("../cross-test/*/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no cross-test script found")
	}
	for name, floor := range dependencyFloors["rust"] {
		// `name = "1.2.3"` and `name = { version = "1.2.3", ... }`.
		pin := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*=\s*(?:\{\s*version\s*=\s*)?"=?([0-9][^"]*)"`)
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range pin.FindAllSubmatchIndex(data, -1) {
				version := string(data[m[2]:m[3]])
				if compareMavenVersions(version, floor) < 0 {
					line := strings.Count(string(data[:m[0]]), "\n") + 1
					t.Errorf("%s:%d pins %s %s, below its floor %s", path, line, name, version, floor)
				}
			}
		}
	}
}
