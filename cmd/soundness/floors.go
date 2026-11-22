// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// chassisInventoryFiles are the inventories a chassis manifest is checked
// against: the one consumers copy and the base fixture's. A package either
// declares is the inventory's, and its floor moves there alone (tacplus#949).
var chassisInventoryFiles = []string{
	"example/proto/dependencies.yaml",
	"cross-test/proto/dependencies.yaml",
}

type chassisManifestViolation struct {
	file    string
	line    int
	pkg     string
	version string
}

var (
	// name = "1" or name = { version = "1", ... }: a dependency line of a
	// Cargo.toml template.
	cargoLiteralVersion = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*=\s*(?:"([^"]*)"|\{.*\bversion\s*=\s*"([^"]*)")`)
	// "name": "^1.2.3": a dependency line of a package.json template.
	npmLiteralVersion = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*"([^"]*)"`)
)

// chassisManifestVersionViolations lists each version literal a chassis
// manifest template (Rust Cargo.toml, TypeScript package.json) names for a
// package the inventory declares: such a crate takes { workspace = true },
// such an npm package a template value from dependencies.yaml.
func chassisManifestVersionViolations(root string) ([]chassisManifestViolation, error) {
	rust := map[string]bool{}
	npm := map[string]bool{}
	for _, rel := range chassisInventoryFiles {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		var inv struct {
			Tools map[string]string `yaml:"tools"`
			Lang  map[string]struct {
				Dependencies map[string]string `yaml:"dependencies"`
			} `yaml:"lang"`
		}
		if err := yaml.Unmarshal(data, &inv); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		for name := range inv.Lang["rust"].Dependencies {
			rust[name] = true
		}
		for name := range inv.Lang["typescript"].Dependencies {
			npm[name] = true
		}
		if _, ok := inv.Tools["typescript"]; ok {
			npm["typescript"] = true
		}
	}

	var violations []chassisManifestViolation
	scan := func(pattern string, declared map[string]bool, match func(string) (string, string, bool)) error {
		files, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, path := range files {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			s := bufio.NewScanner(f)
			for n := 1; s.Scan(); n++ {
				name, version, ok := match(s.Text())
				if ok && declared[name] && !strings.Contains(version, "{{") {
					violations = append(violations, chassisManifestViolation{filepath.ToSlash(rel), n, name, version})
				}
			}
			err = s.Err()
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := scan("pkg/templates/rust/chassis/*/Cargo.toml.tmpl", rust, func(line string) (string, string, bool) {
		m := cargoLiteralVersion.FindStringSubmatch(line)
		if m == nil || (m[2] == "" && m[3] == "") {
			return "", "", false
		}
		return m[1], m[2] + m[3], true
	}); err != nil {
		return nil, err
	}
	if err := scan("pkg/templates/typescript/chassis/*/package.json.tmpl", npm, func(line string) (string, string, bool) {
		m := npmLiteralVersion.FindStringSubmatch(line)
		if m == nil {
			return "", "", false
		}
		return m[1], m[2], true
	}); err != nil {
		return nil, err
	}
	return violations, nil
}

func renderChassisManifestVersions(root string) int {
	fmt.Println("\n## Chassis manifest versions come from the inventory (ENFORCED)")
	fmt.Println("\nA chassis Cargo.toml or package.json template names no version for a package dependencies.yaml declares: `just floors` checks the inventory's floors against OSV, and a literal here would ship a floor it never sees (tacplus#949).")
	violations, err := chassisManifestVersionViolations(root)
	if err != nil {
		fmt.Printf("- · scan failed: %v\n", err)
		return 1
	}
	for _, v := range violations {
		fmt.Printf("- `%s:%d` · %s = %q: the inventory declares %s (Rust: { workspace = true }; TypeScript: a template value)\n", v.file, v.line, v.pkg, v.version, v.pkg)
	}
	if len(violations) == 0 {
		fmt.Println("- ✓ no chassis manifest names an inventory package's version")
		return 0
	}
	return 1
}
