// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Java holds a uint32/fixed32 in a signed int and a uint64/fixed64 in a signed
// long. Compared as signed, a uint32 of 4000000000 passed `< 100`; the
// kitchensink's fuzzer found it (tacplus#196). The bounds compare as unsigned.
func TestJavaBoundsOrderUnsignedFieldsAsUnsigned(t *testing.T) {
	for _, tc := range []struct {
		typ, annotation string
		want            string
	}{
		{"uint32", "assert_lt", "if (Integer.toUnsignedLong(msg.getF()) >= 100L) {"},
		{"fixed32", "assert_ge", "if (Integer.toUnsignedLong(msg.getF()) < 100L) {"},
		{"uint64", "assert_le", "if (Long.compareUnsigned(msg.getF(), 100L) > 0) {"},
		{"fixed64", "assert_gt", "if (Long.compareUnsigned(msg.getF(), 100L) <= 0) {"},
		{"int32", "assert_lt", "if (msg.getF() >= 100) {"},
		{"int64", "assert_le", "if (msg.getF() > 100L) {"},
	} {
		t.Run(tc.typ+"/"+tc.annotation, func(t *testing.T) {
			msg := model.Message{Name: "M", Fields: []model.Field{javaBound("f", tc.typ, tc.annotation, 100)}}
			var b strings.Builder
			if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.String(), tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, b.String())
			}
		})
	}
}

func TestJavaNumericConstraintOrdersUnsignedFieldsAsUnsigned(t *testing.T) {
	le, mult := int64(64), int64(2)
	for _, tc := range []struct {
		typ  string
		want []string
	}{
		{"uint32", []string{"long val = Integer.toUnsignedLong(msg.getF());", "if (val > 64L) {", "if (val % 2L != 0L) {"}},
		{"uint64", []string{"long val = msg.getF();", "if (Long.compareUnsigned(val, 64L) > 0) {", "if (Long.remainderUnsigned(val, 2L) != 0L) {"}},
		{"int64", []string{"long val = msg.getF();", "if (val > 64L) {", "if (val % 2L != 0L) {"}},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			var b strings.Builder
			field := model.Field{Name: "f", Type: tc.typ}
			if err := writeJavaNumericConstraintValidation(&b, model.Message{Name: "M"}, field, "f", "getF", &model.NumericConstraint{Le: &le, MultipleOf: &mult}); err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("want %q in:\n%s", w, b.String())
				}
			}
		})
	}
}
