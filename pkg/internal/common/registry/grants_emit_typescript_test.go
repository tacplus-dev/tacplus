// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

func TestEmitTypeScriptGrantsHelper(t *testing.T) {
	grants := []*spec.EntitlementGrantInfo{
		{Token: "OPENAI_OPEN_AI_SERVICE_GET_API_KEY", Package: "openai", ServiceName: "OpenAIService", Rpc: "GetAPIKey"},
		{Token: "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS", Package: "openai", ServiceName: "OpenAIService", Rpc: "WatchAPIKeys"},
		{Token: "OPENAI_ANY", Package: "openai", CapabilityScope: "ANY", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY", "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS",
		}},
		{Token: "OPENAI_READ_ANY", Package: "openai", CapabilityScope: "READ", ExpandsTo: []string{
			"OPENAI_OPEN_AI_SERVICE_GET_API_KEY",
		}},
	}

	dir := t.TempDir()
	if err := emitTypeScriptGrantsHelper(Config{OutputDir: dir, Language: "typescript"}, grants); err != nil {
		t.Fatalf("emitTypeScriptGrantsHelper: %v", err)
	}

	path := filepath.Join(dir, "grants", "grants.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated grants.ts: %v", err)
	}
	src := string(data)

	// Catalog entries and the helper API must be present.
	for _, want := range []string{
		`"OPENAI_OPEN_AI_SERVICE_GET_API_KEY":`,
		`service: "OpenAIService"`,
		`rpc: "GetAPIKey"`,
		`"OPENAI_READ_ANY":`,
		`capability: "READ"`,
		`expandsTo: ["OPENAI_OPEN_AI_SERVICE_GET_API_KEY"]`,
		`"OPENAI_ANY":`,
		`capability: "ANY"`,
		`export interface Grant {`,
		`const byToken: Record<string, Grant> = {`,
		`export function lookup(token: string): Grant | undefined`,
		`export function isWildcard(token: string): boolean`,
		`export function expand(token: string): string[]`,
		`export function normalize(tokens: readonly string[]): string[]`,
		`export function unknownTokens(tokens: readonly string[]): string[]`,
		`export function containsAll(candidate: readonly string[], ceiling: readonly string[]): boolean`,
		`export function explain(token: string): string[]`,
		`Unknown grants stay fail-closed`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated grants.ts missing %q", want)
		}
	}

	// Wildcard entries precede their member tokens? No: order is by token string.
	// Sanity-check deterministic stable ordering: OPENAI_ANY sorts before
	// OPENAI_OPEN_AI_SERVICE_GET_API_KEY, which sorts before OPENAI_READ_ANY.
	iAny := strings.Index(src, `"OPENAI_ANY":`)
	iGet := strings.Index(src, `"OPENAI_OPEN_AI_SERVICE_GET_API_KEY":`)
	iRead := strings.Index(src, `"OPENAI_READ_ANY":`)
	if !(iAny < iGet && iGet < iRead) {
		t.Errorf("catalog entries not in stable token order: ANY=%d GET=%d READ=%d", iAny, iGet, iRead)
	}
}
