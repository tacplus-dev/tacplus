// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
	"text/template"
)

func TestRustStateWrapperWatchStatusRenders(t *testing.T) {
	rendered := renderRustStateWrapperForTest(t, rustStateWrapperData{
		Header:                 "// generated for test",
		StateRootName:          "DemoState",
		TypesCratePath:         "services::demo",
		PackageName:            "demo",
		StateServiceName:       "DemoStateService",
		StateServiceModule:     "demo_state_service_client",
		StateServiceClientType: "DemoStateServiceClient",
		Singletons: []rustSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
		}},
	})

	for _, expected := range []string{
		"pub struct WatchStatus",
		"pub type WatchStatusListener = Arc<dyn Fn(WatchStatus) + Send + Sync>;",
		"pub fn set_watch_status_listener(&self, listener: Option<WatchStatusListener>)",
		"pub fn watch_statuses(&self) -> HashMap<String, WatchStatus>",
		"pub fn watches_healthy(&self) -> bool",
		"self.record_watch_healthy(\"WatchConfig\", false);",
		"self.record_watch_retrying(\"WatchConfig\", made_progress, next_attempt, \"stream ended\", None);",
		"_ = &mut close => return Ok(false),",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Rust state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}

func TestRustStateWrapperOwnsSingleHashMapImport(t *testing.T) {
	rendered := renderRustStateWrapperForTest(t, rustStateWrapperData{
		Header:                 "// generated for test",
		StateRootName:          "DemoState",
		TypesCratePath:         "services::demo",
		PackageName:            "demo",
		StateServiceName:       "DemoStateService",
		StateServiceModule:     "demo_state_service_client",
		StateServiceClientType: "DemoStateServiceClient",
		Collections: []rustCollectionBoundary{{
			FieldName:    "items",
			FieldPascal:  "Items",
			TypeName:     "Item",
			KeyFieldName: "item_id",
		}},
	})

	if got := strings.Count(rendered, "use std::collections::HashMap;"); got != 1 {
		t.Fatalf("Rust state wrapper rendered %d HashMap imports, want 1:\n%s", got, rendered)
	}
}

func renderRustStateWrapperForTest(t *testing.T, data rustStateWrapperData) string {
	t.Helper()
	tmplStr, err := loadTemplate("statewrapper.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("statewrapper").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	return out.String()
}
