// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// (spec.eviction_max_size) was collected for every language but consumed only by
// the Go state wrapper, so a Java consumer declaring a bound got no snapshot
// check at all -- not even the default safety limit Go applies unannotated.
func TestJavaStateWrapperRendersCollectionSafetyLimit(t *testing.T) {
	rendered := renderJavaStateWrapperForTest(t, javaStateWrapperData{
		Header:             "// generated for test",
		Package:            "com.example.tacplus.s2sm.internal.state.services.demo.demostate",
		OuterClassName:     "Demo",
		StateRootName:      "DemoState",
		StateServiceName:   "DemoStateService",
		GrpcServiceFQN:     "com.example.tacplus.types.services.demo.DemoStateServiceGrpc",
		TraceFQN:           "com.example.tacplus.types.sys.trace.Trace",
		IdentityManagerFQN: "com.example.tacplus.chassis.service_im.IdentityManager",
		Collections: []javaCollectionBoundary{{
			FieldName:      "items",
			FieldPascal:    "Items",
			TypeName:       "Item",
			JavaType:       "Demo.Item",
			KeyFieldName:   "itemId",
			KeyFieldPascal: "ItemId",
			MaxEntries:     250,
		}},
	})

	for _, expected := range []string{
		"if (value.getEntriesMap().size() > 250) {",
		"LOG.severe(\"WatchItems snapshot exceeds max collection size - implement paging for large collections\"",
		"max=250)",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered Java state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}
