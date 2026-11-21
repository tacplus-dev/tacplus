// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdvisorRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, Filename), []byte(`language: rust
output-dir: model
kind: service
advisor:
  path: advisor
`), 0o644)
	cfg, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Advisor == nil || cfg.Advisor.Path != "advisor" {
		t.Fatalf("advisor = %+v", cfg.Advisor)
	}
}

func TestCheckAdvisorRefuses(t *testing.T) {
	for _, tc := range []struct {
		kind string
		a    *Advisor
		want string
	}{
		{"service", &Advisor{}, "advisor: path is required"},
		{"service", &Advisor{Path: "/abs/advisor"}, "must be a directory of this repo"},
		{"service", &Advisor{Path: "../elsewhere"}, "must be a directory of this repo"},
		{"service", &Advisor{Path: "a/../../b"}, "must be a directory of this repo"},
		{"service", &Advisor{Path: "."}, "must be its own directory"},
		{"client", &Advisor{Path: "advisor"}, "only a service has an advisor"},
		{"library", &Advisor{Path: "advisor"}, "only a service has an advisor"},
	} {
		err := CheckAdvisor(tc.kind, tc.a)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckAdvisor(%q, %+v) = %v, want %q", tc.kind, tc.a, err, tc.want)
		}
	}
	for _, kind := range []string{"", "service"} {
		if err := CheckAdvisor(kind, &Advisor{Path: "crates/advisor"}); err != nil {
			t.Errorf("CheckAdvisor(%q) = %v", kind, err)
		}
	}
	if err := CheckAdvisor("client", nil); err != nil {
		t.Errorf("CheckAdvisor(nil) = %v", err)
	}
}

func TestAdvisorReadRefusesAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, Filename), []byte(`language: rust
output-dir: model
advisor: {path: /tmp/advisor}
`), 0o644)
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "advisor") {
		t.Fatalf("Read = %v, want the advisor refused", err)
	}
	if _, err := ReadFiles([]string{filepath.Join(dir, Filename)}); err == nil || !strings.Contains(err.Error(), "advisor") {
		t.Fatalf("ReadFiles = %v, want the advisor refused", err)
	}
}

func TestMergeConfigsAdvisorOverlayWins(t *testing.T) {
	base := &Config{Advisor: &Advisor{Path: "a"}}
	if got := MergeConfigs(base, &Config{}); got.Advisor == nil || got.Advisor.Path != "a" {
		t.Errorf("an overlay without advisor dropped the base's: %+v", got.Advisor)
	}
	if got := MergeConfigs(base, &Config{Advisor: &Advisor{Path: "b"}}); got.Advisor.Path != "b" {
		t.Errorf("overlay advisor = %+v, want b", got.Advisor)
	}
}
