// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
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
	if strings.Contains(readOnly, "\"fmt\"") {
		t.Fatalf("read-only state wrapper imported fmt without locked accessors:\n%s", readOnly)
	}
	if strings.Contains(readOnly, "fmt.") {
		t.Fatalf("read-only state wrapper unexpectedly rendered fmt usage:\n%s", readOnly)
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
	if strings.Contains(writable, "\"fmt\"") {
		t.Fatalf("writable state wrapper imported fmt (only the removed locked accessors needed it):\n%s", writable)
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
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}
	tmpl, err := template.New("stateclient").Funcs(funcMap).Parse(tmplStr)
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
