// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"go/format"
	"strings"
	"testing"
	"text/template"
)

func TestStateWrapperHasNoLockMachineryRender(t *testing.T) {
	readOnly := renderStateWrapperForTest(t, stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		ReadOnly:              true,
		Singletons: []singletonBoundary{{
			FieldName: "Config",
			TypeName:  "Config",
			GoType:    "Config",
		}},
	})
	// fmt has exactly one user left: the singleton Fetch's validate-retry timeout
	// error. Any second use would mean the removed locked accessors came back.
	if got := strings.Count(readOnly, "fmt."); got != 1 {
		t.Fatalf("read-only state wrapper has %d fmt uses, want exactly 1 (the singleton Fetch timeout error):\n%s", got, readOnly)
	}
	for _, expected := range []string{
		"type WatchStatus struct",
		"func (c *StateClient) SetWatchStatusListener(listener func(WatchStatus))",
		"func (c *StateClient) WatchStatuses() map[string]WatchStatus",
		"func (c *StateClient) WatchesHealthy() bool",
		"c.recordWatchHealthy(\"WatchConfig\", false)",
		"c.recordWatchRetrying(\"WatchConfig\", received, attempt+1, \"stream ended\", nil)",
	} {
		if !strings.Contains(readOnly, expected) {
			t.Fatalf("read-only state wrapper missing watch status surface %q:\n%s", expected, readOnly)
		}
	}

	// The ATOMIC / optimistic-locking machinery was removed (state is convergent
	// only), so a writable state wrapper must emit no lock surface at all, and must
	// not import fmt (only the removed locked accessors needed it).
	writable := renderStateWrapperForTest(t, stateClientData{
		PackageName:           "demo",
		ClientTypeName:        "StateClient",
		StateServiceName:      "DemoStateService",
		StateRootName:         "DemoState",
		TypesImportPath:       "example.com/project/tacplus/types/services/demo",
		ServiceManagerLibPath: "example.com/project/tacplus/chassis/servicemanager",
		TraceTypesPath:        "example.com/project/tacplus/types/sys/trace",
		Singletons: []singletonBoundary{{
			FieldName: "Config",
			TypeName:  "Config",
			GoType:    "Config",
		}},
	})
	for _, banned := range []string{"lock_pb", "lockClient", "releaseLock", "GetLock", "GetLocked", "SetLocked", "CancelLock"} {
		if strings.Contains(writable, banned) {
			t.Fatalf("writable state wrapper still contains removed lock machinery %q:\n%s", banned, writable)
		}
	}
	if got := strings.Count(writable, "fmt."); got != 1 {
		t.Fatalf("writable state wrapper has %d fmt uses, want exactly 1 (the singleton Fetch timeout error):\n%s", got, writable)
	}

	// The singleton Fetch retries while the fetched value fails its own generated
	// validation, bounded by config_fetch_timeout_seconds — see
	// decisions/state-singleton-startup-retry-2026-09-20.md.
	for _, expected := range []string{
		"func (c *StateClient) configFetchTimeout() time.Duration {",
		"c.sc.Config().GetConfigFetchTimeoutSeconds()",
		"func (c *StateClient) sleepBeforeFetchRetry(ctx context.Context, attempt int, deadline time.Time) bool {",
		"delay := c.watchRetryDelay(attempt)",
		"timeout := w.parent.configFetchTimeout()",
		// Proto defaults are applied to the fetched value BEFORE it is validated;
		// the wire carries no defaults, so an omitted (spec.default_*) field would
		// otherwise fail its own (spec.assert_*) check for the whole retry budget.
		"types.ConfigDefaults(value)\n\t\tvalidateErr := types.ValidateConfig(value)",
		"if !w.parent.sleepBeforeFetchRetry(ctx, attempt, deadline) {",
		"return nil, fmt.Errorf(\"GetConfig: state value still failed validation after %s: %w\", timeout, validateErr)",
	} {
		if !strings.Contains(writable, expected) {
			t.Fatalf("writable state wrapper missing singleton fetch retry surface %q:\n%s", expected, writable)
		}
	}
}

func renderStateWrapperForTest(t *testing.T, data stateClientData) string {
	t.Helper()
	tmplStr, err := loadTemplate("state_wrapper.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	funcMap := template.FuncMap{
		"TypeRef": func(goType string, isCross bool) string {
			if isCross {
				return goType
			}
			return "types." + goType
		},
		"ValidateRef": func(goType string, isCross bool) string {
			if isCross {
				if idx := strings.LastIndex(goType, "."); idx >= 0 {
					return goType[:idx+1] + "Validate" + goType[idx+1:]
				}
				return "Validate" + goType
			}
			return "types.Validate" + goType
		},
		"DefaultsRef": func(goType string, isCross bool) string {
			if isCross {
				return goType + "Defaults"
			}
			return "types." + goType + "Defaults"
		},
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}
	tmpl, err := template.New("stateclient").Funcs(funcMap).Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	src, err := format.Source([]byte(out.String()))
	if err != nil {
		t.Fatalf("format state wrapper template: %v\n%s", err, out.String())
	}
	return string(src)
}
