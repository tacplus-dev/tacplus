// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func rustSensAnn(required bool, bits int64) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"sensitive": {Type: model.AnnotationTypeBool, BoolValue: required, IntValue: bits}}
}

// rustRedactFiles is a small uni-config: a (spec.sensitive_message) Secrets,
// a plain Database reached through a sensitive field, repeated / map / optional
// leaves, and a message that needs nothing.
func rustRedactFiles() []model.ProtoFile {
	secrets := model.Message{Name: "Secrets",
		Annotations: map[string]model.AnnotationValue{"sensitive_message": {Type: model.AnnotationTypeBool, IntValue: -1}},
		Fields: []model.Field{
			{Name: "api_token", Type: "string", Number: 1, Annotations: rustSensAnn(true, -1)},
			{Name: "signing_key", Type: "bytes", Number: 2, Annotations: rustSensAnn(false, 256)},
			{Name: "secret_version", Type: "string", Number: 3, Annotations: map[string]model.AnnotationValue{"not_sensitive": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "recovery_code", Type: "string", Number: 4, IsOptional: true},
		}}
	database := model.Message{Name: "Database", Fields: []model.Field{
		{Name: "dsn", Type: "string", Number: 1},
		{Name: "port", Type: "int32", Number: 2},
	}}
	endpoint := model.Message{Name: "Endpoint", Fields: []model.Field{
		{Name: "password", Type: "string", Number: 1, Annotations: rustSensAnn(false, -1)},
	}}
	uni := model.Message{Name: "UniConfig", Fields: []model.Field{
		{Name: "name", Type: "string", Number: 1},
		{Name: "secrets", Type: "Secrets", Number: 2, IsMessage: true},
		{Name: "database", Type: "Database", Number: 3, IsMessage: true, Annotations: rustSensAnn(false, -1)},
		{Name: "peers", Number: 4, IsMap: true, MapKeyType: "string", MapValueType: "Endpoint", MapValueIsMessage: true},
		{Name: "endpoints", Type: "Endpoint", Number: 5, IsMessage: true, IsRepeated: true},
		{Name: "api_tokens", Type: "string", Number: 6, IsRepeated: true, Annotations: rustSensAnn(false, -1)},
		{Name: "signing_keys", Number: 7, IsMap: true, MapKeyType: "string", MapValueType: "bytes", Annotations: rustSensAnn(false, -1)},
	}}
	plain := model.Message{Name: "Plain", Fields: []model.Field{{Name: "label", Type: "string", Number: 1}}}
	return []model.ProtoFile{{Package: "demo", Messages: []model.Message{uni, secrets, database, endpoint, plain}}}
}

func TestRustRedact_Shape(t *testing.T) {
	files := rustRedactFiles()
	var b strings.Builder
	writeRedact(&b, files[0], files)
	got := b.String()
	for _, want := range []string{
		"pub fn redact_uni_config(m: &UniConfig) -> UniConfig {",
		"let mut out = m.clone();",
		"redact_uni_config_in_place(&mut out);",
		"pub fn redact_uni_config_in_place(m: &mut UniConfig) {",
		// annotated recursion into a sub-message, a map value, a repeated element
		"if let Some(v) = m.secrets.as_mut() {\n        redact_secrets_in_place(v);\n    }",
		"for v in m.peers.values_mut() {\n        redact_endpoint_in_place(v);\n    }",
		"for v in m.endpoints.iter_mut() {\n        redact_endpoint_in_place(v);\n    }",
		// a plain type through a sensitive field: every leaf, and the indicator
		"if let Some(v) = m.database.as_mut() {\n        redact_all_database_in_place(v);\n        m.database_redacted = true;\n    }",
		"pub fn redact_all_database_in_place(m: &mut Database) {",
		"if !m.dsn.is_empty() {\n        m.dsn = \"REDACTED\".to_string();\n    }",
		// repeated string, map<string, bytes>: each non-empty element / value
		"for v in m.api_tokens.iter_mut() {\n        if !v.is_empty() {\n            *v = \"REDACTED\".to_string();\n            m.api_tokens_redacted = true;",
		"for v in m.signing_keys.values_mut() {\n        if !v.is_empty() {\n            *v = b\"REDACTED\".to_vec();\n            m.signing_keys_redacted = true;",
		// sensitive_message: every leaf but the not_sensitive one
		"pub fn redact_secrets(m: &Secrets) -> Secrets {",
		"if !m.api_token.is_empty() {\n        m.api_token = \"REDACTED\".to_string();\n        m.api_token_redacted = true;\n    }",
		"m.signing_key = b\"REDACTED\".to_vec();",
		"if let Some(v) = m.recovery_code.as_mut() {\n        if !v.is_empty() {\n            *v = \"REDACTED\".to_string();\n            m.recovery_code_redacted = true;",
		"pub fn redact_endpoint(m: &Endpoint) -> Endpoint {",
		// the verdict helper, with the integer entropy table
		"fn tacplus_sensitive_verdict(v: &[u8], is_bytes: bool, min_bits: i64) -> u8 {",
		`v.eq_ignore_ascii_case(b"redacted") || v.eq_ignore_ascii_case(b"changeme")`,
		"95 => 6569855,",
		"v.len() as i64 * 8_000_000",
		"if est < min_bits * 1_000_000 {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, not := range []string{"secret_version", "redact_plain", "m.name", "m.port", "redact_database_in_place", "redact_all_endpoint"} {
		if strings.Contains(got, not) {
			t.Errorf("unexpected %q in:\n%s", not, got)
		}
	}
}

// A proto with no sensitive annotation gets no redaction output at all.
func TestRustRedact_NothingWithoutAnnotations(t *testing.T) {
	files := []model.ProtoFile{{Package: "demo", Messages: []model.Message{
		{Name: "Plain", Fields: []model.Field{{Name: "password_hint", Type: "string", Number: 1}}},
	}}}
	var b strings.Builder
	writeRedact(&b, files[0], files)
	if b.Len() != 0 {
		t.Fatalf("want no output, got:\n%s", b.String())
	}
}

func TestRustSensitiveChecks(t *testing.T) {
	msg := model.Message{Name: "SecretCheck", Fields: []model.Field{
		{Name: "token", Type: "string", Number: 1, Annotations: rustSensAnn(true, -1)},
		{Name: "key", Type: "bytes", Number: 2, Annotations: rustSensAnn(true, -1)},
		{Name: "optional_token", Type: "string", Number: 3, Annotations: rustSensAnn(false, 64)},
		{Name: "maybe", Type: "string", Number: 4, IsOptional: true, Annotations: rustSensAnn(true, -1)},
		{Name: "plain", Type: "string", Number: 5, Annotations: rustSensAnn(false, -1)},
	}}
	var b strings.Builder
	if !writeRustSensitiveChecks(&b, msg) {
		t.Fatal("want sensitive checks")
	}
	got := b.String()
	for _, want := range []string{
		"if self.token.is_empty() {\nreturn Err(\"field SecretCheck.token (sensitive): required and empty\".to_string());",
		"match tacplus_sensitive_verdict(self.token.as_bytes(), false, 128) {",
		"1 => return Err(\"field SecretCheck.token (sensitive): a known placeholder value\".to_string()),",
		"2 => return Err(\"field SecretCheck.token (sensitive): estimated entropy below 128 bits\".to_string()),",
		"match tacplus_sensitive_verdict(self.key.as_slice(), true, 128) {",
		"match tacplus_sensitive_verdict(self.optional_token.as_bytes(), false, 64) {",
		"estimated entropy below 64 bits",
		"if self.maybe.as_deref().unwrap_or_default().is_empty() {",
		"tacplus_sensitive_verdict(self.maybe.as_deref().unwrap_or_default().as_bytes(), false, 128)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "optional_token.is_empty()") || strings.Contains(got, "self.plain") {
		t.Errorf("a non-required / unchecked field must not be validated:\n%s", got)
	}

	// Wired into both validator families, and the Kani harness is skipped (the
	// default message fails a required sensitive field).
	var vb strings.Builder
	if err := generateMessageValidation(&vb, msg, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(vb.String(), "field SecretCheck.token (sensitive): required and empty"); n != 2 {
		t.Errorf("want the check in validate and validate_at_rest, got %d", n)
	}
	var pb strings.Builder
	writeProofHarnesses(&pb, model.ProtoFile{Package: "demo", Messages: []model.Message{msg}})
	if strings.Contains(pb.String(), "verify_secret_check_validate") {
		t.Errorf("kani harness must skip a message with sensitive checks:\n%s", pb.String())
	}
}

// The entropy table is the parser's, verbatim.
func TestRustSensitiveVerdict_TableFromParser(t *testing.T) {
	var b strings.Builder
	writeRustSensitiveVerdictHelper(&b)
	tab := parser.EntropyMicroBits()
	for _, p := range parser.EntropyPools() {
		want := fmt.Sprintf("%d => %d,", p, tab[p])
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
