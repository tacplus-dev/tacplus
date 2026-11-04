// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"math"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func sensAnn(required bool, bits int64) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"sensitive": {Type: model.AnnotationTypeBool, BoolValue: required, IntValue: bits}}
}

func notSensAnn() map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"not_sensitive": {Type: model.AnnotationTypeBool, BoolValue: true}}
}

func sensMsgAnn() map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"sensitive_message": {Type: model.AnnotationTypeBool, IntValue: -1}}
}

// uniConfigFiles is the shape of a uni-config: a Secrets message secret by
// construction (its version reference kept visible), nested and repeated
// messages, maps, and plain fields.
func uniConfigFiles() []model.ProtoFile {
	return []model.ProtoFile{{Package: "uni", Messages: []model.Message{
		{Name: "UniConfig", Fields: []model.Field{
			{Name: "name", Type: "string", Number: 1},
			{Name: "secrets", Type: "Secrets", Number: 2, IsMessage: true},
			{Name: "endpoints", Type: "Endpoint", Number: 3, IsMessage: true, IsRepeated: true},
			{Name: "labels", Number: 4, IsMap: true, MapKeyType: "string", MapValueType: "string"},
			{Name: "port", Type: "int32", Number: 5},
		}},
		{Name: "Secrets", Annotations: sensMsgAnn(), Fields: []model.Field{
			{Name: "api_token", Type: "string", Number: 1},
			{Name: "signing_key", Type: "bytes", Number: 2},
			{Name: "version", Type: "string", Number: 3, Annotations: notSensAnn()},
			{Name: "pki", Type: "Pki", Number: 4, IsMessage: true},
			// The injected trace_id is never sensitive: a redacted copy keeps its trace.
			{Name: "trace_id", Type: "string", Number: 200},
		}},
		{Name: "Pki", Fields: []model.Field{
			{Name: "private_key_pem", Type: "string", Number: 1},
		}},
		{Name: "Endpoint", Fields: []model.Field{
			{Name: "url", Type: "string", Number: 1},
			{Name: "password", Type: "string", Number: 2, Annotations: sensAnn(true, -1)},
		}},
	}}}
}

func TestSensitive_UniConfigValidatesAndPlans(t *testing.T) {
	files := uniConfigFiles()
	if err := ValidateSensitiveAnnotations(files); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// Pki.private_key_pem is reached only through the sensitive Secrets, so the
	// gate counts it as covered.
	if err := ValidateSensitiveNames(files); err != nil {
		t.Fatalf("gate: %v", err)
	}
	ix := NewSensitiveIndex(files, NewMessageResolver(files))
	for _, want := range []struct {
		name      string
		redact    bool
		redactAll bool
	}{{"UniConfig", true, false}, {"Secrets", true, false}, {"Pki", false, true}, {"Endpoint", true, false}} {
		if got := ix.NeedsRedact("uni", want.name); got != want.redact {
			t.Errorf("NeedsRedact(%s) = %v, want %v", want.name, got, want.redact)
		}
		if got := ix.NeedsRedactAll("uni", want.name); got != want.redactAll {
			t.Errorf("NeedsRedactAll(%s) = %v, want %v", want.name, got, want.redactAll)
		}
	}
	uc := ix.Plan(&files[0].Messages[0], "uni", false)
	got := map[string]RedactAction{}
	for _, f := range uc.Fields {
		got[f.Field.Name] = f.Action
		if f.Indicator {
			t.Errorf("UniConfig.%s: no indicator expected on an unannotated field", f.Field.Name)
		}
	}
	if len(got) != 2 || got["secrets"] != RedactInto || got["endpoints"] != RedactInto {
		t.Errorf("UniConfig plan = %v, want secrets and endpoints RedactInto", got)
	}
	sec := ix.Plan(&files[0].Messages[1], "uni", false)
	if !sec.All {
		t.Error("a sensitive_message's Redact<T> is its all-leaves plan")
	}
	acts := map[string]RedactField{}
	for _, f := range sec.Fields {
		acts[f.Field.Name] = f
	}
	if _, ok := acts["trace_id"]; ok {
		t.Error("the injected trace_id must not be redacted")
	}
	if _, ok := acts["version"]; ok {
		t.Error("Secrets.version is not_sensitive and must stay visible")
	}
	if a := acts["api_token"]; a.Action != RedactLeafValue || !a.Indicator || a.Kind != LeafString {
		t.Errorf("Secrets.api_token = %+v", a)
	}
	if a := acts["signing_key"]; a.Action != RedactLeafValue || !a.Indicator || a.Kind != LeafBytes {
		t.Errorf("Secrets.signing_key = %+v", a)
	}
	if a := acts["pki"]; a.Action != RedactAllLeaves || !a.Indicator {
		t.Errorf("Secrets.pki = %+v", a)
	}
}

