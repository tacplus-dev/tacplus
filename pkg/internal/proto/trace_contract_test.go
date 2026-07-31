// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"strings"
	"testing"
)

// Regression for OF-7: the trace contract (field numbers + names) is a single
// source of truth. Emission (TraceFieldsProto) is built from the same constants
// the detection path uses, so they cannot drift apart.
func TestTraceContractSingleSource(t *testing.T) {
	for _, want := range []string{
		fmt.Sprintf("%s = %d", traceIDFieldName, traceIDFieldNumber),
		fmt.Sprintf("%s = %d", noopFieldName, noopFieldNumber),
	} {
		if !strings.Contains(TraceFieldsProto, want) {
			t.Errorf("TraceFieldsProto %q does not contain %q (emission/detection drift)", TraceFieldsProto, want)
		}
	}
	// The two trace fields must have distinct numbers.
	if traceIDFieldNumber == noopFieldNumber {
		t.Fatal("trace_id and noop must have distinct field numbers")
	}
}
