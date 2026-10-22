// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/extraction"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Each projection field kind copies through the protobuf-java accessor that
// kind has. Every row here is a statement that compiles against protoc output;
// a plain set in its place does not (enum ↔ int, collections) or changes the
// value (a set on an absent message marks it present, where Go copies nil).
func TestJavaExtractionStatementPerFieldKind(t *testing.T) {
	enum := model.Field{Name: "level", IsEnum: true}
	scalar := model.Field{Name: "level", Type: "int32"}
	for _, tc := range []struct {
		name string
		f    extraction.Field
		want string
	}{
		{"scalar", extraction.Field{Projection: model.Field{Name: "probe_id", Type: "string"}, SourceName: "probe_id", Source: model.Field{Name: "probe_id", Type: "string"}, Resolved: true},
			"msg.setProbeId(event.getProbeId());"},
		{"enum to enum", extraction.Field{Projection: enum, SourceName: "level", Source: enum, Resolved: true},
			"msg.setLevelValue(event.getLevelValue());"},
		{"enum to int", extraction.Field{Projection: scalar, SourceName: "level", Source: enum, Resolved: true},
			"msg.setLevel(event.getLevelValue());"},
		{"int to enum", extraction.Field{Projection: enum, SourceName: "level", Source: scalar, Resolved: true},
			"msg.setLevelValue(event.getLevel());"},
		{"message", extraction.Field{Projection: model.Field{Name: "detail", IsMessage: true}, SourceName: "detail", Source: model.Field{Name: "detail", IsMessage: true}, Resolved: true},
			"if (event.hasDetail()) { msg.setDetail(event.getDetail()); }"},
		{"optional scalar", extraction.Field{Projection: model.Field{Name: "n", Type: "int64", IsOptional: true}, SourceName: "n", Source: model.Field{Name: "n", Type: "int64", IsOptional: true}, Resolved: true},
			"if (event.hasN()) { msg.setN(event.getN()); }"},
		{"repeated", extraction.Field{Projection: model.Field{Name: "tags", Type: "string", IsRepeated: true}, SourceName: "tags", Resolved: true},
			"msg.addAllTags(event.getTagsList());"},
		{"map", extraction.Field{Projection: model.Field{Name: "attrs", IsMap: true}, SourceName: "attrs", Resolved: true},
			"msg.putAllAttrs(event.getAttrsMap());"},
	} {
		if got := javaExtractionStatement("event", tc.f); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A projection with oneof variants is extracted only when a member is set,
// reading each member's fields off that member — the shape Go's Get<Variant>()
// and Rust's `if let` give.
func TestJavaExtractionTemplateRendersVariantsConditionally(t *testing.T) {
	tmplStr, err := loadTemplate("extraction_projections.java.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := template.Must(template.New("x").Parse(tmplStr))
	data := javaExtractionTemplateData{
		Package:             "p",
		ClassName:           "EventProjections",
		SourceType:          "Event",
		SourceFqn:           "p.E.Event",
		ToProjectionsMethod: "eventToProjections",
		Projections: []javaExtractionProjection{{
			TypeName:      "Node",
			TypeFqn:       "p.E.Node",
			FieldName:     "node",
			ExtractMethod: "extractNodeFromEvent",
			Fields:        []javaExtractionField{{Statement: "msg.setSessionId(event.getSessionId());"}},
			Variants: []javaExtractionVariant{{
				Accessor: "ToolUse",
				VarName:  "vToolUse",
				Fields:   []javaExtractionField{{Statement: "msg.setTool(vToolUse.getTool());"}},
			}},
		}},
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"public final class EventProjections {",
		"public final p.E.Node node;",
		"public static EventProjections eventToProjections(p.E.Event event) {",
		"return new EventProjections(extractNodeFromEvent(event));",
		"public static p.E.Node extractNodeFromEvent(p.E.Event event) {",
		"msg.setSessionId(event.getSessionId());\n        boolean matched = false;",
		"if (event.hasToolUse()) {\n            var vToolUse = event.getToolUse();\n            msg.setTool(vToolUse.getTool());\n            matched = true;\n        }",
		"if (!matched) {\n            return null;\n        }\n        return msg.build();",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered class missing %q:\n%s", want, out)
		}
	}
}
