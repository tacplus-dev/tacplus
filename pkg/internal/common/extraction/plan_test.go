// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package extraction

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func source(v string) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"projection_source": {StringValue: v}}
}

// A projection with oneof references is planned as its top-level fields plus
// one variant per oneof member, in first-use order; each source field is
// resolved on its own receiver — a variant field on the member's message type,
// not on the source message.
func TestPlanResolvesTopLevelAndVariantFields(t *testing.T) {
	files := []model.ProtoFile{
		{
			Package: "events",
			RelPath: "events/events.proto",
			Messages: []model.Message{
				{Name: "ToolUse", Fields: []model.Field{{Name: "level", IsEnum: true, Type: "Level"}}},
				{Name: "Event", Fields: []model.Field{
					{Name: "session_id", Type: "string"},
					{Name: "kind", IsEnum: true, Type: "Kind"},
					{Name: "tool_use", IsMessage: true, IsOneOf: true, Type: "ToolUse"},
				}},
			},
		},
		{
			Package: "proj",
			RelPath: "proj/proj.proto",
			Messages: []model.Message{
				{
					Name:        "Node",
					Annotations: map[string]model.AnnotationValue{"timeseries_table_source": {StringValue: "events.Event"}},
					Fields: []model.Field{
						{Name: "session_id", Type: "string", Annotations: source("session_id")},
						{Name: "tool_level", Type: "int32", Annotations: source("tool_use.level")},
						{Name: "kind", Type: "int32", Annotations: source("kind")},
						{Name: "unmapped", Type: "string"},
					},
				},
				{
					Name:        "Flat",
					Annotations: map[string]model.AnnotationValue{"timeseries_table_source": {StringValue: "events.Event"}},
					Fields:      []model.Field{{Name: "session_id", Type: "string", Annotations: source("session_id")}},
				},
			},
		},
	}

	groups, err := Plan(files)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want one (events.Event, proj) group, got %d", len(groups))
	}
	g := groups[0]
	if g.Source.Name != "Event" || g.SourceFile.Package != "events" || g.File().Package != "proj" {
		t.Fatalf("group = source %s.%s into %s", g.SourceFile.Package, g.Source.Name, g.File().Package)
	}
	if len(g.Projections) != 2 || g.Projections[0].Message.Name != "Node" || g.Projections[1].Message.Name != "Flat" {
		t.Fatalf("projections out of declaration order: %+v", g.Projections)
	}

	node := g.Projections[0]
	if len(node.Fields) != 2 || node.Fields[0].SourceName != "session_id" || node.Fields[1].SourceName != "kind" {
		t.Fatalf("top-level fields = %+v, want session_id then kind (unannotated fields skipped)", node.Fields)
	}
	if !node.Fields[1].Resolved || !node.Fields[1].Source.IsEnum {
		t.Fatalf("kind must resolve to the source's enum field: %+v", node.Fields[1])
	}
	if len(node.Variants) != 1 || node.Variants[0].Name != "tool_use" {
		t.Fatalf("variants = %+v, want tool_use", node.Variants)
	}
	inner := node.Variants[0].Fields
	if len(inner) != 1 || inner[0].SourceName != "level" || !inner[0].Resolved || !inner[0].Source.IsEnum {
		t.Fatalf("tool_use.level must resolve on ToolUse (an enum there), got %+v", inner)
	}
	if len(g.Projections[1].Variants) != 0 {
		t.Fatalf("a projection with no dotted source is unconditional, got variants %+v", g.Projections[1].Variants)
	}
}

func TestPlanFailsWhenSourceMissing(t *testing.T) {
	files := []model.ProtoFile{{
		Package: "projection",
		RelPath: "projection/metric.proto",
		Messages: []model.Message{{
			Name:        "MetricProjection",
			Annotations: map[string]model.AnnotationValue{"timeseries_table_source": {StringValue: "events.MetricEvent"}},
		}},
	}}
	_, err := Plan(files)
	if err == nil || !strings.Contains(err.Error(), `extraction source "events.MetricEvent" for projection projection.MetricProjection not found`) {
		t.Fatalf("Plan error = %v, want the missing source named", err)
	}
}

// dispatchFiles is one source, events.Event, with a projection in its own
// package and, when foreign is set, a second one declared in package proj.
func dispatchFiles(foreign bool) []model.ProtoFile {
	from := map[string]model.AnnotationValue{"timeseries_table_source": {StringValue: "events.Event"}}
	files := []model.ProtoFile{{
		Package: "events",
		RelPath: "events/events.proto",
		Messages: []model.Message{
			{Name: "Event", Fields: []model.Field{
				{Name: "id", Type: "string", Number: 1},
				{Name: "noop", Type: "bool", Number: 201},
			}},
			{Name: "Row", Annotations: from},
			{Name: "Other", Fields: []model.Field{{Name: "noop", Type: "bool", Number: 7}}},
			{Name: "OtherRow", Annotations: map[string]model.AnnotationValue{"timeseries_table_source": {StringValue: "events.Other"}}},
		},
	}}
	if foreign {
		files = append(files, model.ProtoFile{
			Package:  "proj",
			RelPath:  "proj/proj.proto",
			Messages: []model.Message{{Name: "ForeignRow", Annotations: from}},
		})
	}
	return files
}

// The dispatch a subscription runs is the source's own-package group; the noop
// skip keys on the injected field (noop = 201), not on the name alone.
func TestDispatchedFindsTheSourceGroupAndItsNoop(t *testing.T) {
	groups, err := Plan(dispatchFiles(false))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	g, ok, err := Dispatched(groups, "events.Event")
	if err != nil || !ok {
		t.Fatalf("Dispatched(events.Event) = %v, %v", ok, err)
	}
	if len(g.Projections) != 1 || g.Projections[0].Message.Name != "Row" || !g.SourceHasNoop() {
		t.Fatalf("group = %+v, noop %v; want Row with the injected noop", g.Projections, g.SourceHasNoop())
	}
	other, ok, err := Dispatched(groups, "events.Other")
	if err != nil || !ok || other.SourceHasNoop() {
		t.Fatalf("events.Other: ok %v err %v noop %v; a noop field at another number is the author's own", ok, err, other.SourceHasNoop())
	}
	if _, ok, err := Dispatched(groups, "events.Missing"); ok || err != nil {
		t.Fatalf("a source nothing projects from has no dispatch: ok %v err %v", ok, err)
	}
}

// A subscribed source projected from another package is refused, not dropped.
func TestDispatchedRefusesAProjectionOutsideTheSourcePackage(t *testing.T) {
	groups, err := Plan(dispatchFiles(true))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	_, _, err = Dispatched(groups, "events.Event")
	if err == nil || !strings.Contains(err.Error(), "projection proj.ForeignRow of subscribed source events.Event is declared outside the source's package") {
		t.Fatalf("Dispatched error = %v, want the foreign projection named", err)
	}
}
