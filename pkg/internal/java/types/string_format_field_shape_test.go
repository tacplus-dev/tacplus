// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// Same gap as the go emitter's (see
// pkg/internal/go/types/string_format_field_shape_test.go). Java had the string
// guard on assert_url, assert_file_path and assert_enc_type only; the other
// eight wrote `Validation.validateEmail(msg.getAge(), "age")` against an int,
// which does not compile, and on a repeated or map field named an accessor that
// protobuf-java does not generate (`get<F>()` rather than `get<F>List()` /
// `get<F>Map()`).
func javaStringFormat(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: javaStringFormatValue(annotation)},
	}
}

func javaStringFormatValue(annotation string) model.AnnotationValue {
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

var javaStringFormatAnnotations = []string{
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
func javaStringFormatBadShapes(annotation string) []struct {
	name  string
	field model.Field
} {
	repeated := javaStringFormat("ports", "int32", annotation)
	repeated.IsRepeated = true

	mapped := javaStringFormat("labels", "map", annotation)
	mapped.IsMap = true
	mapped.MapKeyType = "string"
	mapped.MapValueType = "string"

	enum := javaStringFormat("mode", "TlsMode", annotation)
	enum.IsEnum = true

	return []struct {
		name  string
		field model.Field
	}{
		{name: "bytes", field: javaStringFormat("blob", "bytes", annotation)},
		{name: "numeric", field: javaStringFormat("age", "int32", annotation)},
		{name: "repeated-numeric", field: repeated},
		{name: "map", field: mapped},
		{name: "enum", field: enum},
	}
}

func TestJavaStringFormatRejectsNonStringField(t *testing.T) {
	for _, annotation := range javaStringFormatAnnotations {
		for _, tc := range javaStringFormatBadShapes(annotation) {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := writeMessageValidation(&b, msg, "Outer", nil)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Java does not compile:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + tc.field.Name + " requires string type"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

// The guard is a guard, not a removal: both documented shapes still render, and
// the scalar and repeated accessor forms are unchanged.
func TestJavaStringFormatKeepsSupportedShapes(t *testing.T) {
	for _, annotation := range javaStringFormatAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: javaStringFormat("endpoint", "string", annotation)},
			{
				name: "repeated-string",
				field: func() model.Field {
					f := javaStringFormat("endpoints", "string", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
					t.Fatalf("writeMessageValidation: %v", err)
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
			javaStringFormat("email", "string", "assert_email"),
			javaStringFormat("hostname", "string", "assert_host"),
			func() model.Field {
				f := javaStringFormat("aliases", "string", "assert_fqdn")
				f.IsRepeated = true
				return f
			}(),
		},
	}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validation.validateEmail(msg.getEmail(), \"email\");",
		"Validation.validateHost(msg.getHostname(), \"hostname\");",
		"for (String val : msg.getAliasesList()) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated Java validation:\n%s", want, rendered)
		}
	}
}
