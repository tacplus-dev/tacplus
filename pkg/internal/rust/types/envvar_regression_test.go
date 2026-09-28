// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

func TestRustEnvVarEnumsUseHelpersOnlyForSamePackageEnums(t *testing.T) {
	pf := model.ProtoFile{
		Package: "audit",
		RelPath: "services/audit.proto",
		Imports: []model.Import{{
			Path:    "sys/spec.proto",
			Package: "spec",
		}},
		Enums: []model.Enum{{
			Name: "LocalMode",
			Values: []model.EnumValue{
				{Name: "LOCAL_MODE_UNSPECIFIED"},
				{Name: "LOCAL_MODE_ACTIVE"},
			},
		}},
		Messages: []model.Message{{
			Name: "AuditConfig",
			Fields: []model.Field{
				{Name: "local_mode", Type: "LocalMode", IsEnum: true},
				{Name: "cross_mode", Type: "TlsKeyType", TypePackage: "spec", IsEnum: true},
				{Name: "cross_modes", Type: "TlsKeyType", TypePackage: "spec", IsEnum: true, IsRepeated: true},
				{Name: "blob", Type: "bytes"},
			},
		}, {
			Name:   "EmptyConfig",
			Fields: []model.Field{{Name: "skipped_map", IsMap: true, MapKeyType: "string", MapValueType: "string"}},
		}},
	}

	var b strings.Builder
	writeEnumHelpers(&b, pf)
	writeEnvVar(&b, pf)
	rendered := b.String()

	for _, want := range []string{
		`LocalMode::to_string_value(msg.local_mode)`,
		`LocalMode::parse_i32(val)`,
		`crate::sys::spec::TlsKeyType::try_from(msg.cross_mode)`,
		`e.as_str_name().to_string()`,
		`crate::sys::spec::TlsKeyType::from_str_name(val).map(|v| v as i32)`,
		`crate::sys::spec::TlsKeyType::from_str_name(s).map(|v| v as i32)`,
		`base64::Engine::encode(&base64::engine::general_purpose::STANDARD, &msg.blob)`,
		`base64::Engine::decode(&base64::engine::general_purpose::STANDARD, val)`,
		`pub fn empty_config_to_env_var(_msg: &EmptyConfig) -> std::collections::HashMap<String, String>`,
		`let result = std::collections::HashMap::new();`,
		`pub fn empty_config_from_env_var(_env: &std::collections::HashMap<String, String>) -> EmptyConfig`,
		`let result = EmptyConfig::default();`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust env-var helpers missing %q:\n%s", want, rendered)
		}
	}

	for _, notWant := range []string{
		"crate::sys::spec::TlsKeyType::to_string_value",
		"crate::sys::spec::TlsKeyType::parse_i32",
		"base64::encode",
		"base64::decode",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered Rust env-var helpers used local helper on cross enum %q:\n%s", notWant, rendered)
		}
	}
}
