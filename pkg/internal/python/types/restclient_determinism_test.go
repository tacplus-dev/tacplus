// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// TestPyRestRootAggregatorGroupsAreSorted is a GEN-DET-1 regression guard.
// generatePyRestClientAggregators built groupNames by ranging the `groups` map,
// so the root rest.py — whose import lines, constructor statements and property
// blocks nothing downstream reorders — byte-diffed across otherwise-identical
// runs whenever a build had >=2 REST client groups.
//
// The cross-test determinism gate cannot catch this: its fixture resolves no
// python REST client services at all, so the aggregators are never emitted.
// Drive the emitter directly with three groups supplied in reverse order, and
// repeat: each run is an independent randomized map walk, so a reverted sort
// cannot pass by landing on the sorted permutation once.
func TestPyRestRootAggregatorGroupsAreSorted(t *testing.T) {
	const runs = 20

	var first string
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		config := &tactype.Config{OutputDir: dir}
		services := []ServiceInfo{
			newDeterminismRestService(config, "services", "zebra", "Zebra"),
			newDeterminismRestService(config, "platform", "mango", "Mango"),
			newDeterminismRestService(config, "common", "apple", "Apple"),
		}
		if err := generatePyRestClientAggregators(services); err != nil {
			t.Fatalf("generatePyRestClientAggregators: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, config.GetSmPrefix(), "internal", "rest", "rest.py"))
		if err != nil {
			t.Fatalf("read root rest.py: %v", err)
		}
		rendered := string(raw)

		assertPyGroupsInOrder(t, rendered, "common", "platform", "services")

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("root rest.py is not byte-identical across runs (run %d differs); group order is nondeterministic", i)
		}
	}
}

func newDeterminismRestService(config *tactype.Config, group, pkg, service string) ServiceInfo {
	return ServiceInfo{
		ProtoFile:   group + "/" + pkg + "/" + pkg + ".proto",
		PackageName: pkg,
		ServiceName: service,
		ParentDir:   group,
		OutputDir:   config.OutputDir,
		Config:      config,
	}
}

func assertPyGroupsInOrder(t *testing.T, rendered string, groups ...string) {
	t.Helper()
	prev := -1
	for _, group := range groups {
		needle := "from ." + group + ".rests import RestClients"
		idx := strings.Index(rendered, needle)
		if idx < 0 {
			t.Fatalf("root rest.py missing import for group %q:\n%s", group, rendered)
		}
		if idx < prev {
			t.Fatalf("root rest.py group imports are not sorted (%q out of order):\n%s", group, rendered)
		}
		prev = idx
	}
}
