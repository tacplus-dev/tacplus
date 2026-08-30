// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func TestGoValidationEmitsDefaultCollectionBounds(t *testing.T) {
	var b strings.Builder
	if err := generateValidationHelpers(&b, auditCollectionMessage()); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"func ValidateAuditCollection(m *AuditCollection) error",
		"Validate Names (default max size: 1024 elements)",
		"if len(m.Names) > 1024",
		"Validate Names elements (default max size: 32768 bytes)",
		"for i, val := range m.Names",
		"if len(val) > 32768",
		"Validate Blobs elements (default max size: 32768 bytes)",
		"for i, val := range m.Blobs",
		"Validate Labels (default max size: 1024 elements)",
		"for key := range m.Labels",
		"for key, val := range m.Labels",
		"Validate Payloads (default max size: 1024 elements)",
		"for key, val := range m.Payloads",
		"Validate Title (default max size: 32768 bytes)",
		"if len(m.Title) > 32768",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Go validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestGoValidationEmitsConstraintParityHandlers(t *testing.T) {
	var b strings.Builder
	if err := generateValidationHelpers(&b, auditConstraintParityMessage()); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate EndpointUrl (assert_url: URL_SCHEME_HTTPS)",
		"utils.ValidateAssertURL_URL_SCHEME_HTTPS(m.EndpointUrl)",
		"Validate ConfigPath (assert_file_path: PATH_ABSOLUTE)",
		"validateFilePathPATH_ABSOLUTE(m.ConfigPath)",
		"Validate PublicKeyPem (assert_enc_type: ENC_TYPE_KEY_PUBLIC_ANY)",
		"validateENC_TYPE_KEY_PUBLIC_ANY(normalizePEMData(m.PublicKeyPem))",
		"Formal invariant for TtlSeconds (assert_invariant): self.ttl_seconds > 0",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Go validation missing %q:\n%s", want, rendered)
		}
	}
}

func TestGoValidationEmitsOneofScalarBounds(t *testing.T) {
	var b strings.Builder
	if err := generateValidationHelpers(&b, auditOneofMessage()); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"Validate Choice (default max size: 32768 bytes)",
		"if len(m.GetChoice()) > 32768",
		"Validate LimitedChoice (assert_max_size: 8 bytes)",
		"if len(m.GetLimitedChoice()) > 8",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Go validation missing %q:\n%s", want, rendered)
		}
	}
}

func auditCollectionMessage() model.Message {
	return model.Message{
		Name: "AuditCollection",
		Fields: []model.Field{
			{Name: "names", Type: "string", IsRepeated: true},
			{Name: "blobs", Type: "bytes", IsRepeated: true},
			{Name: "labels", IsMap: true, MapKeyType: "string", MapValueType: "string"},
			{Name: "payloads", IsMap: true, MapKeyType: "string", MapValueType: "bytes"},
			{Name: "title", Type: "string"},
		},
	}
}

func auditOneofMessage() model.Message {
	return model.Message{
		Name: "AuditOneof",
		Fields: []model.Field{
			{Name: "choice", Type: "string", IsOneOf: true, OneOfName: "selector"},
			{
				Name:      "limited_choice",
				Type:      "string",
				IsOneOf:   true,
				OneOfName: "selector",
				Annotations: map[string]model.AnnotationValue{
					"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
				},
			},
		},
	}
}

func auditConstraintParityMessage() model.Message {
	return model.Message{
		Name: "AuditConstraintParity",
		Fields: []model.Field{
			{
				Name: "endpoint_url",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_url": {Type: model.AnnotationTypeEnum, EnumValue: "URL_SCHEME_HTTPS"},
				},
			},
			{
				Name: "config_path",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_file_path": {Type: model.AnnotationTypeEnum, EnumValue: "PATH_ABSOLUTE"},
				},
			},
			{
				Name: "public_key_pem",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_enc_type": {Type: model.AnnotationTypeEnum, EnumValue: "ENC_TYPE_KEY_PUBLIC_ANY"},
				},
			},
			{
				Name: "ttl_seconds",
				Type: "int64",
				Annotations: map[string]model.AnnotationValue{
					"assert_invariant": {Type: model.AnnotationTypeString, StringValue: "self.ttl_seconds > 0"},
				},
			},
		},
	}
}

// Regression for OF-26d: an unknown validation annotation (assert_*) must be a
// hard generation error, not silently skipped — otherwise a typo'd or
// unsupported constraint would be dropped without a trace.
func TestGoValidationRejectsUnknownAssertAnnotation(t *testing.T) {
	msg := model.Message{
		Name: "Widget",
		Fields: []model.Field{
			{
				Name: "name",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_bogus": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			},
		},
	}
	var b strings.Builder
	err := generateValidationHelpers(&b, msg)
	if err == nil {
		t.Fatal("expected error for unknown assert_ annotation, got nil")
	}
	if !strings.Contains(err.Error(), "assert_bogus") {
		t.Fatalf("error %q does not name the offending annotation", err.Error())
	}
}

// A non-validation annotation (e.g. topic) must NOT trigger the fail-loud path.
func TestGoValidationIgnoresNonValidationAnnotation(t *testing.T) {
	msg := model.Message{
		Name: "Widget",
		Fields: []model.Field{
			{
				Name: "stream",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"topic": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			},
		},
	}
	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("non-validation annotation must not error: %v", err)
	}
}

// requiredIfEqMessage carries a required_if_eq condition whose value is
// interpolated into the generated comparison and error literals.
func requiredIfEqMessage(cond string) model.Message {
	return model.Message{
		Name: "Widget",
		Fields: []model.Field{
			{Name: "kind", Type: "string"},
			{Name: "name", Type: "string", Annotations: map[string]model.AnnotationValue{
				"required_if_eq": {Type: model.AnnotationTypeString, StringValue: cond},
			}},
		},
	}
}

// Defense in depth behind parser.ValidateAnnotationCharset: even handed a
// condition carrying a quote, the emitted comparison and message literals stay
// well-formed and the payload stays inert data. Before this, the same input
// emitted `if m.Kind == "a" || Pwned() || "b" && ...` — code that compiles and
// calls a function the .proto author named.
func TestGoValidationQuotesRequiredIfEqValue(t *testing.T) {
	var b strings.Builder
	if err := generateValidationHelpers(&b, requiredIfEqMessage(`kind=a" || Pwned() || "b`)); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		`if m.Kind == "a\" || Pwned() || \"b" &&`,
		`"field Name is required when kind is \"a\" || Pwned() || \"b\""`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Go validation missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `== "a" || Pwned()`) {
		t.Fatalf("required_if_eq value broke out of the generated literal:\n%s", rendered)
	}
}
