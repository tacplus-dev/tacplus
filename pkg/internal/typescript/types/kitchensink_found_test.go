// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Found by the kitchensink (tacplus#196): the generated TypeScript did not
// compile.

// protobuf-es names an enum's values without the prefix they all share
// (MODE_ON is Mode.ON), so required_if_eq on an enum sibling rendered
// Mode.MODE_ON, which does not exist. An enum whose values do not all carry
// the prefix keeps the proto names.
func TestTypeScriptRequiredIfEqNamesTheEnumValueAsProtobufEsDoes(t *testing.T) {
	enums := []model.Enum{
		{Name: "Mode", Values: []model.EnumValue{{Name: "MODE_UNSPECIFIED"}, {Name: "MODE_ON", Number: 1}}},
		{Name: "Phase", Values: []model.EnumValue{{Name: "PHASE_UNSPECIFIED"}, {Name: "RUNNING", Number: 1}}},
		{Name: "HttpCode", Values: []model.EnumValue{{Name: "HTTP_CODE_UNSPECIFIED"}, {Name: "HTTP_CODE_404", Number: 1}}},
	}
	for _, tc := range []struct {
		enum, value, want string
	}{
		{"Mode", "MODE_ON", "m.mode === Mode.ON &&"},
		{"Phase", "RUNNING", "m.mode === Phase.RUNNING &&"},
		{"HttpCode", "HTTP_CODE_404", "m.mode === HttpCode.HTTP_CODE_404 &&"},
	} {
		t.Run(tc.enum, func(t *testing.T) {
			msg := model.Message{Name: "Toggle", Fields: []model.Field{
				{Name: "mode", Type: tc.enum, IsEnum: true},
				{Name: "on_reason", Type: "string", Annotations: map[string]model.AnnotationValue{
					"required_if_eq": {Type: model.AnnotationTypeString, StringValue: "mode=" + tc.value},
				}},
			}}
			var b strings.Builder
			if err := generateTypeScriptValidationFunction(&b, msg, enums); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.String(), tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, b.String())
			}
		})
	}
}

// A proto3 optional scalar is `x?: T` in protobuf-es: absent is undefined,
// so merge fills it when dst's is undefined. `dst.fBytes.length` did not
// compile on an `fBytes?: Uint8Array`.
func TestTypeScriptMergeFillsAnAbsentOptionalScalar(t *testing.T) {
	pf := model.ProtoFile{Messages: []model.Message{{Name: "OptionalScalars", Fields: []model.Field{
		{Name: "f_bytes", Type: "bytes", IsOptional: true},
		{Name: "f_string", Type: "string", IsOptional: true},
		{Name: "f_int64", Type: "int64", IsOptional: true},
		{Name: "plain", Type: "bytes"},
	}}}}
	var b strings.Builder
	writeMerge(&b, pf)
	got := b.String()
	for _, want := range []string{
		"if (dst.fBytes === undefined) { dst.fBytes = src.fBytes; }",
		"if (dst.fString === undefined) { dst.fString = src.fString; }",
		"if (dst.fInt64 === undefined) { dst.fInt64 = src.fInt64; }",
		"if (dst.plain.length === 0) { dst.plain = src.plain; }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}
