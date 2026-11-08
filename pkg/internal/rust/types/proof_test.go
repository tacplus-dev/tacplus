// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func proofHarnessesOf(msgs ...model.Message) string {
	var b strings.Builder
	writeProofHarnesses(&b, model.ProtoFile{Package: "demo", Messages: msgs})
	return b.String()
}

func boolAnnotation() model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
}

// tacplus#211: the harnesses did not compile (kani::any() into a String or a
// Vec, .len() on an Option<String>, an integer literal against an f64) and
// asserted validate().is_ok(), which validate() does not guarantee (an enum
// left at 0 by a required check). A harness now only refuses an integer or enum
// bound, and the fields Kani cannot make arbitrary get none.
func TestRustProofHarnessesOnlyRefuseIntegerBounds(t *testing.T) {
	optionalID := model.Field{Name: "client_id", Type: "string", IsOptional: true,
		Annotations: map[string]model.AnnotationValue{"assert_min_size": {Type: model.AnnotationTypeInt, IntValue: 1}}}
	ids := model.Field{Name: "ids", Type: "string", IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 64}}}
	multiplier := rustBound("backoff_multiplier", "double", "assert_gt", 0)
	mode := model.Field{Name: "mode", Type: "SslMode", IsEnum: true,
		Annotations: map[string]model.AnnotationValue{"assert_required": boolAnnotation()}}
	retries := rustBound("retries", "int32", "assert_le", 10)
	msg := model.Message{Name: "Config", Fields: []model.Field{
		rustBound("id", "string", "assert_max_size", 256), optionalID, ids, multiplier, mode, retries,
	}}

	got := proofHarnessesOf(msg)
	for _, bad := range []string{
		"msg.id = kani::any()", "client_id", "msg.ids", "backoff_multiplier", "msg.mode", "is_ok()",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("harness must not contain %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{
		"#[kani::proof]\n#[kani::stub(std::fmt::format, format_stub)]\nfn verify_config_retries_assert_le() {\n",
		"fn format_stub(_args: std::fmt::Arguments<'_>) -> String {\n",
		"let mut msg = Config::default();\n",
		"msg.retries = kani::any();\n",
		"kani::assume(msg.retries > 10);\n",
		"assert!(msg.validate().is_err());\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "#[kani::proof]"); n != 1 {
		t.Errorf("want 1 harness, got %d:\n%s", n, got)
	}
}

// Each harness's refused values are exactly the ones validate() fails on: the
// assumption, applied to the field, is the validator's own failing comparison.
func TestRustProofRefusedBoundIsTheValidatorsCheck(t *testing.T) {
	duration := func(name, enum string) model.Field {
		return model.Field{Name: name, Type: "int64", Annotations: map[string]model.AnnotationValue{
			"assert_duration": {Type: model.AnnotationTypeEnum, EnumValue: enum}}}
	}
	optional := rustBound("maybe", "uint32", "assert_lt", 7)
	optional.IsOptional = true
	enum := rustBound("level", "Level", "assert_ge", 2)
	enum.IsEnum = true
	fields := []model.Field{
		rustBound("a", "int32", "assert_gt", 3),
		rustBound("b", "int64", "assert_ge", -4),
		rustBound("c", "uint64", "assert_lt", 5),
		rustBound("d", "sint32", "assert_le", 6),
		{Name: "port", Type: "int32", Annotations: map[string]model.AnnotationValue{"assert_net_port": boolAnnotation()}},
		duration("ttl", "DURATION_TYPE_POSITIVE"),
		duration("grace", "DURATION_TYPE_NON_NEGATIVE"),
		rustBound("min_s", "int64", "assert_duration_min", 10),
		rustBound("max_s", "int64", "assert_duration_max", 20),
		optional,
		enum,
	}
	msg := model.Message{Name: "Bounds", Fields: fields}

	var vb strings.Builder
	if err := generateMessageValidation(&vb, msg, nil); err != nil {
		t.Fatal(err)
	}
	validator := vb.String()

	pm := buildProofMessage(msg)
	if len(pm.Harnesses) != len(fields) {
		t.Fatalf("want %d harnesses, got %d: %+v", len(fields), len(pm.Harnesses), pm.Harnesses)
	}
	for _, h := range pm.Harnesses {
		value := "self." + h.Field
		if h.Optional {
			value = "val"
		}
		check := "if " + fmt.Sprintf(h.Refused, value) + " {"
		if !strings.Contains(validator, check) {
			t.Errorf("%s: validator has no %q:\n%s", h.Name, check, validator)
		}
	}

	got := proofHarnessesOf(msg)
	for _, want := range []string{
		"let v = kani::any();\nmsg.maybe = Some(v);\nkani::assume(v >= 7);\n",
		"kani::assume(msg.port < 0 || msg.port > 65535);\n",
		"kani::assume(msg.level < 2);\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Kani compiles everything a harness reaches, run or not, and panics compiling
// the regex crate; a nested message's validator can reach it from another
// package; a map field's default makes a syscall Kani does not model. Such a
// message gets no harness.
func TestRustProofHarnessesSkipUnreachableValidators(t *testing.T) {
	bound := rustBound("retries", "int32", "assert_le", 10)
	for _, tc := range []struct {
		name  string
		field model.Field
	}{
		{"email", model.Field{Name: "email", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_email": boolAnnotation()}}},
		{"fqdn", model.Field{Name: "server", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_fqdn": boolAnnotation()}}},
		{"host", model.Field{Name: "host", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_host": boolAnnotation()}}},
		{"semver", model.Field{Name: "version", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_semver": boolAnnotation()}}},
		{"nested", model.Field{Name: "tls", Type: "TlsConfig", TypePackage: "tls", IsMessage: true}},
		{"map", model.Field{Name: "labels", Type: "string", IsMap: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := model.Message{Name: "Conn", Fields: []model.Field{bound, tc.field}}
			if got := proofHarnessesOf(msg); got != "" {
				t.Errorf("want no harness:\n%s", got)
			}
		})
	}

	// A well-known type has no validator to reach.
	ts := model.Field{Name: "at", Type: "Timestamp", TypePackage: "google.protobuf", IsMessage: true}
	if got := proofHarnessesOf(model.Message{Name: "Conn", Fields: []model.Field{bound, ts}}); !strings.Contains(got, "verify_conn_retries_assert_le") {
		t.Errorf("want the harness beside a well-known type:\n%s", got)
	}
}

// An assert_invariant names the message's fields through self, as validate()'s
// debug_assert! does; the harness assumes it of msg, so a value it rules out
// does not panic validate() before the bound's check refuses it.
func TestRustProofHarnessAssumesInvariants(t *testing.T) {
	ttl := rustBound("ttl", "int64", "assert_le", 86400)
	ttl.Annotations["assert_invariant"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: "self.ttl >= 0 || self.myself.is_empty()"}
	got := proofHarnessesOf(model.Message{Name: "Grant", Fields: []model.Field{ttl}})
	if want := "kani::assume(msg.ttl >= 0 || msg.myself.is_empty());\n"; !strings.Contains(got, want) {
		t.Errorf("missing %q in:\n%s", want, got)
	}
}
