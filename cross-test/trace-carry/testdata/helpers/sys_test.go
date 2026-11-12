// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's Go sys-contract leg (tacplus#327): every message of
// the generated sys contract has trace_id = 200, but sys/spec.proto's option
// schemas and trace.TraceEvent. The run script links every generated sys
// package into this test (sys_imports_test.go), so the registry holds them all.
package sm

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestTraceHelpersSysContract(t *testing.T) {
	checked := 0
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		path := fd.Path()
		if !strings.HasPrefix(path, "sys/") || path == "sys/spec.proto" {
			return true
		}
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			if md.FullName() == "trace.TraceEvent" {
				continue
			}
			f := md.Fields().ByNumber(200)
			if f == nil || f.Name() != "trace_id" || f.Kind() != protoreflect.StringKind {
				t.Errorf("%s (%s) has no string trace_id = 200", md.FullName(), path)
			}
			checked++
		}
		return true
	})
	if checked < 100 {
		t.Fatalf("checked %d sys messages; the sys packages were not all linked", checked)
	}
	t.Logf("trace-helpers go sys: %d messages", checked)
}
