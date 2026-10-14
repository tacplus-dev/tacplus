// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

func entField(name, typ string, repeated bool, mode string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		IsRepeated:  repeated,
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: mode}},
	}
}

// Phase 1 (data entitlement): the two new eligible modes (may_redact,
// may_anonymize) are accepted, and anonymize is widened to string OR bytes.
func TestValidateDataEntitlementFieldModes(t *testing.T) {
	cases := []struct {
		name    string
		field   model.Field
		wantErr string // "" = accept; otherwise a substring the error must contain
	}{
		{"may_redact on string", entField("payload", "string", false, "may_redact"), ""},
		{"may_redact on int64", entField("count", "int64", false, "may_redact"), ""},
		{"drop on int64", entField("count", "int64", false, "drop"), ""},
		{"may_anonymize on string", entField("email", "string", false, "may_anonymize"), ""},
		{"may_anonymize on bytes", entField("blob", "bytes", false, "may_anonymize"), ""},
		{"pseudonymize on bytes (widened)", entField("blob", "bytes", false, "pseudonymize"), ""},
		{"may_anonymize on int64 rejected", entField("count", "int64", false, "may_anonymize"), "requires a string or bytes"},
		{"pseudonymize on int64 rejected", entField("count", "int64", false, "pseudonymize"), "requires a string or bytes"},
		{"unknown mode rejected", entField("payload", "string", false, "bogus"), "requires a mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDataEntitlementAnnotations(recordWith(tc.field))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected accept, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// The message-level (spec.data_entitlement) default must carry a real mode.
func TestValidateDataEntitlementMessageLevelDefault(t *testing.T) {
	mk := func(mode string) []model.ProtoFile {
		return []model.ProtoFile{{
			Package: "sensor",
			Messages: []model.Message{{
				Name: "Event",
				Annotations: map[string]model.AnnotationValue{
					"record":           {Type: model.AnnotationTypeBool, BoolValue: true},
					"data_entitlement": {Type: model.AnnotationTypeString, StringValue: mode},
				},
			}},
		}}
	}
	if err := ValidateDataEntitlementAnnotations(mk("may_redact")); err != nil {
		t.Fatalf("valid message-level default should pass, got: %v", err)
	}
	err := ValidateDataEntitlementAnnotations(mk(""))
	if err == nil || !strings.Contains(err.Error(), "message-level (spec.data_entitlement) requires a mode") {
		t.Fatalf("empty message-level default must fail loudly, got: %v", err)
	}
}

// redactModeString is the descriptor→model contract: it stamps exactly the four
// mode strings the validator recognizes (and "" for UNSPECIFIED, so a present-but-
// modeless annotation is rejected rather than silently dropped).
func TestRedactModeString(t *testing.T) {
	cases := map[spec.RedactMode]string{
		spec.RedactMode_REDACT_MODE_DROP:         "drop",
		spec.RedactMode_REDACT_MODE_PSEUDONYMIZE: "pseudonymize",
		spec.RedactMode_REDACT_MODE_MAY_REDACT:   "may_redact",
		spec.RedactMode_REDACT_MODE_MAY_ANONYMIZE: "may_anonymize",
		spec.RedactMode_REDACT_MODE_UNSPECIFIED:  "",
	}
	for m, want := range cases {
		got := redactModeString(m)
		if got != want {
			t.Errorf("redactModeString(%v) = %q, want %q", m, got, want)
		}
		// Every non-empty stamped string must be recognized by the validator.
		if want != "" && !isValidEntitlementMode(got) {
			t.Errorf("isValidEntitlementMode(%q) = false, want true", got)
		}
	}
}

// A field cannot be both (spec.redact) and (spec.query_field): the record map emits
// a typed <T>Filter over every query field, so filtering on a redacted value leaks
// it back by inference. Model-level seam; the proto-fixture proof that this fires on
// the annotations protoc actually stamps lives in
// pkg/internal/proto/redact_query_field_test.go.
func TestValidateDataEntitlementRejectsRedactedQueryField(t *testing.T) {
	field := entField("title", "string", false, "drop")
	field.Annotations["query_field"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: "eq"}
	err := ValidateDataEntitlementAnnotations(recordWith(field))
	if err == nil || !strings.Contains(err.Error(), "Event.title: cannot be both (spec.redact) and (spec.query_field)") {
		t.Fatalf("expected redact+query_field to fail loudly, got: %v", err)
	}
}
