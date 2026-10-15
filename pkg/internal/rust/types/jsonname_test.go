// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// (spec.json_key) reaches pbjson as the field's json_name; a comment quoting
// the option is left alone.
func TestInjectJsonNameForJsonKey(t *testing.T) {
	dir := t.TempDir()
	src := `syntax = "proto3";
// Example: int64 reset_at = 4 [(spec.json_key) = "reset"];
message M {
  int64 reset_at = 1 [(spec.json_key) = "reset"];
  string plain = 2;
}
`
	path := filepath.Join(dir, "m.proto")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := injectJsonNameForJsonKey(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`// Example: int64 reset_at = 4 [(spec.json_key) = "reset"];`,
		`int64 reset_at = 1 [(spec.json_key) = "reset", json_name = "reset"];`,
		`string plain = 2;`,
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Count(string(got), "json_name") != 1 {
		t.Fatalf("json_name must be added to the declaration only:\n%s", got)
	}
}

// From JSON reads each alias as its field's key (json_key when set), one
// message down too; the enum normalizer and stringifier look the field up under
// the key pbjson writes, which for a json_key'd field is the json_key.
func TestRustSerializationHonoursJsonKeyAndAliases(t *testing.T) {
	inner := model.Message{Name: "Inner", Fields: []model.Field{{Name: "note", Type: "string", JsonAliases: []string{"memo"}}}}
	probe := model.Message{Name: "Probe", Fields: []model.Field{
		{Name: "callback_url", Type: "string", JsonAliases: []string{"cb"}},
		{Name: "reset_at", Type: "int64", JsonKey: "reset", JsonAliases: []string{"resetTime"}},
		{Name: "level", Type: "Level", IsEnum: true, JsonKey: "lvl"},
		{Name: "inner", Type: "Inner", IsMessage: true},
	}}
	pf := model.ProtoFile{Package: "p", RelPath: "p/p.proto", Messages: []model.Message{inner, probe}}

	var b strings.Builder
	writeSerialization(&b, pf, []model.ProtoFile{pf})
	out := b.String()
	for _, want := range []string{
		`if let Some(val) = obj.remove("cb") { obj.entry("callback_url").or_insert(val); }`,
		`if let Some(val) = obj.remove("resetTime") { obj.entry("reset").or_insert(val); }`,
		`let key = if obj.contains_key("lvl") { "lvl" } else { "level" };`,
		`if let Some(val) = obj.remove("memo") { obj.entry("note").or_insert(val); }`,
		// Inner has only an alias, yet Probe must recurse into it.
		"normalize_inner_enums(val);",
		// An alias-only message has nothing to stringify and must not warn.
		"pub fn stringify_inner_enums(v: &mut serde_json::Value) {\nlet _ = v;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("serialization missing %q:\n%s", want, out)
		}
	}
}
