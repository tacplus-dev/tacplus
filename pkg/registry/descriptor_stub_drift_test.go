// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T2: the descriptor-only stub protos written by writeDescriptorGeneratedStubs
// are a parallel, hand-maintained model that can silently drift from the real
// generated surface emitted by pkg/internal/proto/configholder.go. This guard
// fails if the stub's stable, field-bearing shapes diverge from the canonical
// emitter, so a change to (e.g.) the TokenEntitlements field numbers or the
// ServiceName enum sentinels cannot land on one side only.
func TestDescriptorStubsMatchCanonicalSurface(t *testing.T) {
	dir := t.TempDir()
	if err := writeDescriptorGeneratedStubs(dir); err != nil {
		t.Fatalf("writeDescriptorGeneratedStubs: %v", err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, "generated", name))
		if err != nil {
			t.Fatalf("read stub %s: %v", name, err)
		}
		return string(b)
	}

	// Canonical, input-independent shape lines the descriptor stubs MUST mirror.
	canonical := map[string][]string{
		"services.proto": {
			"package services;",
			"enum ServiceName {",
			"SERVICE_NAME_UNSPECIFIED = 0;",
			"SERVICE_NAME_ANY = 99;",
		},
		"entitlements.proto": {
			"package entitlements;",
			"message TokenEntitlements {",
			"repeated string grants = 1;",
			"repeated string azp = 100;",
		},
		"services_config.proto": {
			"package services_config;",
			"message Services {",
		},
		"accounting_method.proto": {
			"package accounting_method;",
			"message AccountingMethod {",
		},
		"tenant_config.proto": {
			"package tenant_config;",
			"message TenantConfig {",
		},
		"project_config.proto": {
			"package project_config;",
			"message ProjectConfig {",
		},
	}
	for file, lines := range canonical {
		got := read(file)
		for _, want := range lines {
			if !strings.Contains(got, want) {
				t.Errorf("descriptor stub %s missing canonical shape %q", file, want)
			}
		}
	}

	// Cross-check the field-bearing shapes against the canonical emitter
	// (configholder.go). If the real generator changes these stable field
	// numbers / enum sentinels, the stub (and this guard) must be updated in
	// lockstep — otherwise the descriptor-only scan model silently drifts.
	canonicalSrc := readCanonicalEmitterSource(t)
	for _, want := range []string{
		"SERVICE_NAME_UNSPECIFIED = 0;",
		"SERVICE_NAME_ANY = 99;",
		"repeated string grants = 1;",
		"repeated string azp = 100;",
		"package services_config;",
		"message Services {",
	} {
		if !strings.Contains(canonicalSrc, want) {
			t.Errorf("configholder.go no longer declares %q — descriptor stub may have drifted; update writeDescriptorGeneratedStubs and this guard", want)
		}
	}
}

// readCanonicalEmitterSource returns the source of the canonical descriptor
// emitter. go test runs with CWD = the package dir (pkg/registry), and the
// emitter lives under pkg/internal/proto.
func readCanonicalEmitterSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "internal", "proto", "configholder.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical emitter %s: %v", path, err)
	}
	return string(b)
}
