// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

func TestEmitJavaGrantsHelper(t *testing.T) {
	grants := []*spec.EntitlementGrantInfo{
		{Token: "OPENAI_OPEN_AI_SERVICE_GET_API_KEY", Package: "openai", ServiceName: "OpenAIService", Rpc: "GetAPIKey"},
		{Token: "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS", Package: "openai", ServiceName: "OpenAIService", Rpc: "WatchAPIKeys"},
		{Token: "OPENAI_ANY", Package: "openai", CapabilityScope: "ANY", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY", "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS",
		}},
		{Token: "OPENAI_READ_ANY", Package: "openai", CapabilityScope: "READ", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY",
		}},
		{Token: "TASKS_DELEGATION_PROBE_BUS_STATE_SERVICE_PUBLISH_TO_STREAM", Package: "tasks",
			ServiceName: "DelegationProbeBusStateService", Rpc: "PublishToStream",
			RoutingAliasPackages: []string{"eventbus"}},
	}

	dir := t.TempDir()
	cfg := Config{OutputDir: dir, Language: "java", PackageBase: "com.example.app.model"}
	if err := emitJavaGrantsHelper(cfg, grants); err != nil {
		t.Fatalf("emitJavaGrantsHelper: %v", err)
	}

	// Class must land at the package path under the maven-compiled source dir.
	path := filepath.Join(dir, "src", "main", "java", "com", "example", "app", "model", "grants", "Grants.java")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated Grants.java: %v", err)
	}
	src := string(data)

	// Catalog entries, package declaration, and helper API must be present.
	for _, want := range []string{
		"package com.example.app.model.grants;",
		"public final class Grants {",
		`"OPENAI_OPEN_AI_SERVICE_GET_API_KEY"`,
		`"OpenAIService"`,
		`"GetAPIKey"`,
		`"OPENAI_READ_ANY"`,
		`"READ"`,
		`List.of("OPENAI_OPEN_AI_SERVICE_GET_API_KEY")`,
		"public static List<String> expand(String token)",
		"public static List<String> normalize(List<String> tokens)",
		"public static List<String> unknownTokens(List<String> tokens)",
		"public static boolean containsAll(List<String> candidate, List<String> ceiling)",
		"public static List<String> explain(String token)",
		"public static List<String> tokens()",
		"public static Optional<Grant> lookup(String token)",
		"public static boolean isWildcard(String token)",
		"stay fail-closed",
		`"DelegationProbeBusStateService"`,
		`"PublishToStream"`,
		`List.of("eventbus")`,
		"public final List<String> routingAliasPackages;",
		"public static boolean authorizesAudience(String token, String targetPackage)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated Grants.java missing %q", want)
		}
	}
}

func TestEmitJavaGrantsHelperEmptyCatalog(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{OutputDir: dir, Language: "java", PackageBase: "com.example.app.model"}
	if err := emitJavaGrantsHelper(cfg, nil); err != nil {
		t.Fatalf("emitJavaGrantsHelper empty: %v", err)
	}
	path := filepath.Join(dir, "src", "main", "java", "com", "example", "app", "model", "grants", "Grants.java")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected Grants.java to be written for an empty catalog: %v", err)
	}
}
