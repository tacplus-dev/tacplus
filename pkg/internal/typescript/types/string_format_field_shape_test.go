// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Same gap as the go emitter's (see
// pkg/internal/go/types/string_format_field_shape_test.go). TypeScript had the
// string guard on assert_url, assert_enc_type and assert_listen_addr only, and
// is the emitter where a miss is worst: there is no compile step to catch
// `emailRegex.test(m.age)`, so the number is coerced to a string, the regex
// runs against "42" and the check either passes or fails for reasons the author
// never intended — a silent divergence from the go/java/rust siblings generated
// from the same proto.
func tsStringFormat(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: tsStringFormatValue(annotation)},
	}
}

func tsStringFormatValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_sql":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "SQL_PERMIT_SELECT"}
	case "assert_file_path":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "PATH_ABSOLUTE"}
	case "assert_ipaddr_type":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "IP_ADDR_TYPE_IP_ANY"}
	case "assert_enc_type":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "ENC_TYPE_X509_CERT_ANY"}
	case "assert_url":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "URL_SCHEME_HTTPS"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
}

var tsStringFormatAnnotations = []string{
	"assert_email",
	"assert_fqdn",
	"assert_base64",
	"assert_semver",
	"assert_sql",
	"assert_listen_addr",
	"assert_file_path",
	"assert_ipaddr_type",
	"assert_enc_type",
	"assert_url",
	"assert_host",
}

// A map field carries Type "map" out of the parser, which is why the map case is
// spelled that way rather than as a string field with IsMap set.
func tsStringFormatBadShapes(annotation string) []struct {
	name  string
	field model.Field
} {
	repeated := tsStringFormat("ports", "int32", annotation)
	repeated.IsRepeated = true

	mapped := tsStringFormat("labels", "map", annotation)
	mapped.IsMap = true
	mapped.MapKeyType = "string"
	mapped.MapValueType = "string"

	enum := tsStringFormat("mode", "TlsMode", annotation)
	enum.IsEnum = true

	return []struct {
		name  string
		field model.Field
	}{
		{name: "bytes", field: tsStringFormat("blob", "bytes", annotation)},
		{name: "numeric", field: tsStringFormat("age", "int32", annotation)},
		{name: "repeated-numeric", field: repeated},
		{name: "map", field: mapped},
		{name: "enum", field: enum},
	}
}

func TestTypeScriptStringFormatRejectsNonStringField(t *testing.T) {
	for _, annotation := range tsStringFormatAnnotations {
		for _, tc := range tsStringFormatBadShapes(annotation) {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateTypeScriptValidationFunction(&b, msg)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated TypeScript coerces the value and checks the wrong thing:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + tc.field.Name + " requires string type"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

// The guard is a guard, not a removal: both documented shapes still render.
func TestTypeScriptStringFormatKeepsSupportedShapes(t *testing.T) {
	for _, annotation := range tsStringFormatAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: tsStringFormat("endpoint", "string", annotation)},
			{
				name: "repeated-string",
				field: func() model.Field {
					f := tsStringFormat("endpoints", "string", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
					t.Fatalf("generateTypeScriptValidationFunction: %v", err)
				}
				if !strings.Contains(b.String(), tc.field.Name) {
					t.Errorf("%s on a %s field emitted no validation:\n%s", annotation, tc.name, b.String())
				}
			})
		}
	}

	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			tsStringFormat("email", "string", "assert_email"),
			tsStringFormat("hostname", "string", "assert_host"),
			func() model.Field {
				f := tsStringFormat("aliases", "string", "assert_fqdn")
				f.IsRepeated = true
				return f
			}(),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"	// Validate email (assert_email)\n",
		"	// Validate hostname (assert_host: IP address or FQDN)\n",
		"	// Validate aliases (assert_fqdn)\n",
		"for (let i = 0; i < m.aliases.length; i++) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated TypeScript validation:\n%s", want, rendered)
		}
	}
}
