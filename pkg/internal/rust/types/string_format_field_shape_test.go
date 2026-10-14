// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Same gap as the go emitter's (see
// pkg/internal/go/types/string_format_field_shape_test.go). Rust had the string
// guard on assert_url, assert_file_path and assert_enc_type only; the other
// eight emitted `if !self.age.is_empty() { validate_email(&self.age)?; }`
// against an i32 and `for val in &self.labels` against a HashMap, neither of
// which compiles.
func rustStringFormat(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: rustStringFormatValue(annotation)},
	}
}

func rustStringFormatValue(annotation string) model.AnnotationValue {
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

var rustStringFormatAnnotations = []string{
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
func rustStringFormatBadShapes(annotation string) []struct {
	name  string
	field model.Field
} {
	repeated := rustStringFormat("ports", "int32", annotation)
	repeated.IsRepeated = true

	mapped := rustStringFormat("labels", "map", annotation)
	mapped.IsMap = true
	mapped.MapKeyType = "string"
	mapped.MapValueType = "string"

	enum := rustStringFormat("mode", "TlsMode", annotation)
	enum.IsEnum = true

	return []struct {
		name  string
		field model.Field
	}{
		{name: "bytes", field: rustStringFormat("blob", "bytes", annotation)},
		{name: "numeric", field: rustStringFormat("age", "int32", annotation)},
		{name: "repeated-numeric", field: repeated},
		{name: "map", field: mapped},
		{name: "enum", field: enum},
	}
}

func TestRustStringFormatRejectsNonStringField(t *testing.T) {
	for _, annotation := range rustStringFormatAnnotations {
		for _, tc := range rustStringFormatBadShapes(annotation) {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateMessageValidation(&b, msg, nil)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Rust does not compile:\n%s", annotation, tc.name, b.String())
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
func TestRustStringFormatKeepsSupportedShapes(t *testing.T) {
	for _, annotation := range rustStringFormatAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: rustStringFormat("endpoint", "string", annotation)},
			{
				name: "repeated-string",
				field: func() model.Field {
					f := rustStringFormat("endpoints", "string", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				if err := generateMessageValidation(&b, msg, nil); err != nil {
					t.Fatalf("generateMessageValidation: %v", err)
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
			rustStringFormat("email", "string", "assert_email"),
			rustStringFormat("hostname", "string", "assert_host"),
			func() model.Field {
				f := rustStringFormat("aliases", "string", "assert_fqdn")
				f.IsRepeated = true
				return f
			}(),
		},
	}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"validate_email(&self.email)?;",
		"validate_host(&self.hostname)?;",
		"for val in &self.aliases {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated Rust validation:\n%s", want, rendered)
		}
	}
}
