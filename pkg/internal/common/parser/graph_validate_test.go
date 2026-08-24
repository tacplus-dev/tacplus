// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ValidateGraphAnnotations is the generation-time gate on (spec.graph): a
// predicate that cannot be indexed, a reverse edge on a scalar, or a uid edge
// pointing at a node with no upsert key all produce a graph schema that cannot
// be written to. Each case below is a model that must fail the build.

func graphAnn(s string) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: s}
}

func graphBool(b bool) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: b}
}

// graphFiles wraps messages into the single-package []model.ProtoFile the
// validator consumes.
func graphFiles(msgs ...model.Message) []model.ProtoFile {
	return []model.ProtoFile{{Package: "graphsample", RelPath: "graphsample.proto", Messages: msgs}}
}

// scalarPredicate is a well-formed indexed scalar predicate, optionally the
// message's upsert key.
func scalarPredicate(name string, upsert bool) model.Field {
	ann := map[string]model.AnnotationValue{
		"graph_mode":        graphAnn("PREDICATE"),
		"graph_field_index": graphAnn("exact"),
	}
	if upsert {
		ann["graph_upsert"] = graphBool(true)
	}
	return model.Field{Name: name, Type: "string", Annotations: ann}
}

// nodeMsg is a valid graph node: one indexed scalar predicate that is also the
// upsert key.
func nodeMsg(name string) model.Message {
	return model.Message{Name: name, Fields: []model.Field{scalarPredicate("node_id", true)}}
}

func TestValidateGraphAnnotationsAcceptsWellFormedGraph(t *testing.T) {
	// Owner holds a uid edge to Node; Node carries exactly one upsert field.
	owner := model.Message{
		Name: "Owner",
		Fields: []model.Field{
			scalarPredicate("owner_id", true),
			{Name: "target", Type: "Node", IsMessage: true, Annotations: map[string]model.AnnotationValue{
				"graph_mode":    graphAnn("PREDICATE"),
				"graph_reverse": graphBool(true),
			}},
			{Name: "blob", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode": graphAnn("INCLUDE"),
			}},
		},
	}
	if err := ValidateGraphAnnotations(graphFiles(owner, nodeMsg("Node"))); err != nil {
		t.Fatalf("ValidateGraphAnnotations rejected a well-formed graph: %v", err)
	}
}

func TestValidateGraphAnnotationsRejectsMalformedFields(t *testing.T) {
	cases := map[string]struct {
		field model.Field
		want  string
	}{
		"PREDICATE on a repeated field": {
			field: model.Field{Name: "tags", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{
				"graph_mode":        graphAnn("PREDICATE"),
				"graph_field_index": graphAnn("exact"),
			}},
			want: "PREDICATE is not valid on repeated fields",
		},
		"PREDICATE on a map field": {
			field: model.Field{Name: "labels", Type: "string", IsMap: true, Annotations: map[string]model.AnnotationValue{
				"graph_mode":        graphAnn("PREDICATE"),
				"graph_field_index": graphAnn("exact"),
			}},
			want: "PREDICATE is not valid on map fields",
		},
		"scalar PREDICATE with no index": {
			field: model.Field{Name: "label", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode": graphAnn("PREDICATE"),
			}},
			want: "PREDICATE on scalar field requires index",
		},
		"scalar PREDICATE with an empty index": {
			field: model.Field{Name: "label", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode":        graphAnn("PREDICATE"),
				"graph_field_index": graphAnn(""),
			}},
			want: "PREDICATE on scalar field requires index",
		},
		"reverse on a scalar predicate": {
			field: model.Field{Name: "label", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode":        graphAnn("PREDICATE"),
				"graph_field_index": graphAnn("exact"),
				"graph_reverse":     graphBool(true),
			}},
			want: "reverse is only valid on message (uid edge) predicates",
		},
		"upsert on an INCLUDE field": {
			field: model.Field{Name: "blob", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode":   graphAnn("INCLUDE"),
				"graph_upsert": graphBool(true),
			}},
			want: "upsert is only valid with mode PREDICATE, not INCLUDE",
		},
		"reverse on an INCLUDE field": {
			field: model.Field{Name: "blob", Type: "string", Annotations: map[string]model.AnnotationValue{
				"graph_mode":    graphAnn("INCLUDE"),
				"graph_reverse": graphBool(true),
			}},
			want: "reverse is only valid with mode PREDICATE, not INCLUDE",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			msg := model.Message{Name: "Node", Fields: []model.Field{scalarPredicate("node_id", true), tc.field}}
			err := ValidateGraphAnnotations(graphFiles(msg))
			if err == nil {
				t.Fatal("ValidateGraphAnnotations accepted a malformed graph annotation, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "graphsample.Node."+tc.field.Name) {
				t.Fatalf("error = %q, want it to locate the offending field", err.Error())
			}
		})
	}
}

