// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func redactField() model.Field {
	return model.Field{
		Name:        "payload",
		Type:        "string",
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "drop"}},
	}
}

func TestValidateRedactAnnotationsAllowsRecordField(t *testing.T) {
	files := []model.ProtoFile{{
		Package: "sensor",
		Messages: []model.Message{{
			Name:        "Event",
			Annotations: map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}},
			Fields:      []model.Field{redactField()},
		}},
	}}
	if err := ValidateRedactAnnotations(files); err != nil {
		t.Fatalf("expected redact on a record field to pass, got: %v", err)
	}
}

// Data entitlement applies to ANY message a service returns, not only a
// (spec.record) — a non-record message (e.g. a sub-message reached through a
// message-typed field) may carry redaction. The record-only rule was dropped in
// Phase 2.
func TestValidateRedactAnnotationsAllowsNonRecordField(t *testing.T) {
	files := []model.ProtoFile{{
		Package: "sensor",
		Messages: []model.Message{{
			Name:   "PlainMessage", // no (spec.record)
			Fields: []model.Field{redactField()},
		}},
	}}
	if err := ValidateRedactAnnotations(files); err != nil {
		t.Fatalf("redact on a non-record message must be allowed, got: %v", err)
	}
}

func recordWith(field model.Field) []model.ProtoFile {
	return []model.ProtoFile{{
		Package: "sensor",
		Messages: []model.Message{{
			Name:        "Event",
			Annotations: map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}},
			Fields:      []model.Field{field},
		}},
	}}
}

func TestValidateRedactAnnotationsRejectsUnspecifiedMode(t *testing.T) {
	// Extension present with no/UNSPECIFIED mode → empty StringValue → must fail loudly.
	files := recordWith(model.Field{
		Name:        "payload",
		Type:        "string",
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: ""}},
	})
	err := ValidateRedactAnnotations(files)
	if err == nil || !strings.Contains(err.Error(), "requires a mode") {
		t.Fatalf("expected unspecified redact mode to fail loudly, got: %v", err)
	}
}

func TestValidateRedactAnnotationsRejectsPseudonymizeOnNonString(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "count",
		Type:        "int64",
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "pseudonymize"}},
	})
	err := ValidateRedactAnnotations(files)
	if err == nil || !strings.Contains(err.Error(), "requires a string") {
		t.Fatalf("expected PSEUDONYMIZE on int64 to fail, got: %v", err)
	}
}

func TestValidateRedactAnnotationsRejectsPseudonymizeOnRepeatedString(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "tags",
		Type:        "string",
		IsRepeated:  true,
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "pseudonymize"}},
	})
	if err := ValidateRedactAnnotations(files); err == nil {
		t.Fatal("expected redact on a repeated field to fail")
	}
}

func TestValidateRedactAnnotationsRejectsOneofMember(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "payload",
		Type:        "string",
		IsOneOf:     true,
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "drop"}},
	})
	err := ValidateRedactAnnotations(files)
	if err == nil || !strings.Contains(err.Error(), "oneof member") {
		t.Fatalf("expected redact on a oneof member to fail loudly, got: %v", err)
	}
}

// A REDACT mode (drop/may_redact) on a message-typed field is now valid — the
// whole sub-message is cleared. Field-by-field recursion is handled downstream by
// BuildEntitlementTree.
func TestValidateRedactAnnotationsAllowsDropOnMessageField(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "nested",
		Type:        "Sub",
		IsMessage:   true,
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "drop"}},
	})
	if err := ValidateRedactAnnotations(files); err != nil {
		t.Fatalf("drop on a message-typed field (whole-message clear) must be allowed, got: %v", err)
	}
}

// Anonymize (keyed HMAC over a single value) has no value to hash for a whole
// message — it must still fail loud on a message-typed field.
func TestValidateRedactAnnotationsRejectsAnonymizeOnMessageField(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "nested",
		Type:        "Sub",
		IsMessage:   true,
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "may_anonymize"}},
	})
	err := ValidateRedactAnnotations(files)
	if err == nil || !strings.Contains(err.Error(), "string or bytes") {
		t.Fatalf("expected anonymize on a message field to fail loudly, got: %v", err)
	}
}

func TestValidateRedactLanguageSupportGatesByLanguage(t *testing.T) {
	files := recordWith(model.Field{
		Name:        "payload",
		Type:        "string",
		Annotations: map[string]model.AnnotationValue{"redact": {Type: model.AnnotationTypeString, StringValue: "drop"}},
	})
	// Full-service languages (Release codegen) and client-only languages (no store,
	// no egress to redact) all build with a redact field.
	for _, lang := range []string{"go", "rust", "java", "python", "typescript", "swift", "csharp"} {
		if err := ValidateRedactLanguageSupport(files, lang); err != nil {
			t.Fatalf("%s should build with a redact field (store-hosting or client-only); got: %v", lang, err)
		}
	}
	// A hypothetical full-service language with no Release codegen must still fail loud.
	err := ValidateRedactLanguageSupport(files, "kotlin")
	if err == nil {
		t.Fatal("expected an unsupported full-service language to fail loudly on a redact field")
	}
	if !strings.Contains(err.Error(), "Event.payload") || !strings.Contains(err.Error(), "kotlin") {
		t.Fatalf("error should name the field and language, got: %v", err)
	}
}

