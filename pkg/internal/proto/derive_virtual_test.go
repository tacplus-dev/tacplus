// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// A parser fixture with exactly one opa_eval-annotated proto, in package
// "realpkg".
func opaEvalSourceParser(t *testing.T) *parser.DescriptorParser {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "realpkg.proto", `syntax = "proto3";
package realpkg;

import "sys/spec.proto";

message GoodInput {
  option (spec.opa_eval) = {entry_point: "data.x" result: "GoodResult"};
  string email = 1;
}

message GoodResult {
  bool allow = 1;
}
`)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return dp
}

// A named-but-absent opa_import source package must fail loud rather than
// silently fall back to the alphabetically-first annotated proto in some OTHER
// package (which wires the client to the wrong source).
func TestFindAnnotatedSourceFileByPkgFailsLoudOnMissingPackage(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dp := opaEvalSourceParser(t)

	_, _, err := findAnnotatedSourceFileByPkg(dp, "opa_eval", "missingpkg.SomeInput")
	if err == nil {
		t.Fatal("expected a loud error when the named source package has no annotated proto")
	}
	if !strings.Contains(err.Error(), "missingpkg") || !strings.Contains(err.Error(), "opa_eval") {
		t.Fatalf("error should name the missing package and annotation, got: %v", err)
	}
}

// The correct package still resolves without error.
func TestFindAnnotatedSourceFileByPkgResolvesNamedPackage(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dp := opaEvalSourceParser(t)

	rp, pkg, err := findAnnotatedSourceFileByPkg(dp, "opa_eval", "realpkg.GoodInput")
	if err != nil {
		t.Fatalf("naming the correct package must resolve, got: %v", err)
	}
	if pkg != "realpkg" || rp != "realpkg.proto" {
		t.Fatalf("expected realpkg/realpkg.proto, got pkg=%q rp=%q", pkg, rp)
	}
}

// An empty source filter keeps the legitimate "first annotated proto" fallback
// (no package was named, so there is nothing to fail loud about).
func TestFindAnnotatedSourceFileByPkgEmptyFilterFallsBack(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dp := opaEvalSourceParser(t)

	rp, pkg, err := findAnnotatedSourceFileByPkg(dp, "opa_eval", "")
	if err != nil {
		t.Fatalf("empty filter must not error, got: %v", err)
	}
	if pkg != "realpkg" || rp != "realpkg.proto" {
		t.Fatalf("expected the sole annotated proto, got pkg=%q rp=%q", pkg, rp)
	}
}
