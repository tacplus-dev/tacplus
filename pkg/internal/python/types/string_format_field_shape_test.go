// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Same gap as the go emitter's (see
// pkg/internal/go/types/string_format_field_shape_test.go). Python had the
// string guard on assert_url, assert_file_path and assert_enc_type only. There
// is no compile step here, so the other eight were the worst-placed of the five
// languages: `_validate_email(msg.age, 'age')` runs a string regex against an
// int and raises TypeError inside the validator at request time, so the call
// crashes rather than returning INVALID_ARGUMENT.
func pyStringFormat(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: pyStringFormatValue(annotation)},
	}
}

func pyStringFormatValue(annotation string) model.AnnotationValue {
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

var pyStringFormatAnnotations = []string{
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
func pyStringFormatBadShapes(annotation string) []struct {
	name  string
	field model.Field
} {
	repeated := pyStringFormat("ports", "int32", annotation)
	repeated.IsRepeated = true

	mapped := pyStringFormat("labels", "map", annotation)
	mapped.IsMap = true
	mapped.MapKeyType = "string"
	mapped.MapValueType = "string"

	enum := pyStringFormat("mode", "TlsMode", annotation)
	enum.IsEnum = true

	return []struct {
		name  string
		field model.Field
	}{
		{name: "bytes", field: pyStringFormat("blob", "bytes", annotation)},
		{name: "numeric", field: pyStringFormat("age", "int32", annotation)},
		{name: "repeated-numeric", field: repeated},
		{name: "map", field: mapped},
		{name: "enum", field: enum},
	}
}

func TestPythonStringFormatRejectsNonStringField(t *testing.T) {
	for _, annotation := range pyStringFormatAnnotations {
		for _, tc := range pyStringFormatBadShapes(annotation) {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				blocks, err := buildValidationBlocks(msg, nil)
				if err == nil {
					var rendered strings.Builder
					for _, block := range blocks {
						rendered.WriteString(block.Code)
					}
					t.Fatalf("%s on a %s field accepted; generated python raises TypeError at request time:\n%s", annotation, tc.name, rendered.String())
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
func TestPythonStringFormatKeepsSupportedShapes(t *testing.T) {
	render := func(t *testing.T, msg model.Message) string {
		t.Helper()
		blocks, err := buildValidationBlocks(msg, nil)
		if err != nil {
			t.Fatalf("buildValidationBlocks: %v", err)
		}
		var rendered strings.Builder
		for _, block := range blocks {
			rendered.WriteString(block.Code)
		}
		return rendered.String()
	}

	for _, annotation := range pyStringFormatAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: pyStringFormat("endpoint", "string", annotation)},
			{
				name: "repeated-string",
				field: func() model.Field {
					f := pyStringFormat("endpoints", "string", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				rendered := render(t, model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}})
				if !strings.Contains(rendered, tc.field.Name) {
					t.Errorf("%s on a %s field emitted no validation:\n%s", annotation, tc.name, rendered)
				}
			})
		}
	}

	rendered := render(t, model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			pyStringFormat("email", "string", "assert_email"),
			pyStringFormat("hostname", "string", "assert_host"),
			func() model.Field {
				f := pyStringFormat("aliases", "string", "assert_fqdn")
				f.IsRepeated = true
				return f
			}(),
		},
	})

	for _, want := range []string{
		"_validate_email(msg.email, 'email')",
		"_validate_host(msg.hostname, 'hostname')",
		"for val in msg.aliases:",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated python validation:\n%s", want, rendered)
		}
	}
}