func TestValidateRedactLanguageSupportNoRedactAlwaysPasses(t *testing.T) {
	files := recordWith(model.Field{Name: "payload", Type: "string"})
	for _, lang := range []string{"go", "rust", "java", "python", "typescript"} {
		if err := ValidateRedactLanguageSupport(files, lang); err != nil {
			t.Fatalf("no redact fields; %s should pass, got: %v", lang, err)
		}
	}
}

// TestRedactLanguageMapsCoversAllEmitters derives the set of language emitter
// directories from pkg/internal/ (any directory that contains a "types/"
// subdirectory) and verifies that redactSupportedLanguages and
// redactClientOnlyLanguages together cover exactly that set with no overlap.
//
// This is the mechanical tie the maps were missing (NEW-7): if a new language
// emitter is added under pkg/internal/ without updating these maps, this test
// fails. Conversely, if a map entry names a language with no emitter directory,
// that also fails.
func TestRedactLanguageMapsCoversAllEmitters(t *testing.T) {
	// pkg/internal/ is two levels above this package (common/parser → common → internal)
	internalDir := filepath.Join("..", "..")
	entries, err := os.ReadDir(internalDir)
	if err != nil {
		t.Fatalf("cannot read pkg/internal/: %v", err)
	}

	emitterLangs := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		typesPath := filepath.Join(internalDir, e.Name(), "types")
		if _, err := os.Stat(typesPath); err == nil {
			emitterLangs[e.Name()] = true
		}
	}

	if len(emitterLangs) == 0 {
		t.Fatal("found no language emitter directories under pkg/internal/ — path calculation is wrong")
	}

	// Every emitter language must appear in exactly one map.
	for lang := range emitterLangs {
		inSupported := redactSupportedLanguages[lang]
		inClientOnly := redactClientOnlyLanguages[lang]
		if !inSupported && !inClientOnly {
			t.Errorf("emitter language %q is in neither redactSupportedLanguages nor redactClientOnlyLanguages — add it to one", lang)
		}
		if inSupported && inClientOnly {
			t.Errorf("emitter language %q is in both maps — it must be in exactly one", lang)
		}
	}

	// Every map entry must correspond to an existing emitter directory.
	for lang := range redactSupportedLanguages {
		if !emitterLangs[lang] {
			t.Errorf("redactSupportedLanguages has %q but pkg/internal/%s/types/ does not exist", lang, lang)
		}
	}
	for lang := range redactClientOnlyLanguages {
		if !emitterLangs[lang] {
			t.Errorf("redactClientOnlyLanguages has %q but pkg/internal/%s/types/ does not exist", lang, lang)
		}
	}
}

// TestRedactSupportedLanguagesHaveAnEgressSanitizeEmitter closes the fail-open
// half of NEW-7. A directory under pkg/internal/<lang>/types/ is NOT evidence
// that the language emits the Sanitize<T> egress transform: the whole point of
// redactSupportedLanguages is to name the languages where a (spec.redact) field
// is actually honoured, and every language has that directory.
//
// Get it wrong in the permissive direction and the build stays SILENT: adding a
// language to redactSupportedLanguages before its Release codegen lands makes
// ValidateRedactLanguageSupport return nil, and the service ships an
// unredacted egress surface — exactly the leak the annotation exists to
// prevent. So the set is tied to the artefact that does the work: the
// egress-sanitize emitter file. Both directions are checked, so removing the
// emitter without removing the language fails here too.
func TestRedactSupportedLanguagesHaveAnEgressSanitizeEmitter(t *testing.T) {
	internalDir := filepath.Join("..", "..")
	entries, err := os.ReadDir(internalDir)
	if err != nil {
		t.Fatalf("cannot read pkg/internal/: %v", err)
	}

	withEmitter := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		emitter := filepath.Join(internalDir, e.Name(), "types", "egress_sanitize_gen.go")
		if _, err := os.Stat(emitter); err == nil {
			withEmitter[e.Name()] = true
		}
	}

	if len(withEmitter) == 0 {
		t.Fatal("found no egress_sanitize_gen.go under any pkg/internal/<lang>/types/ — the emitter was renamed and this test no longer proves anything")
	}

	for lang := range redactSupportedLanguages {
		if !withEmitter[lang] {
			t.Errorf("redactSupportedLanguages claims %q honours (spec.redact) but pkg/internal/%s/types/egress_sanitize_gen.go does not exist: the build would pass and ship an UNREDACTED egress surface", lang, lang)
		}
	}
	for lang := range withEmitter {
		if !redactSupportedLanguages[lang] {
			t.Errorf("pkg/internal/%s/types/egress_sanitize_gen.go exists but %q is not in redactSupportedLanguages: a record declaring (spec.redact) is refused for a language that can in fact redact it", lang, lang)
		}
	}
}
