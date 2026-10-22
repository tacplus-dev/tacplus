// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// model.Constant.Value carries a Go-syntax source literal: every language
// backend either emits it verbatim (Go, TypeScript, Rust, Python) or re-wraps
// it, and the two in-tree readers of a string constant's value
// (resolveStringConstant, registry) strip the surrounding quotes. A
// (spec.string_const) value containing a double quote or a backslash therefore
// has to be escaped when the literal is built, or the emitted literal is
// broken source in every one of those backends.
func TestParseFileConstantsEscapesStringValues(t *testing.T) {
	skipIfNoProtoc(t)

	// Proto text format: \" is a quote inside the value, \\\\ is one backslash.
	const wantValue = `a"; const Injected = "pwned` + "\\" + `tail`

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package constinject;

import "sys/spec.proto";

option (spec.string_const) = {
  name: "INJECTED"
  value: "a\"; const Injected = \"pwned\\tail"
};

message Ping { string m = 1; }
`
	writeTempProtoFile(t, dir, "constinject.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("constinject.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}

	var got string
	found := false
	for _, c := range fd.Constants {
		if c.Name == "INJECTED" {
			got = c.Value
			found = true
		}
	}
	if !found {
		t.Fatalf("INJECTED constant not found in %+v", fd.Constants)
	}

	// The literal must round-trip back to the exact proto-declared value.
	unquoted, err := strconv.Unquote(got)
	if err != nil {
		t.Fatalf("Constant.Value %q is not a valid Go string literal: %v", got, err)
	}
	if unquoted != wantValue {
		t.Errorf("round-tripped value = %q, want %q", unquoted, wantValue)
	}

	// And the emitted const declaration must actually compile as Go source,
	// the way constants.go.tmpl renders it.
	src := fmt.Sprintf("package p\n\nconst INJECTED string = %s\n", got)
	if _, err := parser.ParseFile(token.NewFileSet(), "gen.go", src, 0); err != nil {
		t.Errorf("generated Go source does not parse: %v\n%s", err, src)
	}

	// resolveStringConstant is the in-tree reader of that literal; it must
	// hand back the original value, not a quote-stripped approximation.
	if resolved := p.resolveStringConstant("INJECTED"); resolved != wantValue {
		t.Errorf("resolveStringConstant = %q, want %q", resolved, wantValue)
	}
}
