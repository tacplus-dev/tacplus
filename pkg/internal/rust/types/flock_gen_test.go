// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func flockAnn(kv ...any) map[string]model.AnnotationValue {
	out := map[string]model.AnnotationValue{}
	for i := 0; i < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case bool:
			out[kv[i].(string)] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: v}
		case int:
			out[kv[i].(string)] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: int64(v)}
		case string:
			out[kv[i].(string)] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: v}
		}
	}
	return out
}

// rustFlockFiles is a small flock table: an entry with a LAST_WRITE scalar, a
// unique remove-wins set of messages and a nested message, and a package
// without any flock entry.
func rustFlockFiles() []model.ProtoFile {
	hlc := func(name string, n int, ann map[string]model.AnnotationValue) model.Field {
		return model.Field{Name: name, Number: n, Type: "Hlc", TypePackage: parser.FlockPackage, IsMessage: true, Annotations: ann}
	}
	link := model.Message{Name: "Link", Fields: []model.Field{
		{Name: "idp", Type: "string", Number: 1, Annotations: flockAnn("identity", true)},
		{Name: "added_ms", Type: "int64", Number: 2, Annotations: flockAnn("merge", parser.FlockRuleMin)},
	}}
	profile := model.Message{Name: "Profile", Fields: []model.Field{
		{Name: "nickname", Type: "string", Number: 1, Annotations: flockAnn("merge", parser.FlockRuleLastWrite)},
	}}
	user := model.Message{Name: "User", Fields: []model.Field{
		{Name: "key", Type: "string", Number: 1, Annotations: flockAnn("store_key", true, "indexed", true)},
		hlc("created", 2, flockAnn("merge", parser.FlockRuleImmutable)),
		hlc("written", 3, flockAnn("store_epoch", true)),
		{Name: "deleted", Number: 4, Type: "DeletionMark", TypePackage: parser.FlockPackage, IsMessage: true, Annotations: flockAnn("deleted_flag", true)},
		{Name: "display", Type: "string", Number: 5, Annotations: flockAnn("merge", parser.FlockRuleLastWrite)},
		{Name: "links", Type: "Link", Number: 6, IsMessage: true, IsRepeated: true, Annotations: flockAnn("merge", parser.FlockRuleSetRemoveWins, "unique", true)},
		{Name: "profile", Type: "Profile", Number: 7, IsMessage: true},
	}}
	root := model.Message{Name: "UsersState",
		Annotations: map[string]model.AnnotationValue{
			"service_state":       {Type: model.AnnotationTypeBool, BoolValue: true},
			"service_state_label": {StringSliceValue: []string{parser.FlockLabel}},
		},
		Fields: []model.Field{
			{Name: "users", Type: "User", Number: 1, IsMessage: true, IsRepeated: true, Annotations: flockAnn("persistence", true, "assert_max_size", 16)},
		}}
	plain := model.Message{Name: "Plain", Fields: []model.Field{{Name: "label", Type: "string", Number: 1}}}
	return []model.ProtoFile{
		{Package: "people", RelPath: "people/people.proto", Messages: []model.Message{link, profile, user, root}},
		{Package: "other", RelPath: "other/other.proto", Messages: []model.Message{plain}},
		{Package: parser.FlockPackage, RelPath: "sys/flock.proto"},
	}
}

func TestRustFlock_EmitsEntryMerge(t *testing.T) {
	files := rustFlockFiles()
	var b strings.Builder
	writeFlock(&b, files[0], files)
	got := b.String()
	for _, want := range []string{
		"mod tacplus_flock {",
		"use crate::sys::flock as fpb;",
		"pub use tacplus_flock::*;",
		"pub const FLOCK_MAX_SKEW_MS: i64 = 60000;",
		"pub struct FlockClock {",
		"pub fn flock_new_key() -> Result<String, crate::sys::errors::Error> {",
		"pub fn flock_new_user(key: &str, created: &fpb::Hlc) -> User {",
		"pub fn flock_merge_user(a: &User, b: &User) -> Result<User, crate::sys::errors::Error> {",
		"pub fn flock_merge_entries_user(a: &User, b: &User, at: &fpb::Hlc) -> Result<(User, User), crate::sys::errors::Error> {",
		"pub fn flock_set_user_display(e: &mut User, v: String, at: &fpb::Hlc) {",
		"pub fn flock_set_user_profile_nickname(e: &mut User, v: String, at: &fpb::Hlc) {",
		"let t = e.profile.get_or_insert_with(Default::default);",
		"pub fn flock_add_user_links(e: &mut User, v: &Link, at: &fpb::Hlc) {",
		`flock_add_op(&mut m, "links", flock_id_string(&v.idp), flock_marshal(v), at);`,
		"pub fn flock_remove_user_links(e: &mut User, v: &Link, at: &fpb::Hlc) {",
		`for id in flock_live(e.flock_meta.as_ref(), "links", true).keys() {`,
		"pub fn flock_overlap_user(a: &User, b: &User, k: &str, window_ms: i64) -> FlockOverlap {",
		"pub fn flock_live_user(e: &User) -> Result<(), crate::sys::errors::Error> {",
		"pub fn flock_digest_user(e: &User) -> Vec<u8> {",
		"c.links = Default::default();",
		"fn flock_join_profile(",
		"cur.added_ms = flock_min_non_zero(&cur.added_ms, &e.added_ms);",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("flock output lacks %q", want)
		}
	}
	if strings.Count(got, "pub struct FlockClock {") != 1 {
		t.Errorf("the runtime is emitted more than once per package")
	}
}

func TestRustFlock_NothingWithoutEntries(t *testing.T) {
	files := rustFlockFiles()
	var b strings.Builder
	writeFlock(&b, files[1], files)
	if b.Len() != 0 {
		t.Errorf("a package without flock entries got flock output:\n%s", b.String())
	}
}
