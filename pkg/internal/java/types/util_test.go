// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"os"
	"path/filepath"
	"testing"
)

// MS-10 class (java): extractJavaOuterClassName / extractJavaPackageName scan
// raw proto text; their regexes were unanchored and would false-extract a
// COMMENTED-OUT option value, making the java generator reference a
// non-existent outer class/package. Now anchored (no slash before `option`).
func TestExtractJavaOptions_IgnoreCommentedOut(t *testing.T) {
	dir := t.TempDir()

	commented := filepath.Join(dir, "commented.proto")
	if err := os.WriteFile(commented, []byte(`syntax = "proto3";
package foo;
// option java_outer_classname = "GhostClass";
// option java_package = "com.ghost";
message M { string x = 1; }
`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := extractJavaOuterClassName(commented); got != "" {
		t.Errorf("commented java_outer_classname must be ignored, got %q", got)
	}
	if got := extractJavaPackageName(commented); got != "" {
		t.Errorf("commented java_package must be ignored, got %q", got)
	}

	active := filepath.Join(dir, "active.proto")
	if err := os.WriteFile(active, []byte(`syntax = "proto3";
package foo;
option java_outer_classname = "RealClass";
option java_package = "com.real";
message M { string x = 1; }
`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := extractJavaOuterClassName(active); got != "RealClass" {
		t.Errorf("active java_outer_classname = %q, want RealClass", got)
	}
	if got := extractJavaPackageName(active); got != "com.real" {
		t.Errorf("active java_package = %q, want com.real", got)
	}
}
