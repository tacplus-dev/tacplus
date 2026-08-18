// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// (spec.eviction_max_size) was collected for every language but consumed only by
// the Go state wrapper, so a TypeScript consumer declaring a bound got no
// snapshot check at all -- not even the default safety limit Go applies
// unannotated.
func TestTypeScriptStateWrapperRendersCollectionSafetyLimit(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Collections: []tsCollectionBoundary{{
			FieldName:    "items",
			FieldPascal:  "Items",
			TypeName:     "Item",
			TypePrefix:   "types",
			KeyFieldName: "itemId",
			MaxEntries:   250,
		}},
	})

	for _, expected := range []string{
		"const itemsReceived = Object.keys(value.entries ?? {}).length;",
		"if (itemsReceived > 250) {",
		"console.error('WatchItems snapshot exceeds max collection size - implement paging for large collections', { received: itemsReceived, max: 250 });",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered TypeScript state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}
