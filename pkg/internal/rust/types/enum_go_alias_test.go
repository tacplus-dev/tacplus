// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// TestRustEnumFromStrAcceptsGoFriendlyName pins that Rust FromStr accepts the
// string Go's generated <Enum>ToString emits. Go strips the enum-name prefix
// (PROFILE_GATE -> "gate"), and a Rust reader must parse a value a Go writer
// produced (e.g. an env var). Sentinel values stay unparseable, as in Go.
func TestRustEnumFromStrAcceptsGoFriendlyName(t *testing.T) {
	pf := model.ProtoFile{
		Enums: []model.Enum{{
			Name: "Profile",
			Values: []model.EnumValue{
				{Name: "PROFILE_UNSPECIFIED", Number: 0},
				{Name: "PROFILE_GATE", Number: 1},
				{Name: "PROFILE_READ_ONLY", Number: 2},
				{Name: "PROFILE_DESTRUCTIVE", Number: 3, JsonValue: "destroy"},
			},
		}},
	}

	var b strings.Builder
	writeEnumHelpers(&b, pf)
	writeEnumTestContent(&b, pf.Enums[0])
	rendered := b.String()

	for _, want := range []string{
		// FromStr: full proto name and Go's friendly name both parse.
		`"profile-gate" => Ok(Profile::Gate),`,
		`"gate" => Ok(Profile::Gate),`,
		`"profile-read-only" => Ok(Profile::ReadOnly),`,
		`"read-only" => Ok(Profile::ReadOnly),`,
		`"profile-destructive" => Ok(Profile::Destructive),`,
		`"destroy" => Ok(Profile::Destructive),`,
		// Display is unchanged.
		`Profile::Gate => "profile-gate",`,
		`Profile::Destructive => "destroy",`,
		// Generated Rust test exercises the stripped name.
		`assert_eq!(Profile::from_str("gate").unwrap(), Profile::Gate);`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust enum helpers missing %q:\n%s", want, rendered)
		}
	}

	for _, notWant := range []string{`"unspecified"`, `"profile-unspecified"`} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered Rust enum helpers accept sentinel %s:\n%s", notWant, rendered)
		}
	}
}
