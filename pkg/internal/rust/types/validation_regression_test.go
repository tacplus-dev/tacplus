// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func TestRustValidationEmitsDefaultCollectionBounds(t *testing.T) {
	var b strings.Builder
	if err := generateMessageValidation(&b, auditCollectionMessage(), nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"impl AuditCollection {",
		"Validate names (default max size: 1024 elements)",
		"if self.names.len() > 1024",
		"Validate names elements (default max size: 32768 bytes)",
		"for (i, val) in self.names.iter().enumerate()",
		"if val.len() > 32768",
		"Validate blobs elements (default max size: 32768 bytes)",
		"for (i, val) in self.blobs.iter().enumerate()",
		"Validate labels (default max size: 1024 elements)",
		"if self.labels.len() > 1024",
		"for key in self.labels.keys()",
		"for (key, val) in self.labels.iter()",
		"Validate payloads (default max size: 1024 elements)",
		"for (key, val) in self.payloads.iter()",
		"Validate title (default max size: 32768 bytes)",
		"if self.title.len() > 32768",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust validation missing %q:\n%s", want, rendered)
		}
	}
}

// An optional string with assert_min_size/assert_max_size is Option<String> in
// Rust and must be unwrapped before .len() — a bare self.field.len() does not
// compile (Option has no public len). Regression guard for the D-2 length→size
// migration, which first moved optional-string fields onto the size validators.
func TestRustValidationUnwrapsOptionalSizeBounds(t *testing.T) {
	msg := model.Message{
		Name: "OptSize",
		Fields: []model.Field{
			{
				Name:       "client_id",
				Type:       "string",
				IsOptional: true,
				Annotations: map[string]model.AnnotationValue{
					"assert_min_size": {Type: model.AnnotationTypeInt, IntValue: 1},
					"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 64},
				},
			},
		},
	}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if let Some(ref val) = self.client_id {",
		"if val.len() < 1 {",
		"if val.len() > 64 {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("optional size validation missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "self.client_id.len()") {
		t.Fatalf("optional field must be unwrapped, not measured directly:\n%s", rendered)
	}
}

