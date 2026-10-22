// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestGoRootStateWrapperGroupsAreSorted is a regression guard for the GEN-DET-1
// class (commit 6bae90cc, go groupList leg): generateRootStateWrappers built its
// group list by ranging the `groups` map, so the root state.go — whose group
// struct fields, init statements, Close() calls and getters gofmt never reorders
// — byte-diffed across otherwise-identical runs whenever a build had >=2 groups.
//
// The cross-test determinism gate cannot catch this: no cross-test service spans
// >=2 state groups, so the map only ever holds a single (deterministic) entry.
// This test drives the emitter directly with three groups supplied in reverse
// order, and repeats the generation so a reverted sort cannot pass by landing on
// the sorted permutation once (each run is an independent randomized map walk).
func TestGoRootStateWrapperGroupsAreSorted(t *testing.T) {
	const runs = 20

	var first string
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		config := &tactype.Config{
			OutputDir:      dir,
			ModuleBasePath: "example.com/proj/tacplus",
		}
		clients := []generatedStateClient{
			newDeterminismStateClient("services", "zebra"),
			newDeterminismStateClient("platform", "mango"),
			newDeterminismStateClient("common", "apple"),
		}
		if err := generateRootStateWrappers(config, clients); err != nil {
			t.Fatalf("generateRootStateWrappers: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, config.GetSmPrefix(), "state", "state.go"))
		if err != nil {
			t.Fatalf("read root state.go: %v", err)
		}
		rendered := string(raw)

		// Sorted group order: common < platform < services.
		assertSubstringsInOrder(t, rendered,
			"common_stateclients",
			"platform_stateclients",
			"services_stateclients",
		)

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("root state.go is not byte-identical across runs (run %d differs); group order is nondeterministic", i)
		}
	}
}

func newDeterminismStateClient(group, dirName string) generatedStateClient {
	return generatedStateClient{
		PackageName:    dirName,
		DirName:        dirName,
		StateRootName:  strings.ToUpper(dirName[:1]) + dirName[1:],
		ClientTypeName: "StateClient",
		ImportPath:     "example.com/proj/tacplus/s2sm/internal/state/" + group + "/" + dirName,
		Group:          group,
	}
}
