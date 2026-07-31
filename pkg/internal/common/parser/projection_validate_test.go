// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// projectionFile returns a proto file with one message carrying the given
// message-level projection annotation.
func projectionFile(annotation string) []model.ProtoFile {
	return []model.ProtoFile{{
		Package: "sensor",
		Messages: []model.Message{{
			Name:        "TopologyNode",
			Annotations: map[string]model.AnnotationValue{annotation: {Type: model.AnnotationTypeString, StringValue: "sensor.RawEvent"}},
		}},
	}}
}

func TestValidateProjectionLanguageSupportGatesByLanguage(t *testing.T) {
	for _, ann := range projectionAnnotations {
		files := projectionFile(ann)
		// Full-service languages with projection codegen and client-only languages
		// (no store to host a projection) all build.
		for _, lang := range []string{"go", "rust", "python", "typescript", "swift", "csharp"} {
			if err := ValidateProjectionLanguageSupport(files, "sensor", lang); err != nil {
				t.Fatalf("annotation %q: %s should build (store-hosting or client-only); got: %v", ann, lang, err)
			}
		}
		// java is a full-service language with NO projection codegen yet — building
		// the OWNING service must fail loud rather than silently emit a store
		// missing the projection.
		err := ValidateProjectionLanguageSupport(files, "sensor", "java")
		if err == nil {
			t.Fatalf("annotation %q: expected java to fail loudly (no projection codegen)", ann)
		}
		if !strings.Contains(err.Error(), "TopologyNode") || !strings.Contains(err.Error(), "java") {
			t.Fatalf("annotation %q: error should name the message and language, got: %v", ann, err)
		}
	}
}

// A projection record in an imported/ambient dependency (NOT the self service)
// must not fail a java client build of some other service — the projection is the
// owning service's concern, and this build only references it as a client.
func TestValidateProjectionLanguageSupportIgnoresNonSelfPackage(t *testing.T) {
	for _, ann := range projectionAnnotations {
		files := projectionFile(ann) // package "sensor" hosts the projection
		// Building a DIFFERENT service ("client") as java must pass even though the
		// ambient fileset contains sensor's projection record.
		if err := ValidateProjectionLanguageSupport(files, "client", "java"); err != nil {
			t.Fatalf("annotation %q: java build of a non-owning service must ignore an ambient projection record; got: %v", ann, err)
		}
	}
}

func TestValidateProjectionLanguageSupportNoProjectionAlwaysPasses(t *testing.T) {
	files := []model.ProtoFile{{
		Package:  "sensor",
		Messages: []model.Message{{Name: "Plain"}},
	}}
	for _, lang := range []string{"go", "rust", "java", "python", "typescript"} {
		if err := ValidateProjectionLanguageSupport(files, "sensor", lang); err != nil {
			t.Fatalf("no projection annotations; %s should pass, got: %v", lang, err)
		}
	}
}