// The acceptance case: a planted, unannotated api_token on a type with a
// generated Redact<T> fails generation and is named.
func TestSensitiveNames_GateCatchesPlantedApiToken(t *testing.T) {
	files := uniConfigFiles()
	files[0].Messages[0].Fields = append(files[0].Messages[0].Fields, model.Field{Name: "api_token", Type: "string", Number: 6})
	err := ValidateSensitiveNames(files)
	if err == nil || !strings.Contains(err.Error(), "uni.UniConfig.api_token") {
		t.Fatalf("want the gate to name uni.UniConfig.api_token, got %v", err)
	}
	// (spec.not_sensitive) is the explicit answer.
	files[0].Messages[0].Fields[5].Annotations = notSensAnn()
	if err := ValidateSensitiveNames(files); err != nil {
		t.Fatalf("not_sensitive must satisfy the gate: %v", err)
	}
}

// Pki reached through an unannotated path is checked in its own right.
func TestSensitiveNames_SharedTypeOnPlainPathIsGated(t *testing.T) {
	files := uniConfigFiles()
	files[0].Messages[0].Fields = append(files[0].Messages[0].Fields, model.Field{Name: "spare_pki", Type: "Pki", Number: 7, IsMessage: true})
	if err := ValidateSensitiveNames(files); err == nil || !strings.Contains(err.Error(), "uni.Pki.private_key_pem") {
		t.Fatalf("want uni.Pki.private_key_pem named, got %v", err)
	}
}

// A proto with no sensitive annotation anywhere is not gated, so no existing
// consumer build breaks in a minor.
func TestSensitiveNames_UnannotatedProtoNotGated(t *testing.T) {
	files := []model.ProtoFile{{Package: "p", Messages: []model.Message{{Name: "Login", Fields: []model.Field{
		{Name: "password", Type: "string", Number: 1},
	}}}}}
	if err := ValidateSensitiveNames(files); err != nil {
		t.Fatalf("unexpected gate failure: %v", err)
	}
}

func TestSensitiveNames_PrivateKeyEncTypeLooksSensitive(t *testing.T) {
	files := []model.ProtoFile{{Package: "p", Messages: []model.Message{{Name: "Cfg", Fields: []model.Field{
		{Name: "pem", Type: "bytes", Number: 1, Annotations: map[string]model.AnnotationValue{"assert_enc_type": {Type: model.AnnotationTypeEnum, EnumValue: "ENC_TYPE_KEY_PRIVATE_EC"}}},
		{Name: "other", Type: "string", Number: 2, Annotations: sensAnn(false, -1)},
	}}}}}
	if err := ValidateSensitiveNames(files); err == nil || !strings.Contains(err.Error(), "p.Cfg.pem") {
		t.Fatalf("want p.Cfg.pem named, got %v", err)
	}
}

func TestLooksSensitiveName(t *testing.T) {
	for _, n := range []string{"api_token", "password", "client_secret", "secrets", "private_key", "apikey", "credentials", "tls_key", "passwd"} {
		if !LooksSensitiveName(n) {
			t.Errorf("%s should look sensitive", n)
		}
	}
	for _, n := range []string{"name", "url", "port", "monitor", "version"} {
		if LooksSensitiveName(n) {
			t.Errorf("%s should not look sensitive", n)
		}
	}
}

