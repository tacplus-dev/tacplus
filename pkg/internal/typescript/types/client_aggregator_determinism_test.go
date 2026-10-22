// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestTsRestRootAggregatorGroupsAreSorted and
// TestTsGrpcOverRestRootAggregatorGroupsAreSorted are GEN-DET-1 regression
// guards. Both aggregators built groupNames by ranging the `groups` map, so the
// root rest.ts / grpc_over_rest.ts byte-diffed across otherwise-identical runs
// whenever a build had >=2 client groups — and worse, each also picks
// groupNames[0] to decide WHICH group re-exports the shared RestTransport type,
// so the CONTENT (not just the order) of the emitted file changed run to run.
//
// The cross-test determinism gate cannot catch this: its fixture never resolves
// >=2 typescript client groups. Drive the emitters directly, three groups in
// reverse order, repeated: each run is an independent randomized map walk.
func TestTsRestRootAggregatorGroupsAreSorted(t *testing.T) {
	const runs = 20

	var first string
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		config := &tactype.Config{OutputDir: dir}
		if err := generateTsRestClientAggregators(newDeterminismTsServices(config)); err != nil {
			t.Fatalf("generateTsRestClientAggregators: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, config.GetSmPrefix(), "internal", "rest", "rest.ts"))
		if err != nil {
			t.Fatalf("read root rest.ts: %v", err)
		}
		rendered := string(raw)

		assertTsGroupExportsInOrder(t, rendered, "rests")
		// The RestTransport re-export must come from the lowest-sorted group,
		// not from whichever group the map walk happened to yield first.
		if !strings.Contains(rendered, "export type { RestTransport } from './common/") {
			t.Fatalf("root rest.ts must re-export RestTransport from the first sorted group (common):\n%s", rendered)
		}

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("root rest.ts is not byte-identical across runs (run %d differs); group order is nondeterministic", i)
		}
	}
}

func TestTsGrpcOverRestRootAggregatorGroupsAreSorted(t *testing.T) {
	const runs = 20

	var first string
	for i := 0; i < runs; i++ {
		dir := t.TempDir()
		config := &tactype.Config{OutputDir: dir}
		if err := generateTsGrpcOverRestAggregators(newDeterminismTsServices(config), false); err != nil {
			t.Fatalf("generateTsGrpcOverRestAggregators: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, config.GetSmPrefix(), "internal", "grpc_over_rest", "grpc_over_rest.ts"))
		if err != nil {
			t.Fatalf("read root grpc_over_rest.ts: %v", err)
		}
		rendered := string(raw)

		assertTsGroupExportsInOrder(t, rendered, "grpc_over_rest")
		if !strings.Contains(rendered, "export type { RestTransport } from './common/grpc_over_rest.js';") {
			t.Fatalf("root grpc_over_rest.ts must re-export RestTransport from the first sorted group (common):\n%s", rendered)
		}

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("root grpc_over_rest.ts is not byte-identical across runs (run %d differs); group order is nondeterministic", i)
		}
	}
}

func newDeterminismTsServices(config *tactype.Config) []ServiceInfo {
	return []ServiceInfo{
		newDeterminismTsService(config, "services", "zebra", "Zebra"),
		newDeterminismTsService(config, "platform", "mango", "Mango"),
		newDeterminismTsService(config, "common", "apple", "Apple"),
	}
}

func newDeterminismTsService(config *tactype.Config, group, pkg, service string) ServiceInfo {
	return ServiceInfo{
		ProtoFile:   group + "/" + pkg + "/" + pkg + ".proto",
		PackageName: pkg,
		ServiceName: service,
		ParentDir:   group,
		OutputDir:   config.OutputDir,
		Config:      config,
	}
}

func assertTsGroupExportsInOrder(t *testing.T, rendered, moduleBase string) {
	t.Helper()
	prev := -1
	for _, group := range []string{"common", "platform", "services"} {
		needle := "export * from './" + group + "/" + moduleBase + ".js';"
		idx := strings.Index(rendered, needle)
		if idx < 0 {
			t.Fatalf("root aggregator missing re-export for group %q:\n%s", group, rendered)
		}
		if idx < prev {
			t.Fatalf("root aggregator group re-exports are not sorted (%q out of order):\n%s", group, rendered)
		}
		prev = idx
	}
}

// TestTsHelpersKeyEnumImportsAreSorted is a GEN-DET-1 regression guard for the
// fromMap key-enum leg: writeCombinedImports appended the extra key enums by
// ranging the keyEnumNames map, so helpers.ts' `import { … } from './x_pb.js'`
// list reordered across otherwise-identical runs whenever a proto file had >=2
// param-map messages whose key enums live in another file (so they are not
// already in the deterministic enumNames list).
func TestTsHelpersKeyEnumImportsAreSorted(t *testing.T) {
	const runs = 20

	pf := model.ProtoFile{
		Package: "settings",
		RelPath: "common/settings/settings.proto",
		Messages: []model.Message{
			{Name: "Zebra", ParamMapConfig: &model.ParamMapConfig{KeyEnum: "ZebraParamKey"}},
			{Name: "Mango", ParamMapConfig: &model.ParamMapConfig{KeyEnum: "MangoParamKey"}},
			{Name: "Apple", ParamMapConfig: &model.ParamMapConfig{KeyEnum: "AppleParamKey"}},
		},
	}

	var first string
	for i := 0; i < runs; i++ {
		var b strings.Builder
		writeCombinedImports(&b, pf, "settings", buildTsEnvVarContext(pf), map[string]bool{})
		rendered := b.String()

		prev := -1
		for _, enum := range []string{"AppleParamKey", "MangoParamKey", "ZebraParamKey"} {
			idx := strings.Index(rendered, enum)
			if idx < 0 {
				t.Fatalf("helpers imports missing key enum %q:\n%s", enum, rendered)
			}
			if idx < prev {
				t.Fatalf("helpers key-enum imports are not sorted (%q out of order):\n%s", enum, rendered)
			}
			prev = idx
		}

		if i == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("helpers imports are not byte-identical across runs (run %d differs); key-enum order is nondeterministic", i)
		}
	}
}
