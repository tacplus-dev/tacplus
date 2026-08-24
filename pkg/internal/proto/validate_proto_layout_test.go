// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

// ValidateProtoLayout is the layout gate on a user proto tree. It is pure
// filesystem inspection — no protoc — so each case below is a directory shaped
// exactly the way the gate is supposed to refuse.

func TestValidateProtoLayoutAcceptsConformingTree(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "services/sample.proto", `syntax = "proto3";
package sample;
`)
	writeProto(t, dir, "services/nested/other.proto", `syntax = "proto3";
package other;
`)
	// Infrastructure trees are exempt from every rule below, including the
	// package/directory collision one.
	writeProto(t, dir, "sys/spec.proto", `syntax = "proto3";
package sys;
`)
	writeProto(t, dir, "generated/tacplus.proto", `syntax = "proto3";
package generated;
`)
	writeProto(t, dir, "google/api/annotations.proto", `syntax = "proto3";
package google;
`)

	if err := ValidateProtoLayout(dir); err != nil {
		t.Fatalf("ValidateProtoLayout rejected a conforming tree: %v", err)
	}
}

func TestValidateProtoLayoutRejectsRootLevelProto(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "sample.proto", `syntax = "proto3";
package sample;
`)

	err := ValidateProtoLayout(dir)
	if err == nil {
		t.Fatal("ValidateProtoLayout accepted a root-level proto, want error")
	}
	if !strings.Contains(err.Error(), "is at the root level") {
		t.Fatalf("error = %q, want the root-level message", err.Error())
	}
}

func TestValidateProtoLayoutRejectsTooDeepNesting(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "services/a/b/sample.proto", `syntax = "proto3";
package sample;
`)

	err := ValidateProtoLayout(dir)
	if err == nil {
		t.Fatal("ValidateProtoLayout accepted a proto nested three directories deep, want error")
	}
	if !strings.Contains(err.Error(), "nested too deep") {
		t.Fatalf("error = %q, want the nesting message", err.Error())
	}
}

// A package equal to its top-level directory collides with the generated
// output path for that directory, so the gate refuses it.
func TestValidateProtoLayoutRejectsPackageMatchingTopLevelDir(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "sample/sample.proto", `syntax = "proto3";
package sample;
`)

	err := ValidateProtoLayout(dir)
	if err == nil {
		t.Fatal("ValidateProtoLayout accepted a package matching its top-level directory, want error")
	}
	if !strings.Contains(err.Error(), "matches its top-level directory") {
		t.Fatalf("error = %q, want the package-collision message", err.Error())
	}
}
