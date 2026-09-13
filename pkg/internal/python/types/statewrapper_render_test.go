// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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
