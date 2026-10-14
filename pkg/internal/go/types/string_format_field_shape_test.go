// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// The string-format assert family validates that a field holds a well-formed
// string of some kind, so every one of them is scoped to `string` fields. Batch
// 31 inventoried the family and found the shape guard present in a different
// subset of emitters per annotation — ragged, not uniformly missing. In the go
// emitter only assert_host had it, so the other ten wrote their string helper
// call against whatever field they were handed: `utils.ValidateAssertEmail(m.Age)`
// against an int32, `for _, v := range m.Labels` against a map. None of that
// compiles. This pins the guard for all eleven.
func goStringFormat(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: goStringFormatValue(annotation)},
	}
}

func goStringFormatValue(annotation string) model.AnnotationValue {
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

var goStringFormatAnnotations = []string{
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

// goStringFormatBadShapes are the field shapes the family cannot be applied to.
// A map field carries Type "map" out of the parser (descriptor.go sets it when
// it flattens the map entry message), which is why the map case is spelled that
// way rather than as a string field with IsMap set.
func goStringFormatBadShapes(annotation string) []struct {
	name  string
	field model.Field
} {
	repeated := goStringFormat("ports", "int32", annotation)
	repeated.IsRepeated = true

	mapped := goStringFormat("labels", "map", annotation)
	mapped.IsMap = true
	mapped.MapKeyType = "string"
	mapped.MapValueType = "string"

	enum := goStringFormat("mode", "TlsMode", annotation)
	enum.IsEnum = true

	return []struct {
		name  string
		field model.Field
	}{
		{name: "bytes", field: goStringFormat("blob", "bytes", annotation)},
		{name: "numeric", field: goStringFormat("age", "int32", annotation)},
		{name: "repeated-numeric", field: repeated},
		{name: "map", field: mapped},
		{name: "enum", field: enum},
	}
}

func TestGoStringFormatRejectsNonStringField(t *testing.T) {
	for _, annotation := range goStringFormatAnnotations {
		for _, tc := range goStringFormatBadShapes(annotation) {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateValidationHelpers(&b, msg)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Go does not compile:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + toGoFieldName(tc.field.Name) + " requires string type"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

// The guard is a guard, not a removal: both shapes the family is documented for
// — a scalar string and a repeated string — still render, and the two arms that
// already carried the guard before this batch (assert_host, and the utils call
// shape shared by the rest) still emit byte-for-byte what they did.
func TestGoStringFormatKeepsSupportedShapes(t *testing.T) {
	for _, annotation := range goStringFormatAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: goStringFormat("endpoint", "string", annotation)},
			{
				name: "repeated-string",
				field: func() model.Field {
					f := goStringFormat("endpoints", "string", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				if err := generateValidationHelpers(&b, msg); err != nil {
					t.Fatalf("generateValidationHelpers: %v", err)
				}
				if !strings.Contains(b.String(), toGoFieldName(tc.field.Name)) {
					t.Errorf("%s on a %s field emitted no validation:\n%s", annotation, tc.name, b.String())
				}
			})
		}
	}

	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			goStringFormat("email", "string", "assert_email"),
			goStringFormat("hostname", "string", "assert_host"),
			goStringFormat("client_ip", "string", "assert_ipaddr_type"),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"	// Validate Email (assert_email)\n	if m.Email != \"\" {\n		if err := utils.ValidateAssertEmail(m.Email); err != nil {\n",
		"	// Validate Hostname (assert_host: IP address or FQDN)\n",
		"	// Validate ClientIp (assert_ipaddr_type: IPv4 or IPv6)\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}
