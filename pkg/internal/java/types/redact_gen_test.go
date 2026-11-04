// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func javaSensitiveAnn(required bool, minBits int64) map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"sensitive": {Type: model.AnnotationTypeBool, BoolValue: required, IntValue: minBits}}
}

// redactFixture is a cut-down cross-test/redact model: a sensitive_message, a
// plain type reached through a sensitive field, repeated/map/optional leaves.
func redactFixture() model.ProtoFile {
	notSensitive := map[string]model.AnnotationValue{"not_sensitive": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return model.ProtoFile{Package: "demo", Messages: []model.Message{
		{Name: "Config", OuterClass: "Demo", Fields: []model.Field{
			{Name: "name", Type: "string", Number: 1},
			{Name: "secrets", Type: "Secrets", Number: 2, IsMessage: true},
			{Name: "endpoints", Type: "Endpoint", Number: 3, IsMessage: true, IsRepeated: true},
			{Name: "peers", IsMap: true, MapKeyType: "string", MapValueType: "Endpoint", MapValueIsMessage: true, Number: 4},
			{Name: "database", Type: "Database", Number: 5, IsMessage: true, Annotations: javaSensitiveAnn(false, -1)},
			{Name: "api_tokens", Type: "string", Number: 6, IsRepeated: true, Annotations: javaSensitiveAnn(false, -1)},
			{Name: "signing_keys", IsMap: true, MapKeyType: "string", MapValueType: "bytes", Number: 7, Annotations: javaSensitiveAnn(false, -1)},
		}},
		{Name: "Secrets", OuterClass: "Demo", Annotations: map[string]model.AnnotationValue{"sensitive_message": {Type: model.AnnotationTypeBool, IntValue: -1}}, Fields: []model.Field{
			{Name: "api_token", Type: "string", Number: 1, Annotations: javaSensitiveAnn(true, -1)},
			{Name: "signing_key", Type: "bytes", Number: 2, Annotations: javaSensitiveAnn(false, 256)},
			{Name: "secret_version", Type: "string", Number: 3, Annotations: notSensitive},
			{Name: "recovery_code", Type: "string", Number: 4, IsOptional: true},
		}},
		{Name: "Endpoint", OuterClass: "Demo", Fields: []model.Field{
			{Name: "url", Type: "string", Number: 1},
			{Name: "password", Type: "string", Number: 2, Annotations: javaSensitiveAnn(false, -1)},
		}},
		{Name: "Database", OuterClass: "Demo", Fields: []model.Field{
			{Name: "dsn", Type: "string", Number: 1},
		}},
		{Name: "Plain", OuterClass: "Demo", Fields: []model.Field{
			{Name: "note", Type: "string", Number: 1},
		}},
	}}
}

func TestJavaRedact_Shape(t *testing.T) {
	pf := redactFixture()
	var b strings.Builder
	writeRedact(&b, pf, "Demo", nil)
	got := b.String()
	for _, want := range []string{
		"public static Demo.Config redactConfig(Demo.Config m) {",
		"Demo.Config.Builder out = m.toBuilder();",
		// RedactInto: only the sub-type's annotated leaves, no indicator.
		"if (out.hasSecrets()) {\nout.setSecrets(redactSecrets(out.getSecrets()));\n}\n",
		"for (int i = 0; i < out.getEndpointsCount(); i++) {\nout.setEndpoints(i, redactEndpoint(out.getEndpoints(i)));\n}\n",
		"for (java.util.Map.Entry<String, Demo.Endpoint> e : new java.util.ArrayList<>(out.getPeersMap().entrySet())) {\nout.putPeers(e.getKey(), redactEndpoint(e.getValue()));\n}\n",
		// A sensitive message field: every leaf, and the indicator.
		"if (out.hasDatabase()) {\nout.setDatabase(redactAllDatabase(out.getDatabase()));\nout.setDatabaseRedacted(true);\n}\n",
		"static Demo.Database redactAllDatabase(Demo.Database m) {",
		// Repeated and map leaves: each non-empty value, keys untouched.
		"if (!out.getApiTokens(i).isEmpty()) {\nout.setApiTokens(i, \"REDACTED\");\nout.setApiTokensRedacted(true);\n}\n",
		"for (java.util.Map.Entry<String, com.google.protobuf.ByteString> e : new java.util.ArrayList<>(out.getSigningKeysMap().entrySet())) {\nif (!e.getValue().isEmpty()) {\nout.putSigningKeys(e.getKey(), com.google.protobuf.ByteString.copyFromUtf8(\"REDACTED\"));\nout.setSigningKeysRedacted(true);\n}\n}\n",
		// sensitive_message: every leaf but not_sensitive; optional checks presence.
		"public static Demo.Secrets redactSecrets(Demo.Secrets m) {",
		"if (!out.getApiToken().isEmpty()) {\nout.setApiToken(\"REDACTED\");\nout.setApiTokenRedacted(true);\n}\n",
		"if (!out.getSigningKey().isEmpty()) {\nout.setSigningKey(com.google.protobuf.ByteString.copyFromUtf8(\"REDACTED\"));\nout.setSigningKeyRedacted(true);\n}\n",
		"if (out.hasRecoveryCode() && !out.getRecoveryCode().isEmpty()) {\nout.setRecoveryCode(\"REDACTED\");\nout.setRecoveryCodeRedacted(true);\n}\n",
		"public static Demo.Endpoint redactEndpoint(Demo.Endpoint m) {",
		// The verdict helper, once, with the integer entropy table.
		"static int tacplusSensitiveVerdict(com.google.protobuf.ByteString value, boolean isBytes, long minBits) {",
		`if (folded.equals("redacted") || folded.equals("changeme")) { return 1; }`,
		"case 95:\nperChar = 6569855L;\n",
		"est = count * 8000000L;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, absent := range []string{
		"SecretVersion",     // not_sensitive
		"redactPlain",       // nothing sensitive in its tree
		"redactAllEndpoint", // only reached through RedactInto
		"setNameRedacted",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %q in:\n%s", absent, got)
		}
	}
	if n := strings.Count(got, "static int tacplusSensitiveVerdict("); n != 1 {
		t.Errorf("verdict helper emitted %d times, want 1", n)
	}
}

