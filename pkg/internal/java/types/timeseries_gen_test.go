// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import "testing"

// The three column kinds Go and Rust special-case (enum / Timestamp / nested
// message) are pinned by the cross-language parity test. What is Java's ALONE
// is the cast each remaining scalar column needs on the way back out of a row:
// Java has no way to set an int32 field from a bare Object, so every scalar
// carries the kind that names the setter it reads back with. A scalar mapped to
// the wrong kind is a row that round-trips to a different message than it was
// built from — or, for the 32/64-bit pairs, does not compile at all.
func TestJavaTimeseriesScalarKinds(t *testing.T) {
	cases := map[string]string{
		"string":   tsKindString,
		"bytes":    tsKindBytes,
		"bool":     tsKindBool,
		"int32":    tsKindInt,
		"sint32":   tsKindInt,
		"sfixed32": tsKindInt,
		"uint32":   tsKindInt,
		"fixed32":  tsKindInt,
		"int64":    tsKindLong,
		"sint64":   tsKindLong,
		"sfixed64": tsKindLong,
		"uint64":   tsKindLong,
		"fixed64":  tsKindLong,
		"float":    tsKindFloat,
		"double":   tsKindDouble,
	}
	for protoType, want := range cases {
		if got := javaTimeseriesScalarKind(protoType); got != want {
			t.Errorf("javaTimeseriesScalarKind(%q) = %q, want %q", protoType, got, want)
		}
	}
}

// A well-known type used as a projection column resolves to its runtime class,
// not to a tacplus-generated package: com.google.protobuf.Struct is shipped by
// protobuf-java, and naming it under the service's own types package is a
// "cannot find symbol" in the generated source. This branch answers before the
// descriptor parser is consulted at all, which is why it is reachable here with
// no proto tree.
func TestJavaTimeseriesTypeFqnWellKnownType(t *testing.T) {
	got := javaTimeseriesTypeFqn(nil, nil, map[string]string{}, "", "", "google.protobuf.Struct")
	if got != "com.google.protobuf.Struct" {
		t.Errorf("well-known type: got %q, want %q", got, "com.google.protobuf.Struct")
	}
}

// A type no file in this build declares is left as its PROTO name rather than
// guessed into a Java package: a wrong package reads as a tacplus bug, a proto
// name reads as the missing import it actually is.
func TestJavaTimeseriesTypeFqnUndeclaredTypeIsNotGuessed(t *testing.T) {
	got := javaTimeseriesTypeFqn(nil, nil, map[string]string{"sensor": "sensor.proto"}, "", "", "elsewhere.Level")
	if got != "elsewhere.Level" {
		t.Errorf("undeclared type: got %q, want the proto name %q", got, "elsewhere.Level")
	}
}
