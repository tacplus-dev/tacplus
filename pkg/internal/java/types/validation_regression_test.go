// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func TestJavaValidationEmitsDefaultCollectionBounds(t *testing.T) {
	var b strings.Builder
	if err := writeMessageValidation(&b, auditCollectionMessage(), "AuditOuter", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"public static void validateAuditCollection(AuditOuter.AuditCollection msg) throws ProtoException",
		"Validate names (default max size: 1024 elements)",
		"if (msg.getNamesCount() > 1024)",
		"Validate names elements (default max size: 32768 bytes)",
		"for (String val : msg.getNamesList())",
		"if (val.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > 32768)",
		"Validate blobs elements (default max size: 32768 bytes)",
		"for (com.google.protobuf.ByteString val : msg.getBlobsList())",
		"Validate labels (default max size: 1024 elements)",
		"if (msg.getLabelsMap().size() > 1024)",
		"for (String key : msg.getLabelsMap().keySet())",
		"for (String val : msg.getLabelsMap().values())",
		"Validate payloads (default max size: 1024 elements)",
		"for (com.google.protobuf.ByteString val : msg.getPayloadsMap().values())",
		"Validate title (default max size: 32768 bytes)",
		"if (msg.getTitle().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > 32768)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestJavaValidationEmitsConstraintParityHandlers(t *testing.T) {
	var b strings.Builder
	if err := writeMessageValidation(&b, auditConstraintParityMessage(), "AuditOuter", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"//@ invariant self.ttl_seconds > 0;",
		"Validate endpointUrl (assert_url: URL_SCHEME_HTTPS)",
		"Validation.validateUrl(msg.getEndpointUrl(), \"URL_SCHEME_HTTPS\", \"endpointUrl\");",
		"Validate configPath (assert_file_path: PATH_ABSOLUTE)",
		"Validation.validateFilePath(msg.getConfigPath(), \"PATH_ABSOLUTE\", \"configPath\");",
		"Validate publicKeyPem (assert_enc_type: ENC_TYPE_KEY_PUBLIC_ANY)",
		"Validation.validateEncType(msg.getPublicKeyPem(), \"ENC_TYPE_KEY_PUBLIC_ANY\", \"publicKeyPem\");",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestJavaValidationEmitsOneofScalarBounds(t *testing.T) {
	var b strings.Builder
	if err := writeMessageValidation(&b, auditOneofMessage(), "AuditOuter", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate choice (default max size: 32768 bytes)",
		"if (msg.getChoice().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > 32768)",
		"Validate limitedChoice (assert_max_size: 8 bytes)",
		"if (msg.getLimitedChoice().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > 8)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java validation missing %q:\n%s", want, rendered)
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
