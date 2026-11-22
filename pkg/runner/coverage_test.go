// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/coverage"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// uncoveredFeature is a spec option no kitchensink member uses. If the
// kitchensink comes to cover it, pick another: the test says so.
const uncoveredFeature = "field/assert_sql"

const uncoveredProto = `syntax = "proto3";

package reports;

import "sys/spec.proto";

message ReportQuery {
  string sql = 1 [(spec.assert_sql) = SQL_PERMIT_SELECT];
}
`

// A service that uses a feature the kitchensink does not cover fails the
// build, naming the feature; --allow-uncovered builds it with a warning
// (tacplus#196).
func Test_Build_refusesFeatureTheKitchensinkDoesNotCover(t *testing.T) {
	manifest, err := coverage.Release()
	if err != nil {
		t.Fatal(err)
	}
	if langs := manifest.Features[uncoveredFeature]; len(langs) > 0 {
		t.Fatalf("the kitchensink now covers %s (%v): pick a feature it does not cover for this test", uncoveredFeature, langs)
	}

	protoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(protoDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "services", "reports.proto"), []byte(uncoveredProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "dependencies.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	params := func(allow bool) Params {
		return Params{
			ProtoSrcs:   []string{protoDir},
			SrcExplicit: true,
			Config: &tacplusyaml.Config{
				Language:  "go",
				Pkg:       "com.example.services.reports",
				OutputDir: "model",
				Src:       protoDir,
			},
			AllowUncovered: allow,
		}
	}

	t.Chdir(t.TempDir())
	err = Build(params(false))
	if err == nil {
		t.Fatalf("Build of a service using %s succeeded; want the coverage check to refuse it", uncoveredFeature)
	}
	for _, want := range []string{"coverage:", uncoveredFeature, "--allow-uncovered"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Build error does not mention %q:\n%v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join("model", "go.mod")); statErr == nil {
		t.Errorf("a refused build wrote output")
	}

	t.Chdir(t.TempDir())
	if err := Build(params(true)); err != nil {
		t.Fatalf("Build with AllowUncovered: %v", err)
	}
}