func TestValidateSensitiveAnnotations_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		field model.Field
		want  string
	}{
		{"int", model.Field{Name: "n", Type: "int32", Number: 1, Annotations: sensAnn(false, -1)}, "allowed only on string, bytes and message"},
		{"bool", model.Field{Name: "b", Type: "bool", Number: 1, Annotations: sensAnn(false, -1)}, "allowed only on string, bytes and message"},
		{"enum", model.Field{Name: "e", Type: "Kind", IsEnum: true, Number: 1, Annotations: sensAnn(false, -1)}, "allowed only on string, bytes and message"},
		{"map int", model.Field{Name: "m", IsMap: true, MapKeyType: "string", MapValueType: "int64", Number: 1, Annotations: sensAnn(false, -1)}, "allowed only on string, bytes and message"},
		{"oneof", model.Field{Name: "o", Type: "string", IsOneOf: true, OneOfName: "x", Number: 1, Annotations: sensAnn(false, -1)}, "oneof member"},
		{"required repeated", model.Field{Name: "r", Type: "string", IsRepeated: true, Number: 1, Annotations: sensAnn(true, -1)}, "singular string or bytes"},
		{"entropy map", model.Field{Name: "m", IsMap: true, MapKeyType: "string", MapValueType: "string", Number: 1, Annotations: sensAnn(false, 64)}, "singular string or bytes"},
		{"both", model.Field{Name: "s", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{"sensitive": {IntValue: -1}, "not_sensitive": {BoolValue: true}}}, "both (spec.sensitive) and (spec.not_sensitive)"},
		{"legacy", model.Field{Name: "s", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{"sensitive": {IntValue: -1}, "redact": {StringValue: "drop"}}}, "legacy (spec.redact)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := []model.ProtoFile{{Package: "p", Messages: []model.Message{{Name: "M", Fields: []model.Field{c.field}}}}}
			err := ValidateSensitiveAnnotations(files)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestValidateSensitiveAnnotations_TreeRefusals(t *testing.T) {
	// A sensitive tree crossing a package is refused, not left unredacted.
	cross := []model.ProtoFile{
		{Package: "a", Messages: []model.Message{{Name: "Root", Fields: []model.Field{
			{Name: "creds", Type: "Creds", TypePackage: "b", Number: 1, IsMessage: true, Annotations: sensAnn(false, -1)},
		}}}},
		{Package: "b", Messages: []model.Message{{Name: "Creds", Fields: []model.Field{{Name: "v", Type: "string", Number: 1}}}}},
	}
	if err := ValidateSensitiveAnnotations(cross); err == nil || !strings.Contains(err.Error(), "does not cross packages") {
		t.Fatalf("cross-package: got %v", err)
	}
	// A well-known type inside a sensitive message cannot be redacted.
	wkt := []model.ProtoFile{{Package: "a", Messages: []model.Message{{Name: "S", Annotations: sensMsgAnn(), Fields: []model.Field{
		{Name: "extra", Type: "Struct", TypePackage: "google.protobuf", Number: 1, IsMessage: true},
	}}}}}
	if err := ValidateSensitiveAnnotations(wkt); err == nil || !strings.Contains(err.Error(), "cannot redact type") {
		t.Fatalf("well-known type: got %v", err)
	}
	// A string in a oneof of a sensitive message would be skipped: refused.
	oneof := []model.ProtoFile{{Package: "a", Messages: []model.Message{{Name: "S", Annotations: sensMsgAnn(), Fields: []model.Field{
		{Name: "v", Type: "string", Number: 1, IsOneOf: true, OneOfName: "k"},
	}}}}}
	if err := ValidateSensitiveAnnotations(oneof); err == nil || !strings.Contains(err.Error(), "oneof") {
		t.Fatalf("oneof leaf: got %v", err)
	}
}

func TestFieldSensitiveCheck(t *testing.T) {
	msg := sensMsgAnn()
	for _, c := range []struct {
		name string
		msg  map[string]model.AnnotationValue
		f    model.Field
		ok   bool
		want SensitiveCheck
	}{
		{"required default floor", nil, model.Field{Type: "string", Annotations: sensAnn(true, -1)}, true, SensitiveCheck{Required: true, CheckValue: true, MinEntropyBits: 128}},
		{"explicit floor", nil, model.Field{Type: "bytes", Annotations: sensAnn(false, 64)}, true, SensitiveCheck{CheckValue: true, MinEntropyBits: 64}},
		{"redact only", nil, model.Field{Type: "string", Annotations: sensAnn(false, -1)}, false, SensitiveCheck{}},
		{"inherits message", map[string]model.AnnotationValue{"sensitive_message": {BoolValue: true, IntValue: -1}}, model.Field{Type: "string"}, true, SensitiveCheck{Required: true, CheckValue: true, MinEntropyBits: 128}},
		{"message without checks", msg, model.Field{Type: "string"}, false, SensitiveCheck{}},
	} {
		got, ok := FieldSensitiveCheck(c.msg, c.f)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestEntropyMicroBitsTable(t *testing.T) {
	tab := EntropyMicroBits()
	if len(tab) != len(EntropyPools()) {
		t.Fatal("pools and table disagree")
	}
	// lower only: log2(26) = 4.700439...; all four classes: log2(95).
	if tab[26] != 4700439 || tab[95] != int64(math.Floor(math.Log2(95)*1e6)) {
		t.Fatalf("table = %v", tab)
	}
	// A 10-character lowercase value is ~47 bits: below the default 128.
	if 10*tab[26] >= DefaultMinEntropyBits*1_000_000 {
		t.Fatal("10 lowercase characters must be below the default floor")
	}
}

func TestLegacyRedactWarning(t *testing.T) {
	files := []model.ProtoFile{{Package: "p", Messages: []model.Message{{Name: "R", Fields: []model.Field{
		{Name: "ssn", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{"redact": {StringValue: "drop"}}},
	}}}}}
	w := LegacyRedactWarning(files)
	if !strings.HasPrefix(w, "warning: deprecated: p.R.ssn (spec.redact)") || !strings.Contains(w, "removed in 3.0") {
		t.Fatalf("warning = %q", w)
	}
	if LegacyRedactWarning(uniConfigFiles()) != "" {
		t.Fatal("no legacy annotation, no warning")
	}
}
