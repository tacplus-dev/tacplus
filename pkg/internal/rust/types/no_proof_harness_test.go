// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestGenerateEmitsNoKaniHarness: a service's generated Rust carries no proof
// harness (tacplus#196). The generator is verified once per release, on the
// kitchensink (cross-test/kitchensink); `tacplus verify` on a service does not
// re-prove generated code. The kitchensink member is the input because every
// assertion the old harness generator modelled is on one of its fields.
func TestGenerateEmitsNoKaniHarness(t *testing.T) {
	if _, err := exec.LookPath("protoc-gen-prost"); err != nil && os.Getenv("PROTOC_RUST_BINARY") == "" {
		t.Skip("protoc-gen-prost not on PATH")
	}
	inputDir := filepath.Join("..", "..", "..", "..", "cross-test", "kitchensink", "proto")
	flatProtoDir := filepath.Join(t.TempDir(), "flat")
	if err := os.CopyFS(flatProtoDir, os.DirFS(inputDir)); err != nil {
		t.Fatalf("copy kitchensink proto: %v", err)
	}
	if err := protogen.CopySysProtos(flatProtoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(flatProtoDir, flatProtoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	packages.SetProtoDir(inputDir)
	defer packages.SetProtoDir("")

	outputDir := filepath.Join(t.TempDir(), "rust")
	config := &tactype.Config{
		Language:      "rust",
		InputDir:      inputDir,
		FlatProtoDir:  flatProtoDir,
		OutputDir:     outputDir,
		SelfPackage:   "kitchensink",
		CrateBasePath: "kitchensink-model",
	}
	if err := Generate(config); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	sawBoundsValidate := false
	err := filepath.WalkDir(outputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".rs") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(data)
		for _, banned := range []string{"kani::proof", "kani::any", "cfg(kani)"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s: generated Rust carries a proof harness (%q)", path, banned)
			}
		}
		if strings.Contains(src, "impl Bounds") && strings.Contains(src, "fn validate") {
			sawBoundsValidate = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", outputDir, err)
	}
	if !sawBoundsValidate {
		t.Fatalf("no generated validate() for kitchensink Bounds: the test no longer looks at the code the harnesses were generated into")
	}
}