// The entropy table is the parser's, entry for entry.
func TestJavaRedact_EntropyTable(t *testing.T) {
	var b strings.Builder
	writeJavaSensitiveVerdictHelper(&b)
	got := b.String()
	tab := parser.EntropyMicroBits()
	for _, p := range parser.EntropyPools() {
		want := fmt.Sprintf("case %d:\nperChar = %dL;\nbreak;\n", p, tab[p])
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// A proto with no sensitive annotation gets no Redact section and no helper.
func TestJavaRedact_NoSensitiveNoOutput(t *testing.T) {
	pf := model.ProtoFile{Package: "demo", Messages: []model.Message{
		{Name: "Plain", Fields: []model.Field{{Name: "token", Type: "string", Number: 1}}},
	}}
	var b strings.Builder
	writeRedact(&b, pf, "Demo", nil)
	if b.Len() != 0 {
		t.Errorf("want no output, got:\n%s", b.String())
	}
	var v strings.Builder
	if err := writeMessageValidation(&v, pf.Messages[0], "Demo", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v.String(), "sensitive") {
		t.Errorf("unexpected sensitive check in:\n%s", v.String())
	}
}

// The validators carry the required / placeholder / entropy checks with the
// Go/Rust message text, in both the strict and at-rest families.
func TestJavaRedact_SensitiveValidation(t *testing.T) {
	msg := model.Message{Name: "SecretCheck", Fields: []model.Field{
		{Name: "token", Type: "string", Number: 1, Annotations: javaSensitiveAnn(true, -1)},
		{Name: "key", Type: "bytes", Number: 2, Annotations: javaSensitiveAnn(true, -1)},
		{Name: "optional_token", Type: "string", Number: 3, Annotations: javaSensitiveAnn(false, 64)},
		{Name: "plain_tokens", Type: "string", Number: 4, IsRepeated: true, Annotations: javaSensitiveAnn(false, -1)},
	}}
	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Demo", nil); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for want, n := range map[string]int{
		"if (msg.getToken().isEmpty()) {\nthrow new ProtoException(ProtoException.INVALID_ARGUMENT, \"field SecretCheck.token (sensitive): required and empty\");\n}\n": 2,
		"switch (tacplusSensitiveVerdict(com.google.protobuf.ByteString.copyFromUtf8(msg.getToken()), false, 128L)) {":                                                  2,
		"\"field SecretCheck.token (sensitive): a known placeholder value\"":                                                                                            2,
		"\"field SecretCheck.token (sensitive): estimated entropy below 128 bits\"":                                                                                     2,
		"if (msg.getKey().isEmpty()) {":                                2,
		"switch (tacplusSensitiveVerdict(msg.getKey(), true, 128L)) {": 2,
		"switch (tacplusSensitiveVerdict(com.google.protobuf.ByteString.copyFromUtf8(msg.getOptionalToken()), false, 64L)) {": 2,
		"\"field SecretCheck.optional_token (sensitive): estimated entropy below 64 bits\"":                                   2,
		"msg.getOptionalToken().isEmpty()": 0, // not required
		"Validate plainTokens (sensitive)": 0, // repeated: redacted, never validated
	} {
		if c := strings.Count(got, want); c != n {
			t.Errorf("%q occurs %d times, want %d in:\n%s", want, c, n, got)
		}
	}
}

// A map<string, bytes> (the redact fixture's signing_keys) round-trips JSON as
// base64, as a singular bytes field does; it used to emit a String put that
// did not compile.
func TestJavaJson_MapBytesValueBase64(t *testing.T) {
	f := model.Field{Name: "signing_keys", IsMap: true, MapKeyType: "string", MapValueType: "bytes", Number: 1}
	var from, to strings.Builder
	writeFromJsonMapField(&from, f, "SigningKeys", "el", nil)
	writeToJsonMapField(&to, f, "SigningKeys", "signingKeys", nil)
	if want := "builder.putSigningKeys(entry.getKey(), com.google.protobuf.ByteString.copyFrom(Base64.getDecoder().decode(val.getAsString())));"; !strings.Contains(from.String(), want) {
		t.Errorf("missing %q in:\n%s", want, from.String())
	}
	if want := "mapObj.addProperty(entry.getKey(), Base64.getEncoder().encodeToString(entry.getValue().toByteArray()));"; !strings.Contains(to.String(), want) {
		t.Errorf("missing %q in:\n%s", want, to.String())
	}
}
