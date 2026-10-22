// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package recordread

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func TestScalarKeyType(t *testing.T) {
	msg := &parser.MessageDescriptor{Fields: []parser.FieldDescriptor{
		{Name: "id", Type: "string", TypeKind: "scalar"},
		{Name: "seq", Type: "int64", TypeKind: "scalar"},
		{Name: "tags", Type: "string", TypeKind: "scalar", IsRepeated: true},
		{Name: "amount", Type: "double", TypeKind: "scalar"},
		{Name: "owner", Type: "Owner", TypeKind: "message"},
	}}
	cases := []struct {
		field, want string
		ok          bool
	}{
		{"id", "string", true},
		{"seq", "int64", true},
		{"tags", "", false},
		{"amount", "", false},
		{"owner", "", false},
		{"missing", "", false},
	}
	for _, c := range cases {
		got, ok := ScalarKeyType(msg, c.field)
		if got != c.want || ok != c.ok {
			t.Errorf("ScalarKeyType(%q) = (%q,%v), want (%q,%v)", c.field, got, ok, c.want, c.ok)
		}
	}
}
