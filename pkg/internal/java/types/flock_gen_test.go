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

func flockJavaAnn(kv ...string) map[string]model.AnnotationValue {
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

// A flock entry gets FlockHelpers.java with its merge, writers, marks, unique
// check and digest (tacplus#206); a package without one gets no flock file.
func TestGenerateJavaFlock(t *testing.T) {
	hlc := func(name string, n int, ann map[string]model.AnnotationValue) model.Field {
		return model.Field{Name: name, Number: n, Type: "Hlc", TypePackage: "flock", IsMessage: true, Annotations: ann}
	}
	pf := model.ProtoFile{Package: "people", RelPath: "people/people.proto", Messages: []model.Message{
		{Name: "Profile", OuterClass: "People", Fields: []model.Field{
			{Name: "nickname", Number: 1, Type: "string", Annotations: flockJavaAnn("merge", "last_write")},
			{Name: "emails", Number: 2, Type: "string", IsRepeated: true, Annotations: flockJavaAnn("merge", "set_add_wins")},
		}},
		{Name: "Device", OuterClass: "People", Fields: []model.Field{
			{Name: "last_seen_ms", Number: 1, Type: "int64", Annotations: flockJavaAnn("merge", "max")},
		}},
		{Name: "Person", OuterClass: "People", Fields: []model.Field{
			{Name: "key", Number: 1, Type: "string", Annotations: flockJavaAnn("store_key", "true", "indexed", "true")},
			hlc("created", 2, flockJavaAnn("merge", "immutable")),
			hlc("written", 3, flockJavaAnn("store_epoch", "true")),
			{Name: "deleted", Number: 4, Type: "DeletionMark", TypePackage: "flock", IsMessage: true, Annotations: flockJavaAnn("deleted_flag", "true")},
			{Name: "display", Number: 5, Type: "string", Annotations: flockJavaAnn("merge", "last_write")},
			{Name: "tags", Number: 6, Type: "string", IsRepeated: true, Annotations: flockJavaAnn("merge", "set_add_wins", "unique", "true")},
			{Name: "generation", Number: 7, Type: "uint64", Annotations: flockJavaAnn("merge", "max")},
			{Name: "home_region", Number: 8, Type: "string", Annotations: flockJavaAnn("merge", "immutable")},
			{Name: "profile", Number: 9, Type: "Profile", IsMessage: true},
			{Name: "devices", Number: 10, IsMap: true, MapKeyType: "uint32", MapValueType: "Device", MapValueIsMessage: true, Annotations: flockJavaAnn("merge", "set_remove_wins")},
		}},
		{Name: "PeopleState", OuterClass: "People", Annotations: map[string]model.AnnotationValue{
			"service_state":       {Type: model.AnnotationTypeString, StringValue: "PeopleState"},
			"service_state_label": {Type: model.AnnotationTypeString, StringSliceValue: []string{parser.FlockLabel}},
		}, Fields: []model.Field{{Name: "people", Number: 1, Type: "Person", IsMessage: true, IsRepeated: true, Annotations: flockJavaAnn("persistence", "true")}}},
	}}
	dir := t.TempDir()
	cfg := &tactype.Config{JavaPackageBase: "com.example"}
	if err := generateJavaFlock(pf, dir, "com.example.types.people", "People", cfg, parser.NewFlockIndex([]model.ProtoFile{pf}), nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "FlockHelpers.java"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	const H = "com.example.types.sys.flock.Flock.Hlc"
	for _, want := range []string{
		"package com.example.types.people;",
		"import com.example.types.sys.errors.ProtoException;",
		"public final class FlockHelpers {",
		"public static final long FLOCK_MAX_SKEW_MS = 60000L;",
		"public static final class FlockClock {",
		"public static People.Person flockMergePerson(People.Person a, People.Person b) {",
		"public static FlockEntries<People.Person> flockMergeEntriesPerson(People.Person a, People.Person b, " + H + " at) {",
		"public static void flockSetPersonDisplay(People.Person.Builder e, String v, " + H + " at) {",
		"public static void flockAddPersonTags(People.Person.Builder e, String v, " + H + " at) {",
		// A nested writer creates the nested message on the way, as Go's does.
		"public static void flockSetPersonProfileNickname(People.Person.Builder e, String v, " + H + " at) {",
		"e.getProfileBuilder();\n",
		"public static void flockAddPersonProfileEmails(People.Person.Builder e, String v, " + H + " at) {",
		// A uint32 map key encodes unsigned; uint64 MAX compares unsigned.
		"public static void flockPutPersonDevices(People.Person.Builder e, int k, People.Device v, " + H + " at) {",
		"flockIDUint(Integer.toUnsignedLong(k))",
		"flockMaxNonZeroUlong(a.getGeneration(), b.getGeneration())",
		"static java.util.Map<Integer, People.Device> flockSetPerson_devices(",
		// IMMUTABLE: refused with the generated error, never a JDK exception.
		"throw new ProtoException(ProtoException.FAILED_PRECONDITION, \"flock: home_region is immutable\");",
		"public static void flockCheckUniquePerson(People.Person e, java.util.function.Function<String, java.util.Optional<String>> owner) {",
		"public static FlockOverlap flockOverlapPerson(",
		"public static void flockLivePerson(People.Person e) {",
		"public static boolean flockPurgeReadyPerson(",
		"public static java.util.Optional<String> flockRedirectPerson(People.Person e) {",
		// The digest leaves the set fields out, nested ones included.
		"public static byte[] flockDigestPerson(People.Person e) {",
		"if (c.hasProfile()) {\nc.getProfileBuilder().clearEmails();\n}\n",
		"c.clearTags();\n",
		"c.clearDevices();\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"IllegalArgumentException(", "IllegalStateException(", "$F", "$SKEW"} {
		if strings.Contains(out, bad) {
			t.Errorf("FlockHelpers.java contains %q", bad)
		}
	}

	other := model.ProtoFile{Package: "plain", Messages: []model.Message{{Name: "Thing", OuterClass: "Plain"}}}
	odir := t.TempDir()
	if err := generateJavaFlock(other, odir, "com.example.types.plain", "Plain", cfg, parser.NewFlockIndex([]model.ProtoFile{other}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(odir, "FlockHelpers.java")); !os.IsNotExist(err) {
		t.Fatalf("a package without a flock entry got FlockHelpers.java (%v)", err)
	}
}

// A type the generator cannot place is an error, never a guessed class name.
func TestGenerateJavaFlockUnresolvedType(t *testing.T) {
	pf := model.ProtoFile{Package: "people", Messages: []model.Message{
		{Name: "Person", Fields: []model.Field{
			{Name: "key", Number: 1, Type: "string", Annotations: flockJavaAnn("store_key", "true", "indexed", "true")},
			{Name: "created", Number: 2, Type: "Hlc", TypePackage: "flock", IsMessage: true, Annotations: flockJavaAnn("merge", "immutable")},
			{Name: "written", Number: 3, Type: "Hlc", TypePackage: "flock", IsMessage: true, Annotations: flockJavaAnn("store_epoch", "true")},
			{Name: "deleted", Number: 4, Type: "DeletionMark", TypePackage: "flock", IsMessage: true, Annotations: flockJavaAnn("deleted_flag", "true")},
		}},
		{Name: "PeopleState", Annotations: map[string]model.AnnotationValue{
			"service_state":       {Type: model.AnnotationTypeString, StringValue: "PeopleState"},
			"service_state_label": {Type: model.AnnotationTypeString, StringSliceValue: []string{parser.FlockLabel}},
		}, Fields: []model.Field{{Name: "people", Number: 1, Type: "Person", IsMessage: true, IsRepeated: true, Annotations: flockJavaAnn("persistence", "true")}}},
	}}
	err := generateJavaFlock(pf, t.TempDir(), "com.example.types.people", "", &tactype.Config{JavaPackageBase: "com.example"}, parser.NewFlockIndex([]model.ProtoFile{pf}), nil)
	if err == nil || !strings.Contains(err.Error(), "people.Person") {
		t.Fatalf("want an unresolved-type error naming people.Person, got %v", err)
	}
}

func TestToCamelOuter(t *testing.T) {
	for in, want := range map[string]string{"flock.proto": "Flock", "people_state.proto": "PeopleState", "v2_types.proto": "V2Types"} {
		if got := toCamelOuter(in); got != want {
			t.Errorf("toCamelOuter(%q) = %q, want %q", in, got, want)
		}
	}
}
