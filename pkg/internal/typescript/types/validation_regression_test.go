// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func TestTypeScriptValidationEmitsDefaultCollectionBounds(t *testing.T) {
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, auditCollectionMessage()); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"export function validateAuditCollection(m: AuditCollection | undefined, depth = 0): void",
		"Validate names (default max size: 1024 elements)",
		"m.names.length > 1024",
		"Validate names elements (default max size: 32768 bytes)",
		"for (let i = 0; i < m.names.length; i++)",
		"new TextEncoder().encode(val).length > 32768",
		"Validate blobs elements (default max size: 32768 bytes)",
		"for (let i = 0; i < m.blobs.length; i++)",
		"if (val.length > 32768)",
		"Validate labels (default max size: 1024 elements)",
		"Object.keys(m.labels).length > 1024",
		"for (const key of Object.keys(m.labels))",
		"for (const val of Object.values(m.labels))",
		"Validate payloads (default max size: 1024 elements)",
		"for (const val of Object.values(m.payloads))",
		"Validate title (default max size: 32768 bytes)",
		"new TextEncoder().encode(m.title).length > 32768",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestTypeScriptValidationEmitsConstraintParityHandlers(t *testing.T) {
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, auditConstraintParityMessage()); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate endpointUrl (assert_url: URL_SCHEME_HTTPS)",
		"parsed = new URL(m.endpointUrl);",
		"parsed.protocol !== 'https:'",
		"Validate configPath (assert_file_path: PATH_ABSOLUTE)",
		"const isAbsolutePath = (path: string): boolean",
		"field configPath must be an absolute path",
		"Validate publicKeyPem (assert_enc_type: ENC_TYPE_KEY_PUBLIC_ANY)",
		"validatePem(m.publicKeyPem, 'publicKeyPem');",
		"field ${label} must be a public key PEM",
		"Formal invariant for ttlSeconds (assert_invariant): self.ttl_seconds > 0",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestTypeScriptValidationEmitsOneofScalarBounds(t *testing.T) {
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, auditOneofMessage()); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate choice (default max size: 32768 bytes)",
		"m.selector.case === 'choice' && new TextEncoder().encode(m.selector.value).length > 32768",
		"Validate limitedChoice (assert_max_size: 8 bytes)",
		"m.selector.case === 'limitedChoice' && new TextEncoder().encode(m.selector.value).length > 8",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestTypeScriptValidationCompilesRepeatedUrlAndRequiredEnumDefined(t *testing.T) {
	msg := model.Message{
		Name: "AuditProduct",
		Fields: []model.Field{
			{
				Name:   "category",
				Type:   "ProductCategory",
				IsEnum: true,
				Annotations: map[string]model.AnnotationValue{
					"assert_required":     {Type: model.AnnotationTypeBool, BoolValue: true},
					"assert_enum_defined": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			},
			{
				Name:       "image_urls",
				Type:       "string",
				IsRepeated: true,
				Annotations: map[string]model.AnnotationValue{
					"assert_url": {Type: model.AnnotationTypeEnum, EnumValue: "URL_SCHEME_HTTPS"},
				},
			},
			{
				Name:       "listen_addrs",
				Type:       "string",
				IsRepeated: true,
				Annotations: map[string]model.AnnotationValue{
					"assert_listen_addr": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			},
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if (Number(m.category) === 0)",
		"for (let i = 0; i < m.imageUrls.length; i++)",
		"const val = m.imageUrls[i];",
		"parsed = new URL(val);",
		"parsed.protocol !== 'https:'",
		"field imageUrls[${i}] must be a valid URL",
		"for (let i = 0; i < m.listenAddrs.length; i++)",
		"const val = m.listenAddrs[i];",
		"listenAddrRegex.test(val)",
		"field listenAddrs[${i}] must be in host:port format",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript validation missing %q:\n%s", want, rendered)
		}
	}
	for _, banned := range []string{
		"m.category === 0",
		"new URL(m.imageUrls)",
		"listenAddrRegex.test(m.listenAddrs)",
	} {
		if strings.Contains(rendered, banned) {
			t.Fatalf("rendered TypeScript validation contains compile-invalid fragment %q:\n%s", banned, rendered)
		}
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
