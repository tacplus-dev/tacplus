// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Go is not formally verified, by design (tacplus-dev/roadmap:
// decisions/tacplus-images-2026-10-27.md): verify reports it as skipped and
// succeeds, so one pipeline runs verify for every language.
func TestVerifyGoIsSkipped(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("tacplus.yaml", []byte("language: go\noutput-dir: model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	t.Cleanup(func() { rootCmd.SetOut(nil) })

	if err := runVerify(rootCmd, nil); err != nil {
		t.Fatalf("verify of a Go project = %v, want skipped", err)
	}
	if got := out.String(); !strings.Contains(got, "go: skipped") {
		t.Fatalf("verify of a Go project printed %q, want it reported as skipped", got)
	}
}

func TestVerifyUnknownLanguageFails(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("tacplus.yaml", []byte("language: typescript\noutput-dir: model\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runVerify(rootCmd, nil); err == nil || !strings.Contains(err.Error(), "no verification tool") {
		t.Fatalf("verify of a TypeScript project = %v, want no verification tool", err)
	}
}
