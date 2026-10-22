// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
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
		for _, lang := range []string{"go", "java", "rust", "python", "typescript", "swift", "csharp"} {
			if err := ValidateProjectionLanguageSupport(files, "sensor", lang); err != nil {
				t.Fatalf("annotation %q: %s should build (store-hosting or client-only); got: %v", ann, lang, err)
			}
		}
		// A full-service language with NO projection codegen must still fail loud
		// rather than silently emit a store missing the projection. Go, Java and
		// Rust all generate it now, so the case is exercised with a language that
		// is neither listed as supported nor as client-only.
		err := ValidateProjectionLanguageSupport(files, "sensor", "kotlin")
		if err == nil {
			t.Fatalf("annotation %q: expected an unsupported full-service language to fail loudly", ann)
		}
		if !strings.Contains(err.Error(), "TopologyNode") || !strings.Contains(err.Error(), "kotlin") {
			t.Fatalf("annotation %q: error should name the message and language, got: %v", ann, err)
		}
	}
}

// A projection record in an imported/ambient dependency (NOT the self service)
// must not fail an unsupported-language client build of some other service — the projection is the
// owning service's concern, and this build only references it as a client.
func TestValidateProjectionLanguageSupportIgnoresNonSelfPackage(t *testing.T) {
	for _, ann := range projectionAnnotations {
		files := projectionFile(ann) // package "sensor" hosts the projection
		// Building a DIFFERENT service ("client") must pass even though the ambient
		// fileset contains sensor's projection record.
		if err := ValidateProjectionLanguageSupport(files, "client", "kotlin"); err != nil {
			t.Fatalf("annotation %q: build of a non-owning service must ignore an ambient projection record; got: %v", ann, err)
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
