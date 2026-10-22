// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The GCS archive sink imports the Google Cloud Storage SDK. It must be emitted
// only for a project that pins that SDK; otherwise every Java consumer of the
// chassis would need the jar to compile.
func TestJavaGcsArchiveEnabledOnlyWhenSDKIsPinned(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.yaml")
	unpinned := filepath.Join(dir, "unpinned.yaml")
	if err := os.WriteFile(pinned, []byte("lang:\n  java:\n    dependencies:\n      com.google.cloud:google-cloud-storage: 2.75.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unpinned, []byte("lang:\n  java:\n    dependencies:\n      com.google.protobuf:protobuf-java: 4.36.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"sdk pinned for java", pinned, true},
		{"other java deps only", unpinned, false},
		{"no dependencies.yaml path", "", false},
		{"dependencies.yaml missing", filepath.Join(dir, "absent.yaml"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := javaGcsArchiveEnabled(&tactype.Config{DepsYAMLPath: c.path})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("javaGcsArchiveEnabled = %v, want %v", got, c.want)
			}
		})
	}
}
