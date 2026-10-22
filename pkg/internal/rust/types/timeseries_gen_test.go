// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Regression test for the bug caught live in schematic (Piece A accounting record
// generation, 2026-08-25): a timeseries row field whose enum type is declared in a
// different proto package than the message using it rendered as a bare type name
// (e.g. "EventLevel"), which only resolves under the generated file's `use super::*`
// when the enum lives in the *same* package. Cross-package enums need a fully
// qualified crate path, or the generated .rs file fails to compile with
// "cannot find type X in this scope".
func TestBuildRustTimeseriesTemplateDataQualifiesCrossPackageEnum(t *testing.T) {
	protoByPkg := map[string]model.ProtoFile{
		"accounting": {Package: "accounting", RelPath: "sys/accounting.proto"},
		"tasks":      {Package: "tasks", RelPath: "vservices/tasks.proto"},
	}

	msg := model.Message{
		Name: "TimeseriesAccountingRow",
		Fields: []model.Field{
			{Name: "level", Type: "EventLevel", IsEnum: true, TypePackage: "accounting",
				Annotations: map[string]model.AnnotationValue{"timeseries_column": {}}},
			{Name: "local_status", Type: "TaskStatus", IsEnum: true, // same package, no TypePackage
				Annotations: map[string]model.AnnotationValue{"timeseries_column": {}}},
		},
	}

	data := buildRustTimeseriesTemplateData(msg, "tasks", protoByPkg)

	if len(data.Fields) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(data.Fields))
	}

	cross := data.Fields[0]
	if cross.EnumType != "crate::sys::accounting::EventLevel" {
		t.Errorf("cross-package enum: got EnumType %q, want %q (must be fully qualified so it resolves without relying on `use super::*`)",
			cross.EnumType, "crate::sys::accounting::EventLevel")
	}

	same := data.Fields[1]
	if same.EnumType != "TaskStatus" || strings.Contains(same.EnumType, "::") {
		t.Errorf("same-package enum: got EnumType %q, want bare %q (relies on `use super::*`)",
			same.EnumType, "TaskStatus")
	}
}
