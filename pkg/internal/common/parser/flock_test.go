// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// The flock plan and its refusals (tacplus#206).

func flockAnn(kv ...string) map[string]model.AnnotationValue {
	out := map[string]model.AnnotationValue{}
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == "true" {
			out[kv[i]] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		} else {
			out[kv[i]] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: kv[i+1]}
		}
	}
	return out
}

func flockHlcField(name string, n int, ann map[string]model.AnnotationValue) model.Field {
	return model.Field{Name: name, Number: n, Type: "Hlc", TypePackage: "flock", IsMessage: true, Annotations: ann}
}

// flockFiles is a minimal valid flock table: a root with one boundary of
// Person, which has the system fields, a rule per field, a nested message and
// a unique set of Link.
func flockFiles(mutate func(person, link *model.Message, root *model.Message)) []model.ProtoFile {
	link := model.Message{Name: "Link", Fields: []model.Field{
		{Name: "idp", Number: 1, Type: "string", Annotations: flockAnn("identity", "true")},
		{Name: "added_ms", Number: 2, Type: "int64", Annotations: flockAnn("merge", "min")},
	}}
	profile := model.Message{Name: "Profile", Fields: []model.Field{
		{Name: "nickname", Number: 1, Type: "string", Annotations: flockAnn("merge", "last_write")},
	}}
	person := model.Message{Name: "Person", Fields: []model.Field{
		{Name: "key", Number: 1, Type: "string", Annotations: flockAnn("store_key", "true", "indexed", "true")},
		flockHlcField("created", 2, flockAnn("merge", "immutable")),
		flockHlcField("written", 3, flockAnn("store_epoch", "true")),
		{Name: "deleted", Number: 4, Type: "DeletionMark", TypePackage: "flock", IsMessage: true, Annotations: flockAnn("deleted_flag", "true")},
		{Name: "generation", Number: 5, Type: "uint64", Annotations: flockAnn("merge", "max")},
		{Name: "profile", Number: 6, Type: "Profile", IsMessage: true, Annotations: map[string]model.AnnotationValue{}},
		{Name: "links", Number: 7, Type: "Link", IsMessage: true, IsRepeated: true, Annotations: flockAnn("merge", "set_remove_wins", "unique", "true")},
		{Name: "tags", Number: 8, Type: "string", IsRepeated: true, Annotations: flockAnn("merge", "set_add_wins")},
		{Name: FlockMetaFieldName, Number: FlockMetaFieldNumber, Type: "Meta", TypePackage: "flock", IsMessage: true, Annotations: map[string]model.AnnotationValue{}},
	}}
	root := model.Message{Name: "PeopleState", Annotations: map[string]model.AnnotationValue{
		"service_state":       {Type: model.AnnotationTypeString, StringValue: "PeopleState"},
		"service_state_label": {Type: model.AnnotationTypeString, StringSliceValue: []string{FlockLabel}},
	}, Fields: []model.Field{
		{Name: "people", Number: 1, Type: "Person", IsMessage: true, IsRepeated: true, Annotations: map[string]model.AnnotationValue{
			"persistence":     {Type: model.AnnotationTypeBool, BoolValue: true},
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 100},
		}},
	}}
	if mutate != nil {
		mutate(&person, &link, &root)
	}
	return []model.ProtoFile{{Package: "people", Messages: []model.Message{link, profile, person, root}}}
}

func TestFlockPlanValid(t *testing.T) {
	files := flockFiles(nil)
	if err := ValidateFlockAnnotations(files); err != nil {
		t.Fatal(err)
	}
	e, ok := NewFlockIndex(files).Entry("people", "Person")
	if !ok {
		t.Fatal("Person is not planned as a flock entry")
	}
	if e.Key != "key" || e.Created != "created" || e.Epoch != "written" || e.Mark != "deleted" || e.WholeEntry {
		t.Fatalf("system fields: %+v", e)
	}
	var rules []string
	for _, f := range e.Fields {
		rules = append(rules, f.Path+"="+f.Rule)
	}
	if got := strings.Join(rules, " "); got != "generation=max profile=nested links=set_remove_wins tags=set_add_wins" {
		t.Fatalf("fields: %s", got)
	}
	if u := e.UniqueFields(); len(u) != 1 || u[0].Path != "links" || len(u[0].Element.Identity) != 1 {
		t.Fatalf("unique: %+v", u)
	}
	rulesByPath := FlockRules(files)
	for path, want := range map[string]string{
		"people.Person":                  "fields",
		"people.Person.profile.nickname": "last_write",
		"people.Person.links[].idp":      "identity",
		"people.Person.links[].added_ms": "min",
	} {
		if rulesByPath[path] != want {
			t.Errorf("FlockRules[%s] = %q, want %q", path, rulesByPath[path], want)
		}
	}
}

