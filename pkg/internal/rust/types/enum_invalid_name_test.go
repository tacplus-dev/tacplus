// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// TestRustEnumFromStrInvalidTestUsesAnUnacceptedName pins that the generated
// from_str_invalid test asserts on a string FromStr refuses. An enum with a
// value named INVALID (sys.RpcOutcome's RPC_OUTCOME_INVALID) accepts "invalid"
// as Go's friendly name, so a fixed "invalid" made the generated test fail
// (#830: images.yml's pipeline smoke).
func TestRustEnumFromStrInvalidTestUsesAnUnacceptedName(t *testing.T) {
	enum := model.Enum{
		Name: "RpcOutcome",
		Values: []model.EnumValue{
			{Name: "RPC_OUTCOME_UNDEFINED", Number: 0},
			{Name: "RPC_OUTCOME_PERMITTED", Number: 1},
			{Name: "RPC_OUTCOME_INVALID", Number: 4},
			{Name: "RPC_OUTCOME_INVALID_X", Number: 5, JsonValue: "invalid-x"},
		},
	}

	var b strings.Builder
	writeEnumHelpers(&b, model.ProtoFile{Enums: []model.Enum{enum}})
	writeEnumTestContent(&b, enum)
	rendered := b.String()

	if !strings.Contains(rendered, `"invalid" => Ok(RpcOutcome::Invalid),`) {
		t.Fatalf("FromStr does not accept Go's friendly name \"invalid\":\n%s", rendered)
	}
	if strings.Contains(rendered, `RpcOutcome::from_str("invalid").is_err()`) ||
		strings.Contains(rendered, `RpcOutcome::from_str("invalid-x").is_err()`) {
		t.Fatalf("generated test asserts an accepted name is refused:\n%s", rendered)
	}
	if !strings.Contains(rendered, `RpcOutcome::from_str("invalid-x-x").is_err()`) {
		t.Fatalf("generated test lacks a from_str_invalid assertion on an unaccepted name:\n%s", rendered)
	}
}
