// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// TestRustRestRootAggregatorGroupsAreSorted is a GEN-DET-1 regression guard.
// generateRustRestClientAggregators built groupNames by ranging the `groups`
// map, so the root rest.rs — `use` lines, struct fields, constructor lines and
// accessors, none of which any downstream formatter reorders — byte-diffed
// across otherwise-identical runs whenever a build had >=2 REST client groups.
// No cross-test fixture resolves >=2 rust REST client groups, so the
// determinism gate cannot see it; drive the emitter directly instead.
func TestRustRestRootAggregatorGroupsAreSorted(t *testing.T) {
	const runs = 20

	var first string
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		config := &tactype.Config{OutputDir: dir}
		services := []ServiceInfo{
			newDeterminismRustRestService(config, "services", "zebra", "Zebra"),
			newDeterminismRustRestService(config, "platform", "mango", "Mango"),
			newDeterminismRustRestService(config, "common", "apple", "Apple"),
		}
		if err := generateRustRestClientAggregators(services); err != nil {
			t.Fatalf("generateRustRestClientAggregators: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, config.GetSmPrefix(), "internal", "rest", "rest.rs"))
		if err != nil {
			t.Fatalf("read root rest.rs: %v", err)
		}
		rendered := string(raw)

		prev := -1
		for _, group := range []string{"common", "platform", "services"} {
			needle := "use super::" + group + "::rests::RestClients"
			idx := strings.Index(rendered, needle)
			if idx < 0 {
				t.Fatalf("root rest.rs missing use line for group %q:\n%s", group, rendered)
			}
			if idx < prev {
				t.Fatalf("root rest.rs group use lines are not sorted (%q out of order):\n%s", group, rendered)
			}
			prev = idx
		}

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("root rest.rs is not byte-identical across runs (run %d differs); group order is nondeterministic", i)
		}
	}
}

func newDeterminismRustRestService(config *tactype.Config, group, pkg, service string) ServiceInfo {
	return ServiceInfo{
		ProtoFile:   group + "/" + pkg + "/" + pkg + ".proto",
		PackageName: pkg,
		ServiceName: service,
		ParentDir:   group,
		OutputDir:   config.OutputDir,
		Config:      config,
	}
}
