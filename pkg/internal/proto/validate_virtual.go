// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"go.uber.org/zap"
)

// ValidateVirtualImports checks that every virtual service import annotation
// (opa_import) names a target service that actually implements the corresponding
// virtual service.
//
// This catches misconfigurations at build time:
//   - opa_import targeting a service that doesn't import sys/opalib.proto
func ValidateVirtualImports(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for virtual import validation: %w", err)
	}

	allFiles := dp.GetAllFiles()

	// Build index of service capabilities by package name.
	type serviceCapabilities struct {
		hasOpaImpl bool // imports sys/opalib.proto
	}
	capabilities := make(map[string]*serviceCapabilities)

	for _, fd := range allFiles {
		if fd.Package == "" {
			continue
		}
		caps, ok := capabilities[fd.Package]
		if !ok {
			caps = &serviceCapabilities{}
			capabilities[fd.Package] = caps
		}
		// Check for sys/opalib.proto import (OPA server capability).
		for _, imp := range fd.Imports {
			if imp == "sys/opalib.proto" {
				caps.hasOpaImpl = true
			}
		}
	}

	// Validate all virtual imports.
	var errors []string
	for relPath, fd := range allFiles {
		if IsInfraProto(relPath) {
			continue
		}

		// Validate OPA imports.
		for _, imp := range fd.OpaImportTargets {
			if imp.Service == "" {
				errors = append(errors,
					fmt.Sprintf("%s: opa_import: 'service' field is required (source: %s)", relPath, imp.Source))
				continue
			}
			caps := capabilities[imp.Service]
			if caps == nil || !caps.hasOpaImpl {
				errors = append(errors,
					fmt.Sprintf("%s: opa_import: service '%s' does not implement OpaEval (no sys/opalib.proto import in its proto)", relPath, imp.Service))
			} else {
				zap.L().Debug("validated opa_import",
					zap.String("file", relPath),
					zap.String("source", imp.Source),
					zap.String("target", imp.Service))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("virtual import validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}

// ValidateRecordLinkage enforces the core event invariant:
//   - an event/record must declare at least one producer surface (c2s or s2s).
//
// It deliberately does NOT require a transformation's source to be a record. A
// (spec.transformation) is a pure typed conversion A -> B; requiring a record
// would make every transformation drag the eventbus-driven store path in with
// it, which is exactly the coupling the transformation/delivery split cuts.
//
// This is checked at the message level, so it holds regardless of how the proto
// set is scoped. (A complementary "a record/projection must have its hosting
// store service present" check is intentionally NOT enforced here: the host store
// lives in a separate service proto, so it is absent from isolated/partial proto
// sets — e.g. contract-test fixtures — even though the full fleet build always
// includes it. Enforcing store presence per-build produced false failures on
// valid isolated sets. jody asked for the store-presence check; it can be
// reinstated as a whole-fleet (not per-service-build) lint later.)
func ValidateRecordLinkage(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for record linkage validation: %w", err)
	}

	var errors []string
	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			// An event/record must declare at least one producer surface (c2s or
			// s2s) — the producer surface is declared, never defaulted. An
			// internal-only event sets only s2s.
			if _, isEvent := msg.Annotations["event"]; isEvent {
				_, hasC2S := msg.Annotations["event_c2s"]
				_, hasS2S := msg.Annotations["event_s2s"]
				if !hasC2S && !hasS2S {
					errors = append(errors, fmt.Sprintf(
						"%s: message %s is a spec.event/spec.record but declares no producer surface (set c2s and/or s2s)",
						relPath, msg.Name))
				}
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("record/store linkage validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}