func TestRustValidationEmitsConstraintParityHandlers(t *testing.T) {
	var b strings.Builder
	if err := generateMessageValidation(&b, auditConstraintParityMessage(), nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate endpoint_url (assert_url: URL_SCHEME_HTTPS)",
		"validate_url(&self.endpoint_url, \"URL_SCHEME_HTTPS\")",
		"Validate config_path (assert_file_path: PATH_ABSOLUTE)",
		"validate_file_path(&self.config_path, \"PATH_ABSOLUTE\")",
		"Validate public_key_pem (assert_enc_type: ENC_TYPE_KEY_PUBLIC_ANY)",
		"validate_enc_type(&self.public_key_pem, \"ENC_TYPE_KEY_PUBLIC_ANY\")",
		"debug_assert!(self.ttl_seconds > 0, \"invariant violated: ttl_seconds\");",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestRustValidationHelpersDecodeBase64PadsLegacyUnpaddedInput(t *testing.T) {
	var b strings.Builder
	if err := generateHelperValidators(&b, &validatorUsage{needsEncType: true}); err != nil {
		t.Fatalf("generateHelperValidators: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"let mut padded = s.to_string();",
		"padded.extend(std::iter::repeat('=').take(4 - remainder));",
		"general_purpose::STANDARD.decode(&padded)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust validation helpers missing %q:\n%s", want, rendered)
		}
	}
}

func TestRustValidationHelpersDecodeBareBase64PadsLegacyUnpaddedInput(t *testing.T) {
	var b strings.Builder
	if err := generateHelperValidators(&b, &validatorUsage{needsBase64: true}); err != nil {
		t.Fatalf("generateHelperValidators: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"let mut padded = s.to_string();",
		"padded.extend(std::iter::repeat('=').take(4 - remainder));",
		"general_purpose::STANDARD.decode(&padded)",
		"decode_base64_candidate(s).map_err(|_| format!(\"invalid base64 string: {}\", s))?",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust validation helpers missing %q:\n%s", want, rendered)
		}
	}
}

func TestRustValidationEmitsOneofScalarBounds(t *testing.T) {
	var b strings.Builder
	if err := generateMessageValidation(&b, auditOneofMessage(), nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate choice (default max size: 32768 bytes)",
		"if let Some(audit_oneof::Selector::Choice(val)) = &self.selector",
		"if val.len() > 32768",
		"Validate limited_choice (assert_max_size: 8 bytes)",
		"if let Some(audit_oneof::Selector::LimitedChoice(val)) = &self.selector",
		"if val.len() > 8",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestRustFromMapAssignsOptionalAndRequiredFieldsCorrectly(t *testing.T) {
	pf := model.ProtoFile{
		Package: "audit",
		Enums: []model.Enum{{
			Name: "AuditParamKey",
			Values: []model.EnumValue{
				{Name: "AUDIT_PARAM_KEY_REQUIRED_ID", JsonValue: "required-id"},
				{Name: "AUDIT_PARAM_KEY_OPTIONAL_ID", JsonValue: "optional-id"},
			},
		}},
		Messages: []model.Message{{
			Name: "AuditParams",
			ParamMapConfig: &model.ParamMapConfig{
				KeyEnum: "AuditParamKey",
				Strict:  true,
			},
			Fields: []model.Field{
				{Name: "required_id", Type: "int64", ParamKey: "AUDIT_PARAM_KEY_REQUIRED_ID"},
				{Name: "optional_id", Type: "int64", IsOptional: true, ParamKey: "AUDIT_PARAM_KEY_OPTIONAL_ID"},
			},
		}},
	}

	var b strings.Builder
	if err := writeFromMap(&b, pf); err != nil {
		t.Fatalf("writeFromMap: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		`let normalized_key = key.to_uppercase().replace('-', "_");`,
		`"AUDIT_PARAM_KEY_REQUIRED_ID" | "REQUIRED_ID" => Some(AuditParamKey::RequiredId)`,
		"result.required_id = val;",
		"result.optional_id = Some(val);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust from_map missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "result.required_id = Some(val);") {
		t.Fatalf("rendered Rust from_map wraps non-optional scalar in Some():\n%s", rendered)
	}
}

func auditOneofMessage() model.Message {
	return model.Message{
		Name: "AuditOneof",
		Fields: []model.Field{
			{Name: "choice", Type: "string", IsOneOf: true, OneOfName: "selector"},
			{
				Name:      "limited_choice",
				Type:      "string",
				IsOneOf:   true,
				OneOfName: "selector",
				Annotations: map[string]model.AnnotationValue{
					"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
				},
			},
		},
	}
}

func auditCollectionMessage() model.Message {
	return model.Message{
		Name: "AuditCollection",
		Fields: []model.Field{
			{Name: "names", Type: "string", IsRepeated: true},
			{Name: "blobs", Type: "bytes", IsRepeated: true},
			{Name: "labels", IsMap: true, MapKeyType: "string", MapValueType: "string"},
			{Name: "payloads", IsMap: true, MapKeyType: "string", MapValueType: "bytes"},
			{Name: "title", Type: "string"},
		},
	}
}

func auditConstraintParityMessage() model.Message {
	return model.Message{
		Name: "AuditConstraintParity",
		Fields: []model.Field{
			{
				Name: "endpoint_url",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_url": {Type: model.AnnotationTypeEnum, EnumValue: "URL_SCHEME_HTTPS"},
				},
			},
			{
				Name: "config_path",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_file_path": {Type: model.AnnotationTypeEnum, EnumValue: "PATH_ABSOLUTE"},
				},
			},
			{
				Name: "public_key_pem",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_enc_type": {Type: model.AnnotationTypeEnum, EnumValue: "ENC_TYPE_KEY_PUBLIC_ANY"},
				},
			},
			{
				Name: "ttl_seconds",
				Type: "int64",
				Annotations: map[string]model.AnnotationValue{
					"assert_invariant": {Type: model.AnnotationTypeString, StringValue: "self.ttl_seconds > 0"},
				},
			},
		},
	}
}