// A uid edge is written by upserting the target node on its identity field. A
// target with no upsert field, or more than one, has no single identity to
// upsert on — the edge cannot be materialized.
func TestValidateGraphAnnotationsRejectsUidTargetsWithoutExactlyOneUpsert(t *testing.T) {
	edge := model.Field{Name: "target", Type: "Node", IsMessage: true, Annotations: map[string]model.AnnotationValue{
		"graph_mode": graphAnn("PREDICATE"),
	}}
	owner := model.Message{Name: "Owner", Fields: []model.Field{scalarPredicate("owner_id", true), edge}}

	noUpsert := model.Message{Name: "Node", Fields: []model.Field{scalarPredicate("node_id", false)}}
	err := ValidateGraphAnnotations(graphFiles(owner, noUpsert))
	if err == nil {
		t.Fatal("ValidateGraphAnnotations accepted a uid edge target with no upsert field, want error")
	}
	if !strings.Contains(err.Error(), "graphsample.Node: referenced as uid edge target but has no upsert field") {
		t.Fatalf("error = %q, want the missing-upsert message", err.Error())
	}

	twoUpserts := model.Message{Name: "Node", Fields: []model.Field{
		scalarPredicate("node_id", true),
		scalarPredicate("alt_id", true),
	}}
	err = ValidateGraphAnnotations(graphFiles(owner, twoUpserts))
	if err == nil {
		t.Fatal("ValidateGraphAnnotations accepted a uid edge target with two upsert fields, want error")
	}
	if !strings.Contains(err.Error(), "has multiple upsert fields [node_id, alt_id]") {
		t.Fatalf("error = %q, want the multiple-upsert message", err.Error())
	}
}

// A uid edge crossing packages resolves the target by the field's TypePackage,
// so the upsert requirement follows the edge rather than the declaring file.
func TestValidateGraphAnnotationsResolvesCrossPackageUidTargets(t *testing.T) {
	owner := model.ProtoFile{Package: "graphsample", RelPath: "graphsample.proto", Messages: []model.Message{{
		Name: "Owner",
		Fields: []model.Field{
			scalarPredicate("owner_id", true),
			{Name: "target", Type: "Node", TypePackage: "othergraph", IsMessage: true,
				Annotations: map[string]model.AnnotationValue{"graph_mode": graphAnn("PREDICATE")}},
		},
	}}}
	// The other package's Node has no upsert field — the edge is unwritable.
	other := model.ProtoFile{Package: "othergraph", RelPath: "othergraph.proto", Messages: []model.Message{
		{Name: "Node", Fields: []model.Field{scalarPredicate("node_id", false)}},
	}}

	err := ValidateGraphAnnotations([]model.ProtoFile{owner, other})
	if err == nil {
		t.Fatal("ValidateGraphAnnotations accepted a cross-package uid edge with no upsert target, want error")
	}
	if !strings.Contains(err.Error(), "othergraph.Node: referenced as uid edge target") {
		t.Fatalf("error = %q, want it to name the cross-package target", err.Error())
	}

	// The same shape with an upsert key on the target passes.
	other.Messages[0].Fields[0] = scalarPredicate("node_id", true)
	if err := ValidateGraphAnnotations([]model.ProtoFile{owner, other}); err != nil {
		t.Fatalf("ValidateGraphAnnotations rejected a valid cross-package uid edge: %v", err)
	}
}

// Messages with no (spec.graph) annotations at all are not graph nodes and are
// left alone.
func TestValidateGraphAnnotationsIgnoresUnannotatedMessages(t *testing.T) {
	plain := model.Message{Name: "Plain", Fields: []model.Field{
		{Name: "tags", Type: "string", IsRepeated: true},
		{Name: "labels", Type: "string", IsMap: true},
	}}
	if err := ValidateGraphAnnotations(graphFiles(plain)); err != nil {
		t.Fatalf("ValidateGraphAnnotations rejected an unannotated message: %v", err)
	}
}
