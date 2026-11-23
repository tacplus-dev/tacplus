// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// CheckSignInAssert is the generation-time check every language's validation
// emitter runs for (spec.assert_registrable_domain), (spec.assert_not_address)
// and (spec.assert_enum_not_in) (tacplus#224), so the shapes they accept
// cannot drift: the two string checks need a string field, singular or
// repeated (never a map); assert_enum_not_in needs an enum field and refuses
// listing 0, which is assert_enum_defined's job.
func CheckSignInAssert(msgName string, field model.Field, annotation string, value model.AnnotationValue) error {
	switch annotation {
	case "assert_registrable_domain", "assert_not_address":
		if field.IsMap || field.Type != "string" {
			return fmt.Errorf("%s annotation on field %s.%s requires a string field", annotation, msgName, field.Name)
		}
	case "assert_enum_not_in":
		if field.IsMap || !field.IsEnum {
			return fmt.Errorf("assert_enum_not_in annotation on field %s.%s requires an enum field", msgName, field.Name)
		}
		for _, v := range value.IntSliceValue {
			if v == 0 {
				return fmt.Errorf("assert_enum_not_in annotation on field %s.%s lists 0: use assert_enum_defined", msgName, field.Name)
			}
		}
	}
	return nil
}

// McpTestString is the value the generated MCP dispatch tests synthesize for a
// required string field: "mcp-test-<field>", except "example.com" for an
// assert_registrable_domain field, which refuses a single label.
func McpTestString(field model.Field) string {
	if a, ok := field.Annotations["assert_registrable_domain"]; ok && a.BoolValue {
		return "example.com"
	}
	return "mcp-test-" + field.Name
}

// EnumNotInRefuses reports whether field's assert_enum_not_in lists number.
func EnumNotInRefuses(field model.Field, number int32) bool {
	for _, n := range field.Annotations["assert_enum_not_in"].IntSliceValue {
		if n == number {
			return true
		}
	}
	return false
}
