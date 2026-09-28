// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// parseFixtureProtoFiles runs the real descriptor pipeline over dir and returns the
// model.ProtoFiles the builder's semantic validation sees, so these tests exercise
// the same annotation map generation reads — not a hand-rolled model.
func parseFixtureProtoFiles(t *testing.T, dir, rel string) []model.ProtoFile {
	t.Helper()
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	pf, err := dp.ConvertToProtoFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("ConvertToProtoFile: %v", err)
	}
	return []model.ProtoFile{pf}
}

// A field marked both (spec.redact) and (spec.query_field) would let a caller
// filter/predicate on a value they are not entitled to SEE — the record map emits a
// typed <T>Filter criterion over exactly the value Sanitize<T> clears at egress, so
// the filter leaks it back by inference. Generation must refuse, at parse time, for
// every target language. Exercised through a real proto fixture, in every redact
// mode, so the rule is proven to fire on the annotations protoc actually stamps.
func TestValidateDataEntitlementRejectsRedactedQueryField(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	for _, mode := range []string{"REDACT_MODE_DROP", "REDACT_MODE_PSEUDONYMIZE", "REDACT_MODE_MAY_REDACT", "REDACT_MODE_MAY_ANONYMIZE"} {
		t.Run(mode, func(t *testing.T) {
			dir := nestedRedactFixtureDir(t)
			writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Task {
  option (spec.record) = {key: "task_id"};
  string task_id = 1;
  string title = 2 [
    (spec.redact) = {mode: `+mode+`},
    (spec.query_field) = {op: QUERY_OP_EQ}
  ];
}
`)
			err := parser.ValidateDataEntitlementAnnotations(parseFixtureProtoFiles(t, dir, "demo.proto"))
			if err == nil {
				t.Fatal("expected a loud error for a field marked both (spec.redact) and (spec.query_field), got nil")
			}
			for _, want := range []string{"Task.title", "(spec.redact)", "(spec.query_field)", "defeats redaction"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error must contain %q, got: %v", want, err)
				}
			}
		})
	}
}

// Control: redact and query_field on DIFFERENT fields of the same record is the
// normal, valid shape (TaskActivity's own surface) and must keep generating.
func TestValidateDataEntitlementAllowsQueryFieldOnUnredactedField(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := nestedRedactFixtureDir(t)
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Task {
  option (spec.record) = {key: "task_id"};
  string task_id = 1;
  string title = 2 [(spec.redact) = {mode: REDACT_MODE_DROP}];
  string status = 3 [(spec.query_field) = {op: QUERY_OP_EQ}];
  int64 created_at_ms = 4 [(spec.query_field) = {op: QUERY_OP_RANGE}];
}
`)
	if err := parser.ValidateDataEntitlementAnnotations(parseFixtureProtoFiles(t, dir, "demo.proto")); err != nil {
		t.Fatalf("redact and query_field on separate fields must pass, got: %v", err)
	}
}
