// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// (spec.eviction_max_size) was collected for every language but consumed only by
// the Go state wrapper, so a Rust consumer declaring a bound got no snapshot
// check at all -- not even the default safety limit Go applies unannotated.
func TestRustStateWrapperRendersCollectionSafetyLimit(t *testing.T) {
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
			MaxEntries:   250,
		}},
	})

	for _, expected := range []string{
		"if resp.entries.len() > 250 {",
		"max = 250,",
		`"WatchItems snapshot exceeds max collection size - implement paging for large collections"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Rust state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}
