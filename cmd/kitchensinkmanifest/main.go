// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command kitchensinkmanifest rewrites pkg/coverage/kitchensink.json, the
// coverage manifest tacplus ships, from cross-test/kitchensink/members.yaml
// (tacplus#196). Run it as `just kitchensink-manifest` after changing a member.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tacplus-dev/tacplus/pkg/coverage"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	m, err := coverage.Compute(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kitchensink-manifest:", err)
		os.Exit(1)
	}
	data, err := m.Marshal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kitchensink-manifest:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(*root, coverage.ManifestPath), data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "kitchensink-manifest:", err)
		os.Exit(1)
	}
	fmt.Printf("kitchensink-manifest: %d features\n", len(m.Features))
}
