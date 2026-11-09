// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func flockGoAnn(kv ...string) map[string]model.AnnotationValue {
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

// A flock entry gets its merge, writers, marks, unique check and digest
// (tacplus#206); a package without one gets no flock file.
func TestGenerateGoFlock(t *testing.T) {
	hlc := func(name string, n int, ann map[string]model.AnnotationValue) model.Field {
		return model.Field{Name: name, Number: n, Type: "Hlc", TypePackage: "flock", IsMessage: true, Annotations: ann}
	}
	pf := model.ProtoFile{Package: "people", RelPath: "people/people.proto", Messages: []model.Message{
		{Name: "Person", Fields: []model.Field{
			{Name: "key", Number: 1, Type: "string", Annotations: flockGoAnn("store_key", "true", "indexed", "true")},
			hlc("created", 2, flockGoAnn("merge", "immutable")),
			hlc("written", 3, flockGoAnn("store_epoch", "true")),
			{Name: "deleted", Number: 4, Type: "DeletionMark", TypePackage: "flock", IsMessage: true, Annotations: flockGoAnn("deleted_flag", "true")},
			{Name: "display", Number: 5, Type: "string", Annotations: flockGoAnn("merge", "last_write")},
			{Name: "tags", Number: 6, Type: "string", IsRepeated: true, Annotations: flockGoAnn("merge", "set_add_wins", "unique", "true")},
		}},
		{Name: "PeopleState", Annotations: map[string]model.AnnotationValue{
			"service_state":       {Type: model.AnnotationTypeString, StringValue: "PeopleState"},
			"service_state_label": {Type: model.AnnotationTypeString, StringSliceValue: []string{parser.FlockLabel}},
		}, Fields: []model.Field{{Name: "people", Number: 1, Type: "Person", IsMessage: true, IsRepeated: true, Annotations: flockGoAnn("persistence", "true")}}},
	}}
	files := []model.ProtoFile{pf}
	dir := t.TempDir()
	cfg := &tactype.Config{}
	if err := generateGoFlock(pf, dir, "people", cfg, parser.NewFlockIndex(files)); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "golang", "people", "flock_helpers.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func FlockMergePerson(a, b *Person) (*Person, error)",
		"func FlockMergeEntriesPerson(a, b *Person, at *flock.Hlc) (survivor, loser *Person, err error)",
		"func FlockSetPersonDisplay(e *Person, v string, at *flock.Hlc)",
		"func FlockAddPersonTags(e *Person, v string, at *flock.Hlc)",
		"func FlockCheckUniquePerson(",
		"func FlockOverlapPerson(",
		"func FlockLivePerson(e *Person) error",
		"func FlockPurgeReadyPerson(",
		"func FlockDigestPerson(e *Person) []byte",
		"FlockMaxSkewMs int64 = 60000",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q", want)
		}
	}

	other := model.ProtoFile{Package: "plain", Messages: []model.Message{{Name: "Thing"}}}
	if err := generateGoFlock(other, dir, "plain", cfg, parser.NewFlockIndex([]model.ProtoFile{other})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "golang", "plain", "flock_helpers.go")); !os.IsNotExist(err) {
		t.Fatalf("a package without flock entries got a flock file: %v", err)
	}
}
