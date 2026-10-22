// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestPythonStateWrapperWatchLifecycleRenders(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []pySingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
		}},
		Collections: []pyCollectionBoundary{{
			FieldName:    "items",
			FieldPascal:  "Items",
			TypeName:     "Item",
			KeyFieldName: "item_id",
		}},
	})

	for _, expected := range []string{
		"from ......types.services.demo import demo_pb2_grpc as pb_grpc",
		"from ......types.sys.trace import trace_pb2",
		"@dataclass(frozen=True)",
		"class WatchStatus:",
		"def set_watch_status_listener(self, listener: Optional[Callable[[WatchStatus], None]]) -> None:",
		"def watch_statuses(self) -> dict[str, WatchStatus]:",
		"def watches_healthy(self) -> bool:",
		"self._stub = pb_grpc.DemoStateServiceStub(channel)",
		"asyncio.create_task(self._watch_config())",
		"asyncio.create_task(self._watch_items())",
		"await asyncio.gather(*tasks, return_exceptions=True)",
		"self._ensure_stub().WatchConfig(",
		"self._ensure_stub().WatchItems(",
		"trace_pb2.TraceRequest()",
		"metadata=await self._metadata()",
		"async for value in stream:",
		"self._record_watch_healthy(\"WatchConfig\", False)",
		"self._record_watch_retrying(\"WatchConfig\", received, attempt, \"stream completed\", None)",
		"self._items = {",
		"for k, v in value.entries.items()",
		"await self._sleep_before_watch_retry(attempt)",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Python state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}

// The singleton fetch retries while the fetched value fails its own generated
// validation, bounded by config_fetch_timeout_seconds — see
// decisions/state-singleton-startup-retry-2026-09-20.md.
func TestPythonStateWrapperSingletonFetchRetriesOnValidationFailure(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []pySingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			ValidateRef: "pb_helpers.validate_config",
		}},
	})

	for _, expected := range []string{
		"from ......types.services.demo import helpers as pb_helpers",
		"self._config_fetch_timeout_seconds = 30",
		"def set_config_fetch_timeout_seconds(self, seconds: int) -> None:",
		"async def _sleep_before_fetch_retry(self, attempt: int, deadline: float) -> bool:",
		"remaining = deadline - time.monotonic()",
		"await asyncio.sleep(min(self._watch_retry_delay(attempt), remaining))",
		"timeout = self._config_fetch_timeout_seconds",
		"deadline = time.monotonic() + timeout",
		"pb_helpers.validate_config(value)",
		"if not await self._sleep_before_fetch_retry(attempt, deadline):",
		"CODE_FAILED_PRECONDITION,",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Python state wrapper missing singleton fetch retry surface %q:\n%s", expected, rendered)
		}
	}
}

// The singleton fetch must default the fetched value before validating it: the
// wire carries no proto defaults, so a field omitted because it declares a
// (spec.default_*) arrives zero-valued and fails its own (spec.assert_*) check
// for the whole config_fetch_timeout_seconds budget. See the 2026-09-23
// scottyagent crash and the cross-language pin in
// pkg/internal/common/state_singleton_fetch_defaults_parity_test.go.
func TestPythonStateWrapperSingletonFetchDefaultsBeforeValidate(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []pySingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			ValidateRef: "pb_helpers.validate_config",
			DefaultsRef: "pb_helpers.config_defaults",
		}},
	})

	const want = "            pb_helpers.config_defaults(value)\n            try:\n                pb_helpers.validate_config(value)"
	if !strings.Contains(rendered, want) {
		t.Fatalf("rendered Python singleton fetch does not apply config_defaults before validate:\n%s", rendered)
	}
}

func TestPythonStateClientAggregatorsUseWrapperClientClassNames(t *testing.T) {
	outputDir := t.TempDir()
	err := generatePyStateClientAggregators(&tactype.Config{
		OutputDir: outputDir,
		SmPrefix:  "s2sm",
	}, []pyGeneratedStateClient{{
		PackageName:   "demo",
		DirName:       "demo_config",
		StateRootName: "DemoConfigState",
		Group:         "services",
	}})
	if err != nil {
		t.Fatalf("generatePyStateClientAggregators: %v", err)
	}

	renderedBytes, err := os.ReadFile(filepath.Join(outputDir, "s2sm", "internal", "state", "services", "state_clients.py"))
	if err != nil {
		t.Fatalf("read group state_clients.py: %v", err)
	}
	rendered := string(renderedBytes)

	for _, expected := range []string{
		"from .demo_config.state import DemoConfigStateClient",
		"self._demo_config_state = DemoConfigStateClient()",
		"self._demo_config_state.set_channel(channel, token_provider)",
		"def set_config_fetch_timeout_seconds(self, seconds: int) -> None:",
		"await self._demo_config_state.close()",
		"def demo_config_state(self) -> DemoConfigStateClient:",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Python state aggregator missing %q:\n%s", expected, rendered)
		}
	}

	if strings.Contains(rendered, "DemoConfigStateStateClient") {
		t.Fatalf("rendered Python state aggregator still uses stale StateStateClient name:\n%s", rendered)
	}
}

func renderPyStateWrapperForTest(t *testing.T, data pyStateWrapperData) string {
	t.Helper()
	tmplStr, err := loadTemplate("statewrapper.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("statewrapper").Funcs(template.FuncMap{
		"snakeCase": strutil.ToSnakeCase,
		"pyGet":     pyFieldGet,
	}).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	return out.String()
}
