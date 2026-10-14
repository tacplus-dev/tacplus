// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

func TestPythonValidationEmitsDefaultCollectionBounds(t *testing.T) {
	blocks, err := buildValidationBlocks(auditCollectionMessage(), nil)
	if err != nil {
		t.Fatalf("buildValidationBlocks: %v", err)
	}
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Code)
	}
	rendered := b.String()

	for _, want := range []string{
		"# Validate names (default max size: 1024 elements)",
		"if len(msg.names) > 1024:",
		"# Validate names elements (default max size: 32768 bytes)",
		"for val in msg.names:",
		"if len(val.encode('utf-8')) > 32768:",
		"# Validate blobs elements (default max size: 32768 bytes)",
		"for val in msg.blobs:",
		"if len(val) > 32768:",
		"# Validate labels (default max size: 1024 elements)",
		"if len(msg.labels) > 1024:",
		"for key in msg.labels.keys():",
		"for val in msg.labels.values():",
		"# Validate payloads (default max size: 1024 elements)",
		"for val in msg.payloads.values():",
		"# Validate title (default max size: 32768 bytes)",
		"if len(msg.title.encode('utf-8')) > 32768:",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Python validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestPythonValidationEmitsConstraintParityHandlers(t *testing.T) {
	blocks, err := buildValidationBlocks(auditConstraintParityMessage(), nil)
	if err != nil {
		t.Fatalf("buildValidationBlocks: %v", err)
	}
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Code)
	}
	rendered := b.String()

	for _, want := range []string{
		"# Validate endpoint_url (assert_url: URL_SCHEME_HTTPS)",
		"_validate_url(msg.endpoint_url, 'URL_SCHEME_HTTPS', 'endpoint_url')",
		"# Validate config_path (assert_file_path: PATH_ABSOLUTE)",
		"_validate_file_path(msg.config_path, 'PATH_ABSOLUTE', 'config_path')",
		"# Validate public_key_pem (assert_enc_type: ENC_TYPE_KEY_PUBLIC_ANY)",
		"_validate_enc_type(msg.public_key_pem, 'ENC_TYPE_KEY_PUBLIC_ANY', 'public_key_pem')",
		"# Formal invariant for ttl_seconds (assert_invariant): self.ttl_seconds > 0",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Python validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestPythonValidationEmitsOneofScalarBounds(t *testing.T) {
	blocks, err := buildValidationBlocks(auditOneofMessage(), nil)
	if err != nil {
		t.Fatalf("buildValidationBlocks: %v", err)
	}
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Code)
	}
	rendered := b.String()

	for _, want := range []string{
		"# Validate choice (default max size: 32768 bytes)",
		"if len(msg.choice.encode('utf-8')) > 32768:",
		"# Validate limited_choice (assert_max_size: 8 bytes)",
		"if len(msg.limited_choice.encode('utf-8')) > 8:",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Python validation missing %q:\n%s", want, rendered)
		}
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
