// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// A proto3 `optional` scalar reads back as the type default through the
// generated Java getter when the field is absent, so a bound check emitted
// against the bare getter fires on a field the caller legitimately omitted.
// pkg/proto/sys/token.proto's TokenRequestParams.client_id
// (`optional string client_id = 1 [(spec.assert_min_size) = 1]`) is exactly that
// shape: java rejected every token request that omitted client_id, while go
// (nil pointer check) and rust (Option check) skip the bound on an absent field.
func javaOptionalField(name, typ, annotation string, value model.AnnotationValue) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		IsOptional:  true,
		Annotations: map[string]model.AnnotationValue{annotation: value},
	}
}

func javaIntAnnotation(v int64) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: v}
}

func TestJavaOptionalBoundIsPresenceGuarded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field model.Field
		// check is the inner bound test that must stay, now nested inside the
		// presence guard so an absent field skips it.
		check string
	}{
		{
			name:  "assert_min_size",
			field: javaOptionalField("client_id", "string", "assert_min_size", javaIntAnnotation(1)),
			check: "if (msg.getClientId().getBytes(java.nio.charset.StandardCharsets.UTF_8).length < 1) {",
		},
		{
			name:  "assert_max_size",
			field: javaOptionalField("client_id", "string", "assert_max_size", javaIntAnnotation(64)),
			check: "if (msg.getClientId().getBytes(java.nio.charset.StandardCharsets.UTF_8).length > 64) {",
		},
		{
			name:  "assert_min_size_bytes",
			field: javaOptionalField("client_id", "bytes", "assert_min_size", javaIntAnnotation(1)),
			check: "if (msg.getClientId().size() < 1) {",
		},
		{
			name:  "assert_net_port",
			field: javaOptionalField("client_id", "int32", "assert_net_port", model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}),
			check: "if (msg.getClientId() < 0 || msg.getClientId() > 65535) {",
		},
		{
			name:  "assert_duration_positive",
			field: javaOptionalField("client_id", "int64", "assert_duration", model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}),
			check: "if (msg.getClientId() <= 0) {",
		},
		{
			name:  "assert_duration_non_negative",
			field: javaOptionalField("client_id", "int64", "assert_duration", model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_NON_NEGATIVE"}),
			check: "if (msg.getClientId() < 0) {",
		},
		{
			name:  "assert_duration_min",
			field: javaOptionalField("client_id", "int64", "assert_duration_min", javaIntAnnotation(5)),
			check: "if (msg.getClientId() < 5L) {",
		},
		{
			name:  "assert_duration_max",
			field: javaOptionalField("client_id", "int64", "assert_duration_max", javaIntAnnotation(5)),
			check: "if (msg.getClientId() > 5L) {",
		},
		{
			name:  "assert_gt",
			field: javaOptionalField("client_id", "int32", "assert_gt", javaIntAnnotation(0)),
			check: "if (msg.getClientId() <= 0) {",
		},
		{
			name:  "assert_ge",
			field: javaOptionalField("client_id", "int32", "assert_ge", javaIntAnnotation(1)),
			check: "if (msg.getClientId() < 1) {",
		},
		{
			name:  "assert_lt",
			field: javaOptionalField("client_id", "int32", "assert_lt", javaIntAnnotation(10)),
			check: "if (msg.getClientId() >= 10) {",
		},
		{
			name:  "assert_le",
			field: javaOptionalField("client_id", "int32", "assert_le", javaIntAnnotation(10)),
			check: "if (msg.getClientId() > 10) {",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := model.Message{Name: "TokenRequestParams", Fields: []model.Field{tc.field}}

			var b strings.Builder
			if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
				t.Fatalf("writeMessageValidation: %v", err)
			}
			got := b.String()

			if !strings.Contains(got, tc.check) {
				t.Fatalf("bound check %q missing; a set field must still be validated:\n%s", tc.check, got)
			}
			// The presence guard has to wrap the bound check, not merely appear
			// somewhere in the method.
			guarded := "if (msg.hasClientId()) {\n" + tc.check
			if !strings.Contains(got, guarded) {
				t.Errorf("bound check is not wrapped in msg.hasClientId(); an absent optional field fails validation:\n%s", got)
			}
			if unbalanced := strings.Count(got, "{") - strings.Count(got, "}"); unbalanced != 0 {
				t.Errorf("emitted Java braces unbalanced by %d:\n%s", unbalanced, got)
			}
		})
	}
}

// The guard is scoped to proto3 optional: a plain (implicit-presence) field has
// no has<Field>() accessor, so its bound check must render exactly as before.
func TestJavaNonOptionalBoundIsNotPresenceGuarded(t *testing.T) {
	msg := model.Message{Name: "TokenRequestParams", Fields: []model.Field{
		{
			Name: "client_id",
			Type: "string",
			Annotations: map[string]model.AnnotationValue{
				"assert_min_size": javaIntAnnotation(1),
			},
		},
	}}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	got := b.String()

	if strings.Contains(got, "hasClientId()") {
		t.Errorf("non-optional field guarded by hasClientId(); the accessor does not exist:\n%s", got)
	}
	if !strings.Contains(got, "if (msg.getClientId().getBytes(java.nio.charset.StandardCharsets.UTF_8).length < 1) {") {
		t.Errorf("min size check missing for non-optional field:\n%s", got)
	}
}