func TestFlockRefusals(t *testing.T) {
	field := func(m *model.Message, name string) *model.Field {
		for i := range m.Fields {
			if m.Fields[i].Name == name {
				return &m.Fields[i]
			}
		}
		panic(name)
	}
	for _, tc := range []struct {
		name   string
		mutate func(person, link, root *model.Message)
		want   string
	}{
		{"a field with no rule", func(p, _, _ *model.Message) { delete(field(p, "generation").Annotations, "merge") }, "generation: a field of a flock entry needs a (spec.merge) rule"},
		{"a nested field with no rule", func(p, _, _ *model.Message) {
			p.Fields = append(p.Fields, model.Field{Name: "count", Number: 9, Type: "int32", Annotations: map[string]model.AnnotationValue{}})
		}, "count: a field of a flock entry needs a (spec.merge) rule"},
		{"MIN on a bool", func(p, _, _ *model.Message) {
			*field(p, "generation") = model.Field{Name: "generation", Number: 5, Type: "bool", Annotations: flockAnn("merge", "min")}
		}, "MERGE_RULE_MIN and MERGE_RULE_MAX take a singular integer or string"},
		{"an unspecified rule", func(p, _, _ *model.Message) { field(p, "generation").Annotations = flockAnn("merge", "unspecified") }, "names no rule"},
		{"an element with no identity", func(_, l, _ *model.Message) {
			delete(field(l, "idp").Annotations, "identity")
			field(l, "idp").Annotations = flockAnn("merge", "last_write")
		}, "needs at least one (spec.identity) field"},
		{"unique on a non-set", func(p, _, _ *model.Message) {
			field(p, "generation").Annotations = flockAnn("merge", "max", "unique", "true")
		}, "(spec.unique) belongs on a SET_* field"},
		{"no creation stamp", func(p, _, _ *model.Message) { field(p, "created").Annotations = flockAnn("merge", "last_write") }, "exactly one creation stamp"},
		{"a key that is not indexed", func(p, _, _ *model.Message) { field(p, "key").Annotations = flockAnn("store_key", "true") }, "add (spec.indexed) = true"},
		{"an int64 store epoch", func(p, _, _ *model.Message) {
			*field(p, "written") = model.Field{Name: "written", Number: 3, Type: "int64", Annotations: flockAnn("store_epoch", "true")}
		}, "is a flock.Hlc, the hybrid clock"},
		{"a boundary with no max size", func(_, _, r *model.Message) { delete(r.Fields[0].Annotations, "assert_max_size") }, "needs (spec.assert_max_size)"},
		{"a boundary with a field rule", func(_, _, r *model.Message) {
			r.Fields[0].Annotations["merge"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: "max"}
		}, "a boundary takes no (spec.merge) rule but MERGE_RULE_LAST_WRITE"},
		{"a map of scalars", func(p, _, _ *model.Message) {
			p.Fields = append(p.Fields, model.Field{Name: "labels", Number: 9, IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: flockAnn("merge", "set_add_wins")})
		}, "a map of scalars has no flock rule"},
		{"a oneof", func(p, _, _ *model.Message) { field(p, "generation").IsOneOf = true }, "cannot be in a oneof or optional"},
		{"a map in a LAST_WRITE value", func(p, _, _ *model.Message) {
			*field(p, "generation") = model.Field{Name: "generation", Number: 5, IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: flockAnn("merge", "last_write")}
		}, "MERGE_RULE_LAST_WRITE does not take a map"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFlockAnnotations(flockFiles(tc.mutate))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestFlockAnnotationsOutsideAFlockRoot(t *testing.T) {
	files := []model.ProtoFile{{Package: "plain", Messages: []model.Message{{Name: "Thing", Fields: []model.Field{
		{Name: "n", Number: 1, Type: "int64", Annotations: flockAnn("merge", "max")},
		{Name: "id", Number: 2, Type: "string", Annotations: flockAnn("identity", "true")},
		flockHlcField("written", 3, flockAnn("store_epoch", "true")),
	}}}}}
	err := ValidateFlockAnnotations(files)
	if err == nil {
		t.Fatal("flock annotations outside a flock root were accepted")
	}
	for _, want := range []string{"plain.Thing.n: (spec.merge) applies only", "plain.Thing.id: (spec.identity) applies only", "plain.Thing.written: a flock.Hlc store epoch"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestFlockWholeEntryTable(t *testing.T) {
	files := flockFiles(func(p, _, r *model.Message) {
		r.Fields[0].Annotations["merge"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: FlockRuleLastWrite}
		for i := range p.Fields {
			delete(p.Fields[i].Annotations, "unique")
			if p.Fields[i].Name != "created" {
				delete(p.Fields[i].Annotations, "merge")
			}
		}
	})
	// Link and Profile are no longer merged field by field: drop them.
	files[0].Messages = files[0].Messages[2:]
	if err := ValidateFlockAnnotations(files); err != nil {
		t.Fatal(err)
	}
	if e, _ := NewFlockIndex(files).Entry("people", "Person"); !e.WholeEntry || len(e.Fields) != 0 {
		t.Fatalf("a LAST_WRITE boundary's entry is not replaced whole: %+v", e)
	}
	// Its fields take no rules.
	files = flockFiles(func(_, _, r *model.Message) {
		r.Fields[0].Annotations["merge"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: FlockRuleLastWrite}
	})
	if err := ValidateFlockAnnotations(files); err == nil || !strings.Contains(err.Error(), "its fields take no (spec.merge) rule") {
		t.Fatalf("got %v", err)
	}
}

func TestFlockLabelIsServed(t *testing.T) {
	if !CollectServingLabels(nil)[FlockLabel] {
		t.Fatal("the flock label is not served by the fleet's flock service")
	}
}

// A map anywhere inside a value stored whole has no canonical encoding.
func TestFlockRefusesMapInsideAWholeValue(t *testing.T) {
	files := flockFiles(func(p, _, _ *model.Message) {
		p.Fields = append(p.Fields, model.Field{Name: "settings", Number: 9, Type: "Settings", IsMessage: true, Annotations: flockAnn("merge", "last_write")})
	})
	files[0].Messages = append(files[0].Messages, model.Message{Name: "Settings", Fields: []model.Field{
		{Name: "labels", Number: 1, IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: map[string]model.AnnotationValue{}},
	}})
	err := ValidateFlockAnnotations(files)
	if err == nil || !strings.Contains(err.Error(), "people.Person.settings: a value stored whole cannot hold a map") {
		t.Fatalf("got %v", err)
	}
}
