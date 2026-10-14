// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func TestJavaEnumParseAliasesIncludeProtoNames(t *testing.T) {
	enum := model.Enum{
		Name: "Format",
		Values: []model.EnumValue{
			{Name: "FORMAT_JSON", Number: 1},
			{Name: "FORMAT_CONSOLE", Number: 2},
		},
	}

	got := javaEnumParseAliases(enum, enum.Values[0])
	want := []string{"json", "format-json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("javaEnumParseAliases() = %#v, want %#v", got, want)
	}
}

func TestJavaEnumHelperAcceptsNumericAndProtoNames(t *testing.T) {
	enum := model.Enum{
		Name: "Format",
		Values: []model.EnumValue{
			{Name: "FORMAT_UNDEFINED", Number: 0},
			{Name: "FORMAT_JSON", Number: 1, JsonValue: "json"},
			{Name: "FORMAT_CONSOLE", Number: 2, JsonValue: "console"},
		},
	}

	var b strings.Builder
	writeEnumHelperMethods(&b, enum, "Logger")
	rendered := b.String()

	for _, want := range []string{
		"String trimmed = s.trim();",
		"int numeric = Integer.parseInt(trimmed);",
		"Logger.Format parsed = Logger.Format.forNumber(numeric);",
		`case "json", "format-json" -> Logger.Format.FORMAT_JSON;`,
		`case "console", "format-console" -> Logger.Format.FORMAT_CONSOLE;`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java enum helper missing %q:\n%s", want, rendered)
		}
	}
}
