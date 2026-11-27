// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// required_if_eq against an enum sibling must name the member protobuf-es
// generates, which drops the enum's shared UPPER_SNAKE(EnumName)_ prefix
// (EvictionPolicy.LRU), not the proto value name (issue #464).
func requiredIfEqEnumMessage(condValue string) model.Message {
	return model.Message{
		Name: "TierConfig",
		Fields: []model.Field{
			{Name: "warm_eviction", Type: "EvictionPolicy", IsEnum: true, Number: 1},
			{Name: "warm_max_bytes", Type: "int64", Number: 2, Annotations: map[string]model.AnnotationValue{
				"required_if_eq": {Type: model.AnnotationTypeString, StringValue: "warm_eviction=" + condValue},
			}},
		},
	}
}

func evictionPolicyEnum() model.Enum {
	return model.Enum{Name: "EvictionPolicy", Values: []model.EnumValue{
		{Name: "EVICTION_POLICY_UNDEFINED", Number: 0},
		{Name: "EVICTION_POLICY_LRU", Number: 1},
		{Name: "EVICTION_POLICY_LFU", Number: 2},
	}}
}

func TestTypeScriptRequiredIfEqEnumSiblingUsesProtobufEsMemberName(t *testing.T) {
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, requiredIfEqEnumMessage("EVICTION_POLICY_LRU"), []model.Enum{evictionPolicyEnum()}); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()
	if want := "if (m.warmEviction === EvictionPolicy.LRU && m.warmMaxBytes === 0n) {"; !strings.Contains(rendered, want) {
		t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, rendered)
	}
	if strings.Contains(rendered, "EvictionPolicy.EVICTION_POLICY_LRU") {
		t.Fatalf("rendered TypeScript validation names the proto value, not the protobuf-es member:\n%s", rendered)
	}
}

// An enum whose values do not all share the prefix keeps its names whole in
// protobuf-es, so the check must too.
func TestTypeScriptRequiredIfEqEnumSiblingKeepsUnsharedPrefix(t *testing.T) {
	enum := evictionPolicyEnum()
	enum.Values = append(enum.Values, model.EnumValue{Name: "OTHER", Number: 3})
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, requiredIfEqEnumMessage("EVICTION_POLICY_LRU"), []model.Enum{enum}); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	if want := "m.warmEviction === EvictionPolicy.EVICTION_POLICY_LRU &&"; !strings.Contains(b.String(), want) {
		t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, b.String())
	}
}

func TestTypeScriptRequiredIfEqEnumSiblingRejectsUnknownValue(t *testing.T) {
	var b strings.Builder
	err := generateTypeScriptValidationFunction(&b, requiredIfEqEnumMessage("EVICTION_POLICY_MRU"), []model.Enum{evictionPolicyEnum()})
	if err == nil || !strings.Contains(err.Error(), "EVICTION_POLICY_MRU") {
		t.Fatalf("want an error naming the unknown enum value, got %v", err)
	}
}
