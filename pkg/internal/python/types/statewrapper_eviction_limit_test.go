// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// (spec.eviction_max_size) was collected for every language but consumed only by
// the Go state wrapper, so a Python consumer declaring a bound got no snapshot
// check at all -- not even the default safety limit Go applies unannotated.
func TestPythonStateWrapperRendersCollectionSafetyLimit(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Collections: []pyCollectionBoundary{{
			FieldName:    "items",
			FieldPascal:  "Items",
			TypeName:     "Item",
			KeyFieldName: "item_id",
			MaxEntries:   250,
		}},
	})

	for _, expected := range []string{
		"if len(value.entries) > 250:",
		`"WatchItems snapshot exceeds max collection size - implement paging for large collections (received=%d, max=%d)",`,
		"                            250,",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Python state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}
