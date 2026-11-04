// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func redactSensAnn(required bool, bits int64) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"sensitive": {Type: model.AnnotationTypeBool, BoolValue: required, IntValue: bits}}
}

// Redact<T> clones, replaces each set sensitive value with the marker, sets the
// indicator, recurses into nested / repeated / map messages, and leaves keys,
// plain fields and the injected trace_id alone.
func TestGenerateRedact_Shape(t *testing.T) {
	files := []model.ProtoFile{{Package: "uni", Messages: []model.Message{
		{Name: "UniConfig", Fields: []model.Field{
			{Name: "name", Type: "string", Number: 1},
			{Name: "secrets", Type: "Secrets", Number: 2, IsMessage: true},
			{Name: "peers", Number: 3, IsMap: true, MapKeyType: "string", MapValueType: "Endpoint", MapValueIsMessage: true},
			{Name: "tokens", Type: "string", Number: 4, IsRepeated: true, Annotations: redactSensAnn(false, -1)},
			{Name: "trace_id", Type: "string", Number: 200},
		}},
		{Name: "Secrets", Annotations: map[string]model.AnnotationValue{"sensitive_message": {IntValue: -1}}, Fields: []model.Field{
			{Name: "key", Type: "bytes", Number: 1},
			{Name: "code", Type: "string", Number: 2, IsOptional: true},
			{Name: "trace_id", Type: "string", Number: 200},
		}},
		{Name: "Endpoint", Fields: []model.Field{
			{Name: "password", Type: "string", Number: 1, Annotations: redactSensAnn(false, -1)},
		}},
	}}}
	ix := parser.NewSensitiveIndex(files, parser.NewMessageResolver(files))
	var b strings.Builder
	generateRedactHelpers(&b, files[0], ix)
	got := b.String()
	for _, want := range []string{
		"func RedactUniConfig(m *UniConfig) *UniConfig {",
		"out := proto.Clone(m).(*UniConfig)",
		"\tif m.Secrets != nil {\n\t\tredactSecrets(m.Secrets)\n\t}\n",
		"\tfor _, v := range m.Peers {\n\t\tredactEndpoint(v)\n\t}\n",
		"\t\t\tm.Tokens[i] = \"REDACTED\"\n\t\t\tm.TokensRedacted = true\n",
		"\t\tm.Key = []byte(\"REDACTED\")\n\t\tm.KeyRedacted = true\n",
		"\tif m.Code != nil && *m.Code != \"\" {\n\t\tv := \"REDACTED\"\n\t\tm.Code = &v\n\t\tm.CodeRedacted = true\n",
		"\t\tm.Password = \"REDACTED\"\n\t\tm.PasswordRedacted = true\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, absent := range []string{"m.Name =", "TraceId", "NameRedacted"} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %q in:\n%s", absent, got)
		}
	}
}

// The validator names the field and the rule, never the value, and the
// verdict helper carries the parser's integer entropy table.
func TestGenerateSensitiveChecks(t *testing.T) {
	msg := model.Message{Name: "Secrets", Fields: []model.Field{
		{Name: "api_token", Type: "string", Number: 1, Annotations: redactSensAnn(true, -1)},
		{Name: "key", Type: "bytes", Number: 2, Annotations: redactSensAnn(false, 256)},
		{Name: "note", Type: "string", Number: 3, Annotations: redactSensAnn(false, -1)},
	}}
	var b strings.Builder
	if !writeGoSensitiveChecks(&b, msg) {
		t.Fatal("expected checks")
	}
	got := b.String()
	for _, want := range []string{
		`if len(m.GetApiToken()) == 0 {`,
		`"field Secrets.api_token (sensitive): required and empty"`,
		`switch tacplusSensitiveVerdict(m.GetApiToken(), false, 128) {`,
		`"field Secrets.api_token (sensitive): estimated entropy below 128 bits"`,
		`switch tacplusSensitiveVerdict(string(m.GetKey()), true, 256) {`,
		`"field Secrets.key (sensitive): a known placeholder value"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GetNote") {
		t.Errorf("a redact-only sensitive field has no validation:\n%s", got)
	}
	var h strings.Builder
	generateSensitiveVerdictHelper(&h)
	if !strings.Contains(h.String(), "case 26:\n\t\t\tperChar = 4700439\n") || !strings.Contains(h.String(), `case "redacted", "changeme":`) {
		t.Errorf("verdict helper:\n%s", h.String())
	}
}
